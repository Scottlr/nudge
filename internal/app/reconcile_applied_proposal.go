package app

import (
	"context"
	"errors"
	"io"
	"time"

	"github.com/Scottlr/nudge/internal/domain"
	"github.com/Scottlr/nudge/internal/domain/repository"
	"github.com/Scottlr/nudge/internal/domain/review"
)

var (
	ErrPostApplyReconciliationInvalid     = errors.New("invalid post-apply reconciliation")
	ErrPostApplyReconciliationNotApplied  = errors.New("apply operation is not verified applied")
	ErrPostApplyReconciliationConflict    = errors.New("post-apply reconciliation conflict")
	ErrPostApplyExternalDivergence        = errors.New("destination diverged after apply")
	ErrPostApplyReconciliationUnavailable = errors.New("post-apply reconciliation unavailable")
	ErrPostApplyWorkspaceRepairRequired   = errors.New("proposal workspace requires repair after apply")
	ErrProposalValidityPending            = errors.New("proposal validity evaluation is pending")
	ErrProposalValidityStale              = errors.New("proposal is stale for the current destination")
)

// ProposalValidityBatchLimits are the first T070 limits reached by one
// validity journal transaction. A source may return fewer items to provide a
// stable keyset continuation for a large proposal.
type ProposalValidityBatchLimits struct {
	ProposalSummaries    Count
	TouchedPreconditions Count
	EvidenceBytes        ByteSize
}

func DefaultProposalValidityBatchLimits() ProposalValidityBatchLimits {
	return ProposalValidityBatchLimits{ProposalSummaries: 100, TouchedPreconditions: 1000, EvidenceBytes: 4 << 20}
}

func (l ProposalValidityBatchLimits) Validate() error {
	if l.ProposalSummaries == 0 || l.TouchedPreconditions == 0 || l.EvidenceBytes == 0 {
		return ErrPostApplyReconciliationInvalid
	}
	return nil
}

// ApplyReconciliationProvenance preserves whether the destination was
// changed by Nudge or accepted through a separately authorized manual-result
// path. The latter is never relabelled as a Nudge apply.
type ApplyReconciliationProvenance string

const (
	ApplyReconciliationNudgeApplied         ApplyReconciliationProvenance = "nudge_applied"
	ApplyReconciliationAcceptedManualResult ApplyReconciliationProvenance = "accepted_manual_result"
)

func (p ApplyReconciliationProvenance) Validate() error {
	switch p {
	case ApplyReconciliationNudgeApplied, ApplyReconciliationAcceptedManualResult:
		return nil
	default:
		return ErrPostApplyReconciliationInvalid
	}
}

// PostApplyDestinationState is the complete bounded destination evidence
// used to check proposal path preconditions. It is supplied by the
// authoritative refresh owner, never reconstructed from the proposal patch.
type PostApplyDestinationState struct {
	TargetKind             repository.TargetKind
	WorktreeID             domain.WorktreeID
	Head                   repository.ObjectID
	WorkingTreeFingerprint string
	GlobalFingerprint      string
	Paths                  []repository.PathPrecondition
}

func (s PostApplyDestinationState) Validate() error {
	if s.WorktreeID == "" || (s.TargetKind != repository.TargetLocal && s.TargetKind != repository.TargetCommit && s.TargetKind != repository.TargetBranch) || s.WorkingTreeFingerprint == "" || s.GlobalFingerprint == "" {
		return ErrPostApplyReconciliationInvalid
	}
	if s.Head != "" {
		if _, err := repository.NewObjectID(string(s.Head)); err != nil {
			return ErrPostApplyReconciliationInvalid
		}
	}
	if err := validatePostApplyPreconditions(s.Paths); err != nil {
		return err
	}
	return nil
}

// PostApplyTargetRefreshRequest is the handoff from the durable apply result
// to the authoritative target/Git owner. That owner captures and reconciles
// before returning a result.
type PostApplyTargetRefreshRequest struct {
	Operation          ApplyOperation
	ManualResult       *AcceptedManualResult
	Repository         repository.Repository
	Worktree           repository.WorktreeRef
	PreviousTarget     repository.ResolvedTarget
	PreviousGeneration CaptureGeneration
	Provenance         ApplyReconciliationProvenance
}

func (r PostApplyTargetRefreshRequest) Validate() error {
	if r.Repository.Validate() != nil || r.Worktree.Validate() != nil || r.Worktree.RepositoryID != r.Repository.ID || r.PreviousTarget.Validate() != nil || r.PreviousGeneration.Validate() != nil || r.PreviousGeneration.Generation != r.PreviousTarget.Generation || r.Provenance.Validate() != nil {
		return ErrPostApplyReconciliationInvalid
	}
	if r.ManualResult == nil {
		if r.Operation.Validate() != nil || r.Operation.Phase != ApplyOperationApplied || r.Operation.Evidence.RepositoryID != r.Repository.ID || r.Operation.Evidence.WorktreeID != r.Worktree.ID || r.Provenance != ApplyReconciliationNudgeApplied {
			return ErrPostApplyReconciliationInvalid
		}
	} else if r.Operation.ID != "" || r.ManualResult.Validate() != nil || r.ManualResult.Evidence.RepositoryID != r.Repository.ID || r.ManualResult.Evidence.WorktreeID != r.Worktree.ID || r.Provenance != ApplyReconciliationAcceptedManualResult {
		return ErrPostApplyReconciliationInvalid
	}
	return nil
}

// AcceptedManualResult is the T058 verified manual-result outcome. It uses
// the same immutable destination evidence contract but is never represented
// as an ApplyOperation or attributed to Nudge mutation.
type AcceptedManualResult struct {
	ID          domain.OperationID
	SessionID   domain.ReviewSessionID
	ProposalID  domain.ProposalID
	WorkspaceID domain.WorkspaceID
	ThreadID    domain.ReviewThreadID
	Evidence    ApplyVerificationEvidence
	AcceptedAt  time.Time
}

func (r AcceptedManualResult) Validate() error {
	if r.ID == "" || r.SessionID == "" || r.ProposalID == "" || r.WorkspaceID == "" || r.ThreadID == "" || r.Evidence.Validate() != nil || r.Evidence.OperationID != r.ID || r.AcceptedAt.IsZero() {
		return ErrPostApplyReconciliationInvalid
	}
	return nil
}

type AcceptedManualResultStore interface {
	LoadAcceptedManualResult(context.Context, domain.OperationID) (AcceptedManualResult, error)
}

// PostApplyTargetRefreshResult is complete authoritative evidence for the
// new generation. Source is an accepted immutable tree and may be consumed by
// the workspace owner for baseline advancement.
type PostApplyTargetRefreshResult struct {
	Generation       CaptureGeneration
	Manifest         CaptureManifest
	Target           repository.ResolvedTarget
	Transition       review.GenerationTransition
	Destination      PostApplyDestinationState
	Source           AcceptedTreeSource
	ExternalDiverged bool
}

func (r PostApplyTargetRefreshResult) Validate(previous repository.TargetGeneration, worktree domain.WorktreeID) error {
	if r.Generation.Validate() != nil || r.Manifest.Validate() != nil || r.Manifest.CaptureID != r.Generation.CaptureID || r.Manifest.ManifestHash != r.Generation.ManifestHash || r.Target.Validate() != nil || r.Target.Generation != r.Generation.Generation || r.Generation.Generation <= previous || r.Generation.WorktreeID != worktree || r.Destination.Validate() != nil || r.Destination.WorktreeID != worktree || r.Source == nil || r.Source.Identity().Validate() != nil || r.Transition.Validate() != nil || r.Transition.FromGeneration != previous || r.Transition.ToGeneration != r.Generation.Generation {
		return ErrPostApplyReconciliationInvalid
	}
	return nil
}

// AcceptedTreeSource is the application-facing shape of a complete accepted
// tree. workspace.TrustedTreeSource satisfies this interface without making
// app depend on the workspace adapter.
type AcceptedTreeSource interface {
	Identity() WorkspaceSourceIdentity
	List(context.Context) ([]repository.TreeEntry, error)
	Open(context.Context, repository.TreeEntry) (io.ReadCloser, error)
}

// PostApplyTargetRefresher owns T024 capture, Git target resolution, and
// anchor reconciliation. It returns only after the new generation is
// durable/authoritative or reports divergence.
type PostApplyTargetRefresher interface {
	RefreshAfterApply(context.Context, PostApplyTargetRefreshRequest) (PostApplyTargetRefreshResult, error)
}

// ProposalValidityCandidate is one bounded nonterminal proposal summary plus
// the exact preconditions that its immutable version recorded.
type ProposalValidityCandidate struct {
	ProposalID    domain.ProposalID
	Version       review.ProposalVersionNumber
	Status        review.ProposalStatus
	Destination   review.DestinationConstraints
	Preconditions []repository.PathPrecondition
}

func (c ProposalValidityCandidate) Validate() error {
	if c.ProposalID == "" || c.Version == 0 || c.Status.Validate() != nil || c.Status == review.ProposalVersionRejected || c.Status == review.ProposalVersionApplied || c.Status == review.ProposalVersionStale || c.Status == review.ProposalVersionFailed || c.Destination.Validate() != nil || validatePostApplyPreconditions(c.Preconditions) != nil {
		return ErrPostApplyReconciliationInvalid
	}
	return nil
}

type ProposalValidityPageRequest struct {
	SessionID  domain.ReviewSessionID
	WorktreeID domain.WorktreeID
	TargetKind repository.TargetKind
	Generation repository.TargetGeneration
	Cursor     string
	Limit      Count
}

func (r ProposalValidityPageRequest) Validate() error {
	if r.SessionID == "" || r.WorktreeID == "" || (r.TargetKind != repository.TargetLocal && r.TargetKind != repository.TargetCommit && r.TargetKind != repository.TargetBranch) || r.Generation == 0 || r.Limit == 0 || r.Limit > DefaultProposalValidityBatchLimits().ProposalSummaries || len(r.Cursor) > 4<<10 {
		return ErrPostApplyReconciliationInvalid
	}
	return nil
}

type ProposalValidityPage struct {
	Items        []ProposalValidityCandidate
	NextCursor   string
	Done         bool
	EncodedBytes ByteSize
}

func (p ProposalValidityPage) Validate(limits ProposalValidityBatchLimits) error {
	if limits.Validate() != nil || len(p.Items) > int(limits.ProposalSummaries) || p.EncodedBytes > limits.EvidenceBytes || !p.Done && p.NextCursor == "" || p.Done && p.NextCursor != "" {
		return ErrPostApplyReconciliationInvalid
	}
	for _, item := range p.Items {
		if err := item.Validate(); err != nil {
			return err
		}
	}
	return nil
}

// ProposalValiditySource supplies stable keyset pages and never returns the
// complete proposal population to actor state.
type ProposalValiditySource interface {
	PageProposalValidity(context.Context, ProposalValidityPageRequest) (ProposalValidityPage, error)
}

type ProposalValidityOutcome string

const (
	ProposalValidityValid ProposalValidityOutcome = "valid"
	ProposalValidityStale ProposalValidityOutcome = "stale"
)

func (o ProposalValidityOutcome) Validate() error {
	if o != ProposalValidityValid && o != ProposalValidityStale {
		return ErrPostApplyReconciliationInvalid
	}
	return nil
}

// ProposalValidityResult is staged before it can affect approval queries.
// ConflictPath is retained as raw repository identity for path-specific UI.
type ProposalValidityResult struct {
	ApplyOperationID domain.OperationID
	Generation       repository.TargetGeneration
	ProposalID       domain.ProposalID
	Version          review.ProposalVersionNumber
	ExpectedStatus   review.ProposalStatus
	Outcome          ProposalValidityOutcome
	Reason           StaleReason
	ConflictPath     *repository.RepoPath
	EvidenceBytes    ByteSize
}

func (r ProposalValidityResult) Validate() error {
	if r.ApplyOperationID == "" || r.Generation == 0 || r.ProposalID == "" || r.Version == 0 || r.ExpectedStatus.Validate() != nil || r.Outcome.Validate() != nil || r.Reason.Validate() != nil || r.EvidenceBytes == 0 {
		return ErrPostApplyReconciliationInvalid
	}
	if r.ConflictPath != nil {
		if err := r.ConflictPath.Validate(); err != nil {
			return ErrPostApplyReconciliationInvalid
		}
	}
	return nil
}

type PostApplyReconciliationPhase string

const (
	PostApplyPhaseStarted         PostApplyReconciliationPhase = "started"
	PostApplyPhaseValidityPending PostApplyReconciliationPhase = "validity_pending"
	PostApplyPhaseBaselinePending PostApplyReconciliationPhase = "baseline_pending"
	PostApplyPhaseCompleted       PostApplyReconciliationPhase = "completed"
	PostApplyPhaseRepairRequired  PostApplyReconciliationPhase = "repair_required"
)

func (p PostApplyReconciliationPhase) Validate() error {
	switch p {
	case PostApplyPhaseStarted, PostApplyPhaseValidityPending, PostApplyPhaseBaselinePending, PostApplyPhaseCompleted, PostApplyPhaseRepairRequired:
		return nil
	default:
		return ErrPostApplyReconciliationInvalid
	}
}

// PostApplyReconciliationRecord is the durable causal link and resumable
// validity checkpoint. It contains no patch bytes or actor-owned populations.
type PostApplyReconciliationRecord struct {
	ApplyOperationID       domain.OperationID
	SessionID              domain.ReviewSessionID
	WorkspaceID            domain.WorkspaceID
	ProposalID             domain.ProposalID
	PreviousGeneration     repository.TargetGeneration
	NewGeneration          repository.TargetGeneration
	CaptureID              domain.CaptureID
	ManifestHash           string
	Provenance             ApplyReconciliationProvenance
	Target                 repository.ResolvedTarget
	Destination            PostApplyDestinationState
	Phase                  PostApplyReconciliationPhase
	ValidityEpoch          uint64
	ValidityCursor         string
	ProcessedProposals     Count
	ProcessedPreconditions Count
	EvidenceBytes          ByteSize
	RepairReason           string
	StartedAt              time.Time
	CompletedAt            *time.Time
}

func (r PostApplyReconciliationRecord) Validate() error {
	if r.ApplyOperationID == "" || r.SessionID == "" || r.WorkspaceID == "" || r.ProposalID == "" || r.PreviousGeneration == 0 || r.Provenance.Validate() != nil || r.Phase.Validate() != nil || r.StartedAt.IsZero() || len(r.ValidityCursor) > 4<<10 || len(r.RepairReason) > 128 {
		return ErrPostApplyReconciliationInvalid
	}
	if (r.NewGeneration == 0) != (r.CaptureID == "") || (r.NewGeneration == 0) != (r.ManifestHash == "") {
		return ErrPostApplyReconciliationInvalid
	}
	if r.ManifestHash != "" && !validLocalCaptureHash(r.ManifestHash) {
		return ErrPostApplyReconciliationInvalid
	}
	if r.NewGeneration != 0 && (r.Target.Validate() != nil || r.Target.Generation != r.NewGeneration || r.Destination.Validate() != nil || r.Destination.TargetKind != r.Target.Spec.Kind) {
		return ErrPostApplyReconciliationInvalid
	}
	if r.Phase == PostApplyPhaseCompleted && r.CompletedAt == nil || r.CompletedAt != nil && r.CompletedAt.Before(r.StartedAt) {
		return ErrPostApplyReconciliationInvalid
	}
	return nil
}

// PostApplyReconciliationJournal is one short fenced transaction per method.
// Implementations persist staged validity before changing approval-visible
// proposal state.
type PostApplyReconciliationJournal interface {
	Load(context.Context, domain.OperationID) (PostApplyReconciliationRecord, error)
	Start(context.Context, SessionWriteGuard, PostApplyReconciliationRecord) (SessionWriteGuard, error)
	RecordGeneration(context.Context, SessionWriteGuard, PostApplyReconciliationRecord) (SessionWriteGuard, error)
	StageValidity(context.Context, SessionWriteGuard, PostApplyReconciliationRecord, []ProposalValidityResult) (SessionWriteGuard, error)
	CompleteValidity(context.Context, SessionWriteGuard, PostApplyReconciliationRecord, time.Time) (SessionWriteGuard, error)
	Complete(context.Context, SessionWriteGuard, PostApplyReconciliationRecord, time.Time) (SessionWriteGuard, error)
	Repair(context.Context, SessionWriteGuard, PostApplyReconciliationRecord, string, time.Time) (SessionWriteGuard, error)
}

// PostApplyReconciliationStoreTx is the optional durable transaction
// extension used by the SQLite adapter. Keeping it separate preserves the
// narrow core ReviewStore contract for stores that do not persist proposals.
type PostApplyReconciliationStoreTx interface {
	CreatePostApplyReconciliation(context.Context, PostApplyReconciliationRecord) error
	UpdatePostApplyReconciliation(context.Context, PostApplyReconciliationRecord) error
	StagePostApplyValidity(context.Context, ProposalValidityResult) error
	CompletePostApplyValidity(context.Context, PostApplyReconciliationRecord, time.Time) error
	CompletePostApplyReconciliation(context.Context, PostApplyReconciliationRecord, time.Time) error
	RepairPostApplyReconciliation(context.Context, PostApplyReconciliationRecord, string, time.Time) error
}

// ProposalValidityApprovalGate is implemented by durable stores. It prevents
// T041 approval while the current destination epoch is pending or staged.
type ProposalValidityApprovalGate interface {
	CheckProposalApprovalValidity(context.Context, domain.ProposalID, review.ProposalVersionNumber, review.DestinationConstraints) error
}

// PostApplyBaselineRequest is deliberately application-owned so the
// workspace package can adapt its existing lifecycle implementation without
// importing application orchestration back into the domain.
type PostApplyBaselineRequest struct {
	ApplyOperationID domain.OperationID
	ProposalID       domain.ProposalID
	WorkspaceID      domain.WorkspaceID
	WorktreeID       domain.WorktreeID
	Generation       repository.TargetGeneration
	Source           AcceptedTreeSource
	VerifiedAt       time.Time
}

type PostApplyBaselineResult struct {
	BaselineGeneration repository.TargetGeneration
	ManifestHash       string
}

type PostApplyBaselineAdvancer interface {
	AdvanceBaseline(context.Context, PostApplyBaselineRequest) (PostApplyBaselineResult, error)
}

type PostApplyReconciliationRequest struct {
	Guard              SessionWriteGuard
	OperationID        domain.OperationID
	CorrelationID      CorrelationID
	Repository         repository.Repository
	Worktree           repository.WorktreeRef
	PreviousTarget     repository.ResolvedTarget
	PreviousGeneration CaptureGeneration
	WorkspaceID        domain.WorkspaceID
	Provenance         ApplyReconciliationProvenance
	BaselineSource     AcceptedTreeSource
}

func (r PostApplyReconciliationRequest) Validate() error {
	if r.Guard.Validate() != nil || r.OperationID == "" || r.Repository.Validate() != nil || r.Worktree.Validate() != nil || r.Worktree.RepositoryID != r.Repository.ID || r.PreviousTarget.Validate() != nil || r.PreviousGeneration.Validate() != nil || r.PreviousTarget.Generation != r.PreviousGeneration.Generation || r.WorkspaceID == "" || r.Provenance.Validate() != nil {
		return ErrPostApplyReconciliationInvalid
	}
	return nil
}

type PostApplyReconciliationResult struct {
	Record                  PostApplyReconciliationRecord
	Target                  *repository.ResolvedTarget
	Applied                 bool
	WorkspaceRepairRequired bool
	Events                  []Event
}

// PostApplyReconciliationService consumes one verified apply operation and
// advances its durable post-apply side effects in idempotent phases.
type PostApplyReconciliationService struct {
	operations    ApplyOperationStore
	manualResults AcceptedManualResultStore
	refresh       PostApplyTargetRefresher
	validity      ProposalValiditySource
	journal       PostApplyReconciliationJournal
	baseline      PostApplyBaselineAdvancer
	limits        ProposalValidityBatchLimits
	clock         Clock
}

type PostApplyReconciliationServiceConfig struct {
	Operations    ApplyOperationStore
	ManualResults AcceptedManualResultStore
	Refresh       PostApplyTargetRefresher
	Validity      ProposalValiditySource
	Journal       PostApplyReconciliationJournal
	Baseline      PostApplyBaselineAdvancer
	Limits        ProposalValidityBatchLimits
	Clock         Clock
}

func NewPostApplyReconciliationService(config PostApplyReconciliationServiceConfig) (*PostApplyReconciliationService, error) {
	if (config.Operations == nil && config.ManualResults == nil) || config.Refresh == nil || config.Validity == nil || config.Journal == nil || config.Baseline == nil {
		return nil, ErrPostApplyReconciliationUnavailable
	}
	if config.Limits == (ProposalValidityBatchLimits{}) {
		config.Limits = DefaultProposalValidityBatchLimits()
	}
	if config.Limits.Validate() != nil {
		return nil, ErrPostApplyReconciliationUnavailable
	}
	if config.Clock == nil {
		config.Clock = SystemClock{}
	}
	return &PostApplyReconciliationService{operations: config.Operations, manualResults: config.ManualResults, refresh: config.Refresh, validity: config.Validity, journal: config.Journal, baseline: config.Baseline, limits: config.Limits, clock: config.Clock}, nil
}

func (s *PostApplyReconciliationService) Reconcile(ctx context.Context, request PostApplyReconciliationRequest) (PostApplyReconciliationResult, error) {
	if s == nil || ctx == nil || request.Validate() != nil {
		return PostApplyReconciliationResult{}, ErrPostApplyReconciliationInvalid
	}
	var operation ApplyOperation
	var operationErr error = ErrApplyOperationNotFound
	if s.operations != nil {
		operation, operationErr = s.operations.LoadApplyOperation(ctx, request.OperationID)
	}
	var manualResult *AcceptedManualResult
	var outcomeID domain.OperationID
	var proposalID domain.ProposalID
	var workspaceID domain.WorkspaceID
	var sessionID domain.ReviewSessionID
	var verifiedAt time.Time
	if operationErr == nil {
		if operation.Phase != ApplyOperationApplied || operation.SessionID != request.Guard.SessionID || operation.WorkspaceID != request.WorkspaceID || operation.Evidence.WorktreeID != request.Worktree.ID || operation.Verification.OperationID != operation.ID || request.Provenance != ApplyReconciliationNudgeApplied {
			return PostApplyReconciliationResult{}, ErrPostApplyReconciliationNotApplied
		}
		outcomeID, proposalID, workspaceID, sessionID, verifiedAt = operation.ID, operation.ProposalID, operation.WorkspaceID, operation.SessionID, operation.Verification.ObservedAt
	} else if errors.Is(operationErr, ErrApplyOperationNotFound) && s.manualResults != nil {
		value, manualErr := s.manualResults.LoadAcceptedManualResult(ctx, request.OperationID)
		if manualErr != nil {
			return PostApplyReconciliationResult{}, manualErr
		}
		if value.Validate() != nil || value.SessionID != request.Guard.SessionID || value.WorkspaceID != request.WorkspaceID || request.Provenance != ApplyReconciliationAcceptedManualResult {
			return PostApplyReconciliationResult{}, ErrPostApplyReconciliationNotApplied
		}
		manualResult = &value
		outcomeID, proposalID, workspaceID, sessionID, verifiedAt = value.ID, value.ProposalID, value.WorkspaceID, value.SessionID, value.AcceptedAt
	} else {
		return PostApplyReconciliationResult{}, operationErr
	}
	refreshRequest := PostApplyTargetRefreshRequest{Operation: operation, ManualResult: manualResult, Repository: request.Repository, Worktree: request.Worktree, PreviousTarget: request.PreviousTarget, PreviousGeneration: request.PreviousGeneration, Provenance: request.Provenance}
	if refreshRequest.Validate() != nil {
		return PostApplyReconciliationResult{}, ErrPostApplyReconciliationInvalid
	}
	record, err := s.journal.Load(ctx, request.OperationID)
	if err != nil {
		if !errors.Is(err, ErrReviewStoreNotFound) {
			return PostApplyReconciliationResult{}, err
		}
		record = PostApplyReconciliationRecord{ApplyOperationID: outcomeID, SessionID: sessionID, WorkspaceID: workspaceID, ProposalID: proposalID, PreviousGeneration: request.PreviousGeneration.Generation, Provenance: request.Provenance, Phase: PostApplyPhaseStarted, StartedAt: s.clock.Now().UTC()}
		if record.StartedAt.IsZero() || record.Validate() != nil {
			return PostApplyReconciliationResult{}, ErrPostApplyReconciliationInvalid
		}
		request.Guard, err = s.journal.Start(ctx, request.Guard, record)
		if err != nil {
			return PostApplyReconciliationResult{}, err
		}
	} else if record.Validate() != nil || record.ApplyOperationID != outcomeID || record.SessionID != request.Guard.SessionID || record.WorkspaceID != request.WorkspaceID || record.Provenance != request.Provenance || record.PreviousGeneration != request.PreviousGeneration.Generation {
		return PostApplyReconciliationResult{}, ErrPostApplyReconciliationConflict
	}
	if record.Phase == PostApplyPhaseCompleted {
		return postApplyResult(record), nil
	}
	if record.Phase == PostApplyPhaseRepairRequired {
		return postApplyResult(record), ErrPostApplyWorkspaceRepairRequired
	}
	var events []Event

	var refreshed *PostApplyTargetRefreshResult
	if record.NewGeneration == 0 {
		value, refreshErr := s.refresh.RefreshAfterApply(ctx, refreshRequest)
		if refreshErr != nil {
			return PostApplyReconciliationResult{}, refreshErr
		}
		if value.ExternalDiverged {
			record.RepairReason = "external_divergence"
			record.Phase = PostApplyPhaseRepairRequired
			request.Guard, err = s.journal.Repair(ctx, request.Guard, record, record.RepairReason, s.clock.Now().UTC())
			if err != nil {
				return PostApplyReconciliationResult{}, err
			}
			return postApplyResult(record), ErrPostApplyExternalDivergence
		}
		if err := value.Validate(request.PreviousGeneration.Generation, request.Worktree.ID); err != nil {
			return PostApplyReconciliationResult{}, err
		}
		refreshed = &value
		record.NewGeneration = value.Generation.Generation
		record.CaptureID = value.Generation.CaptureID
		record.ManifestHash = value.Generation.ManifestHash
		record.Target = value.Target
		record.Destination = value.Destination
		record.ValidityEpoch++
		record.Phase = PostApplyPhaseValidityPending
		request.Guard, err = s.journal.RecordGeneration(ctx, request.Guard, record)
		if err != nil {
			return PostApplyReconciliationResult{}, err
		}
		events = append(events, TargetReconciled{OperationID: outcomeID, CorrelationID: request.CorrelationID, TargetGeneration: value.Generation.Generation, SessionID: request.Guard.SessionID, PreviousGeneration: request.PreviousGeneration.Generation, Provenance: request.Provenance})
	} else if record.Phase == PostApplyPhaseValidityPending || record.Phase == PostApplyPhaseBaselinePending {
		// The source is only needed for baseline advancement. The durable
		// target/destination identities above are the restart authority.
		if request.BaselineSource == nil && record.Phase == PostApplyPhaseBaselinePending {
			return PostApplyReconciliationResult{}, ErrPostApplyReconciliationUnavailable
		}
	}

	if record.Phase == PostApplyPhaseValidityPending {
		var progressEvents []Event
		request.Guard, progressEvents, err = s.runValiditySweep(ctx, request.Guard, &record, request.CorrelationID)
		if err != nil {
			return postApplyResult(record), err
		}
		events = append(events, progressEvents...)
		events = append(events, ProposalValidityEpochCompleted{OperationID: operation.ID, CorrelationID: request.CorrelationID, TargetGeneration: record.NewGeneration, Epoch: record.ValidityEpoch})
	}
	if record.Phase != PostApplyPhaseBaselinePending {
		return PostApplyReconciliationResult{}, ErrPostApplyReconciliationConflict
	}
	source := request.BaselineSource
	if source == nil && refreshed != nil {
		source = refreshed.Source
	}
	if source == nil {
		return PostApplyReconciliationResult{}, ErrPostApplyReconciliationUnavailable
	}
	if _, err := s.baseline.AdvanceBaseline(ctx, PostApplyBaselineRequest{ApplyOperationID: outcomeID, ProposalID: proposalID, WorkspaceID: workspaceID, WorktreeID: request.Worktree.ID, Generation: record.NewGeneration, Source: source, VerifiedAt: verifiedAt}); err != nil {
		record.RepairReason = "workspace_baseline"
		record.Phase = PostApplyPhaseRepairRequired
		var repairErr error
		request.Guard, repairErr = s.journal.Repair(ctx, request.Guard, record, record.RepairReason, s.clock.Now().UTC())
		if repairErr != nil {
			return PostApplyReconciliationResult{}, repairErr
		}
		return PostApplyReconciliationResult{Record: record, Target: targetPointer(record.Target), Applied: true, WorkspaceRepairRequired: true, Events: events}, ErrPostApplyWorkspaceRepairRequired
	}
	now := s.clock.Now().UTC()
	request.Guard, err = s.journal.Complete(ctx, request.Guard, record, now)
	if err != nil {
		return PostApplyReconciliationResult{}, err
	}
	record.Phase = PostApplyPhaseCompleted
	record.CompletedAt = &now
	events = append(events, WorkspaceBaselineAdvanced{OperationID: outcomeID, CorrelationID: request.CorrelationID, TargetGeneration: record.NewGeneration, WorkspaceID: record.WorkspaceID})
	result := postApplyResult(record)
	result.Events = events
	return result, nil
}

func (s *PostApplyReconciliationService) runValiditySweep(ctx context.Context, guard SessionWriteGuard, record *PostApplyReconciliationRecord, correlationID CorrelationID) (SessionWriteGuard, []Event, error) {
	if record == nil || record.Validate() != nil || record.Phase != PostApplyPhaseValidityPending || record.Destination.Validate() != nil {
		return guard, nil, ErrPostApplyReconciliationInvalid
	}
	cursor := record.ValidityCursor
	events := make([]Event, 0, 1)
	for {
		page, err := s.validity.PageProposalValidity(ctx, ProposalValidityPageRequest{SessionID: record.SessionID, WorktreeID: record.Destination.WorktreeID, TargetKind: record.Destination.TargetKind, Generation: record.NewGeneration, Cursor: cursor, Limit: s.limits.ProposalSummaries})
		if err != nil {
			return guard, events, err
		}
		if err := page.Validate(s.limits); err != nil {
			return guard, events, err
		}
		var preconditions Count
		results := make([]ProposalValidityResult, 0, len(page.Items))
		for _, candidate := range page.Items {
			preconditions += Count(len(candidate.Preconditions))
			if preconditions > s.limits.TouchedPreconditions {
				return guard, events, ErrPostApplyReconciliationInvalid
			}
			result := evaluateProposalValidity(candidate, record.Destination, record.ApplyOperationID, record.NewGeneration)
			if result.Validate() != nil {
				return guard, events, ErrPostApplyReconciliationInvalid
			}
			results = append(results, result)
		}
		if record.ProcessedProposals > ^Count(0)-Count(len(results)) || record.ProcessedPreconditions > ^Count(0)-preconditions || record.EvidenceBytes > ^ByteSize(0)-page.EncodedBytes {
			return guard, events, ErrPostApplyReconciliationInvalid
		}
		record.ProcessedProposals += Count(len(results))
		record.ProcessedPreconditions += preconditions
		record.EvidenceBytes += page.EncodedBytes
		if record.EvidenceBytes > ^ByteSize(0) {
			return guard, events, ErrPostApplyReconciliationInvalid
		}
		if !page.Done && page.NextCursor == cursor {
			return guard, events, ErrPostApplyReconciliationInvalid
		}
		if page.Done {
			cursor = ""
		} else {
			cursor = page.NextCursor
		}
		record.ValidityCursor = cursor
		guard, err = s.journal.StageValidity(ctx, guard, *record, results)
		if err != nil {
			return guard, events, err
		}
		events = append(events, ProposalValidityProgress{OperationID: record.ApplyOperationID, CorrelationID: correlationID, TargetGeneration: record.NewGeneration, ProcessedProposals: record.ProcessedProposals, ProcessedPreconditions: record.ProcessedPreconditions, EvidenceBytes: record.EvidenceBytes, CoalescingKey: "proposal-validity"})
		if page.Done {
			break
		}
	}
	record.Phase = PostApplyPhaseBaselinePending
	guard, err := s.journal.CompleteValidity(ctx, guard, *record, s.clock.Now().UTC())
	return guard, events, err
}

func validatePostApplyPreconditions(values []repository.PathPrecondition) error {
	paths := make(map[repository.RepoPathKey]struct{}, len(values))
	for _, value := range values {
		if value.Validate() != nil {
			return ErrPostApplyReconciliationInvalid
		}
		if _, exists := paths[value.Path.Key()]; exists {
			return ErrPostApplyReconciliationInvalid
		}
		paths[value.Path.Key()] = struct{}{}
	}
	return nil
}

func postApplyResult(record PostApplyReconciliationRecord) PostApplyReconciliationResult {
	return PostApplyReconciliationResult{Record: record, Target: targetPointer(record.Target), Applied: true, WorkspaceRepairRequired: record.Phase == PostApplyPhaseRepairRequired && record.RepairReason == "workspace_baseline"}
}

func targetPointer(value repository.ResolvedTarget) *repository.ResolvedTarget {
	if value.Generation == 0 {
		return nil
	}
	copyValue := value
	return &copyValue
}
