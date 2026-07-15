package app

import (
	"errors"
	"time"

	"github.com/Scottlr/nudge/internal/domain"
	"github.com/Scottlr/nudge/internal/domain/repository"
)

var (
	// ErrReducerClosed reports an input sent after application shutdown.
	ErrReducerClosed = errors.New("application reducer is closed")
	// ErrRepositoryNotLoaded reports a target or file command without a repository.
	ErrRepositoryNotLoaded = errors.New("repository is not loaded")
	// ErrTargetNotLoaded reports a file or refresh command without a target.
	ErrTargetNotLoaded = errors.New("review target is not loaded")
	// ErrOperationNotFound reports an unknown operation identity.
	ErrOperationNotFound = errors.New("operation not found")
	// ErrOperationNotCancellable reports a request for a non-cancellable operation.
	ErrOperationNotCancellable = errors.New("operation is not cancellable")
	// ErrResultDiscarded reports a stale, superseded, cancelled, or completed result.
	ErrResultDiscarded = errors.New("operation result discarded")
	// ErrInvalidReducerInput reports a nil or unsupported reducer input.
	ErrInvalidReducerInput = errors.New("invalid reducer input")
)

// OperationKind identifies the product operation represented in canonical
// state. It is intentionally independent of adapter method names.
type OperationKind string

const (
	OperationOpenRepository        OperationKind = "open_repository"
	OperationSelectTarget          OperationKind = "select_target"
	OperationSelectFile            OperationKind = "select_file"
	OperationRefreshTarget         OperationKind = "refresh_target"
	OperationRequestProposal       OperationKind = "request_proposal"
	OperationApproveProposal       OperationKind = "approve_proposal"
	OperationRejectProposal        OperationKind = "reject_proposal"
	OperationDiscardProposalResult OperationKind = "discard_proposal_result"
)

// OperationStatus describes the lifecycle of one application operation.
type OperationStatus string

const (
	OperationStatusRunning    OperationStatus = "running"
	OperationStatusCancelling OperationStatus = "cancelling"
	OperationStatusSucceeded  OperationStatus = "succeeded"
	OperationStatusCancelled  OperationStatus = "cancelled"
	OperationStatusFailed     OperationStatus = "failed"
)

// OperationState is the safe, bounded operation summary exposed to clients.
type OperationState struct {
	ID               domain.OperationID
	Kind             OperationKind
	Status           OperationStatus
	CorrelationID    CorrelationID
	TargetGeneration repository.TargetGeneration
	StartedAt        time.Time
	FinishedAt       time.Time
	Cancellable      bool
	ErrorCode        ErrorCode
	Message          string
}

func (o OperationState) active() bool {
	return o.Status == OperationStatusRunning || o.Status == OperationStatusCancelling
}

// ResultMetadata identifies the operation and target generation that produced
// an asynchronous result.
type ResultMetadata struct {
	OperationID      domain.OperationID
	CorrelationID    CorrelationID
	TargetGeneration repository.TargetGeneration
}

// Result is a sealed asynchronous fact returned to the reducer. Results are
// never allowed to mutate canonical state outside Reducer.Handle.
type Result interface {
	ReducerInput
	isResult()
	resultMetadata() ResultMetadata
}

// RepositoryLoaded is the normalized result of OpenRepository.
type RepositoryLoaded struct {
	Revision         uint64
	OperationID      domain.OperationID
	CorrelationID    CorrelationID
	TargetGeneration repository.TargetGeneration
	Repository       RepositoryState
}

// TargetLoaded is the normalized result of SelectTarget or RefreshTarget.
type TargetLoaded struct {
	Revision         uint64
	OperationID      domain.OperationID
	CorrelationID    CorrelationID
	TargetGeneration repository.TargetGeneration
	Target           repository.ResolvedTarget
}

// OperationFailed is a safe, typed terminal failure result and event.
type OperationFailed struct {
	Revision         uint64
	OperationID      domain.OperationID
	CorrelationID    CorrelationID
	TargetGeneration repository.TargetGeneration
	Code             ErrorCode
	Message          string
}

// OperationCancelled is a safe, typed terminal cancellation result and event.
type OperationCancelled struct {
	Revision         uint64
	OperationID      domain.OperationID
	CorrelationID    CorrelationID
	TargetGeneration repository.TargetGeneration
	Reason           string
}

func (RepositoryLoaded) isReducerInput()   {}
func (TargetLoaded) isReducerInput()       {}
func (OperationFailed) isReducerInput()    {}
func (OperationCancelled) isReducerInput() {}

func (RepositoryLoaded) isResult()   {}
func (TargetLoaded) isResult()       {}
func (OperationFailed) isResult()    {}
func (OperationCancelled) isResult() {}

func (r RepositoryLoaded) resultMetadata() ResultMetadata {
	return ResultMetadata{OperationID: r.OperationID, CorrelationID: r.CorrelationID, TargetGeneration: r.TargetGeneration}
}

func (r TargetLoaded) resultMetadata() ResultMetadata {
	return ResultMetadata{OperationID: r.OperationID, CorrelationID: r.CorrelationID, TargetGeneration: r.TargetGeneration}
}

func (r OperationFailed) resultMetadata() ResultMetadata {
	return ResultMetadata{OperationID: r.OperationID, CorrelationID: r.CorrelationID, TargetGeneration: r.TargetGeneration}
}

func (r OperationCancelled) resultMetadata() ResultMetadata {
	return ResultMetadata{OperationID: r.OperationID, CorrelationID: r.CorrelationID, TargetGeneration: r.TargetGeneration}
}
