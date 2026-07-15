package workspace

import (
	"context"

	"github.com/Scottlr/nudge/internal/app"
)

// AdoptProposalPatchArtifact persists one complete T111 artifact through the
// session writer fence. The patch target is already published and immutable;
// this helper only performs the durable metadata adoption.
func AdoptProposalPatchArtifact(ctx context.Context, store SessionTransactionStore, guard app.SessionWriteGuard, artifact app.ProposalPatchArtifact) (app.SessionWriteGuard, error) {
	if ctx == nil || store == nil || guard.Validate() != nil || artifact.Validate() != nil {
		return guard, app.ErrInvalidProposalPatchArtifact
	}
	return store.WithSessionTx(ctx, guard, func(tx app.ReviewStoreTx) error {
		adopter, ok := tx.(app.ProposalPatchArtifactStoreTx)
		if !ok {
			return app.ErrInvalidProposalPatchArtifact
		}
		return adopter.AdoptProposalPatchArtifact(ctx, artifact)
	})
}
