package app

import (
	"context"
	"errors"
	"fmt"

	"github.com/Scottlr/nudge/internal/domain"
	"github.com/Scottlr/nudge/internal/domain/review"
)

var (
	ErrProposalApprovalInvalid     = errors.New("invalid proposal approval")
	ErrProposalApprovalConflict    = errors.New("proposal approval conflict")
	ErrProposalApprovalUnavailable = errors.New("proposal approval unavailable")
	ErrProposalApprovalRecovery    = errors.New("proposal approval requires recovery")
)

// ProposalApplyPreflight is the consumer-owned T112 boundary. It prepares a
// durable operation but does not mutate the edit destination.
type ProposalApplyPreflight interface {
	Prepare(context.Context, ApplyPreflightRequest) (ApplyPreparation, error)
}

// ProposalApplyExecutor is the consumer-owned T113 boundary. It consumes or
// recovers one prepared operation and returns only its bounded classification.
type ProposalApplyExecutor interface {
	Execute(context.Context, ApplyExecutionRequest) (ApplyExecutionResult, error)
}

// ProposalApplyOutcome is the proposal-level projection of the apply journal.
type ProposalApplyOutcome string

const (
	ProposalApplyApplied        ProposalApplyOutcome = "applied"
	ProposalApplyStale          ProposalApplyOutcome = "stale"
	ProposalApplyFailedClean    ProposalApplyOutcome = "failed_clean"
	ProposalApplyRepairRequired ProposalApplyOutcome = "repair_required"
	ProposalApplyRetrySafe      ProposalApplyOutcome = "retry_safe"
)

func (o ProposalApplyOutcome) Validate() error {
	switch o {
	case ProposalApplyApplied, ProposalApplyStale, ProposalApplyFailedClean, ProposalApplyRepairRequired, ProposalApplyRetrySafe:
		return nil
	default:
		return ErrProposalApprovalInvalid
	}
}

// ProposalApplyCommit is the complete application-owned result. A retry-safe
// commit remains in applying and carries no terminal proposal transition.
type ProposalApplyCommit struct {
	Guard     SessionWriteGuard
	Proposal  review.Proposal
	Version   review.ProposedPatch
	Thread    review.ReviewThread
	Operation *ApplyOperation
	Outcome   ProposalApplyOutcome
	Failure   ApplyFailureCode
	Events    []Event
}

// ProposalApplyService owns approval linearization and aggregate finalization.
// T112/T113 own destination locks, Git, journal phases, and result evidence.
type ProposalApplyService struct {
	store      ReviewStore
	proposals  ProposalWorkspaceStore
	operations ApplyOperationStore
	preflight  ProposalApplyPreflight
	executor   ProposalApplyExecutor
	clock      Clock
}

type ProposalApplyServiceConfig struct {
	Store      ReviewStore
	Proposals  ProposalWorkspaceStore
	Operations ApplyOperationStore
	Preflight  ProposalApplyPreflight
	Executor   ProposalApplyExecutor
	Clock      Clock
}

func NewProposalApplyService(config ProposalApplyServiceConfig) (*ProposalApplyService, error) {
	if config.Store == nil || config.Proposals == nil || config.Operations == nil || config.Preflight == nil || config.Executor == nil {
		return nil, ErrProposalApprovalUnavailable
	}
	if config.Clock == nil {
		config.Clock = SystemClock{}
	}
	return &ProposalApplyService{store: config.Store, proposals: config.Proposals, operations: config.Operations, preflight: config.Preflight, executor: config.Executor, clock: config.Clock}, nil
}

// ApproveProposal validates the displayed whole proposal, records applying
// and its operation identity in one session transaction, then delegates to
// T112/T113. The durable operation is consulted before starting new work, so
// duplicate and restarted commands never replay a terminal mutation.
func (s *ProposalApplyService) ApproveProposal(ctx context.Context, command ApproveProposal) (ProposalApplyCommit, error) {
	if s == nil || ctx == nil || command.Validate() != nil {
		return ProposalApplyCommit{}, ErrProposalApprovalInvalid
	}
	aggregate, err := s.proposals.LoadProposalAggregate(ctx, command.ProposalID)
	if err != nil {
		return ProposalApplyCommit{}, err
	}
	if err := aggregate.Validate(); err != nil || aggregate.Proposal.ID != command.ProposalID || aggregate.Proposal.ThreadID != command.ThreadID || aggregate.Workspace.SessionID != command.Guard.SessionID || aggregate.Workspace.RepositoryID != command.Repository.ID || aggregate.Workspace.WorktreeID != command.Worktree.ID {
		return ProposalApplyCommit{}, ErrProposalApprovalInvalid
	}
	version, ok := proposalVersion(aggregate, command.Version)
	if !ok || !approvalMatchesVersion(command, version) || aggregate.Proposal.CurrentVersion == nil || *aggregate.Proposal.CurrentVersion != command.Version {
		return ProposalApplyCommit{}, ErrProposalApprovalConflict
	}
	if gate, ok := s.proposals.(ProposalValidityApprovalGate); ok && (version.Status == review.ProposalVersionDeriving || version.Status == review.ProposalVersionReady || version.Status == review.ProposalVersionApplying) {
		if err := gate.CheckProposalApprovalValidity(ctx, command.ProposalID, command.Version, version.Destination); err != nil {
			return ProposalApplyCommit{}, err
		}
	}
	thread, err := s.store.LoadThread(ctx, command.ThreadID)
	if err != nil {
		return ProposalApplyCommit{}, err
	}
	if thread.Validate() != nil || thread.SessionID != command.Guard.SessionID || thread.ID != command.ThreadID {
		return ProposalApplyCommit{}, ErrProposalApprovalInvalid
	}
	var existingOperation *ApplyOperation
	if loaded, loadErr := s.operations.LoadApplyOperation(ctx, command.OperationID); loadErr == nil {
		if !approvalOperationMatches(command, version, loaded) {
			return ProposalApplyCommit{}, ErrProposalApprovalConflict
		}
		existingOperation = operationPointer(loaded)
	} else if !errors.Is(loadErr, ErrApplyOperationNotFound) {
		return ProposalApplyCommit{}, loadErr
	}

	switch version.Status {
	case review.ProposalVersionApplied, review.ProposalVersionStale, review.ProposalVersionFailed:
		return s.terminalCommit(ctx, command, aggregate, version, thread)
	case review.ProposalVersionRejected:
		return ProposalApplyCommit{}, ErrProposalApprovalConflict
	case review.ProposalVersionReady:
		if thread.Resolution != review.ResolutionOpen || thread.Proposal != review.ProposalReady {
			return ProposalApplyCommit{}, ErrProposalApprovalConflict
		}
		aggregate.Proposal.ApplyingOperationID = operationIDPointer(command.OperationID)
		thread, guard, err := s.beginApplying(ctx, command, version, thread)
		if err != nil {
			return ProposalApplyCommit{Guard: guard, Proposal: aggregate.Proposal, Version: version, Thread: thread}, err
		}
		command.Guard = guard
	case review.ProposalVersionApplying:
		if thread.Proposal != review.ProposalApplying || aggregate.Proposal.ApplyingOperationID == nil || *aggregate.Proposal.ApplyingOperationID != command.OperationID {
			return ProposalApplyCommit{}, ErrProposalApprovalConflict
		}
	default:
		return ProposalApplyCommit{}, ErrProposalApprovalConflict
	}

	if existingOperation != nil {
		return s.executeAndFinalize(ctx, command, aggregate, version, thread, *existingOperation, command.Guard, nil)
	}

	preparation, err := s.preflight.Prepare(ctx, ApplyPreflightRequest{
		Guard: command.Guard, OperationID: command.OperationID, ProposalID: command.ProposalID, ProposalVersion: command.Version,
		ConfirmedReviewRevision: command.ConfirmedReviewRevision, IdempotencyKey: command.IdempotencyKey,
		Repository: command.Repository, Worktree: command.Worktree,
	})
	if err != nil {
		if terminal, failure := s.mapPreflightFailure(err); terminal != "" {
			return s.finalize(ctx, command, aggregate, version, thread, command.Guard, terminal, failure, nil)
		}
		return ProposalApplyCommit{Guard: command.Guard, Proposal: aggregate.Proposal, Version: version, Thread: thread}, err
	}
	return s.executeAndFinalize(ctx, command, aggregate, version, thread, preparation.Operation, preparation.Guard, preparation.Lease)
}

func (s *ProposalApplyService) executeAndFinalize(ctx context.Context, command ApproveProposal, aggregate review.ProposalAggregate, version review.ProposedPatch, thread review.ReviewThread, operation ApplyOperation, guard SessionWriteGuard, lease ApplyExecutionLease) (ProposalApplyCommit, error) {
	if lease != nil {
		defer lease.Close()
	}
	result, err := s.executor.Execute(ctx, ApplyExecutionRequest{
		Guard: guard, OperationID: operation.ID, ProposalID: operation.ProposalID, ProposalVersion: operation.ProposalVersion,
		ProposalPatchSHA256: operation.ProposalPatchSHA256, Repository: command.Repository, Worktree: command.Worktree,
		Lease: lease, AuthorizeRetry: true,
	})
	if err != nil {
		return ProposalApplyCommit{Guard: guard, Proposal: aggregate.Proposal, Version: version, Thread: thread, Operation: operationPointer(operation)}, err
	}
	if err := result.Validate(); err != nil || result.Operation.ID != operation.ID {
		return ProposalApplyCommit{Guard: result.Guard, Proposal: aggregate.Proposal, Version: version, Thread: thread, Operation: operationPointer(operation)}, ErrProposalApprovalRecovery
	}
	switch result.Classification {
	case ApplyExecutionApplied:
		return s.finalize(ctx, command, aggregate, version, thread, result.Guard, ProposalApplyApplied, result.Operation.FailureCode, &result.Operation)
	case ApplyExecutionFailedClean:
		return s.finalize(ctx, command, aggregate, version, thread, result.Guard, ProposalApplyFailedClean, result.Operation.FailureCode, &result.Operation)
	case ApplyExecutionRepairRequired:
		return s.finalize(ctx, command, aggregate, version, thread, result.Guard, ProposalApplyRepairRequired, result.Operation.FailureCode, &result.Operation)
	case ApplyExecutionRetrySafe:
		return ProposalApplyCommit{Guard: result.Guard, Proposal: aggregate.Proposal, Version: version, Thread: thread, Operation: operationPointer(result.Operation), Outcome: ProposalApplyRetrySafe, Failure: result.Operation.FailureCode}, nil
	default:
		return ProposalApplyCommit{}, ErrProposalApprovalRecovery
	}
}

func (s *ProposalApplyService) beginApplying(ctx context.Context, command ApproveProposal, version review.ProposedPatch, thread review.ReviewThread) (review.ReviewThread, SessionWriteGuard, error) {
	now := s.clock.Now().UTC()
	if now.IsZero() {
		return thread, command.Guard, ErrProposalApprovalInvalid
	}
	if now.Before(thread.UpdatedAt) {
		now = thread.UpdatedAt
	}
	if err := thread.SetProposal(review.ProposalApplying, proposalIDPointer(command.ProposalID), now); err != nil {
		return thread, command.Guard, err
	}
	guard, err := s.store.WithSessionTx(ctx, command.Guard, func(tx ReviewStoreTx) error {
		proposalTx, ok := tx.(ProposalWorkspaceStoreTx)
		if !ok {
			return ErrProposalApprovalUnavailable
		}
		if err := proposalTx.TransitionProposal(ctx, review.ProposalTransition{ProposalID: command.ProposalID, Version: version.Version, Status: review.ProposalVersionApplying, ApplyOperationID: command.OperationID, Reason: "proposal approval authorized", ChangedAt: now}); err != nil {
			return err
		}
		return tx.SaveThread(ctx, thread)
	})
	if err != nil {
		return thread, guard, err
	}
	return thread, guard, nil
}

func (s *ProposalApplyService) finalize(ctx context.Context, command ApproveProposal, aggregate review.ProposalAggregate, version review.ProposedPatch, thread review.ReviewThread, guard SessionWriteGuard, outcome ProposalApplyOutcome, failure ApplyFailureCode, operation *ApplyOperation) (ProposalApplyCommit, error) {
	status, reason, phase := proposalStatusForApplyOutcome(outcome, failure)
	now := s.clock.Now().UTC()
	if now.IsZero() {
		return ProposalApplyCommit{Guard: guard, Proposal: aggregate.Proposal, Version: version, Thread: thread, Operation: operation, Outcome: outcome, Failure: failure}, ErrProposalApprovalInvalid
	}
	if now.Before(thread.UpdatedAt) {
		now = thread.UpdatedAt
	}
	threadState := review.ProposalFailed
	switch status {
	case review.ProposalVersionApplied:
		threadState = review.ProposalApplied
	case review.ProposalVersionStale:
		threadState = review.ProposalStale
	}
	if err := thread.SetProposal(threadState, proposalIDPointer(command.ProposalID), now); err != nil {
		return ProposalApplyCommit{Guard: guard, Proposal: aggregate.Proposal, Version: version, Thread: thread, Operation: operation, Outcome: outcome, Failure: failure}, err
	}
	nextGuard, err := s.store.WithSessionTx(ctx, guard, func(tx ReviewStoreTx) error {
		proposalTx, ok := tx.(ProposalWorkspaceStoreTx)
		if !ok {
			return ErrProposalApprovalUnavailable
		}
		if err := proposalTx.TransitionProposal(ctx, review.ProposalTransition{ProposalID: command.ProposalID, Version: version.Version, Status: status, FailurePhase: phase, ApplyOperationID: command.OperationID, Reason: reason, ChangedAt: now}); err != nil {
			return err
		}
		return tx.SaveThread(ctx, thread)
	})
	if err != nil {
		return ProposalApplyCommit{Guard: guard, Proposal: aggregate.Proposal, Version: version, Thread: thread, Operation: operation, Outcome: outcome, Failure: failure}, err
	}
	aggregate.Proposal.Status = status
	aggregate.Proposal.CurrentVersion = proposalVersionPointer(version.Version)
	aggregate.Proposal.ApplyingOperationID = operationIDPointer(command.OperationID)
	version.Status = status
	version.StatusReason = reason
	version.StatusChangedAt = &now
	return ProposalApplyCommit{Guard: nextGuard, Proposal: aggregate.Proposal, Version: version, Thread: thread, Operation: operation, Outcome: outcome, Failure: failure, Events: []Event{ProposalApplicationFinalized{ProposalID: command.ProposalID, WorkspaceID: aggregate.Workspace.ID, ThreadID: command.ThreadID, Version: version.Version, OperationID: command.OperationID, CorrelationID: command.CorrelationID, Outcome: outcome}}}, nil
}

func (s *ProposalApplyService) terminalCommit(ctx context.Context, command ApproveProposal, aggregate review.ProposalAggregate, version review.ProposedPatch, thread review.ReviewThread) (ProposalApplyCommit, error) {
	outcome, failure := proposalApplyOutcomeForStatus(version.Status, version.StatusReason)
	var operation *ApplyOperation
	if aggregate.Proposal.ApplyingOperationID != nil {
		loaded, err := s.operations.LoadApplyOperation(ctx, *aggregate.Proposal.ApplyingOperationID)
		if err == nil {
			operation = operationPointer(loaded)
		} else if !errors.Is(err, ErrApplyOperationNotFound) {
			return ProposalApplyCommit{}, err
		}
	}
	return ProposalApplyCommit{Guard: command.Guard, Proposal: aggregate.Proposal, Version: version, Thread: thread, Operation: operation, Outcome: outcome, Failure: failure}, nil
}

func (s *ProposalApplyService) mapPreflightFailure(err error) (ProposalApplyOutcome, ApplyFailureCode) {
	switch {
	case errors.Is(err, ErrApplyStale), errors.Is(err, ErrApplyPreflightRace):
		return ProposalApplyStale, ApplyFailureNone
	case errors.Is(err, ErrApplyPatchCheckFailed), errors.Is(err, ErrApplyUnsupported):
		return ProposalApplyFailedClean, ApplyFailureUnsupported
	default:
		return "", ApplyFailureNone
	}
}

func proposalStatusForApplyOutcome(outcome ProposalApplyOutcome, failure ApplyFailureCode) (review.ProposalStatus, string, review.ProposalFailurePhase) {
	switch outcome {
	case ProposalApplyApplied:
		return review.ProposalVersionApplied, "proposal applied", review.ProposalFailureNone
	case ProposalApplyStale:
		return review.ProposalVersionStale, "proposal became stale before mutation", review.ProposalFailureDestination
	case ProposalApplyFailedClean:
		return review.ProposalVersionFailed, fmt.Sprintf("apply failed cleanly: %s", failure), review.ProposalFailureDestination
	case ProposalApplyRepairRequired:
		return review.ProposalVersionFailed, fmt.Sprintf("repair_required: %s", failure), review.ProposalFailureDestination
	default:
		return review.ProposalVersionFailed, "proposal application did not produce a terminal result", review.ProposalFailureDestination
	}
}

func proposalApplyOutcomeForStatus(status review.ProposalStatus, reason string) (ProposalApplyOutcome, ApplyFailureCode) {
	switch status {
	case review.ProposalVersionApplied:
		return ProposalApplyApplied, ApplyFailureNone
	case review.ProposalVersionStale:
		return ProposalApplyStale, ApplyFailureNone
	case review.ProposalVersionFailed:
		if len(reason) >= len("repair_required:") && reason[:len("repair_required:")] == "repair_required:" {
			return ProposalApplyRepairRequired, ApplyFailureMixedState
		}
		return ProposalApplyFailedClean, ApplyFailureMutationClean
	default:
		return ProposalApplyRepairRequired, ApplyFailureMixedState
	}
}

func approvalMatchesVersion(command ApproveProposal, version review.ProposedPatch) bool {
	return version.ProposalID == command.ProposalID && version.Version == command.Version && version.PatchSHA256 == command.PatchSHA256 && version.Destination == command.Destination && proposalCompletenessIdentity(version) == command.ReviewCompletenessIdentity
}

func approvalOperationMatches(command ApproveProposal, version review.ProposedPatch, operation ApplyOperation) bool {
	return operation.Validate() == nil && operation.ID == command.OperationID && operation.SessionID == command.Guard.SessionID && operation.ProposalID == command.ProposalID && operation.WorkspaceID == version.WorkspaceID && operation.ThreadID == command.ThreadID && operation.ProposalVersion == command.Version && operation.IdempotencyKey == command.IdempotencyKey && operation.ConfirmedReviewRevision == command.ConfirmedReviewRevision && operation.ProposalPatchSHA256 == command.PatchSHA256 && operation.PatchArtifact == version.Artifact && operation.Destination == command.Destination && operation.Evidence.RepositoryID == command.Repository.ID && operation.Evidence.WorktreeID == command.Worktree.ID
}

func proposalCompletenessIdentity(version review.ProposedPatch) string {
	if version.Artifact != (review.ProposedPatchArtifactReference{}) {
		return version.Artifact.IndexHash
	}
	return version.PatchSHA256
}

func (c ApproveProposal) Validate() error {
	if c.Guard.Validate() != nil || c.ThreadID == "" || c.ProposalID == "" || c.Version == 0 || !validSHA256(c.PatchSHA256) || c.ConfirmedReviewRevision == 0 || !validSHA256(c.ReviewCompletenessIdentity) || c.Destination.Validate() != nil || c.Repository.Validate() != nil || c.Worktree.Validate() != nil || c.Worktree.RepositoryID != c.Repository.ID || !safeText(c.IdempotencyKey) || c.OperationID == "" || c.CorrelationID == "" {
		return ErrProposalApprovalInvalid
	}
	return nil
}

func operationIDPointer(value domain.OperationID) *domain.OperationID {
	copyValue := value
	return &copyValue
}

func operationPointer(value ApplyOperation) *ApplyOperation {
	copyValue := value
	return &copyValue
}
