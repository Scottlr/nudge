package app

import (
	"context"

	"github.com/Scottlr/nudge/internal/domain"
	"github.com/Scottlr/nudge/internal/domain/review"
)

// ProposalWorkspaceStore is the application-owned read boundary for proposal
// lineage and immutable versions. Filesystem roots remain owned by the
// workspace adapter and are represented only by their durable identities.
type ProposalWorkspaceStore interface {
	LoadProposalAggregate(context.Context, domain.ProposalID) (review.ProposalAggregate, error)
}

// ProposalWorkspaceStoreTx is an optional extension of ReviewStoreTx. Keeping
// proposal operations separate avoids widening the core transaction port for
// stores that do not persist proposal history.
type ProposalWorkspaceStoreTx interface {
	CreateWorkspace(context.Context, review.ProposalWorkspace, review.ProposalIntent, review.Proposal) error
	RecordProposalAttempt(context.Context, review.ProposalAttempt) error
	RecordNoChanges(context.Context, review.ProposalAttempt) error
	PublishProposal(context.Context, review.ProposedPatch) error
	TransitionProposal(context.Context, review.ProposalTransition) error
}
