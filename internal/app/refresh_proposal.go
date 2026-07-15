package app

import (
	"context"
	"errors"

	"github.com/Scottlr/nudge/internal/domain"
	"github.com/Scottlr/nudge/internal/domain/review"
)

var (
	ErrProposalRefreshInvalid                  = errors.New("invalid proposal refresh")
	ErrProposalRefreshUnavailable              = errors.New("proposal refresh is unavailable")
	ErrProposalRefreshIneligible               = errors.New("proposal version is not eligible for refresh")
	ErrProposalRefreshAnchorConfirmationNeeded = errors.New("proposal anchor requires explicit confirmation before refresh")
	ErrProposalRefreshGenerationConflict       = errors.New("proposal refresh generation conflicts with the recorded lineage")
)

// Validate checks the command before a workspace owner or provider is
// contacted. The intent is immutable except for its new source provenance.
func (c RefreshProposal) Validate() error {
	if c.Guard.Validate() != nil || c.ThreadID == "" || c.ProposalID == "" || c.Version == 0 || c.ConversationID == "" || c.OperationID == "" || c.CorrelationID == "" || c.Intent.Validate() != nil || c.Intent.ID != c.ProposalID || c.Intent.ThreadID != c.ThreadID || c.Context.Validate() != nil || c.Provenance.Validate() != nil || c.Intent.ConfirmedAgainst != c.Provenance {
		return ErrProposalRefreshInvalid
	}
	if c.Eligibility != nil && c.Eligibility.Validate() != nil {
		return ErrProposalRefreshInvalid
	}
	return nil
}

// ProposalRefreshWorkspaceRequest asks the workspace owner to replace the
// isolated baseline/result contents with one newly accepted immutable source.
// The owner must prove reset, materialization, and isolation before returning.
type ProposalRefreshWorkspaceRequest struct {
	Guard         SessionWriteGuard
	OperationID   domain.OperationID
	Aggregate     review.ProposalAggregate
	Provenance    review.GenerationProvenance
	Source        AcceptedTreeSource
	AnchorConfirm bool
}

func (r ProposalRefreshWorkspaceRequest) Validate() error {
	if r.Guard.Validate() != nil || r.OperationID == "" || r.Aggregate.Validate() != nil || r.Provenance.Validate() != nil || r.Source == nil || r.Source.Identity().Validate() != nil || r.Provenance.SessionID != r.Guard.SessionID || r.Aggregate.Workspace.SessionID != r.Guard.SessionID || r.Provenance.Generation <= r.Aggregate.Workspace.SourceGeneration.Generation {
		return ErrProposalRefreshInvalid
	}
	return nil
}

// ProposalRefreshWorkspaceResult is the verified workspace/lifecycle state
// returned after the old result has been discarded and the new baseline has
// been materialized.
type ProposalRefreshWorkspaceResult struct {
	Workspace review.ProposalWorkspace
	Lifecycle ProposalWorkspaceLifecycle
}

func (r ProposalRefreshWorkspaceResult) Validate(request ProposalRefreshWorkspaceRequest) error {
	if request.Validate() != nil || r.Workspace.Validate() != nil || r.Lifecycle.Validate() != nil || r.Workspace.ID != request.Aggregate.Workspace.ID || r.Workspace.SessionID != request.Guard.SessionID || r.Workspace.SourceThreadID != request.Aggregate.Proposal.ThreadID || r.Workspace.State != review.WorkspaceReady || r.Workspace.SourceGeneration != request.Provenance || r.Lifecycle.WorkspaceID != r.Workspace.ID || r.Lifecycle.SessionID != r.Workspace.SessionID || r.Lifecycle.ThreadID != r.Workspace.SourceThreadID || r.Lifecycle.Phase != WorkspaceLifecycleReady || r.Lifecycle.Purpose != WorkspacePurposeRefreshBaseline {
		return ErrProposalRefreshInvalid
	}
	if r.Lifecycle.Source.Generation != 0 && r.Lifecycle.Source.Generation != request.Provenance.Generation {
		return ErrProposalRefreshGenerationConflict
	}
	return nil
}

// ProposalRefreshWorkspace owns the filesystem-facing refresh. It must not
// mutate the destination or provider roots and must return a ready workspace.
type ProposalRefreshWorkspace interface {
	RefreshProposalWorkspace(context.Context, ProposalRefreshWorkspaceRequest) (ProposalRefreshWorkspaceResult, error)
}

// ProposalRefreshStoreTx is the narrow durable seam for rebinding the intent
// to a new source generation while retaining the same proposal lineage.
type ProposalRefreshStoreTx interface {
	UpdateProposalIntent(context.Context, review.ProposalIntent) error
}

// ProposalRefreshCommit contains the refreshed lineage and the provider turn
// started on the original local conversation.
type ProposalRefreshCommit struct {
	Guard            SessionWriteGuard
	PreviousVersion  review.ProposalVersionNumber
	SourceGeneration review.GenerationProvenance
	ConversationID   domain.ProviderConversationID
	Turn             ProposalTurnCommit
}

// ProposalRefreshService coordinates stale-version refresh. Old versions and
// the normalized transcript remain untouched; only the intent's source
// generation and the next attempt move forward.
type ProposalRefreshService struct {
	store     ReviewStore
	proposals ProposalWorkspaceStore
	lifecycle ProposalWorkspaceLifecycleStore
	workspace ProposalRefreshWorkspace
	turns     *ProposalTurnService
	clock     Clock
}

type ProposalRefreshServiceConfig struct {
	Store     ReviewStore
	Proposals ProposalWorkspaceStore
	Lifecycle ProposalWorkspaceLifecycleStore
	Workspace ProposalRefreshWorkspace
	Turns     *ProposalTurnService
	Clock     Clock
}

func NewProposalRefreshService(config ProposalRefreshServiceConfig) (*ProposalRefreshService, error) {
	if config.Store == nil || config.Proposals == nil || config.Lifecycle == nil || config.Workspace == nil || config.Turns == nil {
		return nil, ErrProposalRefreshUnavailable
	}
	if config.Clock == nil {
		config.Clock = SystemClock{}
	}
	return &ProposalRefreshService{store: config.Store, proposals: config.Proposals, lifecycle: config.Lifecycle, workspace: config.Workspace, turns: config.Turns, clock: config.Clock}, nil
}

// Refresh validates the stale lineage, rebuilds the isolated workspace, and
// starts a new proposal turn on the existing provider conversation.
func (s *ProposalRefreshService) Refresh(ctx context.Context, command RefreshProposal, source AcceptedTreeSource) (ProposalRefreshCommit, error) {
	if s == nil || ctx == nil || command.Validate() != nil || source == nil {
		return ProposalRefreshCommit{}, ErrProposalRefreshInvalid
	}
	aggregate, err := s.proposals.LoadProposalAggregate(ctx, command.ProposalID)
	if err != nil {
		return ProposalRefreshCommit{}, err
	}
	if aggregate.Validate() != nil || aggregate.Proposal.ThreadID != command.ThreadID || aggregate.Workspace.SessionID != command.Guard.SessionID || aggregate.Intent.ID != command.Intent.ID || !proposalRefreshIntentEqual(aggregate.Intent, command.Intent) {
		return ProposalRefreshCommit{}, ErrProposalRefreshInvalid
	}
	version, ok := proposalVersion(aggregate, command.Version)
	if !ok || (version.Status != review.ProposalVersionStale && version.Status != review.ProposalVersionRejected) {
		return ProposalRefreshCommit{}, ErrProposalRefreshIneligible
	}
	thread, err := s.store.LoadThread(ctx, command.ThreadID)
	if err != nil {
		return ProposalRefreshCommit{}, err
	}
	if thread.SessionID != command.Guard.SessionID || thread.ProviderConversationID == nil || *thread.ProviderConversationID != command.ConversationID {
		return ProposalRefreshCommit{}, ErrThreadNotOwned
	}
	if (thread.Anchor.State == review.AnchorAmbiguous || thread.Anchor.State == review.AnchorOrphaned) && !command.AnchorConfirmed {
		return ProposalRefreshCommit{}, ErrProposalRefreshAnchorConfirmationNeeded
	}
	if command.Provenance.Generation <= aggregate.Intent.ConfirmedAgainst.Generation || command.Provenance.SessionID != command.Guard.SessionID || command.Intent.ConfirmedAgainst != command.Provenance {
		return ProposalRefreshCommit{}, ErrProposalRefreshGenerationConflict
	}
	if source.Identity().Validate() != nil {
		return ProposalRefreshCommit{}, ErrProposalRefreshInvalid
	}
	request := ProposalRefreshWorkspaceRequest{Guard: command.Guard, OperationID: command.OperationID, Aggregate: aggregate, Provenance: command.Provenance, Source: source, AnchorConfirm: command.AnchorConfirmed}
	refreshed, err := s.workspace.RefreshProposalWorkspace(ctx, request)
	if err != nil {
		return ProposalRefreshCommit{}, err
	}
	if refreshed.Validate(request) != nil {
		return ProposalRefreshCommit{}, ErrProposalRefreshInvalid
	}
	now := s.clock.Now().UTC()
	if now.IsZero() || command.Intent.ConfirmedAt.Before(aggregate.Intent.ConfirmedAt) || command.Intent.ConfirmedAt.After(now) {
		return ProposalRefreshCommit{}, ErrProposalRefreshInvalid
	}
	guard, err := s.store.WithSessionTx(ctx, command.Guard, func(tx ReviewStoreTx) error {
		intentTx, ok := tx.(ProposalRefreshStoreTx)
		if !ok {
			return ErrProposalRefreshUnavailable
		}
		lifecycleTx, ok := tx.(ProposalWorkspaceLifecycleStoreTx)
		if !ok {
			return ErrProposalRefreshUnavailable
		}
		if err := intentTx.UpdateProposalIntent(ctx, command.Intent); err != nil {
			return err
		}
		if err := lifecycleTx.CreateProposalWorkspaceLifecycle(ctx, refreshed.Lifecycle); err != nil {
			return err
		}
		return lifecycleTx.UpdateProposalWorkspace(ctx, refreshed.Workspace)
	})
	if err != nil {
		return ProposalRefreshCommit{}, err
	}
	command.Guard = guard
	turn, err := s.turns.Start(ctx, RequestProposal{Guard: guard, ThreadID: command.ThreadID, ProposalID: command.ProposalID, ConversationID: command.ConversationID, Intent: command.Intent, Context: command.Context, Eligibility: command.Eligibility, OperationID: command.OperationID, CorrelationID: command.CorrelationID})
	if err != nil {
		return ProposalRefreshCommit{Guard: guard, PreviousVersion: command.Version, SourceGeneration: command.Provenance, ConversationID: command.ConversationID}, err
	}
	return ProposalRefreshCommit{Guard: turn.Guard, PreviousVersion: command.Version, SourceGeneration: command.Provenance, ConversationID: command.ConversationID, Turn: turn}, nil
}

func proposalRefreshIntentEqual(left, right review.ProposalIntent) bool {
	if left.ID != right.ID || left.ThreadID != right.ThreadID || left.Summary != right.Summary || left.AnchorVersionID != right.AnchorVersionID || left.ConfirmedAgainst.SessionID != right.ConfirmedAgainst.SessionID || len(left.ExpectedPaths) != len(right.ExpectedPaths) {
		return false
	}
	for i := range left.ExpectedPaths {
		if string(left.ExpectedPaths[i].Bytes()) != string(right.ExpectedPaths[i].Bytes()) {
			return false
		}
	}
	return true
}
