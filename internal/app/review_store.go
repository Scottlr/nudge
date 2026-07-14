package app

import (
	"context"
	"errors"
	"time"

	"github.com/Scottlr/nudge/internal/domain"
	"github.com/Scottlr/nudge/internal/domain/repository"
	"github.com/Scottlr/nudge/internal/domain/review"
)

var (
	// ErrReviewStoreInput reports an invalid application-owned store request.
	ErrReviewStoreInput = errors.New("invalid review store input")
	// ErrReviewStoreClosed reports use after store shutdown.
	ErrReviewStoreClosed = errors.New("review store is closed")
	// ErrReviewStoreNotFound reports an absent durable record.
	ErrReviewStoreNotFound = errors.New("review store record not found")
	// ErrSessionLeaseLost reports a lease or writer-epoch mismatch.
	ErrSessionLeaseLost = errors.New("session lease lost")
	// ErrSessionRevisionConflict reports an optimistic session revision conflict.
	ErrSessionRevisionConflict = errors.New("session revision conflict")
	// ErrReviewStoreCorrupt reports persisted data that no longer validates.
	ErrReviewStoreCorrupt = errors.New("review store data is corrupt")
	// ErrRepositoryBindingChanged reports a repository/worktree identity that
	// no longer matches the durable binding with the same Nudge ID.
	ErrRepositoryBindingChanged = errors.New("repository binding changed")
	// ErrSessionBusy reports that another process owns the compatible session
	// lock. Callers must choose read-only history or an explicit distinct session.
	ErrSessionBusy = errors.New("review session is busy")
	// ErrSessionReadOnly reports a mutation requested from an immutable handle.
	ErrSessionReadOnly = errors.New("review session is read-only")
	// ErrSessionTargetConflict reports an attempt to change a session's target
	// meaning rather than advance its compatible generation.
	ErrSessionTargetConflict = errors.New("review session target conflict")
)

const (
	// DefaultPageLimit is the bounded default for thread and message pages.
	DefaultPageLimit uint32 = 100
	// MaxPageLimit is the hard maximum number of complete items in one page.
	MaxPageLimit uint32 = 200
	// MaxPageEncodedBytes is the hard encoded-result budget for one page.
	MaxPageEncodedBytes uint64 = 4 << 20
	// MaxMessageBodyRange is the maximum returned message-body range.
	MaxMessageBodyRange uint64 = 256 << 10
)

// SessionWriteGuard is the application fence carried by every durable
// session-scoped mutation.
type SessionWriteGuard struct {
	SessionID        domain.ReviewSessionID
	LeaseID          domain.SessionLeaseID
	WriterEpoch      uint64
	ExpectedRevision uint64
}

// Validate checks the complete lease, epoch, and revision fence.
func (g SessionWriteGuard) Validate() error {
	if g.SessionID == "" || g.LeaseID == "" || g.WriterEpoch == 0 || g.ExpectedRevision == 0 {
		return ErrReviewStoreInput
	}
	return nil
}

// MessageSummary is bounded message metadata. Body bytes are read separately
// through ReadMessageBody using the returned identity.
type MessageSummary struct {
	ID           domain.MessageID
	ThreadID     domain.ReviewThreadID
	Role         review.MessageRole
	ProviderID   string
	Status       review.MessageStatus
	Ordinal      uint64
	ByteLength   uint64
	SHA256       string
	Preview      string
	CreatedAt    time.Time
	UpdatedAt    time.Time
	CompletedAt  *time.Time
	FailurePhase review.FailurePhase
	ErrorCode    review.ErrorCode
}

// ThreadCursor is a revision-bound keyset cursor ordered by updated time and
// stable thread identity.
type ThreadCursor struct {
	SessionID domain.ReviewSessionID
	Revision  uint64
	UpdatedAt time.Time
	ID        domain.ReviewThreadID
	FilterKey string
}

// MessageCursor is a revision-bound keyset cursor ordered by updated time and
// stable message identity.
type MessageCursor struct {
	ThreadID  domain.ReviewThreadID
	Revision  uint64
	UpdatedAt time.Time
	ID        domain.MessageID
}

// ThreadPage describes one bounded keyset query.
type ThreadPage struct {
	SessionID domain.ReviewSessionID
	Limit     uint32
	FilterKey string
	Cursor    *ThreadCursor
}

// Validate normalizes the zero limit and checks cursor binding.
func (p *ThreadPage) Validate() error {
	if p == nil || p.SessionID == "" {
		return ErrReviewStoreInput
	}
	if p.Limit == 0 {
		p.Limit = DefaultPageLimit
	}
	if p.Limit > MaxPageLimit {
		return ErrReviewStoreInput
	}
	if p.Cursor != nil && (p.Cursor.SessionID != p.SessionID || p.Cursor.Revision == 0 || p.Cursor.UpdatedAt.IsZero() || p.Cursor.ID == "" || p.Cursor.FilterKey != p.FilterKey) {
		return ErrReviewStoreInput
	}
	return nil
}

// ThreadPageResult contains complete summaries and an optional continuation.
type ThreadPageResult struct {
	Items    []ThreadSummary
	Next     *ThreadCursor
	Revision uint64
	HasMore  bool
}

// MessagePage describes one bounded message keyset query.
type MessagePage struct {
	ThreadID domain.ReviewThreadID
	Limit    uint32
	Cursor   *MessageCursor
}

// Validate normalizes the zero limit and checks cursor binding.
func (p *MessagePage) Validate() error {
	if p == nil || p.ThreadID == "" {
		return ErrReviewStoreInput
	}
	if p.Limit == 0 {
		p.Limit = DefaultPageLimit
	}
	if p.Limit > MaxPageLimit {
		return ErrReviewStoreInput
	}
	if p.Cursor != nil && (p.Cursor.ThreadID != p.ThreadID || p.Cursor.Revision == 0 || p.Cursor.UpdatedAt.IsZero() || p.Cursor.ID == "") {
		return ErrReviewStoreInput
	}
	return nil
}

// MessagePageResult contains complete metadata summaries and an optional
// continuation.
type MessagePageResult struct {
	Items    []MessageSummary
	Next     *MessageCursor
	Revision uint64
	HasMore  bool
}

// BodyRange is an identity-bound bounded byte request for one message.
type BodyRange struct {
	MessageID      domain.MessageID
	ExpectedLength uint64
	ExpectedSHA256 string
	Offset         uint64
	Length         uint64
}

// Validate checks range arithmetic and the hard read cap.
func (r BodyRange) Validate() error {
	if r.MessageID == "" || r.ExpectedLength == 0 || len(r.ExpectedSHA256) != 64 || r.Offset > r.ExpectedLength || r.Length > MaxMessageBodyRange || r.Length > r.ExpectedLength-r.Offset {
		return ErrReviewStoreInput
	}
	return nil
}

// MessageBodyChunk is a complete bounded range tied to the stored body
// identity.
type MessageBodyChunk struct {
	MessageID   domain.MessageID
	Offset      uint64
	Bytes       []byte
	TotalLength uint64
	SHA256      string
	Complete    bool
}

// ReconciliationOperationState identifies the staged generation-activation
// lifecycle owned by the core review store.
type ReconciliationOperationState string

const (
	ReconciliationStaged    ReconciliationOperationState = "staged"
	ReconciliationRunning   ReconciliationOperationState = "running"
	ReconciliationCompleted ReconciliationOperationState = "completed"
	ReconciliationFailed    ReconciliationOperationState = "failed"
)

// ReconciliationOperation is bounded durable metadata for one staged epoch.
type ReconciliationOperation struct {
	ID             domain.OperationID
	SessionID      domain.ReviewSessionID
	FromGeneration repository.TargetGeneration
	ToGeneration   repository.TargetGeneration
	State          ReconciliationOperationState
	StartedAt      time.Time
	CompletedAt    *time.Time
	Active         bool
}

// ReconciliationAnchorResult is staged until its operation is completed and
// explicitly activated.
type ReconciliationAnchorResult struct {
	OperationID       domain.OperationID
	ThreadID          domain.ReviewThreadID
	Anchor            review.CodeAnchor
	State             review.AnchorState
	Reason            string
	ReportID          domain.OperationID
	Candidates        []review.AnchorCandidate
	CandidateOverflow bool
	AlgorithmVersion  uint32
}

// Validate checks one staged anchor result before it crosses the persistence
// boundary. Zero AlgorithmVersion remains accepted for schema-v1 rows created
// before T023; new reconciliation results carry the explicit algorithm.
func (r ReconciliationAnchorResult) Validate() error {
	if r.OperationID == "" || r.ThreadID == "" || r.Anchor.Validate() != nil || r.State.Validate() != nil || r.Anchor.State != r.State || r.Reason == "" {
		return ErrReviewStoreInput
	}
	if r.ReportID != "" {
		if _, err := domain.NewOperationID(string(r.ReportID)); err != nil {
			return ErrReviewStoreInput
		}
	}
	if r.AlgorithmVersion != 0 && r.AlgorithmVersion != review.AnchorReconciliationAlgorithmVersion {
		return ErrReviewStoreInput
	}
	if len(r.Candidates) > review.MaxAnchorReconciliationCandidates || (r.AlgorithmVersion == 0 && (len(r.Candidates) != 0 || r.CandidateOverflow)) {
		return ErrReviewStoreInput
	}
	for _, candidate := range r.Candidates {
		if err := candidate.Validate(); err != nil {
			return ErrReviewStoreInput
		}
	}
	if r.State != review.AnchorAmbiguous && (len(r.Candidates) != 0 || r.CandidateOverflow) {
		return ErrReviewStoreInput
	}
	return nil
}

// AcceptedTargetGeneration binds an accepted capture to its durable target
// generation, policy evaluation identity, and retention reference.
type AcceptedTargetGeneration struct {
	Generation         CaptureGeneration
	Manifest           CaptureManifest
	PolicyEvaluation   CapturePolicyEvaluation
	RetentionReference string
	Target             *repository.ResolvedTarget
}

// Validate checks the cross-record identities without requiring the current
// capability policy matrix, which remains owned by the application policy
// boundary.
func (g AcceptedTargetGeneration) Validate() error {
	if err := g.Generation.Validate(); err != nil {
		return ErrReviewStoreInput
	}
	if err := g.Manifest.Validate(); err != nil {
		return ErrReviewStoreInput
	}
	if g.Manifest.CaptureID != g.Generation.CaptureID || g.Manifest.RepositoryID != g.Generation.RepositoryID || g.Manifest.WorktreeID != g.Generation.WorktreeID {
		return ErrReviewStoreInput
	}
	evaluation := g.PolicyEvaluation
	if evaluation.CaptureID != g.Generation.CaptureID || evaluation.CaptureFormatVersion == 0 || evaluation.PolicyVersion == 0 || evaluation.ResourcePolicyVersion == 0 || evaluation.EvidenceVersion == 0 || evaluation.ManifestHash != g.Generation.ManifestHash || !stableText(evaluation.ManifestHash) {
		return ErrReviewStoreInput
	}
	if g.RetentionReference != "" && !stableText(g.RetentionReference) {
		return ErrReviewStoreInput
	}
	if g.Target != nil {
		if err := g.Target.Validate(); err != nil || g.Target.Generation != g.Generation.Generation {
			return ErrReviewStoreInput
		}
	}
	return nil
}

// ReviewStore is the application-owned persistence boundary. The SQLite
// adapter implements this interface; SQL rows never become domain authority.
type ReviewStore interface {
	UpsertRepository(ctx context.Context, repo repository.Repository, worktree repository.WorktreeRef) error
	CreateSession(ctx context.Context, session review.ReviewSession, leaseID domain.SessionLeaseID) (SessionWriteGuard, error)
	ClaimSessionWriter(ctx context.Context, sessionID domain.ReviewSessionID, leaseID domain.SessionLeaseID) (SessionWriteGuard, error)
	FindCompatibleSession(ctx context.Context, key review.SessionKey) (*review.ReviewSession, error)
	ListThreadSummaries(ctx context.Context, sessionID domain.ReviewSessionID, page ThreadPage) (ThreadPageResult, error)
	LoadThread(ctx context.Context, threadID domain.ReviewThreadID) (review.ReviewThread, error)
	ListMessages(ctx context.Context, threadID domain.ReviewThreadID, page MessagePage) (MessagePageResult, error)
	ReadMessageBody(ctx context.Context, bodyRange BodyRange) (MessageBodyChunk, error)
	WithSessionTx(ctx context.Context, guard SessionWriteGuard, fn func(ReviewStoreTx) error) (SessionWriteGuard, error)
	Close() error
}

// ReviewStoreTx contains only application operations needed by the core
// review workflows and staged generation activation.
type ReviewStoreTx interface {
	SaveSession(ctx context.Context, session review.ReviewSession) error
	SaveThread(ctx context.Context, thread review.ReviewThread) error
	SaveMessage(ctx context.Context, message review.Message) error
	SaveCaptureGeneration(ctx context.Context, generation CaptureGeneration, manifest CaptureManifest) error
	SaveAcceptedTargetGeneration(ctx context.Context, generation AcceptedTargetGeneration) error
	CreateReconciliation(ctx context.Context, operation ReconciliationOperation) error
	StageReconciliationResult(ctx context.Context, result ReconciliationAnchorResult) error
	CompleteReconciliation(ctx context.Context, operationID domain.OperationID, completedAt time.Time) error
	ActivateReconciliation(ctx context.Context, operationID domain.OperationID) error
}
