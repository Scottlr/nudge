package app

import (
	"context"
	"errors"
	"time"

	"github.com/Scottlr/nudge/internal/domain"
	"github.com/Scottlr/nudge/internal/domain/review"
)

var (
	ErrProposalDispositionInvalid          = errors.New("invalid proposal disposition")
	ErrProposalDispositionConflict         = errors.New("proposal disposition conflict")
	ErrProposalDispositionUnavailable      = errors.New("proposal disposition unavailable")
	ErrProposalDispositionRecoveryRequired = errors.New("proposal disposition requires workspace recovery")
)

// ProposalDispositionCommit is the durable result of rejection or failed
// result discard. Attempt is populated only for a discard operation.
type ProposalDispositionCommit struct {
	Guard     SessionWriteGuard
	Proposal  review.Proposal
	Workspace review.ProposalWorkspace
	Thread    review.ReviewThread
	Attempt   *review.ProposalAttempt
	Events    []Event
}

// ProposalDispositionService owns the application phase ordering around
// exact proposal rejection, failed-result discard, and T035 reset evidence.
// It never performs filesystem operations itself.
type ProposalDispositionService struct {
	store     ReviewStore
	proposals ProposalWorkspaceStore
	lifecycle ProposalWorkspaceLifecycleStore
	resetter  ProposalBaselineResetter
	clock     Clock
}

// ProposalDispositionServiceConfig composes the durable proposal stores with
// the verified baseline-reset boundary.
type ProposalDispositionServiceConfig struct {
	Store     ReviewStore
	Proposals ProposalWorkspaceStore
	Lifecycle ProposalWorkspaceLifecycleStore
	Resetter  ProposalBaselineResetter
	Clock     Clock
}

// NewProposalDispositionService requires all authorities needed to retain
// history, load the immutable baseline, and reset only the isolated result.
func NewProposalDispositionService(config ProposalDispositionServiceConfig) (*ProposalDispositionService, error) {
	if config.Store == nil || config.Proposals == nil || config.Lifecycle == nil || config.Resetter == nil {
		return nil, ErrProposalDispositionUnavailable
	}
	if config.Clock == nil {
		config.Clock = SystemClock{}
	}
	return &ProposalDispositionService{store: config.Store, proposals: config.Proposals, lifecycle: config.Lifecycle, resetter: config.Resetter, clock: config.Clock}, nil
}

// RejectProposal records rejection before resetting the isolated result. A
// repeated command is idempotent after the workspace is verified ready.
func (s *ProposalDispositionService) RejectProposal(ctx context.Context, command RejectProposal) (ProposalDispositionCommit, error) {
	if s == nil || ctx == nil || command.Guard.Validate() != nil || command.ThreadID == "" || command.ProposalID == "" || command.Version == 0 || command.OperationID == "" || command.CorrelationID == "" || !safeOptionalText(command.Reason, 256) {
		return ProposalDispositionCommit{}, ErrProposalDispositionInvalid
	}
	aggregate, err := s.proposals.LoadProposalAggregate(ctx, command.ProposalID)
	if err != nil {
		return ProposalDispositionCommit{}, err
	}
	if err := aggregate.Validate(); err != nil || aggregate.Proposal.ThreadID != command.ThreadID || aggregate.Workspace.SessionID != command.Guard.SessionID {
		return ProposalDispositionCommit{}, ErrProposalDispositionInvalid
	}
	version, ok := proposalVersion(aggregate, command.Version)
	if !ok {
		return ProposalDispositionCommit{}, ErrProposalDispositionConflict
	}
	thread, err := s.store.LoadThread(ctx, command.ThreadID)
	if err != nil {
		return ProposalDispositionCommit{}, err
	}
	if thread.SessionID != command.Guard.SessionID {
		return ProposalDispositionCommit{}, ErrProposalDispositionInvalid
	}
	if version.Status == review.ProposalVersionRejected && aggregate.Workspace.State == review.WorkspaceReady {
		return ProposalDispositionCommit{Guard: command.Guard, Proposal: aggregate.Proposal, Workspace: aggregate.Workspace, Thread: thread}, nil
	}
	if version.Status != review.ProposalVersionReady && version.Status != review.ProposalVersionRejected {
		return ProposalDispositionCommit{}, ErrProposalDispositionConflict
	}
	if aggregate.Workspace.State == review.WorkspaceRemoved {
		return ProposalDispositionCommit{}, ErrProposalDispositionRecoveryRequired
	}
	now := s.clock.Now().UTC()
	if now.IsZero() {
		return ProposalDispositionCommit{}, ErrProposalDispositionInvalid
	}
	workspace := aggregate.Workspace
	if workspace.State != review.WorkspaceResetting {
		if err := workspace.Transition(review.WorkspaceResetting); err != nil {
			return ProposalDispositionCommit{}, ErrProposalDispositionRecoveryRequired
		}
		workspace.UpdatedAt = now
	}
	threadProposal := thread
	if threadProposal.Proposal != review.ProposalRejected {
		if err := threadProposal.SetProposal(review.ProposalRejected, proposalIDPointer(command.ProposalID), now); err != nil {
			return ProposalDispositionCommit{}, err
		}
	}
	if version.Status == review.ProposalVersionReady {
		reason := command.Reason
		if reason == "" {
			reason = "proposal rejected"
		}
		guard, err := s.store.WithSessionTx(ctx, command.Guard, func(tx ReviewStoreTx) error {
			proposalTx, ok := tx.(ProposalWorkspaceStoreTx)
			if !ok {
				return ErrProposalDispositionUnavailable
			}
			if err := proposalTx.TransitionProposal(ctx, review.ProposalTransition{ProposalID: command.ProposalID, Version: command.Version, Status: review.ProposalVersionRejected, Reason: reason, ChangedAt: now}); err != nil {
				return err
			}
			return persistDispositionWorkspace(ctx, tx, workspace, threadProposal)
		})
		if err != nil {
			return ProposalDispositionCommit{Guard: guard, Proposal: aggregate.Proposal, Workspace: workspace, Thread: threadProposal}, err
		}
		command.Guard = guard
	} else {
		guard, err := s.persistWorkspaceAndThread(ctx, command.Guard, workspace, threadProposal)
		if err != nil {
			return ProposalDispositionCommit{Guard: guard, Proposal: aggregate.Proposal, Workspace: workspace, Thread: threadProposal}, err
		}
		command.Guard = guard
	}
	proposal := aggregate.Proposal
	proposal.Status = review.ProposalVersionRejected
	proposal.CurrentVersion = proposalVersionPointer(command.Version)
	proposal.UpdatedAt = now
	resetRequest, err := s.resetRequest(ctx, command.Guard, aggregate, version, nil, command.OperationID)
	if err != nil {
		return ProposalDispositionCommit{Guard: command.Guard, Proposal: proposal, Workspace: workspace, Thread: threadProposal}, err
	}
	if err := s.resetter.ResetToBaseline(ctx, resetRequest); err != nil {
		repair := workspace
		if repair.State != review.WorkspaceRepairRequired {
			if transitionErr := repair.Transition(review.WorkspaceRepairRequired); transitionErr != nil {
				return ProposalDispositionCommit{Guard: command.Guard, Proposal: aggregate.Proposal, Workspace: workspace, Thread: threadProposal}, errors.Join(err, transitionErr)
			}
			repair.UpdatedAt = now
		}
		guard, persistErr := s.persistWorkspace(ctx, command.Guard, repair)
		return ProposalDispositionCommit{Guard: guard, Proposal: proposal, Workspace: repair, Thread: threadProposal}, errors.Join(err, persistErr)
	}
	workspace.State = review.WorkspaceReady
	workspace.UpdatedAt = now
	guard, err := s.persistWorkspace(ctx, command.Guard, workspace)
	if err != nil {
		return ProposalDispositionCommit{Guard: guard, Proposal: proposal, Workspace: workspace, Thread: threadProposal}, err
	}
	return ProposalDispositionCommit{Guard: guard, Proposal: proposal, Workspace: workspace, Thread: threadProposal, Events: []Event{ProposalRejected{ProposalID: command.ProposalID, WorkspaceID: workspace.ID, ThreadID: command.ThreadID, Version: command.Version, OperationID: command.OperationID, CorrelationID: command.CorrelationID, Reason: rejectionReason(command.Reason)}}}, nil
}

// DiscardProposalResult records a failed attempt's discard decision before
// the same verified reset boundary. A ready proposal version can never be
// discarded through this command.
func (s *ProposalDispositionService) DiscardProposalResult(ctx context.Context, command DiscardProposalResult) (ProposalDispositionCommit, error) {
	if s == nil || ctx == nil || command.Guard.Validate() != nil || command.ProposalID == "" || command.AttemptID == "" || command.OperationID == "" || command.CorrelationID == "" || !safeOptionalText(command.Reason, 256) {
		return ProposalDispositionCommit{}, ErrProposalDispositionInvalid
	}
	aggregate, err := s.proposals.LoadProposalAggregate(ctx, command.ProposalID)
	if err != nil {
		return ProposalDispositionCommit{}, err
	}
	if err := aggregate.Validate(); err != nil || aggregate.Workspace.SessionID != command.Guard.SessionID {
		return ProposalDispositionCommit{}, ErrProposalDispositionInvalid
	}
	attempt, ok := proposalAttemptByID(aggregate, command.AttemptID)
	if !ok || attempt.Outcome != review.ProposalAttemptFailed || attempt.ResultDisposition == review.ProposalResultNone {
		return ProposalDispositionCommit{}, ErrProposalDispositionConflict
	}
	if aggregate.Proposal.Status == review.ProposalVersionReady {
		return ProposalDispositionCommit{}, ErrProposalDispositionConflict
	}
	thread, err := s.store.LoadThread(ctx, aggregate.Proposal.ThreadID)
	if err != nil {
		return ProposalDispositionCommit{}, err
	}
	if thread.SessionID != command.Guard.SessionID {
		return ProposalDispositionCommit{}, ErrProposalDispositionInvalid
	}
	if attempt.ResultDisposition == review.ProposalResultDiscarded && aggregate.Workspace.State == review.WorkspaceReady {
		copyAttempt := attempt
		return ProposalDispositionCommit{Guard: command.Guard, Proposal: aggregate.Proposal, Workspace: aggregate.Workspace, Thread: thread, Attempt: &copyAttempt}, nil
	}
	if attempt.ResultDisposition != review.ProposalResultPresent && attempt.ResultDisposition != review.ProposalResultDiscarding {
		return ProposalDispositionCommit{}, ErrProposalDispositionConflict
	}
	now := s.clock.Now().UTC()
	if now.IsZero() {
		return ProposalDispositionCommit{}, ErrProposalDispositionInvalid
	}
	workspace := aggregate.Workspace
	if workspace.State != review.WorkspaceResetting {
		if err := workspace.Transition(review.WorkspaceResetting); err != nil {
			return ProposalDispositionCommit{}, ErrProposalDispositionRecoveryRequired
		}
		workspace.UpdatedAt = now
	}
	if attempt.ResultDisposition == review.ProposalResultPresent {
		attempt.ResultDisposition = review.ProposalResultDiscarding
		attempt.ResultDispositionReason = discardReason(command.Reason)
		attempt.ResultDispositionChangedAt = timePointer(now)
		guard, err := s.store.WithSessionTx(ctx, command.Guard, func(tx ReviewStoreTx) error {
			dispositionTx, ok := tx.(ProposalResultDispositionStoreTx)
			if !ok {
				return ErrProposalDispositionUnavailable
			}
			if err := dispositionTx.TransitionProposalResultDisposition(ctx, attempt); err != nil {
				return err
			}
			return persistDispositionWorkspace(ctx, tx, workspace, thread)
		})
		if err != nil {
			return ProposalDispositionCommit{Guard: guard, Proposal: aggregate.Proposal, Workspace: workspace, Thread: thread, Attempt: &attempt}, err
		}
		command.Guard = guard
	}
	resetRequest, err := s.resetRequest(ctx, command.Guard, aggregate, review.ProposedPatch{}, &attempt, command.OperationID)
	if err != nil {
		return ProposalDispositionCommit{Guard: command.Guard, Proposal: aggregate.Proposal, Workspace: workspace, Thread: thread, Attempt: &attempt}, err
	}
	if err := s.resetter.ResetToBaseline(ctx, resetRequest); err != nil {
		repair := workspace
		if repair.State != review.WorkspaceRepairRequired {
			if transitionErr := repair.Transition(review.WorkspaceRepairRequired); transitionErr != nil {
				return ProposalDispositionCommit{Guard: command.Guard, Proposal: aggregate.Proposal, Workspace: workspace, Thread: thread, Attempt: &attempt}, errors.Join(err, transitionErr)
			}
			repair.UpdatedAt = now
		}
		guard, persistErr := s.persistWorkspace(ctx, command.Guard, repair)
		return ProposalDispositionCommit{Guard: guard, Proposal: aggregate.Proposal, Workspace: repair, Thread: thread, Attempt: &attempt}, errors.Join(err, persistErr)
	}
	attempt.ResultDisposition = review.ProposalResultDiscarded
	attempt.ResultDispositionChangedAt = timePointer(now)
	workspace.State = review.WorkspaceReady
	workspace.UpdatedAt = now
	guard, err := s.store.WithSessionTx(ctx, command.Guard, func(tx ReviewStoreTx) error {
		dispositionTx, ok := tx.(ProposalResultDispositionStoreTx)
		if !ok {
			return ErrProposalDispositionUnavailable
		}
		if err := dispositionTx.TransitionProposalResultDisposition(ctx, attempt); err != nil {
			return err
		}
		return persistDispositionWorkspace(ctx, tx, workspace, thread)
	})
	if err != nil {
		return ProposalDispositionCommit{Guard: guard, Proposal: aggregate.Proposal, Workspace: workspace, Thread: thread, Attempt: &attempt}, err
	}
	return ProposalDispositionCommit{Guard: guard, Proposal: aggregate.Proposal, Workspace: workspace, Thread: thread, Attempt: &attempt, Events: []Event{ProposalResultDiscarded{ProposalID: command.ProposalID, WorkspaceID: workspace.ID, ThreadID: aggregate.Proposal.ThreadID, AttemptID: attempt.ID, OperationID: command.OperationID, CorrelationID: command.CorrelationID, Reason: attempt.ResultDispositionReason}}}, nil
}

func (s *ProposalDispositionService) resetRequest(ctx context.Context, guard SessionWriteGuard, aggregate review.ProposalAggregate, version review.ProposedPatch, attempt *review.ProposalAttempt, operationID domain.OperationID) (ProposalBaselineResetRequest, error) {
	lifecycle, err := s.lifecycle.LoadLatestProposalWorkspaceLifecycle(ctx, aggregate.Workspace.ID)
	if err != nil || lifecycle.WorkspaceID != aggregate.Workspace.ID || lifecycle.ThreadID != aggregate.Workspace.SourceThreadID || lifecycle.Baseline.Validate() != nil {
		return ProposalBaselineResetRequest{}, ErrProposalDispositionRecoveryRequired
	}
	attemptID := operationID
	baseline := review.SnapshotIdentity{}
	if attempt != nil {
		attemptID = attempt.ID
	} else {
		attemptID = version.AttemptID
		baseline = version.Baseline
	}
	if attemptID == "" {
		return ProposalBaselineResetRequest{}, ErrProposalDispositionInvalid
	}
	return ProposalBaselineResetRequest{SessionID: guard.SessionID, ProposalID: aggregate.Proposal.ID, WorkspaceID: aggregate.Workspace.ID, AttemptID: attemptID, OperationID: operationID, Baseline: baseline, BaselineManifest: lifecycle.Baseline.Clone()}, nil
}

func persistDispositionWorkspace(ctx context.Context, tx ReviewStoreTx, workspace review.ProposalWorkspace, thread review.ReviewThread) error {
	lifecycleTx, ok := tx.(ProposalWorkspaceLifecycleStoreTx)
	if !ok {
		return ErrProposalDispositionUnavailable
	}
	if err := lifecycleTx.UpdateProposalWorkspace(ctx, workspace); err != nil {
		return err
	}
	if thread.Proposal == review.ProposalRejected {
		return tx.SaveThread(ctx, thread)
	}
	return nil
}

func (s *ProposalDispositionService) persistWorkspace(ctx context.Context, guard SessionWriteGuard, workspace review.ProposalWorkspace) (SessionWriteGuard, error) {
	return s.store.WithSessionTx(ctx, guard, func(tx ReviewStoreTx) error {
		lifecycleTx, ok := tx.(ProposalWorkspaceLifecycleStoreTx)
		if !ok {
			return ErrProposalDispositionUnavailable
		}
		return lifecycleTx.UpdateProposalWorkspace(ctx, workspace)
	})
}

func (s *ProposalDispositionService) persistWorkspaceAndThread(ctx context.Context, guard SessionWriteGuard, workspace review.ProposalWorkspace, thread review.ReviewThread) (SessionWriteGuard, error) {
	return s.store.WithSessionTx(ctx, guard, func(tx ReviewStoreTx) error {
		return persistDispositionWorkspace(ctx, tx, workspace, thread)
	})
}

func proposalVersion(aggregate review.ProposalAggregate, version review.ProposalVersionNumber) (review.ProposedPatch, bool) {
	for _, candidate := range aggregate.Versions {
		if candidate.Version == version {
			return candidate, true
		}
	}
	return review.ProposedPatch{}, false
}

func proposalAttemptByID(aggregate review.ProposalAggregate, attemptID domain.OperationID) (review.ProposalAttempt, bool) {
	for _, candidate := range aggregate.Attempts {
		if candidate.ID == attemptID {
			return candidate, true
		}
	}
	return review.ProposalAttempt{}, false
}

func proposalIDPointer(id domain.ProposalID) *domain.ProposalID {
	copyID := id
	return &copyID
}

func proposalVersionPointer(value review.ProposalVersionNumber) *review.ProposalVersionNumber {
	copyValue := value
	return &copyValue
}

func timePointer(value time.Time) *time.Time {
	copyValue := value
	return &copyValue
}

func discardReason(reason string) string {
	if reason == "" {
		return "failed proposal result discarded"
	}
	return reason
}

func rejectionReason(reason string) string {
	if reason == "" {
		return "proposal rejected"
	}
	return reason
}
