package workspace

import (
	"context"

	"github.com/Scottlr/nudge/internal/app"
)

// ApplicationBaselineAdvancer adapts the application post-apply port to the
// existing fenced workspace lifecycle. The builder supplies the verified
// workspace handle, capacity plan, and session guard; this adapter supplies
// only the apply-specific source and verification proof.
type ApplicationBaselineAdvancer struct {
	Lifecycle *Lifecycle
	Build     func(app.PostApplyBaselineRequest) (AdvanceBaselineRequest, error)
}

func (a ApplicationBaselineAdvancer) AdvanceBaseline(ctx context.Context, request app.PostApplyBaselineRequest) (app.PostApplyBaselineResult, error) {
	if a.Lifecycle == nil || a.Build == nil || request.Source == nil || request.ApplyOperationID == "" || request.ProposalID == "" || request.WorkspaceID == "" || request.WorktreeID == "" || request.Generation == 0 || request.VerifiedAt.IsZero() {
		return app.PostApplyBaselineResult{}, app.ErrPostApplyReconciliationUnavailable
	}
	lifecycleRequest, err := a.Build(request)
	if err != nil {
		return app.PostApplyBaselineResult{}, err
	}
	lifecycleRequest.Source = request.Source
	lifecycleRequest.Apply = ApplyVerification{ProposalID: request.ProposalID, WorktreeID: request.WorktreeID, VerifiedAt: request.VerifiedAt, Verified: true}
	if lifecycleRequest.OperationID != request.ApplyOperationID || lifecycleRequest.Workspace.ID != request.WorkspaceID || lifecycleRequest.Workspace.WorktreeID != request.WorktreeID {
		return app.PostApplyBaselineResult{}, app.ErrPostApplyReconciliationConflict
	}
	result, err := a.Lifecycle.AdvanceBaseline(ctx, lifecycleRequest)
	if err != nil {
		return app.PostApplyBaselineResult{}, err
	}
	return app.PostApplyBaselineResult{BaselineGeneration: request.Generation, ManifestHash: result.Evidence.Baseline.Hash}, nil
}
