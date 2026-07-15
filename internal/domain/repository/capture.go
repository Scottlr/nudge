package repository

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/Scottlr/nudge/internal/domain"
)

// LocalCaptureCandidateVersion identifies the immutable candidate manifest
// format consumed by T009.
const LocalCaptureCandidateVersion uint32 = 1

var (
	// ErrInvalidLocalCaptureCandidate reports contradictory or incomplete
	// candidate evidence.
	ErrInvalidLocalCaptureCandidate = errors.New("invalid local capture candidate")
	// ErrLocalCaptureNotCandidate reports a bounded capture that cannot be
	// treated as a complete candidate.
	ErrLocalCaptureNotCandidate = errors.New("local capture is not a candidate")
)

// CaptureArtifactKind identifies the candidate-owned artifact represented by
// one spool identity.
type CaptureArtifactKind string

const (
	CaptureArtifactPatch CaptureArtifactKind = "patch"
	CaptureArtifactBlobs CaptureArtifactKind = "blobs"
)

// CaptureArtifact identifies one complete, still-owned artifact spool. The
// relative path is interpreted only by the typed spool handle that owns it.
type CaptureArtifact struct {
	Kind          CaptureArtifactKind
	SpoolID       string
	ManifestHash  string
	RelativePath  string
	Bytes         uint64
	Entries       uint64
	ContentSHA256 string
	VerifiedAt    time.Time
}

// Validate checks identity shape and keeps patch and per-blob semantics
// distinct.
func (a CaptureArtifact) Validate() error {
	if a.SpoolID == "" || !validCaptureText(a.SpoolID) || !validCaptureSHA256(a.ManifestHash) || a.RelativePath == "" || !validCaptureText(a.RelativePath) || a.VerifiedAt.IsZero() {
		return ErrInvalidLocalCaptureCandidate
	}
	switch a.Kind {
	case CaptureArtifactPatch:
		if a.Entries != 1 || !validCaptureSHA256(a.ContentSHA256) {
			return ErrInvalidLocalCaptureCandidate
		}
	case CaptureArtifactBlobs:
		if a.Entries == 0 {
			if a.Bytes != 0 || a.ContentSHA256 != "" {
				return ErrInvalidLocalCaptureCandidate
			}
		} else if a.Bytes == 0 || a.Entries != 1 && a.ContentSHA256 != "" || a.Entries == 1 && a.ContentSHA256 != "" && !validCaptureSHA256(a.ContentSHA256) {
			return ErrInvalidLocalCaptureCandidate
		}
	default:
		return ErrInvalidLocalCaptureCandidate
	}
	return nil
}

// CaptureBlobSide identifies which immutable side of a changed entry is
// represented by a candidate blob.
type CaptureBlobSide string

const (
	CaptureBlobBase        CaptureBlobSide = "base"
	CaptureBlobIndex       CaptureBlobSide = "index"
	CaptureBlobWorkingTree CaptureBlobSide = "working_tree"
	CaptureBlobStage1      CaptureBlobSide = "stage1"
	CaptureBlobStage2      CaptureBlobSide = "stage2"
	CaptureBlobStage3      CaptureBlobSide = "stage3"
)

// CaptureBlobRef links a changed-file side to one content-addressed spool
// member. Git object IDs remain separate from the artifact hash.
type CaptureBlobRef struct {
	Side          CaptureBlobSide
	Path          RepoPath
	Artifact      CaptureArtifact
	ContentClass  ContentClassV1
	TextSemantics *TextByteSemantics
}

// Validate checks the path/side binding without opening the artifact.
func (b CaptureBlobRef) Validate() error {
	if b.Side != CaptureBlobBase && b.Side != CaptureBlobIndex && b.Side != CaptureBlobWorkingTree && b.Side != CaptureBlobStage1 && b.Side != CaptureBlobStage2 && b.Side != CaptureBlobStage3 {
		return ErrInvalidLocalCaptureCandidate
	}
	if err := b.Path.Validate(); err != nil || b.Artifact.Validate() != nil || b.Artifact.Kind != CaptureArtifactBlobs || b.Artifact.Bytes == 0 || b.Artifact.Entries != 1 || !validCaptureSHA256(b.Artifact.ContentSHA256) {
		return ErrInvalidLocalCaptureCandidate
	}
	if b.ContentClass != "" && b.ContentClass.Validate() != nil {
		return ErrInvalidLocalCaptureCandidate
	}
	if b.TextSemantics != nil && (b.ContentClass != ContentClassRegularTextUTF8 || b.TextSemantics.Validate() != nil || b.TextSemantics.ByteLength != b.Artifact.Bytes || b.TextSemantics.SHA256 != b.Artifact.ContentSHA256) {
		return ErrInvalidLocalCaptureCandidate
	}
	return nil
}

// CaptureIndexFlag is semantic index state that ordinary status may omit.
type CaptureIndexFlag string

const (
	CaptureIndexAssumeUnchanged CaptureIndexFlag = "assume_unchanged"
	CaptureIndexSkipWorktree    CaptureIndexFlag = "skip_worktree"
	CaptureIndexFSMonitorValid  CaptureIndexFlag = "fsmonitor_valid"
	CaptureIndexIntentToAdd     CaptureIndexFlag = "intent_to_add"
	CaptureIndexSparse          CaptureIndexFlag = "sparse"
)

func validCaptureIndexFlag(flag CaptureIndexFlag) bool {
	switch flag {
	case CaptureIndexAssumeUnchanged, CaptureIndexSkipWorktree, CaptureIndexFSMonitorValid, CaptureIndexIntentToAdd, CaptureIndexSparse:
		return true
	default:
		return false
	}
}

// CaptureIndexEntry retains one stage and its independently observed flags.
type CaptureIndexEntry struct {
	Path     RepoPath
	Stage    uint8
	Mode     uint32
	ObjectID ObjectID
	Flags    []CaptureIndexFlag
}

// Validate checks stage/object identity and rejects duplicate flags.
func (e CaptureIndexEntry) Validate() error {
	if err := e.Path.Validate(); err != nil || e.Stage > 3 || e.Mode == 0 || e.ObjectID == "" {
		return ErrInvalidLocalCaptureCandidate
	}
	if _, err := NewObjectID(string(e.ObjectID)); err != nil {
		return ErrInvalidLocalCaptureCandidate
	}
	seen := make(map[CaptureIndexFlag]struct{}, len(e.Flags))
	for _, flag := range e.Flags {
		if !validCaptureIndexFlag(flag) {
			return ErrInvalidLocalCaptureCandidate
		}
		if _, ok := seen[flag]; ok {
			return ErrInvalidLocalCaptureCandidate
		}
		seen[flag] = struct{}{}
	}
	return nil
}

// LocalCaptureIndexEvidence is the before/after index identity and semantic
// state observed during one capture attempt.
type LocalCaptureIndexEvidence struct {
	Exists       bool
	Bytes        uint64
	SHA256       string
	Entries      []CaptureIndexEntry
	Sparse       bool
	BeforeSHA256 string
	AfterSHA256  string
}

// Validate checks index identity and ensures a present index is represented
// by a stable before/after byte hash.
func (i LocalCaptureIndexEvidence) Validate() error {
	if i.Exists {
		if !validCaptureSHA256(i.SHA256) || i.SHA256 != i.BeforeSHA256 || i.SHA256 != i.AfterSHA256 {
			return ErrInvalidLocalCaptureCandidate
		}
	} else if i.Bytes != 0 || i.SHA256 != "" || i.BeforeSHA256 != "" || i.AfterSHA256 != "" || len(i.Entries) != 0 {
		return ErrInvalidLocalCaptureCandidate
	}
	seen := make(map[string]struct{}, len(i.Entries))
	for _, entry := range i.Entries {
		if err := entry.Validate(); err != nil {
			return err
		}
		key := fmt.Sprintf("%s\x00%d", entry.Path, entry.Stage)
		if _, ok := seen[key]; ok {
			return ErrInvalidLocalCaptureCandidate
		}
		seen[key] = struct{}{}
	}
	return nil
}

// CapturePolicyEvidence records the exact policy versions and bounded rename
// outcome used to create the candidate.
type CapturePolicyEvidence struct {
	MachineGitVersion               uint32
	RenameVersion                   uint32
	RenameOutcome                   string
	RenameDeleteCandidates          uint64
	RenameAddCandidates             uint64
	RenameSimilarityPercent         uint8
	RenameMaxDeleteSources          uint64
	RenameMaxAddTargets             uint64
	RenameDetectChangedSourceCopies bool
	RenameFindCopiesHarder          bool
	RenameFlags                     []string
	RenameEvidenceHash              string
	PatchFormatVersion              uint32
	ConversionPolicyVersion         uint32
	ConversionDecision              string
	ConversionReason                string
	ConversionFingerprint           string
	AttributesChanged               bool
	ResourcePolicyVersion           uint32
}

// Validate checks the versioned policy identity without importing adapters.
func (p CapturePolicyEvidence) Validate() error {
	if p.MachineGitVersion == 0 || p.RenameVersion == 0 || p.PatchFormatVersion == 0 || p.ConversionPolicyVersion == 0 || p.ResourcePolicyVersion == 0 || p.RenameOutcome == "" || !validCaptureText(p.RenameOutcome) || (p.ConversionDecision != "byte_neutral" && p.ConversionDecision != "review_only") || !validCaptureSHA256(p.ConversionFingerprint) {
		return ErrInvalidLocalCaptureCandidate
	}
	if p.ConversionDecision == "byte_neutral" && p.ConversionReason != "" || p.ConversionDecision == "review_only" && !validCaptureText(p.ConversionReason) {
		return ErrInvalidLocalCaptureCandidate
	}
	if p.RenameEvidenceHash != "" {
		if len(p.RenameFlags) == 0 || p.RenameSimilarityPercent != 60 || p.RenameMaxDeleteSources != 1000 || p.RenameMaxAddTargets != 1000 || !p.RenameDetectChangedSourceCopies || p.RenameFindCopiesHarder || !validCaptureSHA256(p.RenameEvidenceHash) {
			return ErrInvalidLocalCaptureCandidate
		}
		if expected := RenamePolicyEvidenceHash(p.RenameVersion, p.RenameSimilarityPercent, int(p.RenameMaxDeleteSources), int(p.RenameMaxAddTargets), p.RenameDetectChangedSourceCopies, p.RenameFindCopiesHarder, p.RenameOutcome, int(p.RenameDeleteCandidates), int(p.RenameAddCandidates), p.RenameFlags); expected != p.RenameEvidenceHash {
			return ErrInvalidLocalCaptureCandidate
		}
	}
	return nil
}

// CaptureConsistencyEvidence records phase tokens that were compared before
// a candidate was returned. The tokens are opaque hashes, not Git object IDs.
type CaptureConsistencyEvidence struct {
	HeadToken       string
	IndexToken      string
	StatusToken     string
	FlagsToken      string
	FilesystemToken string
	AggregateToken  string
}

// Validate checks that every capture phase contributed bounded evidence.
func (c CaptureConsistencyEvidence) Validate() error {
	for _, value := range []string{c.HeadToken, c.IndexToken, c.StatusToken, c.FlagsToken, c.FilesystemToken, c.AggregateToken} {
		if !validCaptureSHA256(value) {
			return ErrInvalidLocalCaptureCandidate
		}
	}
	return nil
}

// LocalCaptureEntry is one ordered raw-path change plus the immutable side
// blobs needed to reproduce it without reading the live worktree later.
type LocalCaptureEntry struct {
	Change repositoryChange
	Blobs  []CaptureBlobRef
}

// repositoryChange keeps the public field layout of ChangedFile while making
// LocalCaptureEntry.Validate easy to read below.
type repositoryChange = ChangedFile

// Validate checks the complete entry and rejects duplicate side references.
func (e LocalCaptureEntry) Validate() error {
	if err := e.Change.Validate(); err != nil {
		return ErrInvalidLocalCaptureCandidate
	}
	seen := make(map[CaptureBlobSide]struct{}, len(e.Blobs))
	for _, blob := range e.Blobs {
		if err := blob.Validate(); err != nil {
			return err
		}
		if _, ok := seen[blob.Side]; ok {
			return ErrInvalidLocalCaptureCandidate
		}
		seen[blob.Side] = struct{}{}
	}
	return nil
}

// LocalCaptureBase is the resolved comparison base. Unborn captures use the
// object-format-specific empty tree returned by installed Git.
type LocalCaptureBase struct {
	ObjectFormat string
	ObjectID     ObjectID
	Unborn       bool
}

// Validate checks the base identity and prevents an all-zero fabricated ID.
func (b LocalCaptureBase) Validate() error {
	if b.ObjectFormat == "" || !validCaptureText(b.ObjectFormat) || b.ObjectID == "" {
		return ErrInvalidLocalCaptureCandidate
	}
	if _, err := NewObjectID(string(b.ObjectID)); err != nil || strings.Trim(string(b.ObjectID), "0") == "" {
		return ErrInvalidLocalCaptureCandidate
	}
	return nil
}

// LocalCaptureCandidate is one immutable, complete capture attempt. It has
// no generation number and no authority to publish or apply anything.
type LocalCaptureCandidate struct {
	Version      uint32
	RepositoryID domain.RepositoryID
	WorktreeID   domain.WorktreeID
	Base         LocalCaptureBase
	Index        LocalCaptureIndexEvidence
	Entries      []LocalCaptureEntry
	Patch        CaptureArtifact
	BlobSpool    CaptureArtifact
	Policy       CapturePolicyEvidence
	Consistency  CaptureConsistencyEvidence
	Fingerprint  string
	EntryCount   uint64
	TotalBytes   uint64
	CapturedAt   time.Time
}

// Validate checks completeness and deterministic ordering. It intentionally
// does not inspect spool paths or reopen artifacts; T009 owns adoption.
func (c LocalCaptureCandidate) Validate() error {
	if c.Version != LocalCaptureCandidateVersion || c.RepositoryID == "" || c.WorktreeID == "" || c.CapturedAt.IsZero() || !validCaptureSHA256(c.Fingerprint) {
		return ErrInvalidLocalCaptureCandidate
	}
	if err := c.Base.Validate(); err != nil || c.Index.Validate() != nil || c.Policy.Validate() != nil || c.Consistency.Validate() != nil || c.Patch.Validate() != nil || c.Patch.Kind != CaptureArtifactPatch || c.BlobSpool.Validate() != nil || c.BlobSpool.Kind != CaptureArtifactBlobs {
		return ErrInvalidLocalCaptureCandidate
	}
	if uint64(len(c.Entries)) != c.EntryCount {
		return ErrInvalidLocalCaptureCandidate
	}
	if c.Patch.Bytes > ^uint64(0)-c.BlobSpool.Bytes || c.TotalBytes != c.Patch.Bytes+c.BlobSpool.Bytes {
		return ErrInvalidLocalCaptureCandidate
	}
	previous := ""
	for _, entry := range c.Entries {
		if err := entry.Validate(); err != nil {
			return err
		}
		path := ""
		if entry.Change.NewPath != nil {
			path = string(*entry.Change.NewPath)
		} else if entry.Change.OldPath != nil {
			path = string(*entry.Change.OldPath)
		}
		if path == "" || (previous != "" && path <= previous) {
			return ErrInvalidLocalCaptureCandidate
		}
		previous = path
	}
	expected, err := c.FingerprintValue()
	if err != nil || expected != c.Fingerprint {
		return ErrInvalidLocalCaptureCandidate
	}
	return nil
}

// Fingerprint computes the candidate identity from canonical evidence. The
// timestamp and spool marker identities are deliberately excluded.
func (c LocalCaptureCandidate) FingerprintValue() (string, error) {
	if err := c.validateForFingerprint(); err != nil {
		return "", err
	}
	h := sha256.New()
	writeCaptureString(h, fmt.Sprint(c.Version))
	writeCaptureString(h, string(c.RepositoryID))
	writeCaptureString(h, string(c.WorktreeID))
	writeCaptureString(h, c.Base.ObjectFormat)
	writeCaptureString(h, string(c.Base.ObjectID))
	writeCaptureUint64(h, boolUint(c.Base.Unborn))
	writeCaptureString(h, c.Index.SHA256)
	writeCaptureString(h, captureIndexEntriesFingerprint(c.Index.Entries))
	writeCaptureString(h, c.Consistency.HeadToken)
	writeCaptureString(h, c.Consistency.IndexToken)
	writeCaptureString(h, c.Consistency.StatusToken)
	writeCaptureString(h, c.Consistency.FlagsToken)
	writeCaptureString(h, c.Consistency.FilesystemToken)
	writeCaptureUint64(h, uint64(c.Policy.MachineGitVersion))
	writeCaptureUint64(h, uint64(c.Policy.RenameVersion))
	writeCaptureUint64(h, uint64(c.Policy.RenameDeleteCandidates))
	writeCaptureUint64(h, uint64(c.Policy.RenameAddCandidates))
	writeCaptureUint64(h, uint64(c.Policy.RenameSimilarityPercent))
	writeCaptureUint64(h, c.Policy.RenameMaxDeleteSources)
	writeCaptureUint64(h, c.Policy.RenameMaxAddTargets)
	writeCaptureUint64(h, boolUint(c.Policy.RenameDetectChangedSourceCopies))
	writeCaptureUint64(h, boolUint(c.Policy.RenameFindCopiesHarder))
	for _, flag := range c.Policy.RenameFlags {
		writeCaptureString(h, flag)
	}
	writeCaptureString(h, c.Policy.RenameEvidenceHash)
	writeCaptureUint64(h, uint64(c.Policy.PatchFormatVersion))
	writeCaptureUint64(h, uint64(c.Policy.ConversionPolicyVersion))
	writeCaptureString(h, c.Policy.ConversionDecision)
	writeCaptureString(h, c.Policy.ConversionReason)
	writeCaptureString(h, c.Policy.ConversionFingerprint)
	writeCaptureUint64(h, boolUint(c.Policy.AttributesChanged))
	writeCaptureUint64(h, uint64(c.Policy.ResourcePolicyVersion))
	writeCaptureString(h, c.Policy.RenameOutcome)
	writeCaptureString(h, c.Patch.ContentSHA256)
	for _, entry := range c.Entries {
		writeCaptureString(h, string(entry.Change.Kind))
		if entry.Change.OldPath != nil {
			writeCaptureString(h, string(*entry.Change.OldPath))
		}
		if entry.Change.NewPath != nil {
			writeCaptureString(h, string(*entry.Change.NewPath))
		}
		writeCaptureUint64(h, uint64(entry.Change.OldMode))
		writeCaptureUint64(h, uint64(entry.Change.NewMode))
		writeCaptureString(h, string(entry.Change.OldFileKind))
		writeCaptureString(h, string(entry.Change.NewFileKind))
		if entry.Change.ModeTransition != nil {
			writeCaptureString(h, entry.Change.ModeTransition.EvidenceHash)
			writeCaptureString(h, entry.Change.ModeTransition.PolicyVersion)
			writeCaptureString(h, string(entry.Change.ModeTransition.Kind))
		} else {
			writeCaptureString(h, "")
			writeCaptureString(h, "")
			writeCaptureString(h, "")
		}
		if entry.Change.OldObjectID != nil {
			writeCaptureString(h, string(*entry.Change.OldObjectID))
		}
		if entry.Change.NewObjectID != nil {
			writeCaptureString(h, string(*entry.Change.NewObjectID))
		}
		writeCaptureReviewOnly(h, entry.Change.ReviewOnly)
		writeCaptureUint64(h, boolUint(entry.Change.Staged))
		writeCaptureUint64(h, boolUint(entry.Change.Unstaged))
		writeCaptureUint64(h, boolUint(entry.Change.Binary))
		writeCaptureString(h, string(entry.Change.ContentClass))
		writeCaptureTextSemantics(h, entry.Change.OldTextSemantics)
		writeCaptureTextSemantics(h, entry.Change.NewTextSemantics)
		if entry.Change.Conflict != nil {
			writeCaptureString(h, entry.Change.Conflict.Code)
		}
		if entry.Change.Rename != nil {
			writeCaptureUint64(h, uint64(entry.Change.Rename.PolicyVersion))
			writeCaptureUint64(h, uint64(entry.Change.Rename.SimilarityPercent))
			writeCaptureString(h, string(entry.Change.Rename.Kind))
			writeCaptureString(h, entry.Change.Rename.EvidenceHash)
		}
		for _, blob := range entry.Blobs {
			writeCaptureString(h, string(blob.Side))
			writeCaptureString(h, blob.Artifact.ContentSHA256)
			writeCaptureString(h, string(blob.ContentClass))
			writeCaptureTextSemantics(h, blob.TextSemantics)
		}
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func writeCaptureTextSemantics(h interface{ Write([]byte) (int, error) }, value *TextByteSemantics) {
	if value == nil {
		writeCaptureUint64(h, 0)
		return
	}
	writeCaptureUint64(h, 1)
	writeCaptureString(h, string(value.Encoding))
	writeCaptureUint64(h, value.ByteLength)
	writeCaptureString(h, value.SHA256)
	writeCaptureUint64(h, boolUint(value.HasBOM))
	writeCaptureUint64(h, value.Endings.LFCount)
	writeCaptureUint64(h, value.Endings.CRLFCount)
	writeCaptureUint64(h, value.Endings.CRCount)
	writeCaptureUint64(h, boolUint(value.Endings.FinalLF))
	writeCaptureUint64(h, boolUint(value.Endings.Mixed))
	writeCaptureUint64(h, boolUint(value.Empty))
}

func writeCaptureReviewOnly(h interface{ Write([]byte) (int, error) }, value *ReviewOnlyEntryEvidence) {
	if value == nil {
		writeCaptureUint64(h, 0)
		return
	}
	writeCaptureUint64(h, 1)
	writeCaptureString(h, string(value.SpecialKind))
	writeCaptureString(h, string(value.MetadataLevel))
	writeCaptureString(h, value.MetadataHash)
	writeCaptureString(h, value.ReasonCode)
	writeCaptureString(h, value.EvidenceVersion)
}

func (c LocalCaptureCandidate) validateForFingerprint() error {
	if c.Version != LocalCaptureCandidateVersion || c.RepositoryID == "" || c.WorktreeID == "" {
		return ErrInvalidLocalCaptureCandidate
	}
	if err := c.Base.Validate(); err != nil || c.Index.Validate() != nil || c.Policy.Validate() != nil {
		return ErrInvalidLocalCaptureCandidate
	}
	for _, value := range []string{c.Consistency.HeadToken, c.Consistency.IndexToken, c.Consistency.StatusToken, c.Consistency.FlagsToken, c.Consistency.FilesystemToken} {
		if !validCaptureSHA256(value) {
			return ErrInvalidLocalCaptureCandidate
		}
	}
	for _, entry := range c.Entries {
		if err := entry.Validate(); err != nil {
			return err
		}
	}
	return nil
}

func boolUint(value bool) uint64 {
	if value {
		return 1
	}
	return 0
}

func writeCaptureString(h interface{ Write([]byte) (int, error) }, value string) {
	writeCaptureUint64(h, uint64(len(value)))
	_, _ = h.Write([]byte(value))
}

func writeCaptureUint64(h interface{ Write([]byte) (int, error) }, value uint64) {
	var encoded [8]byte
	for index := range encoded {
		encoded[len(encoded)-index-1] = byte(value >> (index * 8))
	}
	_, _ = h.Write(encoded[:])
}

func captureIndexEntriesFingerprint(entries []CaptureIndexEntry) string {
	h := sha256.New()
	for _, entry := range entries {
		writeCaptureString(h, string(entry.Path))
		writeCaptureUint64(h, uint64(entry.Stage))
		writeCaptureUint64(h, uint64(entry.Mode))
		writeCaptureString(h, string(entry.ObjectID))
		for _, flag := range entry.Flags {
			writeCaptureString(h, string(flag))
		}
	}
	return hex.EncodeToString(h.Sum(nil))
}

func validCaptureSHA256(value string) bool {
	if len(value) != sha256.Size*2 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func validCaptureText(value string) bool {
	return value != "" && !strings.ContainsRune(value, 0) && strings.TrimSpace(value) == value
}

// SortedEntries returns a defensive path-ordered copy for adapter and adoption
// code that needs a stable manifest traversal.
func (c LocalCaptureCandidate) SortedEntries() []LocalCaptureEntry {
	entries := append([]LocalCaptureEntry(nil), c.Entries...)
	sort.SliceStable(entries, func(i, j int) bool {
		return captureEntryPath(entries[i]) < captureEntryPath(entries[j])
	})
	return entries
}

func captureEntryPath(entry LocalCaptureEntry) string {
	if entry.Change.NewPath != nil {
		return string(*entry.Change.NewPath)
	}
	if entry.Change.OldPath != nil {
		return string(*entry.Change.OldPath)
	}
	return ""
}
