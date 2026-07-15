package review

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"time"
	"unicode/utf8"

	"github.com/Scottlr/nudge/internal/domain"
	"github.com/Scottlr/nudge/internal/domain/repository"
)

const (
	// MaxProposalSummaryBytes bounds the user-confirmed request-change summary.
	MaxProposalSummaryBytes = 64 << 10
	// MaxProposalExpectedPaths bounds the number of paths in one confirmed intent.
	MaxProposalExpectedPaths = 5000
	// MaxProposalExpectedPathBytes bounds one raw repository path in an intent.
	MaxProposalExpectedPathBytes = 16 << 10
	// MaxProposalExpectedPathTotalBytes bounds all raw intent path bytes.
	MaxProposalExpectedPathTotalBytes = 512 << 10
	// MaxProposalFiles bounds the file metadata retained by one proposal version.
	MaxProposalFiles = 5000
)

var (
	// ErrInvalidProposal reports incomplete or contradictory proposal evidence.
	ErrInvalidProposal = errors.New("invalid proposal")
	// ErrInvalidProposalTransition reports a lifecycle transition that cannot
	// preserve proposal history or safety invariants.
	ErrInvalidProposalTransition = errors.New("invalid proposal transition")
	// ErrProposalImmutable reports an attempted rewrite of ready proposal data.
	ErrProposalImmutable = errors.New("proposal is immutable")
	// ErrProposalConflict reports competing durable proposal publication.
	ErrProposalConflict = errors.New("proposal conflict")
)

// GenerationProvenance binds a proposal to the accepted review generation
// that supplied its source bytes. It is distinct from destination constraints.
type GenerationProvenance struct {
	SessionID  domain.ReviewSessionID
	Generation repository.TargetGeneration
	CaptureID  *domain.CaptureID
	Base       repository.SnapshotRef
	Head       repository.SnapshotRef
}

// Validate checks generation identity without resolving Git or reading paths.
func (p GenerationProvenance) Validate() error {
	if p.SessionID == "" || p.Generation == 0 || p.Base.Validate() != nil || p.Head.Validate() != nil {
		return ErrInvalidProposal
	}
	if p.CaptureID != nil && *p.CaptureID == "" {
		return ErrInvalidProposal
	}
	if p.Head.Kind == repository.SnapshotWorkingTree && p.CaptureID == nil {
		return ErrInvalidProposal
	}
	return nil
}

// ProposalIntent is the durable, user-confirmed request-change lineage. A
// draft that fails validation must never be persisted as an intent.
type ProposalIntent struct {
	ID               domain.ProposalID
	ThreadID         domain.ReviewThreadID
	Summary          string
	ExpectedPaths    []repository.RepoPath
	AnchorVersionID  uint64
	ConfirmedAgainst GenerationProvenance
	ConfirmedAt      time.Time
}

// NewProposalIntent validates and defensively copies a confirmed intent.
func NewProposalIntent(intent ProposalIntent) (ProposalIntent, error) {
	if err := intent.Validate(); err != nil {
		return ProposalIntent{}, err
	}
	intent.ExpectedPaths = cloneRepoPaths(intent.ExpectedPaths)
	return intent, nil
}

// Validate enforces exact boundary limits without normalizing raw path bytes.
func (i ProposalIntent) Validate() error {
	if i.ID == "" || i.ThreadID == "" || i.Summary == "" || !utf8.ValidString(i.Summary) || len([]byte(i.Summary)) > MaxProposalSummaryBytes || i.AnchorVersionID == 0 || i.ConfirmedAt.IsZero() {
		return ErrInvalidProposal
	}
	if err := i.ConfirmedAgainst.Validate(); err != nil {
		return err
	}
	if len(i.ExpectedPaths) > MaxProposalExpectedPaths {
		return ErrInvalidProposal
	}
	var total int
	seen := make(map[repository.RepoPathKey]struct{}, len(i.ExpectedPaths))
	var previous repository.RepoPathKey
	for index, path := range i.ExpectedPaths {
		if path.Validate() != nil || len(path) > MaxProposalExpectedPathBytes {
			return ErrInvalidProposal
		}
		total += len(path)
		if total > MaxProposalExpectedPathTotalBytes {
			return ErrInvalidProposal
		}
		key := path.Key()
		if _, exists := seen[key]; exists || (index > 0 && string(previous) >= string(key)) {
			return ErrInvalidProposal
		}
		seen[key] = struct{}{}
		previous = key
	}
	return nil
}

// SnapshotIdentity identifies an immutable baseline or result snapshot and
// retains its independent manifest identity.
type SnapshotIdentity struct {
	ID           domain.ReviewSnapshotID
	Ref          repository.SnapshotRef
	ManifestHash string
}

func (s SnapshotIdentity) Validate() error {
	if s.ID == "" || s.Ref.Validate() != nil || !validSHA256(s.ManifestHash) {
		return ErrInvalidProposal
	}
	return nil
}

// WorkspaceRoots are opaque, adapter-verified native roots. The domain only
// persists their identity and never treats them as repository-relative paths.
type WorkspaceRoots struct {
	Baseline    string
	Admin       string
	Result      string
	Destination string
}

func (r WorkspaceRoots) Validate(required bool) error {
	values := []string{r.Baseline, r.Admin, r.Result, r.Destination}
	for _, value := range values {
		if value == "" && required || value != "" && !utf8.ValidString(value) {
			return ErrInvalidProposal
		}
	}
	return nil
}

// ProposalWorkspace is the durable identity and lifecycle of one isolated
// four-root workspace. Workspace identity is not proposal lineage identity.
type ProposalWorkspace struct {
	ID               domain.WorkspaceID
	RepositoryID     domain.RepositoryID
	WorktreeID       domain.WorktreeID
	SessionID        domain.ReviewSessionID
	SourceThreadID   domain.ReviewThreadID
	SourceGeneration GenerationProvenance
	Roots            WorkspaceRoots
	PolicyVersion    uint64
	State            WorkspaceState
	CreatedAt        time.Time
	UpdatedAt        time.Time
}

func (w ProposalWorkspace) Validate() error {
	if w.ID == "" || w.RepositoryID == "" || w.WorktreeID == "" || w.SessionID == "" || w.SourceThreadID == "" || w.PolicyVersion == 0 || w.CreatedAt.IsZero() || w.UpdatedAt.IsZero() || w.UpdatedAt.Before(w.CreatedAt) {
		return ErrInvalidProposal
	}
	if err := w.SourceGeneration.Validate(); err != nil || w.SourceGeneration.SessionID != w.SessionID {
		return ErrInvalidProposal
	}
	if w.State.Validate() != nil || w.Roots.Validate(w.State != WorkspaceCreating) != nil {
		return ErrInvalidProposal
	}
	return nil
}

// Proposal records one stable request-change lineage and its current
// aggregate status. Versions are retained separately and never overwritten.
type Proposal struct {
	ID             domain.ProposalID
	WorkspaceID    domain.WorkspaceID
	ThreadID       domain.ReviewThreadID
	Status         ProposalStatus
	CurrentVersion *ProposalVersionNumber
	// ApplyingOperationID links the mutable aggregate projection to the
	// durable apply journal without changing the immutable proposal version.
	ApplyingOperationID *domain.OperationID
	CreatedAt           time.Time
	UpdatedAt           time.Time
}

func (p Proposal) Validate() error {
	if p.ID == "" || p.WorkspaceID == "" || p.ThreadID == "" || p.Status.Validate() != nil || p.CreatedAt.IsZero() || p.UpdatedAt.IsZero() || p.UpdatedAt.Before(p.CreatedAt) {
		return ErrInvalidProposal
	}
	if p.CurrentVersion != nil && *p.CurrentVersion == 0 {
		return ErrInvalidProposal
	}
	if p.Status == ProposalVersionApplying && p.ApplyingOperationID == nil || p.ApplyingOperationID != nil && *p.ApplyingOperationID == "" {
		return ErrInvalidProposal
	}
	return nil
}

// ProposalAttempt records one provider-turn derivation attempt, including
// terminal no-change evidence that is not itself a proposal version.
type ProposalAttempt struct {
	ID                         domain.OperationID
	ProposalID                 domain.ProposalID
	WorkspaceID                domain.WorkspaceID
	ThreadID                   domain.ReviewThreadID
	ProviderConversationID     *domain.ProviderConversationID
	ProviderTurnID             *domain.ProviderTurnID
	ProviderConversationRef    string
	ProviderTurnRef            string
	SourceGeneration           GenerationProvenance
	Baseline                   *SnapshotIdentity
	Result                     *SnapshotIdentity
	VersionNumber              *ProposalVersionNumber
	Outcome                    ProposalAttemptOutcome
	ResultDisposition          ProposalResultDisposition
	FailurePhase               ProposalFailurePhase
	Reason                     string
	ResultDispositionReason    string
	StartedAt                  time.Time
	FinishedAt                 *time.Time
	ResultDispositionChangedAt *time.Time
}

func (a ProposalAttempt) Validate() error {
	if a.ID == "" || a.ProposalID == "" || a.WorkspaceID == "" || a.ThreadID == "" || a.SourceGeneration.Validate() != nil || a.Outcome.Validate() != nil || a.ResultDisposition.Validate() != nil || a.FailurePhase.Validate() != nil || a.StartedAt.IsZero() || !utf8.ValidString(a.Reason) || !utf8.ValidString(a.ResultDispositionReason) || !utf8.ValidString(a.ProviderConversationRef) || !utf8.ValidString(a.ProviderTurnRef) {
		return ErrInvalidProposal
	}
	if a.ProviderConversationID != nil && *a.ProviderConversationID == "" || a.ProviderTurnID != nil && *a.ProviderTurnID == "" {
		return ErrInvalidProposal
	}
	if a.Baseline != nil && a.Baseline.Validate() != nil || a.Result != nil && a.Result.Validate() != nil {
		return ErrInvalidProposal
	}
	if a.VersionNumber != nil && *a.VersionNumber == 0 {
		return ErrInvalidProposal
	}
	if a.FinishedAt != nil && (a.FinishedAt.IsZero() || a.FinishedAt.Before(a.StartedAt)) || a.ResultDispositionChangedAt != nil && (a.ResultDispositionChangedAt.IsZero() || a.ResultDispositionChangedAt.Before(a.StartedAt)) {
		return ErrInvalidProposal
	}
	if a.Outcome == ProposalAttemptVersionPublished && a.VersionNumber == nil {
		return ErrInvalidProposal
	}
	if a.Outcome == ProposalAttemptNoChanges && (a.VersionNumber != nil || a.ProviderTurnID == nil || a.Baseline == nil || a.Result == nil || a.ResultDisposition != ProposalResultDiscarded) {
		return ErrInvalidProposal
	}
	if a.Outcome == ProposalAttemptDeriving && a.FinishedAt != nil {
		return ErrInvalidProposal
	}
	return nil
}

// DestinationConstraints capture the destination generation independently of
// the source generation that produced a proposal patch.
type DestinationConstraints struct {
	TargetKind                     repository.TargetKind
	WorktreeID                     domain.WorktreeID
	ExpectedHead                   repository.ObjectID
	ExpectedWorkingTreeFingerprint string
}

func (d DestinationConstraints) Validate() error {
	if d.WorktreeID == "" {
		return ErrInvalidProposal
	}
	switch d.TargetKind {
	case repository.TargetLocal:
		if d.ExpectedHead != "" || d.ExpectedWorkingTreeFingerprint == "" || !utf8.ValidString(d.ExpectedWorkingTreeFingerprint) {
			return ErrInvalidProposal
		}
	case repository.TargetCommit, repository.TargetBranch:
		if d.ExpectedHead == "" || repositorySnapshotObjectID(d.ExpectedHead) != nil || d.ExpectedWorkingTreeFingerprint != "" {
			return ErrInvalidProposal
		}
	default:
		return ErrInvalidProposal
	}
	return nil
}

// ProposedFile is complete, path-identity-preserving file metadata for one
// immutable proposal version.
type ProposedFile struct {
	Path             repository.RepoPath
	OldPath          *repository.RepoPath
	OldKind          repository.FileKind
	Kind             repository.FileKind
	OldMode          uint32
	Mode             uint32
	ModeTransition   *repository.ModeTransition
	ContentBytes     uint64
	ContentHash      string
	OldContentBytes  uint64
	OldContentHash   string
	ContentClass     repository.ContentClassV1
	OldContentClass  repository.ContentClassV1
	TextSemantics    *repository.TextByteSemantics
	OldTextSemantics *repository.TextByteSemantics
	Added            bool
	Deleted          bool
	Copied           bool
	TypeChanged      bool
	Binary           bool
}

func (f ProposedFile) Validate() error {
	if f.Path.Validate() != nil || f.Added && f.Deleted || f.Deleted && (f.Mode != 0 || f.Kind != repository.FileKindUnknown || f.ContentBytes != 0 || f.ContentClass != "" || f.TextSemantics != nil || f.ModeTransition != nil) || !f.Deleted && (!validFileKind(f.Kind) || f.Kind == repository.FileKindUnknown || repository.ValidateGitMode(f.Mode) != nil) || (f.OldPath != nil && f.OldPath.Validate() != nil) || (f.OldPath == nil && (f.OldKind != "" || f.OldMode != 0 || f.OldContentBytes != 0 || f.OldContentHash != "" || f.OldContentClass != "" || f.OldTextSemantics != nil || f.ModeTransition != nil)) || (f.OldPath != nil && (!validFileKind(f.OldKind) || f.OldKind == repository.FileKindUnknown || repository.ValidateGitMode(f.OldMode) != nil)) || (f.ContentHash != "" && !validSHA256(f.ContentHash)) || (f.OldContentHash != "" && !validSHA256(f.OldContentHash)) || f.ContentClass != "" && f.ContentClass.Validate() != nil || f.OldContentClass != "" && f.OldContentClass.Validate() != nil {
		return ErrInvalidProposal
	}
	if f.Added && f.OldPath != nil || f.Deleted && f.OldPath != nil {
		return ErrInvalidProposal
	}
	if f.ModeTransition != nil {
		if f.OldPath == nil || f.ModeTransition.Validate() != nil || f.ModeTransition.OldMode != f.OldMode || f.ModeTransition.NewMode != f.Mode {
			return ErrInvalidProposal
		}
		if f.ModeTransition.Kind == repository.ModeTypeChanged && !f.TypeChanged {
			return ErrInvalidProposal
		}
	} else if f.OldPath != nil && (f.OldMode != f.Mode || f.OldKind != f.Kind) {
		return ErrInvalidProposal
	}
	if f.OldPath != nil && !modeMatchesFileKind(f.OldMode, f.OldKind) || !f.Deleted && !modeMatchesFileKind(f.Mode, f.Kind) {
		return ErrInvalidProposal
	}
	if f.Copied && (f.OldPath == nil || f.Added || f.Deleted || f.TypeChanged || f.OldContentHash == "") {
		return ErrInvalidProposal
	}
	if f.ContentClass != "" && f.ContentClass.IsByteOriented() && !f.Binary || f.OldContentClass != "" && f.OldContentClass.IsByteOriented() && !f.Binary || f.TextSemantics != nil && (f.ContentClass != repository.ContentClassRegularTextUTF8 || f.TextSemantics.Validate() != nil || f.TextSemantics.ByteLength != f.ContentBytes || f.TextSemantics.SHA256 != f.ContentHash) || f.OldTextSemantics != nil && (f.OldContentClass != repository.ContentClassRegularTextUTF8 || f.OldTextSemantics.Validate() != nil || f.OldTextSemantics.ByteLength != f.OldContentBytes || f.OldTextSemantics.SHA256 != f.OldContentHash) {
		return ErrInvalidProposal
	}
	return nil
}

// ProposedPatchArtifactReference identifies the independently adopted T111
// patch and review index without copying patch bytes into the proposal row.
type ProposedPatchArtifactReference struct {
	ArtifactID              string
	SpoolID                 string
	ManifestHash            string
	PatchFormatVersion      uint32
	RenamePolicyVersion     uint32
	ConversionPolicyVersion uint32
	PatchSHA256             string
	PatchBytes              uint64
	IndexHash               string
	FileCount               uint64
	HunkCount               uint64
	RowCount                uint64
	BinaryFiles             uint64
}

// Validate checks the immutable artifact identity and bounded review summary.
func (r ProposedPatchArtifactReference) Validate() error {
	if r.ArtifactID == "" || r.SpoolID == "" || !validSHA256(r.ManifestHash) || r.PatchFormatVersion == 0 || r.RenamePolicyVersion == 0 || r.ConversionPolicyVersion == 0 || !validSHA256(r.PatchSHA256) || r.PatchBytes == 0 || !validSHA256(r.IndexHash) || r.FileCount == 0 || r.BinaryFiles > r.FileCount {
		return ErrInvalidProposal
	}
	return nil
}

// ProposedPatch is the immutable, displayable patch artifact derived from an
// attempt's baseline and result snapshots.
type ProposedPatch struct {
	ProposalID              domain.ProposalID
	WorkspaceID             domain.WorkspaceID
	ThreadID                domain.ReviewThreadID
	AttemptID               domain.OperationID
	ProviderConversationRef string
	ProviderTurnRef         string
	SourceGeneration        GenerationProvenance
	Baseline                SnapshotIdentity
	Result                  SnapshotIdentity
	Destination             DestinationConstraints
	Version                 ProposalVersionNumber
	PatchFormat             string
	PatchBytes              []byte
	PatchSHA256             string
	Artifact                ProposedPatchArtifactReference
	Files                   []ProposedFile
	Preconditions           []repository.PathPrecondition
	Scope                   ProposalScope
	ScopeReason             string
	Status                  ProposalStatus
	StatusReason            string
	StatusChangedAt         *time.Time
	CreatedAt               time.Time
}

func NewProposedPatch(patch ProposedPatch) (ProposedPatch, error) {
	if err := patch.Validate(); err != nil {
		return ProposedPatch{}, err
	}
	patch.PatchBytes = append([]byte(nil), patch.PatchBytes...)
	patch.Files = append([]ProposedFile(nil), patch.Files...)
	for index := range patch.Files {
		if patch.Files[index].ModeTransition != nil {
			transition := *patch.Files[index].ModeTransition
			patch.Files[index].ModeTransition = &transition
		}
		if patch.Files[index].TextSemantics != nil {
			semantics := *patch.Files[index].TextSemantics
			patch.Files[index].TextSemantics = &semantics
		}
		if patch.Files[index].OldTextSemantics != nil {
			semantics := *patch.Files[index].OldTextSemantics
			patch.Files[index].OldTextSemantics = &semantics
		}
	}
	patch.Preconditions = append([]repository.PathPrecondition(nil), patch.Preconditions...)
	return patch, nil
}

func modeClassForFileKind(kind repository.FileKind) repository.GitModeClass {
	switch kind {
	case repository.FileKindRegular:
		return repository.ModeRegularNonExecutable
	case repository.FileKindSymlink:
		return repository.ModeSymlink
	case repository.FileKindGitlink:
		return repository.ModeGitlink
	case repository.FileKindDirectory:
		return repository.ModeTree
	default:
		return repository.ModeUnsupported
	}
}

func modeMatchesFileKind(mode uint32, kind repository.FileKind) bool {
	class := repository.ClassifyGitMode(mode)
	if kind == repository.FileKindRegular {
		return class == repository.ModeRegularNonExecutable || class == repository.ModeRegularExecutable
	}
	return class == modeClassForFileKind(kind)
}

func (p ProposedPatch) Validate() error {
	if p.ProposalID == "" || p.WorkspaceID == "" || p.ThreadID == "" || p.AttemptID == "" || p.SourceGeneration.Validate() != nil || p.Baseline.Validate() != nil || p.Result.Validate() != nil || p.Destination.Validate() != nil || p.Version == 0 || !utf8.ValidString(p.PatchFormat) || p.PatchFormat == "" || !validSHA256(p.PatchSHA256) || p.Scope.Validate() != nil || !utf8.ValidString(p.ScopeReason) || !utf8.ValidString(p.StatusReason) || p.Status.Validate() != nil || p.CreatedAt.IsZero() || p.StatusChangedAt != nil && (p.StatusChangedAt.IsZero() || p.StatusChangedAt.Before(p.CreatedAt)) {
		return ErrInvalidProposal
	}
	if p.Baseline.ID == p.Result.ID {
		return ErrInvalidProposal
	}
	if p.Artifact == (ProposedPatchArtifactReference{}) {
		if len(p.PatchBytes) == 0 {
			return ErrInvalidProposal
		}
		digest := sha256.Sum256(p.PatchBytes)
		if !equalHexDigest(p.PatchSHA256, digest[:]) {
			return ErrInvalidProposal
		}
	} else if p.Artifact.Validate() != nil || len(p.PatchBytes) != 0 || p.Artifact.PatchSHA256 != p.PatchSHA256 || p.Artifact.PatchBytes == 0 {
		return ErrInvalidProposal
	}
	if len(p.Files) == 0 || len(p.Files) > MaxProposalFiles {
		return ErrInvalidProposal
	}
	seen := make(map[repository.RepoPathKey]struct{}, len(p.Files))
	for _, file := range p.Files {
		if file.Validate() != nil {
			return ErrInvalidProposal
		}
		if _, exists := seen[file.Path.Key()]; exists {
			return ErrInvalidProposal
		}
		seen[file.Path.Key()] = struct{}{}
	}
	seen = make(map[repository.RepoPathKey]struct{}, len(p.Preconditions))
	for _, precondition := range p.Preconditions {
		if precondition.Validate() != nil {
			return ErrInvalidProposal
		}
		if _, exists := seen[precondition.Path.Key()]; exists {
			return ErrInvalidProposal
		}
		seen[precondition.Path.Key()] = struct{}{}
	}
	return nil
}

// ProposalAggregate is the durable load shape for one lineage. Old versions
// remain present and at most one version can be ready at once.
type ProposalAggregate struct {
	Workspace ProposalWorkspace
	Intent    ProposalIntent
	Proposal  Proposal
	Attempts  []ProposalAttempt
	Versions  []ProposedPatch
}

func (a ProposalAggregate) Validate() error {
	if a.Workspace.Validate() != nil || a.Intent.Validate() != nil || a.Proposal.Validate() != nil || a.Intent.ID != a.Proposal.ID || a.Proposal.WorkspaceID != a.Workspace.ID || a.Proposal.ThreadID != a.Workspace.SourceThreadID || a.Intent.ThreadID != a.Workspace.SourceThreadID {
		return ErrInvalidProposal
	}
	ready := 0
	versionNumbers := make(map[ProposalVersionNumber]struct{}, len(a.Versions))
	for _, attempt := range a.Attempts {
		if attempt.Validate() != nil || attempt.ProposalID != a.Proposal.ID || attempt.WorkspaceID != a.Workspace.ID || attempt.ThreadID != a.Workspace.SourceThreadID {
			return ErrInvalidProposal
		}
	}
	for _, version := range a.Versions {
		if version.Validate() != nil || version.ProposalID != a.Proposal.ID || version.WorkspaceID != a.Workspace.ID || version.ThreadID != a.Workspace.SourceThreadID {
			return ErrInvalidProposal
		}
		if _, exists := versionNumbers[version.Version]; exists {
			return ErrInvalidProposal
		}
		versionNumbers[version.Version] = struct{}{}
		if version.Status == ProposalVersionReady {
			ready++
		}
	}
	if ready > 1 {
		return ErrInvalidProposal
	}
	if a.Proposal.CurrentVersion != nil {
		if _, exists := versionNumbers[*a.Proposal.CurrentVersion]; !exists {
			return ErrInvalidProposal
		}
	}
	return nil
}

// ProposalTransition is the atomic status update consumed by the store port.
type ProposalTransition struct {
	ProposalID       domain.ProposalID
	Version          ProposalVersionNumber
	Status           ProposalStatus
	FailurePhase     ProposalFailurePhase
	Reason           string
	ApplyOperationID domain.OperationID
	ChangedAt        time.Time
}

func (t ProposalTransition) Validate() error {
	if t.ProposalID == "" || t.Version == 0 || t.Status.Validate() != nil || t.FailurePhase.Validate() != nil || t.Status == ProposalVersionApplying && t.ApplyOperationID == "" || !utf8.ValidString(t.Reason) || t.ChangedAt.IsZero() {
		return ErrInvalidProposal
	}
	return nil
}

func cloneRepoPaths(paths []repository.RepoPath) []repository.RepoPath {
	copyPaths := make([]repository.RepoPath, len(paths))
	for index, path := range paths {
		copyPaths[index] = repository.RepoPath(path.Bytes())
	}
	return copyPaths
}

func validSHA256(value string) bool {
	if len(value) != sha256.Size*2 || !utf8.ValidString(value) {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func equalHexDigest(value string, digest []byte) bool {
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == len(digest) && string(decoded) == string(digest)
}

func repositorySnapshotObjectID(id repository.ObjectID) error {
	_, err := repository.NewObjectID(string(id))
	return err
}

func validFileKind(kind repository.FileKind) bool {
	switch kind {
	case repository.FileKindRegular, repository.FileKindSymlink, repository.FileKindGitlink, repository.FileKindDirectory, repository.FileKindUnknown:
		return true
	default:
		return false
	}
}

// SortProposalPaths sorts raw repository path keys without Unicode
// normalization. It is useful to prepare an editable draft before intent
// validation; it does not remove duplicates or silently alter bytes.
func SortProposalPaths(paths []repository.RepoPath) {
	sort.Slice(paths, func(i, j int) bool { return string(paths[i].Key()) < string(paths[j].Key()) })
}

// String returns a bounded, stable description for logs without patch bytes.
func (p ProposedPatch) String() string {
	return fmt.Sprintf("proposal %s version %d (%s)", p.ProposalID, p.Version, p.Status)
}
