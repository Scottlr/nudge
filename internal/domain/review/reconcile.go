package review

import (
	"errors"
	"time"

	"github.com/Scottlr/nudge/internal/domain"
	"github.com/Scottlr/nudge/internal/domain/repository"
)

// AnchorReconciliationAlgorithmVersion identifies the ordered relocation
// tiers and candidate bounds used by ReconcileAnchor.
const AnchorReconciliationAlgorithmVersion uint32 = 1

const (
	// MaxAnchorReconciliationCandidates is the maximum evidence retained for
	// an ambiguous result.
	MaxAnchorReconciliationCandidates = 20
	// AnchorReconciliationWindow is the inclusive line-distance search window.
	AnchorReconciliationWindow = 200
)

var (
	// ErrInvalidReconcileInput reports incomplete or contradictory immutable
	// transition evidence.
	ErrInvalidReconcileInput = errors.New("invalid anchor reconciliation input")
	// ErrInvalidReconcileOutcome reports an outcome that cannot be staged.
	ErrInvalidReconcileOutcome = errors.New("invalid anchor reconciliation outcome")
)

// EvidenceTier identifies one deterministic relocation confidence tier.
type EvidenceTier string

const (
	EvidenceTierExactGeneration    EvidenceTier = "exact_generation"
	EvidenceTierExactContextAtLine EvidenceTier = "exact_context_at_line"
	EvidenceTierContextWindow      EvidenceTier = "context_window"
	EvidenceTierContextFile        EvidenceTier = "context_file"
	EvidenceTierSelectionWindow    EvidenceTier = "selection_window"
	EvidenceTierSelectionFile      EvidenceTier = "selection_file"
	EvidenceTierLineDiff           EvidenceTier = "line_diff"
	EvidenceTierOverflow           EvidenceTier = "overflow"

	// Short aliases keep call sites readable while retaining the serialized
	// names above as the compatibility contract.
	EvidenceExactGeneration    = EvidenceTierExactGeneration
	EvidenceExactContextAtLine = EvidenceTierExactContextAtLine
	EvidenceContextWindow      = EvidenceTierContextWindow
	EvidenceContextFile        = EvidenceTierContextFile
	EvidenceSelectionWindow    = EvidenceTierSelectionWindow
	EvidenceSelectionFile      = EvidenceTierSelectionFile
	EvidenceLineDiff           = EvidenceTierLineDiff
)

// AnchorCandidate is one bounded, displayable possible destination for an
// anchor that could not be selected with confidence.
type AnchorCandidate struct {
	Generation         repository.TargetGeneration
	SourcePath         repository.RepoPath
	Path               repository.RepoPath
	Side               repository.DiffSide
	StartLine          int
	EndLine            int
	ContentFingerprint string
	SelectedText       string
	BeforeContext      []string
	AfterContext       []string
	Tier               EvidenceTier
	Reason             string
}

func (c AnchorCandidate) Validate() error {
	if c.Generation == 0 || (c.Side != repository.DiffBase && c.Side != repository.DiffHead) || c.StartLine <= 0 || c.EndLine < c.StartLine || !validEvidenceTier(c.Tier) || !validMetadata(c.Reason) || !validFingerprintText(c.ContentFingerprint) || !validContent(c.SelectedText) {
		return ErrInvalidReconcileOutcome
	}
	if err := c.Path.Validate(); err != nil {
		return ErrInvalidReconcileOutcome
	}
	if len(c.SourcePath) > 0 {
		if err := c.SourcePath.Validate(); err != nil {
			return ErrInvalidReconcileOutcome
		}
	}
	if len([]byte(c.SelectedText)) > MaxAnchorCandidateEvidenceBytes {
		return ErrInvalidReconcileOutcome
	}
	contextBytes := 0
	for _, line := range append(append([]string(nil), c.BeforeContext...), c.AfterContext...) {
		if !validContent(line) {
			return ErrInvalidReconcileOutcome
		}
		contextBytes += len([]byte(line))
	}
	if contextBytes > MaxAnchorCandidateEvidenceBytes {
		return ErrInvalidReconcileOutcome
	}
	return nil
}

// MaxAnchorCandidateEvidenceBytes bounds the display and persistence evidence
// carried by one manual-reattachment candidate.
const MaxAnchorCandidateEvidenceBytes = 256 << 10

// CapturedFile is immutable, capture-owned text for exactly one path and
// diff side. Lines contain original text without line terminators.
type CapturedFile struct {
	Path            repository.RepoPath
	Side            repository.DiffSide
	ContentIdentity string
	Lines           []string
}

// CapturedContent is a compatibility alias for callers that use the design's
// content terminology.
type CapturedContent = CapturedFile

func (f CapturedFile) Validate() error {
	if err := f.Path.Validate(); err != nil || (f.Side != repository.DiffBase && f.Side != repository.DiffHead) || !validFingerprintText(f.ContentIdentity) {
		return ErrInvalidReconcileInput
	}
	for _, line := range f.Lines {
		if !validFingerprintText(line) {
			return ErrInvalidReconcileInput
		}
	}
	return nil
}

// PathIdentity records immutable old/new content identity for one path. T024
// supplies it from accepted capture manifests; T023 never derives it from a
// live worktree.
type PathIdentity struct {
	OldPath            repository.RepoPath
	NewPath            repository.RepoPath
	Side               repository.DiffSide
	OldContentIdentity string
	NewContentIdentity string
}

func (p PathIdentity) Validate() error {
	if err := p.OldPath.Validate(); err != nil {
		return ErrInvalidReconcileInput
	}
	if err := p.NewPath.Validate(); err != nil || (p.Side != repository.DiffBase && p.Side != repository.DiffHead) || !validFingerprintText(p.OldContentIdentity) || !validFingerprintText(p.NewContentIdentity) {
		return ErrInvalidReconcileInput
	}
	return nil
}

// RenameMapping is path-scope evidence only. It never proves that an anchor
// belongs at a destination; content or context evidence must still match.
type RenameMapping struct {
	OldPath           repository.RepoPath
	NewPath           repository.RepoPath
	Side              repository.DiffSide
	SimilarityPercent uint8
	Kind              repository.ChangeKind
	EvidenceHash      string
}

func (m RenameMapping) Validate() error {
	if err := m.OldPath.Validate(); err != nil {
		return ErrInvalidReconcileInput
	}
	if err := m.NewPath.Validate(); err != nil || string(m.OldPath) == string(m.NewPath) || (m.Side != repository.DiffBase && m.Side != repository.DiffHead) {
		return ErrInvalidReconcileInput
	}
	if m.SimilarityPercent != 0 || m.Kind != "" || m.EvidenceHash != "" {
		if m.SimilarityPercent < 60 || m.SimilarityPercent > 100 || m.Kind != repository.ChangeRenamed && m.Kind != repository.ChangeCopied || len(m.EvidenceHash) != 64 {
			return ErrInvalidReconcileInput
		}
	}
	return nil
}

// RenamePolicyEvidence is the adapter-neutral subset of RenamePolicyV1 that
// must agree before a mapping can change reconciliation search scope.
type RenamePolicyEvidence struct {
	Version                   uint32
	SimilarityPercent         uint8
	MaxDeleteSources          int
	MaxAddTargets             int
	DetectChangedSourceCopies bool
	FindCopiesHarder          bool
	Outcome                   string
	DeleteCandidates          int
	AddCandidates             int
	Flags                     []string
	EvidenceHash              string
}

func (e RenamePolicyEvidence) Complete() bool {
	return e.Validate() == nil && e.Outcome == "complete" && len(e.Flags) > 0 && len(e.EvidenceHash) == 64
}

func (e RenamePolicyEvidence) Validate() error {
	if e.Version != 1 || e.SimilarityPercent != 60 || e.MaxDeleteSources != 1000 || e.MaxAddTargets != 1000 || !e.DetectChangedSourceCopies || e.FindCopiesHarder || (e.Outcome != "complete" && e.Outcome != "rename_detection_limited" && e.Outcome != "skipped") || e.DeleteCandidates < 0 || e.AddCandidates < 0 || e.DeleteCandidates > e.MaxDeleteSources || e.AddCandidates > e.MaxAddTargets {
		return ErrInvalidReconcileInput
	}
	if e.Outcome == "rename_detection_limited" && e.DeleteCandidates < e.MaxDeleteSources && e.AddCandidates < e.MaxAddTargets {
		return ErrInvalidReconcileInput
	}
	if e.EvidenceHash != "" && (len(e.Flags) == 0 || len(e.EvidenceHash) != 64) {
		return ErrInvalidReconcileInput
	}
	if e.EvidenceHash != "" && repository.RenamePolicyEvidenceHash(uint32(e.Version), e.SimilarityPercent, e.MaxDeleteSources, e.MaxAddTargets, e.DetectChangedSourceCopies, e.FindCopiesHarder, e.Outcome, e.DeleteCandidates, e.AddCandidates, e.Flags) != e.EvidenceHash {
		return ErrInvalidReconcileInput
	}
	return nil
}

// GenerationTransition binds old persisted anchor evidence to one newly
// accepted capture. Optional snapshot refs are supplied by T024 when the
// resolved target changed; the relocator preserves the old refs otherwise.
type GenerationTransition struct {
	FromCaptureID      domain.CaptureID
	ToCaptureID        domain.CaptureID
	FromGeneration     repository.TargetGeneration
	ToGeneration       repository.TargetGeneration
	ChangedPaths       []PathIdentity
	RenameMappings     []RenameMapping
	RenameEvidence     RenamePolicyEvidence
	FromRenameEvidence RenamePolicyEvidence
	ToRenameEvidence   RenamePolicyEvidence
	NewBase            repository.SnapshotRef
	NewHead            repository.SnapshotRef
}

func (t GenerationTransition) Validate() error {
	if t.FromGeneration == 0 || t.ToGeneration <= t.FromGeneration || t.FromCaptureID == "" || t.ToCaptureID == "" {
		return ErrInvalidReconcileInput
	}
	if _, err := domain.NewCaptureID(string(t.FromCaptureID)); err != nil {
		return ErrInvalidReconcileInput
	}
	if _, err := domain.NewCaptureID(string(t.ToCaptureID)); err != nil {
		return ErrInvalidReconcileInput
	}
	if (t.NewBase.Kind != "" && t.NewBase.Validate() != nil) || (t.NewHead.Kind != "" && t.NewHead.Validate() != nil) {
		return ErrInvalidReconcileInput
	}
	for _, path := range t.ChangedPaths {
		if err := path.Validate(); err != nil {
			return err
		}
	}
	for _, mapping := range t.RenameMappings {
		if err := mapping.Validate(); err != nil {
			return err
		}
	}
	if renamePolicyEvidencePresent(t.RenameEvidence) && t.RenameEvidence.Validate() != nil {
		return ErrInvalidReconcileInput
	}
	if renamePolicyEvidencePresent(t.FromRenameEvidence) && t.FromRenameEvidence.Validate() != nil {
		return ErrInvalidReconcileInput
	}
	if renamePolicyEvidencePresent(t.ToRenameEvidence) && t.ToRenameEvidence.Validate() != nil {
		return ErrInvalidReconcileInput
	}
	return nil
}

func renamePolicyEvidencePresent(e RenamePolicyEvidence) bool {
	return e.Version != 0 || e.SimilarityPercent != 0 || e.MaxDeleteSources != 0 || e.MaxAddTargets != 0 || e.DetectChangedSourceCopies || e.FindCopiesHarder || e.Outcome != "" || e.DeleteCandidates != 0 || e.AddCandidates != 0 || len(e.Flags) != 0 || e.EvidenceHash != ""
}

// ReconcileInput is the complete pure input to one anchor relocation. New
// content is capture-owned; no field is a live filesystem path.
type ReconcileInput struct {
	Anchor          CodeAnchor
	Transition      GenerationTransition
	NewContent      CapturedFile
	PreviousContent *CapturedFile
	Now             time.Time
}

func (i ReconcileInput) Validate() error {
	if err := i.Anchor.Validate(); err != nil {
		return ErrInvalidReconcileInput
	}
	if err := i.Transition.Validate(); err != nil || i.NewContent.Validate() != nil || i.Now.IsZero() {
		return ErrInvalidReconcileInput
	}
	if i.Anchor.TargetGeneration != i.Transition.FromGeneration || i.NewContent.Side != i.Anchor.Side {
		return ErrInvalidReconcileInput
	}
	if i.PreviousContent != nil {
		if err := i.PreviousContent.Validate(); err != nil || string(i.PreviousContent.Path) != string(i.Anchor.Path) || i.PreviousContent.Side != i.Anchor.Side {
			return ErrInvalidReconcileInput
		}
	}
	return nil
}

// ReconcileOutcome is one immutable staged result. Candidates remain part of
// the result so ambiguous anchors can be presented without rescanning.
type ReconcileOutcome struct {
	Anchor            CodeAnchor
	Candidates        []AnchorCandidate
	State             AnchorState
	Reason            string
	AlgorithmVersion  uint32
	CandidateOverflow bool
}

func (o ReconcileOutcome) Validate() error {
	if err := o.Anchor.Validate(); err != nil || o.State.Validate() != nil || o.Anchor.State != o.State || !validMetadata(o.Reason) || o.AlgorithmVersion != AnchorReconciliationAlgorithmVersion || len(o.Candidates) > MaxAnchorReconciliationCandidates {
		return ErrInvalidReconcileOutcome
	}
	for _, candidate := range o.Candidates {
		if err := candidate.Validate(); err != nil {
			return err
		}
	}
	if o.State != AnchorAmbiguous && (len(o.Candidates) != 0 || o.CandidateOverflow) {
		return ErrInvalidReconcileOutcome
	}
	return nil
}

// ProposalStalenessInput identifies proposal state that a later coordinator
// must re-evaluate when a generation changes.
type ProposalStalenessInput struct {
	ThreadID       domain.ReviewThreadID
	ProposalID     domain.ProposalID
	FromGeneration repository.TargetGeneration
	ToGeneration   repository.TargetGeneration
	Reason         string
}

// ReconcileReport is the bounded operation summary referenced by staged
// results. T024 owns report aggregation and the canonical state flip.
type ReconcileReport struct {
	ID                 domain.OperationID
	AlgorithmVersion   uint32
	FromGeneration     repository.TargetGeneration
	ToGeneration       repository.TargetGeneration
	RelocatedThreadIDs []domain.ReviewThreadID
	AmbiguousThreadIDs []domain.ReviewThreadID
	OrphanedThreadIDs  []domain.ReviewThreadID
	UnchangedThreadIDs []domain.ReviewThreadID
	ProposalStaleness  []ProposalStalenessInput
}

func (r ReconcileReport) Validate() error {
	if r.ID == "" || r.AlgorithmVersion != AnchorReconciliationAlgorithmVersion || r.FromGeneration == 0 || r.ToGeneration <= r.FromGeneration {
		return ErrInvalidReconcileOutcome
	}
	if _, err := domain.NewOperationID(string(r.ID)); err != nil {
		return ErrInvalidReconcileOutcome
	}
	seen := make(map[domain.ReviewThreadID]struct{})
	for _, ids := range [][]domain.ReviewThreadID{r.RelocatedThreadIDs, r.AmbiguousThreadIDs, r.OrphanedThreadIDs, r.UnchangedThreadIDs} {
		for _, id := range ids {
			if id == "" {
				return ErrInvalidReconcileOutcome
			}
			if _, ok := seen[id]; ok {
				return ErrInvalidReconcileOutcome
			}
			seen[id] = struct{}{}
		}
	}
	for _, input := range r.ProposalStaleness {
		if input.ThreadID == "" || input.ProposalID == "" || input.FromGeneration != r.FromGeneration || input.ToGeneration != r.ToGeneration || !validMetadata(input.Reason) {
			return ErrInvalidReconcileOutcome
		}
	}
	return nil
}

func validEvidenceTier(tier EvidenceTier) bool {
	switch tier {
	case EvidenceTierExactGeneration, EvidenceTierExactContextAtLine, EvidenceTierContextWindow, EvidenceTierContextFile, EvidenceTierSelectionWindow, EvidenceTierSelectionFile, EvidenceTierLineDiff, EvidenceTierOverflow:
		return true
	default:
		return false
	}
}
