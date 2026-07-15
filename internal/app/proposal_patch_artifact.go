package app

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"math"
	"time"

	"github.com/Scottlr/nudge/internal/diff"
	"github.com/Scottlr/nudge/internal/domain"
	"github.com/Scottlr/nudge/internal/domain/repository"
	"github.com/Scottlr/nudge/internal/domain/review"
)

const (
	ProposalPatchArtifactVersion uint32   = 1
	ProposalReviewIndexVersion   uint32   = 1
	ProposalPatchFormatVersion   uint32   = 1
	ProposalPatchRangeBytes      ByteSize = 256 * KiB
)

var (
	ErrInvalidProposalPatchArtifact   = errors.New("invalid proposal patch artifact")
	ErrProposalPatchArtifactConflict  = errors.New("proposal patch artifact conflict")
	ErrProposalPatchArtifactNotFound  = errors.New("proposal patch artifact not found")
	ErrProposalPatchArtifactNotReady  = errors.New("proposal patch artifact not ready")
	ErrProposalPatchArtifactNoChanges = errors.New("proposal patch artifact has no changes")
	ErrProposalPatchRangeInvalid      = errors.New("invalid proposal patch range")
	ErrProposalPatchRangeStale        = errors.New("stale proposal patch range")
)

// ProposalPatchRangeRequest requests one bounded range from one adopted patch.
// The target and identity are repeated so an adapter cannot substitute a
// different published artifact behind an otherwise valid range request.
type ProposalPatchRangeRequest struct {
	ArtifactID  string
	Published   PublishedArtifact
	PatchSHA256 string
	PatchBytes  ByteSize
	Offset      ByteSize
	MaxBytes    ByteSize
}

func (r ProposalPatchRangeRequest) Validate() error {
	if r.ArtifactID == "" || r.Published.Identity.Validate() != nil || r.Published.Target.OwnerKind != OwnerProposal || r.PatchSHA256 == "" || !validSHA256(r.PatchSHA256) || r.PatchBytes == 0 || r.Published.Identity.Bytes != r.PatchBytes || r.MaxBytes == 0 || r.MaxBytes > ProposalPatchRangeBytes || r.Offset > r.PatchBytes {
		return ErrProposalPatchRangeInvalid
	}
	return nil
}

// ProposalPatchRange is a complete, identity-bound response for one bounded
// patch read. Bytes are owned by the caller and never retained by the store.
type ProposalPatchRange struct {
	ArtifactID  string
	PatchSHA256 string
	Offset      ByteSize
	Length      ByteSize
	SHA256      string
	Bytes       []byte
	Complete    bool
}

func (r ProposalPatchRange) Validate(request ProposalPatchRangeRequest) error {
	if request.Validate() != nil || r.ArtifactID != request.ArtifactID || r.PatchSHA256 != request.PatchSHA256 || r.Offset != request.Offset || r.Length != ByteSize(len(r.Bytes)) || r.Length > request.MaxBytes || r.Offset > request.PatchBytes || r.Length > request.PatchBytes-r.Offset || !validSHA256(r.SHA256) || !r.Complete {
		return ErrProposalPatchRangeInvalid
	}
	digest := sha256.Sum256(r.Bytes)
	if hex.EncodeToString(digest[:]) != r.SHA256 {
		return ErrProposalPatchRangeInvalid
	}
	return nil
}

// ProposalPatchReader is the application-owned bounded patch read boundary.
type ProposalPatchReader interface {
	ReadProposalPatchRange(context.Context, ProposalPatchRangeRequest) (ProposalPatchRange, error)
}

// ProposalReviewHunk is the bounded byte and logical-row metadata for one
// parsed hunk. The patch BLOB remains authoritative for displayed bytes.
type ProposalReviewHunk struct {
	ID        string
	Offset    int64
	Length    int64
	BaseStart int
	BaseCount int
	HeadStart int
	HeadCount int
	Rows      int
	SHA256    string
}

func (h ProposalReviewHunk) Validate(fileOffset, fileLength int64) error {
	if h.ID == "" || h.Offset < fileOffset || h.Length <= 0 || h.Offset > math.MaxInt64-h.Length || h.Offset+h.Length > fileOffset+fileLength || h.BaseStart < 0 || h.BaseCount < 0 || h.HeadStart < 0 || h.HeadCount < 0 || h.Rows < 0 || !validSHA256(h.SHA256) {
		return ErrInvalidProposalPatchArtifact
	}
	return nil
}

// ProposalReviewFile is one complete file section in the immutable patch.
type ProposalReviewFile struct {
	Ordinal        int
	File           repository.ChangedFile
	Offset         int64
	Length         int64
	HeaderLength   int64
	Binary         bool
	BinaryComplete bool
	BinaryOffset   int64
	SHA256         string
	Hunks          []ProposalReviewHunk
}

func (f ProposalReviewFile) Validate(sourceID string, sourceSize int64) error {
	if f.Ordinal < 0 || f.File.Validate() != nil || f.Offset < 0 || f.Length <= 0 || f.Offset > sourceSize || f.Length > sourceSize-f.Offset || f.HeaderLength <= 0 || f.HeaderLength > f.Length || !validSHA256(f.SHA256) || f.Binary != f.File.Binary || f.BinaryComplete && !f.Binary || (f.Binary && (f.BinaryOffset < f.Offset || f.BinaryOffset-f.Offset >= f.Length)) || (!f.Binary && f.BinaryOffset != 0) {
		return ErrInvalidProposalPatchArtifact
	}
	for _, hunk := range f.Hunks {
		if hunk.Validate(f.Offset, f.Length) != nil {
			return ErrInvalidProposalPatchArtifact
		}
	}
	return nil
}

// ProposalReviewIndex is the complete, deterministic seek index for one
// immutable patch source. It contains metadata only, never the patch bytes.
type ProposalReviewIndex struct {
	Version     uint32
	SourceID    string
	Size        int64
	PatchSHA256 string
	Files       []ProposalReviewFile
	HunkCount   int
	RowCount    int
	Hash        string
	Complete    bool
}

func (i ProposalReviewIndex) Validate() error {
	if i.Version != ProposalReviewIndexVersion || i.SourceID == "" || i.Size <= 0 || !validSHA256(i.PatchSHA256) || !validSHA256(i.Hash) || !i.Complete || i.HunkCount < 0 || i.RowCount < 0 {
		return ErrInvalidProposalPatchArtifact
	}
	previousEnd := int64(0)
	hunks, rows := 0, 0
	for ordinal, file := range i.Files {
		if file.Ordinal != ordinal || file.Validate(i.SourceID, i.Size) != nil || file.Offset < previousEnd {
			return ErrInvalidProposalPatchArtifact
		}
		previousEnd = file.Offset + file.Length
		hunks += len(file.Hunks)
		for _, hunk := range file.Hunks {
			rows += hunk.Rows
		}
	}
	if len(i.Files) == 0 || i.HunkCount != hunks || i.RowCount != rows || proposalReviewIndexHash(i) != i.Hash {
		return ErrInvalidProposalPatchArtifact
	}
	return nil
}

func (i ProposalReviewIndex) Clone() ProposalReviewIndex {
	i.Files = append([]ProposalReviewFile(nil), i.Files...)
	for index := range i.Files {
		i.Files[index].Hunks = append([]ProposalReviewHunk(nil), i.Files[index].Hunks...)
	}
	return i
}

// NewProposalReviewIndex converts the bounded parser output into the
// application-owned immutable index format.
func NewProposalReviewIndex(identity diff.PatchIndexIdentity, entries []diff.PatchIndexEntry) (ProposalReviewIndex, error) {
	index := proposalReviewIndex(identity, entries)
	if index.Validate() != nil {
		return ProposalReviewIndex{}, ErrInvalidProposalPatchArtifact
	}
	return index, nil
}

// ProposalPatchSummary is a bounded projection used before proposal-version
// publication. It is derived from the complete review index, not provider data.
type ProposalPatchSummary struct {
	FileCount   Count
	HunkCount   Count
	RowCount    Count
	BinaryFiles Count
	PatchBytes  ByteSize
}

func (s ProposalPatchSummary) Validate(index ProposalReviewIndex) error {
	if index.Validate() != nil || s.FileCount != Count(len(index.Files)) || s.HunkCount != Count(index.HunkCount) || s.RowCount != Count(index.RowCount) || s.PatchBytes != ByteSize(index.Size) {
		return ErrInvalidProposalPatchArtifact
	}
	var binary Count
	for _, file := range index.Files {
		if file.Binary {
			binary++
		}
	}
	if s.BinaryFiles != binary {
		return ErrInvalidProposalPatchArtifact
	}
	return nil
}

// ProposalPatchArtifact is T111's immutable handoff to T038. The published
// patch bytes live in a Nudge-owned artifact target; this value carries only
// the identity, complete review index, and bounded summary.
type ProposalPatchArtifact struct {
	Version                 uint32
	ID                      string
	SessionID               domain.ReviewSessionID
	ProposalID              domain.ProposalID
	WorkspaceID             domain.WorkspaceID
	AttemptID               domain.OperationID
	ThreadID                domain.ReviewThreadID
	Baseline                review.SnapshotIdentity
	Result                  review.SnapshotIdentity
	BaselineSnapshotID      domain.ReviewSnapshotID
	ResultSnapshotID        domain.ReviewSnapshotID
	PatchFormatVersion      uint32
	RenamePolicyVersion     uint32
	ConversionPolicyVersion uint32
	ConversionFingerprint   string
	ResourcePolicyVersion   ResourcePolicyVersion
	Published               PublishedArtifact
	PatchSHA256             string
	Index                   ProposalReviewIndex
	Summary                 ProposalPatchSummary
	CreatedAt               time.Time
}

// NewProposalPatchArtifact validates and defensively clones one adopted
// artifact value. Callers may omit Version and ID when constructing a fresh
// value; both are derived from the immutable fields.
func NewProposalPatchArtifact(value ProposalPatchArtifact) (ProposalPatchArtifact, error) {
	if value.Version == 0 {
		value.Version = ProposalPatchArtifactVersion
	}
	if value.ID == "" {
		value.ID = proposalPatchArtifactID(value)
	}
	value.Index = value.Index.Clone()
	if err := value.Validate(); err != nil {
		return ProposalPatchArtifact{}, err
	}
	return value, nil
}

func (a ProposalPatchArtifact) Validate() error {
	if a.Version != ProposalPatchArtifactVersion || a.ID == "" || a.SessionID == "" || a.ProposalID == "" || a.WorkspaceID == "" || a.AttemptID == "" || a.ThreadID == "" || a.Baseline.Validate() != nil || a.Result.Validate() != nil || a.Baseline.ID == a.Result.ID || a.BaselineSnapshotID == "" || a.ResultSnapshotID == "" || a.BaselineSnapshotID == a.ResultSnapshotID || a.PatchFormatVersion != ProposalPatchFormatVersion || a.RenamePolicyVersion == 0 || a.ConversionPolicyVersion == 0 || a.ResourcePolicyVersion == 0 || !validSHA256(a.ConversionFingerprint) || a.Published.Identity.Validate() != nil || a.Published.Identity.Bytes == 0 || a.Published.Target.OwnerKind != OwnerProposal || !validSHA256(a.PatchSHA256) || a.Index.Validate() != nil || a.Summary.Validate(a.Index) != nil || a.Published.Identity.Bytes != ByteSize(a.Index.Size) || a.PatchSHA256 != a.Index.PatchSHA256 || a.CreatedAt.IsZero() {
		return ErrInvalidProposalPatchArtifact
	}
	if a.Index.SourceID == "" || a.ID != proposalPatchArtifactID(a) {
		return ErrInvalidProposalPatchArtifact
	}
	return nil
}

// ProposalPatchArtifactStore is the restart boundary for immutable T111
// artifacts. Implementations must not reread a mutable proposal workspace.
type ProposalPatchArtifactStore interface {
	LoadProposalPatchArtifact(context.Context, string) (ProposalPatchArtifact, error)
	LoadProposalPatchArtifactForAttempt(context.Context, domain.OperationID) (ProposalPatchArtifact, error)
}

// ProposalPatchArtifactStoreTx atomically adopts metadata for one published,
// independently verified patch/index pair.
type ProposalPatchArtifactStoreTx interface {
	AdoptProposalPatchArtifact(context.Context, ProposalPatchArtifact) error
}

// ProposalPatchBuildInput binds the parser to the exact T110 result and
// already published, identity-checked patch stream.
type ProposalPatchBuildInput struct {
	Source                  diff.PatchSource
	Published               PublishedArtifact
	Baseline                WorkspaceManifest
	Result                  ResultSnapshot
	PatchFormatVersion      uint32
	RenamePolicyVersion     uint32
	ConversionPolicyVersion uint32
	ConversionFingerprint   string
	PatchSHA256             string
	ResourcePolicy          ResourcePolicy
	SessionID               domain.ReviewSessionID
	ProposalID              domain.ProposalID
	WorkspaceID             domain.WorkspaceID
	AttemptID               domain.OperationID
	ThreadID                domain.ReviewThreadID
	CreatedAt               time.Time
}

// BuildProposalPatchArtifact parses and validates the complete published
// patch without materializing its full bytes in memory.
func BuildProposalPatchArtifact(ctx context.Context, input ProposalPatchBuildInput) (ProposalPatchArtifact, error) {
	if ctx == nil || input.Source == nil || input.ResourcePolicy.Validate() != nil || input.Baseline.Validate() != nil || input.Result.Validate() != nil || input.Result.State != ResultSnapshotReady || input.Published.Identity.Validate() != nil || input.Published.Target.OwnerKind != OwnerProposal || input.Published.Identity.Bytes == 0 || !validSHA256(input.PatchSHA256) || input.PatchFormatVersion != ProposalPatchFormatVersion || input.RenamePolicyVersion == 0 || input.ConversionPolicyVersion == 0 || !validSHA256(input.ConversionFingerprint) || input.SessionID == "" || input.ProposalID == "" || input.WorkspaceID == "" || input.AttemptID == "" || input.ThreadID == "" || input.CreatedAt.IsZero() {
		return ProposalPatchArtifact{}, ErrInvalidProposalPatchArtifact
	}
	if input.Baseline.Hash != input.Result.Baseline.ManifestHash || input.Source.Size() != int64(input.Published.Identity.Bytes) {
		return ProposalPatchArtifact{}, ErrInvalidProposalPatchArtifact
	}
	expectedDelta, err := CompareResultManifest(input.Baseline, input.Result.Manifest)
	if err != nil || expectedDelta.Hash != input.Result.Delta.Hash || !expectedDelta.Complete || len(expectedDelta.Entries) == 0 {
		if len(expectedDelta.Entries) == 0 && err == nil {
			return ProposalPatchArtifact{}, ErrProposalPatchArtifactNoChanges
		}
		return ProposalPatchArtifact{}, ErrInvalidProposalPatchArtifact
	}
	limits := diff.DefaultPatchParseLimits()
	limits.MaxPatchBytes = int64(input.ResourcePolicy.Artifact.CompletePatchBytes)
	limits.MaxFileBytes = int64(input.ResourcePolicy.Artifact.ProposalFileBytes)
	limits.MaxBinaryBytes = int64(input.ResourcePolicy.Artifact.ProposalFileBytes)
	limits.MaxFiles = int(input.ResourcePolicy.Artifact.ProposalFiles)
	sink := new(diff.MemoryPatchIndexSink)
	identity, err := diff.BuildPatchIndex(ctx, input.Source, limits, sink)
	if err != nil || identity.Size != input.Source.Size() || identity.SHA256 != input.PatchSHA256 {
		return ProposalPatchArtifact{}, ErrInvalidProposalPatchArtifact
	}
	entries := sink.Entries()
	if err := crossCheckProposalPatch(input.Baseline, expectedDelta, entries, input.RenamePolicyVersion); err != nil {
		return ProposalPatchArtifact{}, err
	}
	index, err := NewProposalReviewIndex(identity, entries)
	if err != nil {
		return ProposalPatchArtifact{}, err
	}
	var binaryFiles Count
	for _, entry := range index.Files {
		if entry.Binary {
			binaryFiles++
		}
	}
	artifact := ProposalPatchArtifact{
		Version: ProposalPatchArtifactVersion, SessionID: input.SessionID, ProposalID: input.ProposalID,
		WorkspaceID: input.WorkspaceID, AttemptID: input.AttemptID, ThreadID: input.ThreadID,
		Baseline: input.Result.Baseline, Result: input.Result.Result, BaselineSnapshotID: input.Result.Baseline.ID, ResultSnapshotID: input.Result.ID,
		PatchFormatVersion: input.PatchFormatVersion, RenamePolicyVersion: input.RenamePolicyVersion,
		ConversionPolicyVersion: input.ConversionPolicyVersion, ConversionFingerprint: input.ConversionFingerprint,
		ResourcePolicyVersion: input.ResourcePolicy.Version, Published: input.Published, PatchSHA256: input.PatchSHA256, Index: index,
		Summary:   ProposalPatchSummary{FileCount: Count(len(index.Files)), HunkCount: Count(index.HunkCount), RowCount: Count(index.RowCount), BinaryFiles: binaryFiles, PatchBytes: ByteSize(index.Size)},
		CreatedAt: input.CreatedAt.UTC(),
	}
	artifact.ID = proposalPatchArtifactID(artifact)
	if artifact.Validate() != nil {
		return ProposalPatchArtifact{}, ErrInvalidProposalPatchArtifact
	}
	return artifact, nil
}

func proposalReviewIndex(identity diff.PatchIndexIdentity, entries []diff.PatchIndexEntry) ProposalReviewIndex {
	files := make([]ProposalReviewFile, len(entries))
	for index, entry := range entries {
		hunks := make([]ProposalReviewHunk, len(entry.Hunks))
		for hunkIndex, hunk := range entry.Hunks {
			hunks[hunkIndex] = ProposalReviewHunk{ID: hunk.ID, Offset: hunk.Offset, Length: hunk.Length, BaseStart: hunk.BaseStart, BaseCount: hunk.BaseCount, HeadStart: hunk.HeadStart, HeadCount: hunk.HeadCount, Rows: hunk.Rows, SHA256: hunk.SHA256}
		}
		files[index] = ProposalReviewFile{Ordinal: index, File: entry.File, Offset: entry.Offset, Length: entry.Length, HeaderLength: entry.HeaderLength, Binary: entry.Binary, BinaryComplete: entry.BinaryComplete, BinaryOffset: entry.BinaryOffset, SHA256: entry.SHA256, Hunks: hunks}
	}
	return ProposalReviewIndex{Version: ProposalReviewIndexVersion, SourceID: identity.SourceID, Size: identity.Size, PatchSHA256: identity.SHA256, Files: files, HunkCount: identity.HunkCount, RowCount: identity.RowCount, Hash: proposalReviewIndexHash(ProposalReviewIndex{Version: ProposalReviewIndexVersion, SourceID: identity.SourceID, Size: identity.Size, PatchSHA256: identity.SHA256, Files: files, HunkCount: identity.HunkCount, RowCount: identity.RowCount, Complete: true}), Complete: true}
}

func crossCheckProposalPatch(baseline WorkspaceManifest, delta ResultDelta, entries []diff.PatchIndexEntry, renamePolicyVersion uint32) error {
	byPath := make(map[repository.RepoPathKey]ResultDeltaEntry, len(delta.Entries))
	for _, entry := range delta.Entries {
		if entry.Validate() != nil || !entry.Complete || entry.Reason != ResultReasonNone {
			return ErrProposalPatchArtifactNotReady
		}
		byPath[entry.Path.Key()] = entry
	}
	consumed := make(map[repository.RepoPathKey]struct{}, len(byPath))
	for _, entry := range entries {
		if entry.Validate() != nil {
			return ErrInvalidProposalPatchArtifact
		}
		file := entry.File
		if file.Binary && !entry.BinaryComplete {
			return ErrProposalPatchArtifactNotReady
		}
		if file.Binary && !binaryPatchClassesReady(byPath, file) {
			return ErrProposalPatchArtifactNotReady
		}
		if !file.Binary && !textPatchSemanticsReady(byPath, file) {
			return ErrProposalPatchArtifactNotReady
		}
		if file.OldPath != nil && file.NewPath != nil && (file.Kind == repository.ChangeRenamed || file.Kind == repository.ChangeCopied) {
			if !renamePatchMatchesDelta(delta, file, renamePolicyVersion) {
				return ErrInvalidProposalPatchArtifact
			}
			if file.Kind == repository.ChangeRenamed {
				if !consumePatchSide(byPath, consumed, *file.OldPath, true, file.OldFileKind, file.OldMode) || !consumePatchSide(byPath, consumed, *file.NewPath, false, file.NewFileKind, file.NewMode) {
					return ErrInvalidProposalPatchArtifact
				}
			} else if !consumePatchSide(byPath, consumed, *file.NewPath, false, file.NewFileKind, file.NewMode) || !baselineManifestSideMatches(baseline, *file.OldPath, file.OldFileKind, file.OldMode) {
				return ErrInvalidProposalPatchArtifact
			}
			continue
		}
		if file.OldPath != nil && file.NewPath != nil && bytes.Equal(file.OldPath.Bytes(), file.NewPath.Bytes()) {
			entry, ok := byPath[file.OldPath.Key()]
			if !ok || entry.Baseline == nil || entry.Result == nil || entry.Baseline.Kind != file.OldFileKind || !patchModeMatches(entry.Baseline.Mode, file.OldMode, file.OldFileKind) || entry.Result.Kind != file.NewFileKind || !patchModeMatches(entry.Result.Mode, file.NewMode, file.NewFileKind) {
				return ErrInvalidProposalPatchArtifact
			}
			if _, exists := consumed[file.OldPath.Key()]; exists {
				return ErrInvalidProposalPatchArtifact
			}
			consumed[file.OldPath.Key()] = struct{}{}
			continue
		}
		if file.OldPath != nil && !consumePatchSide(byPath, consumed, *file.OldPath, true, file.OldFileKind, file.OldMode) || file.NewPath != nil && !consumePatchSide(byPath, consumed, *file.NewPath, false, file.NewFileKind, file.NewMode) {
			return ErrInvalidProposalPatchArtifact
		}
	}
	if len(consumed) != len(byPath) {
		return ErrInvalidProposalPatchArtifact
	}
	return nil
}

func textPatchSemanticsReady(delta map[repository.RepoPathKey]ResultDeltaEntry, file repository.ChangedFile) bool {
	check := func(path *repository.RepoPath, baseline bool, kind repository.FileKind) bool {
		if path == nil || kind != repository.FileKindRegular {
			return true
		}
		entry, ok := delta[path.Key()]
		if !ok {
			return false
		}
		var value *repository.TextByteSemantics
		var class repository.ContentClassV1
		if baseline {
			if entry.Baseline == nil {
				return false
			}
			class, value = entry.Baseline.ContentClass, entry.Baseline.TextSemantics
		} else {
			if entry.Result == nil {
				return false
			}
			class, value = entry.Result.ContentClass, entry.Result.TextSemantics
		}
		return class != repository.ContentClassRegularTextUTF8 || value != nil
	}
	return check(file.OldPath, true, file.OldFileKind) && check(file.NewPath, false, file.NewFileKind)
}

func binaryPatchClassesReady(delta map[repository.RepoPathKey]ResultDeltaEntry, file repository.ChangedFile) bool {
	if file.OldPath != nil && file.OldFileKind != repository.FileKindRegular || file.NewPath != nil && file.NewFileKind != repository.FileKindRegular {
		return false
	}
	check := func(path *repository.RepoPath, baseline bool, kind repository.FileKind) bool {
		if path == nil || kind != repository.FileKindRegular {
			return true
		}
		entry, ok := delta[path.Key()]
		if !ok {
			return false
		}
		var class repository.ContentClassV1
		if baseline {
			if entry.Baseline == nil {
				return false
			}
			class = entry.Baseline.ContentClass
		} else {
			if entry.Result == nil {
				return false
			}
			class = entry.Result.ContentClass
		}
		return class.IsByteOriented()
	}
	return file.ContentClass.IsByteOriented() && check(file.OldPath, true, file.OldFileKind) && check(file.NewPath, false, file.NewFileKind)
}

func consumePatchSide(delta map[repository.RepoPathKey]ResultDeltaEntry, consumed map[repository.RepoPathKey]struct{}, path repository.RepoPath, old bool, kind repository.FileKind, mode uint32) bool {
	entry, ok := delta[path.Key()]
	if !ok {
		return false
	}
	if _, exists := consumed[path.Key()]; exists {
		return false
	}
	if old {
		if entry.Baseline == nil || entry.Baseline.Kind != kind || !patchModeMatches(entry.Baseline.Mode, mode, kind) {
			return false
		}
	} else if entry.Result == nil || entry.Result.Kind != kind || !patchModeMatches(entry.Result.Mode, mode, kind) {
		return false
	}
	consumed[path.Key()] = struct{}{}
	return true
}

func baselineManifestSideMatches(manifest WorkspaceManifest, path repository.RepoPath, kind repository.FileKind, mode uint32) bool {
	for _, entry := range manifest.Entries {
		if bytes.Equal(entry.Path, path.Bytes()) {
			return entry.Kind == kind && patchModeMatches(entry.Mode, mode, kind)
		}
	}
	return false
}

func patchModeMatches(manifestMode, patchMode uint32, kind repository.FileKind) bool {
	return manifestMode == patchMode && repository.ValidateGitMode(manifestMode) == nil && repository.ValidateGitMode(patchMode) == nil && patchModeKind(manifestMode) == kind && patchModeKind(patchMode) == kind
}

func patchModeKind(mode uint32) repository.FileKind {
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

func proposalPatchArtifactID(a ProposalPatchArtifact) string {
	h := sha256.New()
	writeArtifactHashString(h, string(a.SessionID))
	writeArtifactHashString(h, string(a.ProposalID))
	writeArtifactHashString(h, string(a.WorkspaceID))
	writeArtifactHashString(h, string(a.AttemptID))
	writeArtifactHashString(h, string(a.ThreadID))
	writeArtifactHashSnapshot(h, a.Baseline)
	writeArtifactHashSnapshot(h, a.Result)
	writeArtifactHashString(h, string(a.BaselineSnapshotID))
	writeArtifactHashString(h, string(a.ResultSnapshotID))
	writeArtifactHashUint(h, uint64(a.PatchFormatVersion))
	writeArtifactHashUint(h, uint64(a.RenamePolicyVersion))
	writeArtifactHashUint(h, uint64(a.ConversionPolicyVersion))
	writeArtifactHashString(h, a.ConversionFingerprint)
	writeArtifactHashUint(h, uint64(a.ResourcePolicyVersion))
	writeArtifactHashString(h, a.Published.Identity.SpoolID)
	writeArtifactHashString(h, a.PatchSHA256)
	writeArtifactHashUint(h, uint64(a.Published.Identity.Bytes))
	writeArtifactHashString(h, a.Published.Identity.ManifestHash)
	writeArtifactHashString(h, a.Index.Hash)
	return "proposal-patch-" + hex.EncodeToString(h.Sum(nil))
}

func proposalReviewIndexHash(index ProposalReviewIndex) string {
	h := sha256.New()
	writeArtifactHashUint(h, uint64(index.Version))
	writeArtifactHashString(h, index.SourceID)
	writeArtifactHashUint(h, uint64(index.Size))
	writeArtifactHashString(h, index.PatchSHA256)
	writeArtifactHashUint(h, uint64(index.HunkCount))
	writeArtifactHashUint(h, uint64(index.RowCount))
	writeArtifactHashUint(h, boolUint(index.Complete))
	for _, file := range index.Files {
		writeArtifactHashUint(h, uint64(file.Ordinal))
		writeChangedFileHash(h, file.File)
		writeArtifactHashUint(h, uint64(file.Offset))
		writeArtifactHashUint(h, uint64(file.Length))
		writeArtifactHashUint(h, uint64(file.HeaderLength))
		writeArtifactHashUint(h, boolUint(file.Binary))
		writeArtifactHashUint(h, boolUint(file.BinaryComplete))
		writeArtifactHashUint(h, uint64(file.BinaryOffset))
		writeArtifactHashString(h, file.SHA256)
		for _, hunk := range file.Hunks {
			writeArtifactHashString(h, hunk.ID)
			writeArtifactHashUint(h, uint64(hunk.Offset))
			writeArtifactHashUint(h, uint64(hunk.Length))
			writeArtifactHashUint(h, uint64(hunk.BaseStart))
			writeArtifactHashUint(h, uint64(hunk.BaseCount))
			writeArtifactHashUint(h, uint64(hunk.HeadStart))
			writeArtifactHashUint(h, uint64(hunk.HeadCount))
			writeArtifactHashUint(h, uint64(hunk.Rows))
			writeArtifactHashString(h, hunk.SHA256)
		}
	}
	return hex.EncodeToString(h.Sum(nil))
}

func writeArtifactHashSnapshot(h interface{ Write([]byte) (int, error) }, snapshot review.SnapshotIdentity) {
	writeArtifactHashString(h, string(snapshot.ID))
	writeArtifactHashString(h, string(snapshot.Ref.Kind))
	writeArtifactHashString(h, string(snapshot.Ref.ObjectID))
	writeArtifactHashString(h, string(snapshot.Ref.WorktreeID))
	writeArtifactHashString(h, snapshot.Ref.Fingerprint)
	writeArtifactHashString(h, snapshot.ManifestHash)
}

func writeChangedFileHash(h interface{ Write([]byte) (int, error) }, file repository.ChangedFile) {
	writeArtifactHashPath(h, file.OldPath)
	writeArtifactHashPath(h, file.NewPath)
	writeArtifactHashString(h, string(file.Kind))
	writeArtifactHashString(h, string(file.OldFileKind))
	writeArtifactHashString(h, string(file.NewFileKind))
	writeArtifactHashUint(h, uint64(file.OldMode))
	writeArtifactHashUint(h, uint64(file.NewMode))
	if file.ModeTransition == nil {
		writeArtifactHashUint(h, 0)
	} else {
		writeArtifactHashUint(h, 1)
		writeArtifactHashString(h, string(file.ModeTransition.OldClass))
		writeArtifactHashString(h, string(file.ModeTransition.NewClass))
		writeArtifactHashString(h, string(file.ModeTransition.Kind))
		writeArtifactHashString(h, file.ModeTransition.EvidenceHash)
		writeArtifactHashString(h, file.ModeTransition.PolicyVersion)
	}
	writeArtifactHashString(h, string(file.ContentClass))
	writeArtifactTextSemanticsHash(h, file.OldTextSemantics)
	writeArtifactTextSemanticsHash(h, file.NewTextSemantics)
	writeArtifactHashBool(h, file.Binary)
	if file.Rename == nil {
		writeArtifactHashUint(h, 0)
	} else {
		writeArtifactHashUint(h, 1)
		writeArtifactHashUint(h, uint64(file.Rename.PolicyVersion))
		writeArtifactHashUint(h, uint64(file.Rename.SimilarityPercent))
		writeArtifactHashString(h, string(file.Rename.Kind))
		writeArtifactHashString(h, file.Rename.EvidenceHash)
	}
}

func writeArtifactTextSemanticsHash(h interface{ Write([]byte) (int, error) }, value *repository.TextByteSemantics) {
	if value == nil {
		writeArtifactHashUint(h, 0)
		return
	}
	writeArtifactHashUint(h, 1)
	writeArtifactHashString(h, string(value.Encoding))
	writeArtifactHashUint(h, value.ByteLength)
	writeArtifactHashString(h, value.SHA256)
	writeArtifactHashBool(h, value.HasBOM)
	writeArtifactHashUint(h, value.Endings.LFCount)
	writeArtifactHashUint(h, value.Endings.CRLFCount)
	writeArtifactHashUint(h, value.Endings.CRCount)
	writeArtifactHashBool(h, value.Endings.FinalLF)
	writeArtifactHashBool(h, value.Endings.Mixed)
	writeArtifactHashBool(h, value.Empty)
}

func writeArtifactHashPath(h interface{ Write([]byte) (int, error) }, path *repository.RepoPath) {
	if path == nil {
		writeArtifactHashUint(h, 0)
		return
	}
	writeArtifactHashBytes(h, path.Bytes())
}
func writeArtifactHashBool(h interface{ Write([]byte) (int, error) }, value bool) {
	writeArtifactHashUint(h, boolUint(value))
}
func boolUint(value bool) uint64 {
	if value {
		return 1
	}
	return 0
}
func writeArtifactHashString(h interface{ Write([]byte) (int, error) }, value string) {
	writeArtifactHashBytes(h, []byte(value))
}
func writeArtifactHashBytes(h interface{ Write([]byte) (int, error) }, value []byte) {
	writeArtifactHashUint(h, uint64(len(value)))
	_, _ = h.Write(value)
}
func writeArtifactHashUint(h interface{ Write([]byte) (int, error) }, value uint64) {
	var encoded [8]byte
	for index := range encoded {
		encoded[len(encoded)-1-index] = byte(value >> (index * 8))
	}
	_, _ = h.Write(encoded[:])
}
