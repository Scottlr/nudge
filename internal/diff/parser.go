package diff

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"math"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/Scottlr/nudge/internal/domain/repository"
)

const patchReadChunk = 32 * 1024

// BuildPatchIndex scans source bytes and publishes complete file records to
// sink only after the source size, hash, and all records have been verified.
func BuildPatchIndex(ctx context.Context, source PatchSource, limits PatchParseLimits, sink PatchIndexSink) (PatchIndexIdentity, error) {
	if ctx == nil {
		return PatchIndexIdentity{}, ErrPatchMalformed
	}
	if err := validateSource(source); err != nil || sink == nil {
		return PatchIndexIdentity{}, errOr(err, ErrPatchSink)
	}
	limits = limits.withDefaults()
	if err := limits.Validate(); err != nil {
		return PatchIndexIdentity{}, err
	}
	if source.Size() > limits.MaxPatchBytes {
		return PatchIndexIdentity{}, limited("patch size %d exceeds %d", source.Size(), limits.MaxPatchBytes)
	}

	scanner := newPatchScanner(source, limits.MaxLineBytes)
	entries := make([]PatchIndexEntry, 0, 16)
	var current *sectionBuilder
	rowCount := 0
	for {
		if err := contextError(ctx); err != nil {
			return PatchIndexIdentity{}, err
		}
		line, ok, err := scanner.next()
		if err != nil {
			return PatchIndexIdentity{}, err
		}
		if !ok {
			break
		}
		if bytes.HasPrefix(line.Text, []byte("diff --git ")) {
			if current != nil {
				entry, finishErr := current.finish(line.Offset, source, nil, limits)
				if finishErr != nil {
					return PatchIndexIdentity{}, finishErr
				}
				entry.Index = len(entries)
				entries = append(entries, entry)
				rowCount += entryRows(entry)
			}
			if len(entries) >= limits.MaxFiles {
				return PatchIndexIdentity{}, limited("file count exceeds %d", limits.MaxFiles)
			}
			current, err = newSection(line, limits, false)
			if err != nil {
				return PatchIndexIdentity{}, err
			}
			continue
		}
		if bytes.HasPrefix(line.Text, []byte("diff --cc ")) || bytes.HasPrefix(line.Text, []byte("diff --combined ")) {
			return PatchIndexIdentity{}, fmt.Errorf("%w: combined diff", ErrPatchUnsupported)
		}
		if current == nil {
			if len(bytes.TrimSpace(line.Text)) == 0 {
				continue
			}
			return PatchIndexIdentity{}, malformed("content before first diff section")
		}
		if err := current.consume(line, limits); err != nil {
			return PatchIndexIdentity{}, err
		}
		if current.totalRows > limits.MaxRows {
			return PatchIndexIdentity{}, limited("logical row count exceeds %d", limits.MaxRows)
		}
	}
	if current != nil {
		entry, err := current.finish(source.Size(), source, nil, limits)
		if err != nil {
			return PatchIndexIdentity{}, err
		}
		entry.Index = len(entries)
		entries = append(entries, entry)
		rowCount += entryRows(entry)
	}
	if scanner.consumed != source.Size() {
		return PatchIndexIdentity{}, fmt.Errorf("%w: source ended at %d of %d bytes", ErrPatchTruncated, scanner.consumed, source.Size())
	}
	identity := PatchIndexIdentity{
		Version:   PatchIndexVersion,
		SourceID:  source.ID(),
		Size:      source.Size(),
		SHA256:    hex.EncodeToString(scanner.hash.Sum(nil)),
		FileCount: len(entries),
		HunkCount: countHunks(entries),
		RowCount:  rowCount,
	}
	if err := identity.Validate(); err != nil {
		return PatchIndexIdentity{}, err
	}
	for _, entry := range entries {
		if err := sink.Append(entry); err != nil {
			return PatchIndexIdentity{}, fmt.Errorf("%w: %v", ErrPatchSink, err)
		}
	}
	return identity, nil
}

// ParseFileDiff reads one indexed text section into a bounded structured
// model. Binary sections return an identity-bound byte range without loading
// the binary patch body.
func ParseFileDiff(ctx context.Context, source PatchSource, entry PatchIndexEntry, limits PatchParseLimits) (repository.FileDiff, error) {
	if ctx == nil {
		return repository.FileDiff{}, ErrPatchMalformed
	}
	if err := validateSource(source); err != nil {
		return repository.FileDiff{}, err
	}
	limits = limits.withDefaults()
	if err := limits.Validate(); err != nil {
		return repository.FileDiff{}, err
	}
	if err := entry.Validate(); err != nil || entry.SourceID != source.ID() || entry.Offset > source.Size() || entry.Length > source.Size()-entry.Offset {
		return repository.FileDiff{}, ErrPatchMalformed
	}
	if err := contextError(ctx); err != nil {
		return repository.FileDiff{}, err
	}
	actualHash, err := hashRange(source, entry.Offset, entry.Length, ctx)
	if err != nil || actualHash != entry.SHA256 {
		return repository.FileDiff{}, ErrPatchTruncated
	}
	if entry.Binary {
		if entry.Length > limits.MaxBinaryBytes {
			return repository.FileDiff{}, limited("binary section exceeds %d", limits.MaxBinaryBytes)
		}
		binaryOffset := entry.BinaryOffset
		if binaryOffset < entry.Offset || binaryOffset >= entry.Offset+entry.Length {
			binaryOffset = entry.Offset
		}
		binaryLength := entry.Offset + entry.Length - binaryOffset
		binaryHash, err := hashRange(source, binaryOffset, binaryLength, ctx)
		if err != nil {
			return repository.FileDiff{}, err
		}
		result := repository.FileDiff{
			File:           entry.File,
			BinaryComplete: entry.BinaryComplete,
			BinaryPatch: &repository.PatchByteRange{
				ArtifactID: repository.PatchArtifactID(source.ID()),
				Offset:     binaryOffset,
				Length:     binaryLength,
				SHA256:     binaryHash,
			},
		}
		if err := result.Validate(); err != nil {
			return repository.FileDiff{}, err
		}
		return result, nil
	}
	if entry.Length > limits.MaxFileBytes {
		return repository.FileDiff{}, limited("text section exceeds %d", limits.MaxFileBytes)
	}
	data, err := readRange(source, entry.Offset, entry.Length, ctx)
	if err != nil {
		return repository.FileDiff{}, err
	}
	b, err := parseSectionBytes(data, entry.Offset, limits, true)
	if err != nil {
		return repository.FileDiff{}, err
	}
	if err := b.finishHunk(entry.Offset+entry.Length, limits); err != nil {
		return repository.FileDiff{}, err
	}
	result := b.fileDiff(source.ID(), source, entry.Offset+entry.Length, ctx)
	if err := result.Validate(); err != nil {
		return repository.FileDiff{}, err
	}
	return result, nil
}

// ParsePatch parses a deliberately small in-memory fixture using the same
// bounded index and on-demand file parser as production sources.
func ParsePatch(data []byte) ([]repository.FileDiff, error) {
	limits := DefaultPatchParseLimits()
	limits.MaxPatchBytes = 8 * 1024 * 1024
	limits.MaxFileBytes = 8 * 1024 * 1024
	if int64(len(data)) > limits.MaxPatchBytes {
		return nil, limited("fixture exceeds %d bytes", limits.MaxPatchBytes)
	}
	source := bytePatchSource{data: append([]byte(nil), data...), id: "fixture"}
	sink := new(MemoryPatchIndexSink)
	if _, err := BuildPatchIndex(context.Background(), source, limits, sink); err != nil {
		return nil, err
	}
	entries := sink.Entries()
	result := make([]repository.FileDiff, 0, len(entries))
	for _, entry := range entries {
		file, err := ParseFileDiff(context.Background(), source, entry, limits)
		if err != nil {
			return nil, err
		}
		result = append(result, file)
	}
	return result, nil
}

type patchLine struct {
	Offset     int64
	End        int64
	Text       []byte
	Terminated bool
}

type patchScanner struct {
	source    PatchSource
	maxLine   int
	readAt    int64
	consumed  int64
	buffer    []byte
	bufferPos int
	eof       bool
	hash      hashWriter
}

func newPatchScanner(source PatchSource, maxLine int) *patchScanner {
	return &patchScanner{source: source, maxLine: maxLine, hash: sha256.New()}
}

func (s *patchScanner) fill() error {
	if s.eof || s.bufferPos < len(s.buffer) {
		return nil
	}
	if s.readAt >= s.source.Size() {
		s.eof = true
		s.buffer = nil
		s.bufferPos = 0
		return nil
	}
	length := patchReadChunk
	remaining := s.source.Size() - s.readAt
	if remaining < int64(length) {
		length = int(remaining)
	}
	buffer := make([]byte, length)
	n, err := s.source.ReadAt(buffer, s.readAt)
	if n < 0 || n > len(buffer) {
		return ErrInvalidPatchSource
	}
	if n > 0 {
		_, _ = s.hash.Write(buffer[:n])
		s.readAt += int64(n)
	}
	if err != nil && !errors.Is(err, io.EOF) {
		return fmt.Errorf("%w: %v", ErrInvalidPatchSource, err)
	}
	if n == 0 {
		s.eof = true
	}
	s.buffer = buffer[:n]
	s.bufferPos = 0
	return nil
}

func (s *patchScanner) next() (patchLine, bool, error) {
	start := s.consumed
	line := make([]byte, 0, minInt(s.maxLine, 256))
	for {
		if err := s.fill(); err != nil {
			return patchLine{}, false, err
		}
		if s.bufferPos == len(s.buffer) {
			if s.eof {
				if len(line) == 0 {
					return patchLine{}, false, nil
				}
				return patchLine{Offset: start, End: s.consumed, Text: line, Terminated: false}, true, nil
			}
			continue
		}
		remaining := s.buffer[s.bufferPos:]
		newline := bytes.IndexByte(remaining, '\n')
		if newline >= 0 {
			part := remaining[:newline]
			if len(line)+len(part) > s.maxLine {
				return patchLine{}, false, limited("line exceeds %d bytes", s.maxLine)
			}
			line = append(line, part...)
			s.bufferPos += newline + 1
			s.consumed += int64(newline + 1)
			return patchLine{Offset: start, End: s.consumed, Text: line, Terminated: true}, true, nil
		}
		if len(line)+len(remaining) > s.maxLine {
			return patchLine{}, false, limited("line exceeds %d bytes", s.maxLine)
		}
		line = append(line, remaining...)
		s.consumed += int64(len(remaining))
		s.bufferPos = len(s.buffer)
	}
}

type sectionBuilder struct {
	start             int64
	headerLen         int64
	lastEnd           int64
	oldPath           *repository.RepoPath
	newPath           *repository.RepoPath
	renameFrom        *repository.RepoPath
	renameTo          *repository.RepoPath
	copyFrom          *repository.RepoPath
	copyTo            *repository.RepoPath
	oldMode           uint32
	newMode           uint32
	indexMode         uint32
	oldModeSet        bool
	newModeSet        bool
	indexModeSet      bool
	oldObject         *repository.ObjectID
	newObject         *repository.ObjectID
	binary            bool
	binaryStart       int64
	binaryComplete    bool
	metadataBytes     int64
	hunk              *hunkBuilder
	hunks             []PatchHunkIndex
	diffHunks         []repository.DiffHunk
	collect           bool
	totalRows         int
	similarityPercent uint8
	similaritySet     bool
}

type hunkBuilder struct {
	index             PatchHunkIndex
	header            string
	baseSeen          int
	headSeen          int
	rows              int
	lines             []repository.DiffLine
	collect           bool
	lastContentIndex  int
	lastContentKind   repository.DiffLineKind
	baseNoNewlineSeen bool
	headNoNewlineSeen bool
}

func newSection(line patchLine, limits PatchParseLimits, collect bool) (*sectionBuilder, error) {
	oldRaw, newRaw, err := parseGitHeaderPaths(line.Text)
	if err != nil {
		return nil, err
	}
	oldPath, err := parseRepoPath(oldRaw, "a/", false)
	if err != nil {
		return nil, err
	}
	newPath, err := parseRepoPath(newRaw, "b/", false)
	if err != nil {
		return nil, err
	}
	if line.End-line.Offset > int64(limits.MaxHeaderBytes) {
		return nil, limited("diff header exceeds %d bytes", limits.MaxHeaderBytes)
	}
	return &sectionBuilder{
		start:         line.Offset,
		headerLen:     line.End - line.Offset,
		lastEnd:       line.End,
		oldPath:       oldPath,
		newPath:       newPath,
		binaryStart:   -1,
		metadataBytes: line.End - line.Offset,
		collect:       collect,
	}, nil
}

func (b *sectionBuilder) consume(line patchLine, limits PatchParseLimits) error {
	b.lastEnd = line.End
	text := line.Text
	if b.hunk != nil {
		if len(text) > 0 && (text[0] == ' ' || text[0] == '+' || text[0] == '-') {
			return b.consumeDiffLine(text, limits)
		}
		if bytes.Equal(text, []byte("\\ No newline at end of file")) {
			return b.consumeNoNewlineMarker()
		}
		if isSectionRecord(text) {
			if err := b.finishHunk(line.Offset, limits); err != nil {
				return err
			}
			return b.consume(line, limits)
		}
		return malformed("unexpected line inside hunk")
	}
	if b.binary {
		if bytes.HasPrefix(text, []byte("diff --cc ")) || bytes.HasPrefix(text, []byte("diff --combined ")) {
			return fmt.Errorf("%w: combined diff", ErrPatchUnsupported)
		}
		return nil
	}
	b.metadataBytes += line.End - line.Offset
	if b.metadataBytes > int64(limits.MaxHeaderBytes) {
		return limited("metadata exceeds %d bytes", limits.MaxHeaderBytes)
	}
	if bytes.HasPrefix(text, []byte("@@ ")) {
		return b.startHunk(line, limits)
	}
	if bytes.Equal(text, []byte("GIT binary patch")) {
		b.binary = true
		b.binaryStart = line.Offset
		b.binaryComplete = true
		return nil
	}
	if bytes.HasPrefix(text, []byte("Binary files ")) {
		b.binary = true
		b.binaryStart = line.Offset
		return nil
	}
	if bytes.HasPrefix(text, []byte("old mode ")) {
		mode, err := parseMode(text[len("old mode "):])
		if err != nil {
			return err
		}
		b.oldMode, b.oldModeSet = mode, true
		return nil
	}
	if bytes.HasPrefix(text, []byte("new mode ")) {
		mode, err := parseMode(text[len("new mode "):])
		if err != nil {
			return err
		}
		b.newMode, b.newModeSet = mode, true
		return nil
	}
	if bytes.HasPrefix(text, []byte("new file mode ")) {
		mode, err := parseMode(text[len("new file mode "):])
		if err != nil {
			return err
		}
		b.newMode, b.newModeSet = mode, true
		b.oldPath = nil
		return nil
	}
	if bytes.HasPrefix(text, []byte("deleted file mode ")) {
		mode, err := parseMode(text[len("deleted file mode "):])
		if err != nil {
			return err
		}
		b.oldMode, b.oldModeSet = mode, true
		b.newPath = nil
		return nil
	}
	if bytes.HasPrefix(text, []byte("index ")) {
		return b.consumeIndex(text[len("index "):])
	}
	if bytes.HasPrefix(text, []byte("rename from ")) {
		raw := text[len("rename from "):]
		path, err := parseRepoPath(raw, "", len(raw) > 0 && raw[0] == '"')
		if err != nil {
			return err
		}
		b.renameFrom = path
		return nil
	}
	if bytes.HasPrefix(text, []byte("rename to ")) {
		raw := text[len("rename to "):]
		path, err := parseRepoPath(raw, "", len(raw) > 0 && raw[0] == '"')
		if err != nil {
			return err
		}
		b.renameTo = path
		return nil
	}
	if bytes.HasPrefix(text, []byte("copy from ")) {
		raw := text[len("copy from "):]
		path, err := parseRepoPath(raw, "", len(raw) > 0 && raw[0] == '"')
		if err != nil {
			return err
		}
		b.copyFrom = path
		return nil
	}
	if bytes.HasPrefix(text, []byte("copy to ")) {
		raw := text[len("copy to "):]
		path, err := parseRepoPath(raw, "", len(raw) > 0 && raw[0] == '"')
		if err != nil {
			return err
		}
		b.copyTo = path
		return nil
	}
	if bytes.HasPrefix(text, []byte("--- ")) {
		path, err := parsePatchPathLine(text[4:], "a/")
		if err != nil {
			return err
		}
		b.oldPath = path
		return nil
	}
	if bytes.HasPrefix(text, []byte("+++ ")) {
		path, err := parsePatchPathLine(text[4:], "b/")
		if err != nil {
			return err
		}
		b.newPath = path
		return nil
	}
	if bytes.HasPrefix(text, []byte("similarity index ")) || bytes.HasPrefix(text, []byte("dissimilarity index ")) {
		if bytes.HasPrefix(text, []byte("similarity index ")) {
			value, err := parseSimilarityPercent(text[len("similarity index "):])
			if err != nil {
				return err
			}
			b.similarityPercent = value
			b.similaritySet = true
		}
		return nil
	}
	if len(text) == 0 {
		return malformed("empty metadata line")
	}
	return malformed("unknown patch record %q", string(text))
}

func (b *sectionBuilder) consumeDiffLine(text []byte, limits PatchParseLimits) error {
	kind := repository.DiffLineContext
	switch text[0] {
	case '+':
		kind = repository.DiffLineAdded
	case '-':
		kind = repository.DiffLineDeleted
	}
	if b.hunk == nil {
		return ErrPatchMalformed
	}
	if b.totalRows == math.MaxInt {
		return limited("logical row count overflow")
	}
	b.totalRows++
	b.hunk.rows++
	lineText := text[1:]
	terminator := repository.TerminatorLF
	if len(lineText) > 0 && lineText[len(lineText)-1] == '\r' {
		lineText = lineText[:len(lineText)-1]
		terminator = repository.TerminatorCRLF
	}
	line := repository.DiffLine{Kind: kind, Text: string(lineText), Terminator: terminator}
	switch kind {
	case repository.DiffLineContext:
		base, head := b.hunk.baseSeen+b.hunk.index.BaseStart, b.hunk.headSeen+b.hunk.index.HeadStart
		line.BaseLine, line.HeadLine = intPointer(base), intPointer(head)
		b.hunk.baseSeen++
		b.hunk.headSeen++
	case repository.DiffLineAdded:
		head := b.hunk.headSeen + b.hunk.index.HeadStart
		line.HeadLine = intPointer(head)
		b.hunk.headSeen++
	case repository.DiffLineDeleted:
		base := b.hunk.baseSeen + b.hunk.index.BaseStart
		line.BaseLine = intPointer(base)
		b.hunk.baseSeen++
	}
	if b.hunk.baseSeen > b.hunk.index.BaseCount || b.hunk.headSeen > b.hunk.index.HeadCount {
		return malformed("hunk line count exceeds header")
	}
	b.hunk.lastContentKind = kind
	if b.collect {
		b.hunk.lastContentIndex = len(b.hunk.lines)
		b.hunk.lines = append(b.hunk.lines, line)
	}
	if b.totalRows > limits.MaxRows {
		return limited("logical row count exceeds %d", limits.MaxRows)
	}
	return nil
}

func (b *sectionBuilder) consumeNoNewlineMarker() error {
	if b.hunk == nil || b.hunk.lastContentKind == "" {
		return malformed("no-newline marker without a preceding content line")
	}
	base, head := false, false
	switch b.hunk.lastContentKind {
	case repository.DiffLineDeleted:
		base = true
	case repository.DiffLineAdded:
		head = true
	case repository.DiffLineContext:
		base, head = true, true
	default:
		return malformed("no-newline marker after non-content line")
	}
	if base && b.hunk.baseNoNewlineSeen || head && b.hunk.headNoNewlineSeen {
		return malformed("contradictory no-newline marker")
	}
	if b.collect {
		if b.hunk.lastContentIndex < 0 || b.hunk.lastContentIndex >= len(b.hunk.lines) {
			return malformed("no-newline marker content index missing")
		}
		previous := &b.hunk.lines[b.hunk.lastContentIndex]
		previous.Terminator = repository.TerminatorNone
		previous.NoNewlineBase = base
		previous.NoNewlineHead = head
	}
	b.hunk.baseNoNewlineSeen = b.hunk.baseNoNewlineSeen || base
	b.hunk.headNoNewlineSeen = b.hunk.headNoNewlineSeen || head
	if b.collect {
		b.hunk.lines = append(b.hunk.lines, repository.DiffLine{Kind: repository.DiffLineNoNewline, Text: "\\ No newline at end of file", NoNewlineBase: base, NoNewlineHead: head})
	}
	return nil
}

func (b *sectionBuilder) startHunk(line patchLine, limits PatchParseLimits) error {
	if len(b.hunks) >= limits.MaxHunks {
		return limited("hunk count exceeds %d", limits.MaxHunks)
	}
	baseStart, baseCount, headStart, headCount, err := parseHunkHeader(line.Text)
	if err != nil {
		return err
	}
	idBytes := sha256.Sum256(line.Text)
	b.hunk = &hunkBuilder{
		index: PatchHunkIndex{
			Version:   PatchIndexVersion,
			ID:        hex.EncodeToString(idBytes[:]),
			Offset:    line.Offset,
			BaseStart: baseStart,
			BaseCount: baseCount,
			HeadStart: headStart,
			HeadCount: headCount,
		},
		header:           string(line.Text),
		collect:          b.collect,
		lastContentIndex: -1,
	}
	return nil
}

func (b *sectionBuilder) finishHunk(end int64, limits PatchParseLimits) error {
	if b.hunk == nil {
		return nil
	}
	if b.hunk.baseSeen != b.hunk.index.BaseCount || b.hunk.headSeen != b.hunk.index.HeadCount {
		return malformed("hunk line count does not match header")
	}
	if end <= b.hunk.index.Offset {
		return malformed("empty hunk range")
	}
	b.hunk.index.Length = end - b.hunk.index.Offset
	b.hunk.index.Rows = b.hunk.rows
	if b.collect {
		hunk := repository.DiffHunk{
			ID:        b.hunk.index.ID,
			BaseStart: b.hunk.index.BaseStart,
			BaseCount: b.hunk.index.BaseCount,
			HeadStart: b.hunk.index.HeadStart,
			HeadCount: b.hunk.index.HeadCount,
			Header:    b.hunk.header,
			Lines:     append([]repository.DiffLine(nil), b.hunk.lines...),
		}
		if err := hunk.Validate(); err != nil {
			return fmt.Errorf("%w: %v", ErrPatchMalformed, err)
		}
		b.diffHunks = append(b.diffHunks, hunk)
	}
	b.hunks = append(b.hunks, b.hunk.index)
	b.hunk = nil
	if len(b.hunks) > limits.MaxHunks {
		return limited("hunk count exceeds %d", limits.MaxHunks)
	}
	return nil
}

func (b *sectionBuilder) consumeIndex(value []byte) error {
	fields := strings.Fields(string(value))
	if len(fields) < 1 || len(fields) > 2 {
		return malformed("index record")
	}
	parts := strings.Split(fields[0], "..")
	if len(parts) != 2 {
		return malformed("index object range")
	}
	oldObject, err := parsePatchObject(parts[0])
	if err != nil {
		return err
	}
	newObject, err := parsePatchObject(parts[1])
	if err != nil {
		return err
	}
	b.oldObject, b.newObject = oldObject, newObject
	if len(fields) == 2 {
		mode, err := parseMode([]byte(fields[1]))
		if err != nil {
			return err
		}
		b.indexMode, b.indexModeSet = mode, true
	}
	return nil
}

func (b *sectionBuilder) finish(end int64, source PatchSource, data []byte, limits PatchParseLimits) (PatchIndexEntry, error) {
	if err := b.finishHunk(end, limits); err != nil {
		return PatchIndexEntry{}, err
	}
	if end <= b.start {
		return PatchIndexEntry{}, malformed("empty file section")
	}
	file, err := b.changedFile()
	if err != nil {
		return PatchIndexEntry{}, err
	}
	sectionHash, err := hashRangeOrData(source, data, b.start, end-b.start)
	if err != nil {
		return PatchIndexEntry{}, err
	}
	for index := range b.hunks {
		hunk := &b.hunks[index]
		hash, err := hashRangeOrData(source, data, hunk.Offset, hunk.Length)
		if err != nil {
			return PatchIndexEntry{}, err
		}
		hunk.SHA256 = hash
	}
	binaryOffset := int64(0)
	if b.binary {
		binaryOffset = b.binaryOffset(end)
	}
	return PatchIndexEntry{
		Version:        PatchIndexVersion,
		SourceID:       sourceID(source, "section"),
		Offset:         b.start,
		Length:         end - b.start,
		HeaderLength:   b.headerLen,
		File:           file,
		Binary:         b.binary,
		BinaryOffset:   binaryOffset,
		BinaryComplete: b.binaryComplete,
		SHA256:         sectionHash,
		Hunks:          append([]PatchHunkIndex(nil), b.hunks...),
	}, nil
}

func (b *sectionBuilder) binaryOffset(end int64) int64 {
	if b.binaryStart >= b.start && b.binaryStart < end {
		return b.binaryStart
	}
	return b.start
}

func (b *sectionBuilder) fileDiff(sourceID string, source PatchSource, end int64, ctx context.Context) repository.FileDiff {
	file, _ := b.changedFile()
	result := repository.FileDiff{File: file, Hunks: append([]repository.DiffHunk(nil), b.diffHunks...), BinaryComplete: b.binaryComplete}
	if b.binary {
		offset := b.binaryOffset(end)
		length := end - offset
		hash, err := hashRange(source, offset, length, ctx)
		if err == nil {
			result.BinaryPatch = &repository.PatchByteRange{ArtifactID: repository.PatchArtifactID(sourceID), Offset: offset, Length: length, SHA256: hash}
		}
	}
	return result
}

func (b *sectionBuilder) changedFile() (repository.ChangedFile, error) {
	oldPath := b.oldPath
	newPath := b.newPath
	if b.renameFrom != nil {
		oldPath = b.renameFrom
	}
	if b.renameTo != nil {
		newPath = b.renameTo
	}
	if b.copyFrom != nil {
		oldPath = b.copyFrom
	}
	if b.copyTo != nil {
		newPath = b.copyTo
	}
	oldMode, newMode := b.oldMode, b.newMode
	if !b.oldModeSet {
		oldMode = b.indexMode
	}
	if !b.newModeSet {
		newMode = b.indexMode
	}
	if oldPath != nil && oldMode == 0 {
		oldMode = 0o100644
	}
	if newPath != nil && newMode == 0 {
		newMode = 0o100644
	}
	oldKind := modeKind(oldMode)
	newKind := modeKind(newMode)
	kind := repository.ChangeModified
	switch {
	case oldPath == nil && newPath != nil:
		kind = repository.ChangeAdded
	case oldPath != nil && newPath == nil:
		kind = repository.ChangeDeleted
	case b.renameFrom != nil || b.renameTo != nil:
		kind = repository.ChangeRenamed
	case b.copyFrom != nil || b.copyTo != nil:
		kind = repository.ChangeCopied
	case oldKind != newKind:
		kind = repository.ChangeTypeChanged
	}
	file := repository.ChangedFile{
		OldPath: oldPath, NewPath: newPath, Kind: kind,
		OldFileKind: oldKind, NewFileKind: newKind,
		OldMode: oldMode, NewMode: newMode,
		OldObjectID: b.oldObject, NewObjectID: b.newObject,
		Binary: b.binary,
	}
	if oldPath != nil && newPath != nil && (oldMode != newMode || oldKind != newKind) {
		transition, transitionErr := repository.NewModeTransition(oldMode, newMode)
		if transitionErr != nil {
			return repository.ChangedFile{}, fmt.Errorf("%w: mode transition", ErrPatchMalformed)
		}
		if transition.Kind == repository.ModeTypeChanged && (kind == repository.ChangeRenamed || kind == repository.ChangeCopied) {
			return repository.ChangedFile{}, fmt.Errorf("%w: rename type transition", ErrPatchMalformed)
		}
		file.ModeTransition = &transition
		if transition.Kind == repository.ModeTypeChanged {
			file.Kind = repository.ChangeTypeChanged
		}
	}
	if b.binary && (oldKind == repository.FileKindRegular || newKind == repository.FileKindRegular) {
		file.ContentClass = repository.ContentClassRegularBinary
	} else if (oldKind == repository.FileKindRegular || newKind == repository.FileKindRegular) && len(b.diffHunks) != 0 {
		file.ContentClass = repository.ContentClassRegularTextUTF8
		opaque := false
		for _, hunk := range b.diffHunks {
			for _, line := range hunk.Lines {
				if bytes.IndexByte([]byte(line.Text), 0) >= 0 || !utf8.ValidString(line.Text) {
					file.ContentClass = repository.ContentClassOpaqueBytes
					opaque = true
				}
			}
		}
		if opaque {
			b.binary = true
			b.binaryStart = b.start
			b.diffHunks = nil
			b.hunks = nil
			file.Binary = true
		}
	}
	if kind == repository.ChangeRenamed || kind == repository.ChangeCopied {
		if !b.similaritySet {
			return repository.ChangedFile{}, malformed("rename similarity evidence missing")
		}
		evidence, err := repository.NewRenameEvidence(1, b.similarityPercent, kind, *oldPath, *newPath)
		if err != nil {
			return repository.ChangedFile{}, fmt.Errorf("%w: rename evidence", ErrPatchMalformed)
		}
		file.Rename = &evidence
	}
	if err := file.Validate(); err != nil {
		return repository.ChangedFile{}, fmt.Errorf("%w: file metadata: %v", ErrPatchMalformed, err)
	}
	return file, nil
}

func parseSimilarityPercent(value []byte) (uint8, error) {
	value = bytes.TrimSpace(value)
	if len(value) != 4 || value[3] != '%' {
		return 0, malformed("similarity index")
	}
	parsed, err := strconv.Atoi(string(value[:3]))
	if err != nil || parsed < 60 || parsed > 100 {
		return 0, malformed("similarity index")
	}
	return uint8(parsed), nil
}

func parseSectionBytes(data []byte, base int64, limits PatchParseLimits, collect bool) (*sectionBuilder, error) {
	if len(data) == 0 {
		return nil, ErrPatchTruncated
	}
	firstEnd := bytes.IndexByte(data, '\n')
	firstText := data
	firstTerminated := false
	if firstEnd >= 0 {
		firstText = data[:firstEnd]
		firstTerminated = true
	}
	first := patchLine{Offset: base, End: base + int64(len(firstText)), Text: append([]byte(nil), firstText...), Terminated: firstTerminated}
	if firstTerminated {
		first.End++
	}
	b, err := newSection(first, limits, collect)
	if err != nil {
		return nil, err
	}
	position := len(firstText)
	if firstTerminated {
		position++
	}
	for position < len(data) {
		remaining := data[position:]
		lineEnd := bytes.IndexByte(remaining, '\n')
		text := remaining
		terminated := false
		length := len(remaining)
		if lineEnd >= 0 {
			text = remaining[:lineEnd]
			terminated = true
			length = lineEnd + 1
		}
		if len(text) > limits.MaxLineBytes {
			return nil, limited("line exceeds %d bytes", limits.MaxLineBytes)
		}
		line := patchLine{Offset: base + int64(position), End: base + int64(position+length), Text: append([]byte(nil), text...), Terminated: terminated}
		if err := b.consume(line, limits); err != nil {
			return nil, err
		}
		position += length
	}
	return b, nil
}

func isSectionRecord(text []byte) bool {
	for _, prefix := range [][]byte{
		[]byte("@@ "), []byte("old mode "), []byte("new mode "), []byte("new file mode "), []byte("deleted file mode "), []byte("index "), []byte("rename from "), []byte("rename to "), []byte("copy from "), []byte("copy to "), []byte("--- "), []byte("+++ "), []byte("Binary files "), []byte("GIT binary patch"),
	} {
		if bytes.HasPrefix(text, prefix) {
			return true
		}
	}
	return false
}

func parseGitHeaderPaths(line []byte) ([]byte, []byte, error) {
	if !bytes.HasPrefix(line, []byte("diff --git ")) {
		return nil, nil, malformed("diff header")
	}
	value := string(line[len("diff --git "):])
	if strings.HasPrefix(value, "\"") {
		oldValue, rest, err := takeQuotedToken(value)
		if err != nil {
			return nil, nil, err
		}
		newValue, rest, err := takeQuotedToken(rest)
		if err != nil || strings.TrimSpace(rest) != "" {
			return nil, nil, malformed("quoted diff header paths")
		}
		return oldValue, newValue, nil
	}
	separator := strings.LastIndex(value, " b/")
	if !strings.HasPrefix(value, "a/") || separator <= 0 || separator+1 >= len(value) {
		return nil, nil, malformed("diff header paths")
	}
	return []byte(value[:separator]), []byte(value[separator+1:]), nil
}

func takeQuotedToken(value string) ([]byte, string, error) {
	value = strings.TrimLeft(value, " \t")
	if !strings.HasPrefix(value, "\"") {
		return nil, value, malformed("quoted path")
	}
	decoded, consumed, err := decodeCQuoted(value)
	if err != nil {
		return nil, value, err
	}
	return decoded, value[consumed:], nil
}

func decodeCQuoted(value string) ([]byte, int, error) {
	if len(value) == 0 || value[0] != '"' {
		return nil, 0, malformed("quoted path")
	}
	result := make([]byte, 0, len(value))
	for index := 1; index < len(value); index++ {
		char := value[index]
		if char == '"' {
			return result, index + 1, nil
		}
		if char != '\\' {
			result = append(result, char)
			continue
		}
		if index+1 >= len(value) {
			return nil, 0, malformed("unterminated quoted path")
		}
		index++
		switch value[index] {
		case 'a':
			result = append(result, '\a')
		case 'b':
			result = append(result, '\b')
		case 'f':
			result = append(result, '\f')
		case 'n':
			result = append(result, '\n')
		case 'r':
			result = append(result, '\r')
		case 't':
			result = append(result, '\t')
		case 'v':
			result = append(result, '\v')
		case '\\':
			result = append(result, '\\')
		case '"':
			result = append(result, '"')
		case '0', '1', '2', '3', '4', '5', '6', '7':
			valueOctal := int(value[index] - '0')
			for count := 0; count < 2 && index+1 < len(value) && value[index+1] >= '0' && value[index+1] <= '7'; count++ {
				index++
				valueOctal = valueOctal*8 + int(value[index]-'0')
			}
			if valueOctal == 0 {
				return nil, 0, malformed("NUL in quoted path")
			}
			result = append(result, byte(valueOctal))
		default:
			return nil, 0, malformed("unknown quoted path escape")
		}
	}
	return nil, 0, malformed("unterminated quoted path")
}

func parseRepoPath(raw []byte, prefix string, quoted bool) (*repository.RepoPath, error) {
	if quoted || (len(raw) > 0 && raw[0] == '"') {
		decoded, consumed, err := decodeCQuoted(string(raw))
		if err != nil || strings.TrimSpace(string(raw[consumed:])) != "" {
			return nil, malformed("quoted repository path")
		}
		raw = decoded
	}
	if bytes.Equal(raw, []byte("/dev/null")) {
		return nil, nil
	}
	if prefix != "" && bytes.HasPrefix(raw, []byte(prefix)) {
		raw = raw[len(prefix):]
	}
	path, err := repository.NewRepoPath(raw)
	if err != nil {
		return nil, malformed("repository path")
	}
	return &path, nil
}

func parsePatchPathLine(raw []byte, prefix string) (*repository.RepoPath, error) {
	if tab := bytes.IndexByte(raw, '\t'); tab >= 0 {
		raw = raw[:tab]
	}
	return parseRepoPath(raw, prefix, len(raw) > 0 && raw[0] == '"')
}

func parseMode(raw []byte) (uint32, error) {
	if len(raw) == 0 {
		return 0, malformed("empty file mode")
	}
	value, err := strconv.ParseUint(string(raw), 8, 32)
	if err != nil || value == 0 || repository.ValidateGitMode(uint32(value)) != nil {
		return 0, malformed("file mode")
	}
	return uint32(value), nil
}

func parsePatchObject(raw string) (*repository.ObjectID, error) {
	if raw == "" {
		return nil, malformed("empty object ID")
	}
	if strings.Trim(raw, "0") == "" {
		return nil, nil
	}
	for _, char := range raw {
		if !((char >= '0' && char <= '9') || (char >= 'a' && char <= 'f') || (char >= 'A' && char <= 'F')) {
			return nil, malformed("object ID")
		}
	}
	object, err := repository.NewObjectID(raw)
	if err != nil {
		return nil, malformed("object ID")
	}
	return &object, nil
}

func parseHunkHeader(line []byte) (int, int, int, int, error) {
	value := string(line)
	if !strings.HasPrefix(value, "@@ ") {
		return 0, 0, 0, 0, malformed("hunk header")
	}
	body := value[3:]
	end := strings.Index(body, " @@")
	if end < 0 {
		return 0, 0, 0, 0, malformed("hunk header terminator")
	}
	fields := strings.Fields(body[:end])
	if len(fields) != 2 {
		return 0, 0, 0, 0, malformed("hunk ranges")
	}
	baseStart, baseCount, err := parseHunkRange(fields[0], '-')
	if err != nil {
		return 0, 0, 0, 0, err
	}
	headStart, headCount, err := parseHunkRange(fields[1], '+')
	if err != nil {
		return 0, 0, 0, 0, err
	}
	return baseStart, baseCount, headStart, headCount, nil
}

func parseHunkRange(value string, prefix byte) (int, int, error) {
	if len(value) < 2 || value[0] != prefix {
		return 0, 0, malformed("hunk range")
	}
	parts := strings.Split(value[1:], ",")
	if len(parts) > 2 || parts[0] == "" {
		return 0, 0, malformed("hunk range")
	}
	start64, err := strconv.ParseInt(parts[0], 10, 32)
	if err != nil || start64 < 0 {
		return 0, 0, malformed("hunk start")
	}
	count := 1
	if len(parts) == 2 {
		count64, parseErr := strconv.ParseInt(parts[1], 10, 32)
		if parseErr != nil || count64 < 0 {
			return 0, 0, malformed("hunk count")
		}
		count = int(count64)
	}
	if count > 0 && start64 == 0 {
		return 0, 0, malformed("non-empty hunk starts at zero")
	}
	return int(start64), count, nil
}

func modeKind(mode uint32) repository.FileKind {
	switch repository.ClassifyGitMode(mode) {
	case repository.ModeRegularNonExecutable, repository.ModeRegularExecutable:
		return repository.FileKindRegular
	case repository.ModeSymlink:
		return repository.FileKindSymlink
	case repository.ModeGitlink:
		return repository.FileKindGitlink
	case repository.ModeTree:
		return repository.FileKindDirectory
	default:
		return repository.FileKindUnknown
	}
}

func intPointer(value int) *int { return &value }

func entryRows(entry PatchIndexEntry) int {
	rows := 0
	for _, hunk := range entry.Hunks {
		rows += hunk.Rows
	}
	return rows
}

func countHunks(entries []PatchIndexEntry) int {
	count := 0
	for _, entry := range entries {
		count += len(entry.Hunks)
	}
	return count
}

func readRange(source PatchSource, offset, length int64, ctx context.Context) ([]byte, error) {
	if offset < 0 || length < 0 || offset > source.Size() || length > source.Size()-offset || length > int64(math.MaxInt) {
		return nil, ErrPatchTruncated
	}
	data := make([]byte, int(length))
	read := int64(0)
	for read < length {
		if err := contextError(ctx); err != nil {
			return nil, err
		}
		n, err := source.ReadAt(data[read:], offset+read)
		if n < 0 || int64(n) > length-read {
			return nil, ErrInvalidPatchSource
		}
		read += int64(n)
		if err != nil && !errors.Is(err, io.EOF) {
			return nil, fmt.Errorf("%w: %v", ErrInvalidPatchSource, err)
		}
		if n == 0 {
			return nil, ErrPatchTruncated
		}
	}
	return data, nil
}

func hashRange(source PatchSource, offset, length int64, ctx context.Context) (string, error) {
	if offset < 0 || length < 0 || offset > source.Size() || length > source.Size()-offset {
		return "", ErrPatchTruncated
	}
	hash := sha256.New()
	buffer := make([]byte, patchReadChunk)
	read := int64(0)
	for read < length {
		if err := contextError(ctx); err != nil {
			return "", err
		}
		want := int64(len(buffer))
		if remaining := length - read; remaining < want {
			want = remaining
		}
		n, err := source.ReadAt(buffer[:want], offset+read)
		if n < 0 || int64(n) > want {
			return "", ErrInvalidPatchSource
		}
		if n > 0 {
			_, _ = hash.Write(buffer[:n])
			read += int64(n)
		}
		if err != nil && !errors.Is(err, io.EOF) {
			return "", fmt.Errorf("%w: %v", ErrInvalidPatchSource, err)
		}
		if n == 0 {
			return "", ErrPatchTruncated
		}
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func hashRangeOrData(source PatchSource, data []byte, offset, length int64) (string, error) {
	if data != nil {
		base := offset
		if base < 0 || length < 0 || base > int64(len(data)) || length > int64(len(data))-base {
			return "", ErrPatchTruncated
		}
		sum := sha256.Sum256(data[base : base+length])
		return hex.EncodeToString(sum[:]), nil
	}
	return hashRange(source, offset, length, context.Background())
}

func sourceID(source PatchSource, fallback string) string {
	if source == nil {
		return fallback
	}
	return source.ID()
}

func contextError(ctx context.Context) error {
	if ctx == nil {
		return ErrPatchMalformed
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return nil
}

func errOr(first, fallback error) error {
	if first != nil {
		return first
	}
	return fallback
}

func minInt(left, right int) int {
	if left < right {
		return left
	}
	return right
}

type hashWriter interface {
	Write([]byte) (int, error)
	Sum([]byte) []byte
}

type bytePatchSource struct {
	data []byte
	id   string
}

func (s bytePatchSource) ID() string  { return s.id }
func (s bytePatchSource) Size() int64 { return int64(len(s.data)) }
func (s bytePatchSource) Open() (io.ReadCloser, error) {
	return io.NopCloser(bytes.NewReader(s.data)), nil
}
func (s bytePatchSource) ReadAt(buffer []byte, offset int64) (int, error) {
	if offset < 0 {
		return 0, errors.New("negative offset")
	}
	if offset >= int64(len(s.data)) {
		return 0, io.EOF
	}
	n := copy(buffer, s.data[offset:])
	if n < len(buffer) {
		return n, io.EOF
	}
	return n, nil
}
