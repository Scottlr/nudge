// Package export encodes application-owned review selections as bounded,
// human-readable Markdown. It never queries SQLite or reads workspace paths.
package export

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/Scottlr/nudge/internal/app"
	"github.com/Scottlr/nudge/internal/domain/repository"
	"github.com/Scottlr/nudge/internal/domain/review"
)

const (
	formatVersion      = "nudge-markdown-v1"
	messageRangeBytes  = uint64(64 << 10)
	maxHunkExportBytes = app.ProposalPatchRangeBytes
	maxStaticTextBytes = 64 << 10
)

var errMarkdown = errors.New("markdown export failed")

// WriteMarkdown writes one already-selected export. Selection happens before
// this function is called, so slow sink I/O never holds a store transaction.
func WriteMarkdown(ctx context.Context, selection app.ExportSelection, source app.ExportSource, patches app.ProposalPatchReader, output io.Writer) error {
	if ctx == nil || source == nil || output == nil || selection.Validate() != nil {
		return app.ErrExportInput
	}
	writer := &markdownWriter{ctx: ctx, output: output}
	if err := writer.line("# Nudge export"); err != nil {
		return err
	}
	if err := writer.line("- format: " + formatVersion); err != nil {
		return err
	}
	switch selection.Kind {
	case app.ExportThreadKind:
		if err := writer.writeThread(selection.Thread, source); err != nil {
			return err
		}
	case app.ExportProposalKind:
		if err := writer.writeProposal(selection.Proposal, source, patches); err != nil {
			return err
		}
	default:
		return app.ErrExportInput
	}
	return writer.check()
}

type markdownWriter struct {
	ctx    context.Context
	output io.Writer
	err    error
}

func (w *markdownWriter) check() error {
	if w.err != nil {
		return errors.Join(errMarkdown, w.err)
	}
	if err := w.ctx.Err(); err != nil {
		return err
	}
	return nil
}

func (w *markdownWriter) write(data []byte) error {
	if w.err != nil {
		return w.err
	}
	select {
	case <-w.ctx.Done():
		w.err = w.ctx.Err()
		return w.err
	default:
	}
	var written int
	written, w.err = w.output.Write(data)
	if w.err == nil && written != len(data) {
		w.err = io.ErrShortWrite
	}
	return w.err
}

func (w *markdownWriter) line(value string) error {
	if err := w.write([]byte(value)); err != nil {
		return err
	}
	return w.write([]byte("\n"))
}

func (w *markdownWriter) writeThread(selection *app.ExportThread, source app.ExportSource) error {
	if err := w.line("- kind: thread"); err != nil {
		return err
	}
	if err := w.line("- id: " + string(selection.Thread.ID)); err != nil {
		return err
	}
	if err := w.line("- session: " + string(selection.Thread.SessionID)); err != nil {
		return err
	}
	if err := w.line("- title: " + safeInline(selection.Thread.Title)); err != nil {
		return err
	}
	if err := w.line("- status: " + statusLine(selection.Thread)); err != nil {
		return err
	}
	if err := w.line("- created: " + formatTime(selection.Thread.CreatedAt)); err != nil {
		return err
	}
	if err := w.line(""); err != nil {
		return err
	}
	if err := w.line("## Anchor history"); err != nil {
		return err
	}
	for _, record := range selection.Anchors {
		if err := w.line(fmt.Sprintf("### Version %d (%s)", record.Version, record.Method)); err != nil {
			return err
		}
		if err := w.line(fmt.Sprintf("- path: %s", displayPath(record.Anchor.Path))); err != nil {
			return err
		}
		if err := w.line(fmt.Sprintf("- side: %s", record.Anchor.Side)); err != nil {
			return err
		}
		if err := w.line(fmt.Sprintf("- lines: %d-%d", record.Anchor.StartLine, record.Anchor.EndLine)); err != nil {
			return err
		}
		if err := w.line(fmt.Sprintf("- state: %s", record.Anchor.State)); err != nil {
			return err
		}
		if record.Anchor.Relocation != nil {
			if err := w.line(fmt.Sprintf("- relocation: %s", safeInline(record.Anchor.Relocation.Reason))); err != nil {
				return err
			}
		}
	}
	if err := w.line(""); err != nil {
		return err
	}
	if err := w.line("## Messages"); err != nil {
		return err
	}
	for _, message := range selection.Messages {
		if err := w.line(fmt.Sprintf("### Message %d (%s, %s)", message.Ordinal, message.Role, message.Status)); err != nil {
			return err
		}
		if err := w.line(fmt.Sprintf("- id: %s", message.ID)); err != nil {
			return err
		}
		if err := w.line(fmt.Sprintf("- bytes: %d", message.ByteLength)); err != nil {
			return err
		}
		if err := w.line("- sha256: " + message.SHA256); err != nil {
			return err
		}
		if err := w.line(""); err != nil {
			return err
		}
		if err := w.writeMessageBody(source, message); err != nil {
			return err
		}
		if err := w.line(""); err != nil {
			return err
		}
	}
	return nil
}

func (w *markdownWriter) writeMessageBody(source app.ExportSource, message app.MessageSummary) error {
	if message.ByteLength == 0 {
		empty := sha256.Sum256(nil)
		if message.SHA256 != hex.EncodeToString(empty[:]) {
			return app.ErrExportSource
		}
		return w.line("> (empty)")
	}
	hash := sha256.New()
	var offset uint64
	stream := newUTF8MarkdownStream(w)
	for offset < message.ByteLength {
		if err := w.check(); err != nil {
			return err
		}
		length := message.ByteLength - offset
		if length > messageRangeBytes {
			length = messageRangeBytes
		}
		chunk, err := source.ReadMessageBody(w.ctx, app.BodyRange{MessageID: message.ID, ExpectedLength: message.ByteLength, ExpectedSHA256: message.SHA256, Offset: offset, Length: length})
		if err != nil {
			return err
		}
		if chunk.MessageID != message.ID || chunk.Offset != offset || chunk.TotalLength != message.ByteLength || len(chunk.Bytes) != int(length) || chunk.SHA256 != message.SHA256 || chunk.Complete != (offset+length == message.ByteLength) {
			return app.ErrExportSource
		}
		_, _ = hash.Write(chunk.Bytes)
		if err := stream.write(chunk.Bytes); err != nil {
			return err
		}
		offset += length
	}
	if err := stream.finish(); err != nil {
		return err
	}
	if hex.EncodeToString(hash.Sum(nil)) != message.SHA256 {
		return app.ErrExportSource
	}
	return nil
}

func (w *markdownWriter) writeProposal(selection *app.ExportProposal, source app.ExportSource, patches app.ProposalPatchReader) error {
	proposal := selection.Aggregate.Proposal
	patch := selection.Version
	if err := w.line("- kind: proposal"); err != nil {
		return err
	}
	if err := w.line("- id: " + string(proposal.ID)); err != nil {
		return err
	}
	if err := w.line("- status: " + string(patch.Status)); err != nil {
		return err
	}
	if err := w.line(fmt.Sprintf("- version: %d", patch.Version)); err != nil {
		return err
	}
	if err := w.line(""); err != nil {
		return err
	}
	if err := w.line("## Intent"); err != nil {
		return err
	}
	if err := w.line("- thread: " + string(selection.Aggregate.Intent.ThreadID)); err != nil {
		return err
	}
	if err := w.line("- summary:"); err != nil {
		return err
	}
	if err := w.writeStaticQuoted(selection.Aggregate.Intent.Summary); err != nil {
		return err
	}
	if err := w.line("- expected paths:"); err != nil {
		return err
	}
	for _, path := range selection.Aggregate.Intent.ExpectedPaths {
		if err := w.line("  - " + displayPath(path)); err != nil {
			return err
		}
	}
	if err := w.line(""); err != nil {
		return err
	}
	if err := w.line("## Entries"); err != nil {
		return err
	}
	files := append([]review.ProposedFile(nil), patch.Files...)
	sort.SliceStable(files, func(i, j int) bool { return string(files[i].Path.Key()) < string(files[j].Path.Key()) })
	for index, file := range files {
		if err := w.writeProposalFile(index+1, file); err != nil {
			return err
		}
	}
	if err := w.line(""); err != nil {
		return err
	}
	if err := w.line("## Text hunks"); err != nil {
		return err
	}
	if selection.Artifact != nil {
		return w.writeArtifactHunks(selection.Artifact, patches)
	}
	if !utf8.Valid(patch.PatchBytes) || len(patch.PatchBytes) > maxStaticTextBytes {
		return w.line("- patch bytes: unavailable as bounded UTF-8 text; entry metadata above is authoritative.")
	}
	if err := w.line("- legacy patch:"); err != nil {
		return err
	}
	stream := newUTF8MarkdownStream(w)
	if err := stream.write(patch.PatchBytes); err != nil {
		return err
	}
	return stream.finish()
}

func (w *markdownWriter) writeProposalFile(index int, file review.ProposedFile) error {
	if err := w.line(fmt.Sprintf("### Entry %d", index)); err != nil {
		return err
	}
	if err := w.line("- path: " + displayPath(file.Path)); err != nil {
		return err
	}
	if file.OldPath != nil {
		if err := w.line("- old path: " + displayPath(*file.OldPath)); err != nil {
			return err
		}
		if err := w.line("- old kind: " + string(file.OldKind)); err != nil {
			return err
		}
		if err := w.line(fmt.Sprintf("- old mode: %o", file.OldMode)); err != nil {
			return err
		}
		if err := w.line(fmt.Sprintf("- old bytes: %d", file.OldContentBytes)); err != nil {
			return err
		}
		if file.OldContentHash != "" {
			if err := w.line("- old sha256: " + file.OldContentHash); err != nil {
				return err
			}
		}
	}
	if err := w.line("- kind: " + string(file.Kind)); err != nil {
		return err
	}
	if err := w.line(fmt.Sprintf("- mode: %o", file.Mode)); err != nil {
		return err
	}
	if err := w.line(fmt.Sprintf("- bytes: %d", file.ContentBytes)); err != nil {
		return err
	}
	if file.ContentHash != "" {
		if err := w.line("- sha256: " + file.ContentHash); err != nil {
			return err
		}
	}
	if file.Binary {
		if err := w.line("- content: binary or byte-oriented; no text decoding performed"); err != nil {
			return err
		}
	}
	return w.line("")
}

func (w *markdownWriter) writeArtifactHunks(artifact *app.ProposalPatchArtifact, patches app.ProposalPatchReader) error {
	if patches == nil {
		return app.ErrExportSource
	}
	for _, file := range artifact.Index.Files {
		if file.Binary {
			continue
		}
		for _, hunk := range file.Hunks {
			if err := w.line(fmt.Sprintf("- hunk %s at %d (%d bytes)", hunk.ID, hunk.BaseStart, hunk.Length)); err != nil {
				return err
			}
			if hunk.Length <= 0 || app.ByteSize(hunk.Length) > maxHunkExportBytes {
				if err := w.line("  text: omitted because the hunk exceeds the export range bound"); err != nil {
					return err
				}
				continue
			}
			request := app.ProposalPatchRangeRequest{ArtifactID: artifact.ID, Published: artifact.Published, PatchSHA256: artifact.PatchSHA256, PatchBytes: artifact.Published.Identity.Bytes, Offset: app.ByteSize(hunk.Offset), MaxBytes: app.ByteSize(hunk.Length)}
			rangeValue, err := patches.ReadProposalPatchRange(w.ctx, request)
			if err != nil {
				return err
			}
			if err := rangeValue.Validate(request); err != nil || rangeValue.Length != app.ByteSize(hunk.Length) || rangeValue.SHA256 != hunk.SHA256 {
				return app.ErrExportSource
			}
			if !utf8.Valid(rangeValue.Bytes) {
				if err := w.line("  text: omitted because the hunk is not valid UTF-8"); err != nil {
					return err
				}
				continue
			}
			stream := newUTF8MarkdownStream(w)
			if err := stream.write(rangeValue.Bytes); err != nil {
				return err
			}
			if err := stream.finish(); err != nil {
				return err
			}
		}
	}
	return nil
}

func (w *markdownWriter) writeStaticQuoted(value string) error {
	if len(value) > maxStaticTextBytes || !utf8.ValidString(value) {
		return app.ErrExportSource
	}
	stream := newUTF8MarkdownStream(w)
	if err := stream.write([]byte(value)); err != nil {
		return err
	}
	return stream.finish()
}

type utf8MarkdownStream struct {
	writer    *markdownWriter
	pending   []byte
	lineStart bool
	wrote     bool
}

func newUTF8MarkdownStream(writer *markdownWriter) utf8MarkdownStream {
	return utf8MarkdownStream{writer: writer, lineStart: true}
}

func (s *utf8MarkdownStream) write(data []byte) error {
	if len(data) == 0 {
		return nil
	}
	combined := make([]byte, 0, len(s.pending)+len(data))
	combined = append(combined, s.pending...)
	combined = append(combined, data...)
	s.pending = s.pending[:0]
	for len(combined) > 0 {
		if !utf8.FullRune(combined) {
			s.pending = append(s.pending, combined...)
			return nil
		}
		runeValue, size := utf8.DecodeRune(combined)
		if runeValue == utf8.RuneError && size == 1 {
			return app.ErrExportSource
		}
		if err := s.writeRune(runeValue); err != nil {
			return err
		}
		combined = combined[size:]
	}
	return nil
}

func (s *utf8MarkdownStream) writeRune(value rune) error {
	s.wrote = true
	if value == '\n' {
		s.lineStart = true
		return s.writer.write([]byte{'\n'})
	}
	if s.lineStart {
		if err := s.writer.write([]byte("> ")); err != nil {
			return err
		}
		s.lineStart = false
	}
	if value == '\r' || unicode.IsControl(value) || unicode.Is(unicode.Bidi_Control, value) {
		value = utf8.RuneError
	}
	var buffer [utf8.UTFMax]byte
	size := utf8.EncodeRune(buffer[:], value)
	return s.writer.write(buffer[:size])
}

func (s *utf8MarkdownStream) finish() error {
	if len(s.pending) != 0 {
		return app.ErrExportSource
	}
	if !s.wrote {
		return s.writer.line("> (empty)")
	}
	if s.lineStart {
		return nil
	}
	return s.writer.write([]byte{'\n'})
}

func statusLine(thread review.ReviewThread) string {
	return fmt.Sprintf("resolution=%s, conversation=%s, proposal=%s, anchor=%s, read=%s", thread.Resolution, thread.Conversation, thread.Proposal, thread.Anchor.State, thread.Read)
}

func displayPath(path repository.RepoPath) string {
	return "`" + strings.ReplaceAll(strconv.QuoteToGraphic(string(path.Bytes())), "`", "\\`") + "`"
}

func safeInline(value string) string {
	if value == "" {
		return "(none)"
	}
	value = strings.Map(func(value rune) rune {
		if value == '\n' || value == '\r' || unicode.IsControl(value) || unicode.Is(unicode.Bidi_Control, value) {
			return ' '
		}
		return value
	}, value)
	return value
}

func formatTime(value time.Time) string {
	return value.UTC().Format(time.RFC3339Nano)
}
