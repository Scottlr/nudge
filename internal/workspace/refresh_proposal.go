package workspace

import (
	"context"

	"github.com/Scottlr/nudge/internal/app"
	"github.com/Scottlr/nudge/internal/domain/review"
)

// ProposalRefreshWorkspaceAdapter adapts the verified workspace lifecycle to
// the application refresh port. Its LifecycleRequest must already contain the
// allocator, capacity policy, ownership handle, and fenced store selected by
// the workspace composition root.
type ProposalRefreshWorkspaceAdapter struct {
	Lifecycle        *Lifecycle
	LifecycleRequest LifecycleRequest
}

// RefreshProposalWorkspace resets the isolated result, materializes the new
// accepted source as baseline, and returns the ready lifecycle evidence.
func (a ProposalRefreshWorkspaceAdapter) RefreshProposalWorkspace(ctx context.Context, request app.ProposalRefreshWorkspaceRequest) (app.ProposalRefreshWorkspaceResult, error) {
	if a.Lifecycle == nil || request.Validate() != nil {
		return app.ProposalRefreshWorkspaceResult{}, ErrWorkspaceLifecycleUnavailable
	}
	identity := request.Source.Identity()
	identity.Generation = request.Provenance.Generation
	if identity.Fingerprint == "" {
		identity.Fingerprint = identity.ManifestHash
	}
	source := refreshSource{AcceptedTreeSource: request.Source, identity: identity}
	lifecycleRequest := a.LifecycleRequest
	lifecycleRequest.Guard = request.Guard
	lifecycleRequest.OperationID = request.OperationID
	lifecycleRequest.Workspace = request.Aggregate.Workspace
	result, err := a.Lifecycle.RefreshBaseline(ctx, RefreshBaselineRequest{LifecycleRequest: lifecycleRequest, Source: source})
	if err != nil {
		return app.ProposalRefreshWorkspaceResult{}, err
	}
	workspace := request.Aggregate.Workspace
	workspace.SourceGeneration = request.Provenance
	workspace.State = review.WorkspaceReady
	workspace.UpdatedAt = result.Evidence.UpdatedAt
	return app.ProposalRefreshWorkspaceResult{Workspace: workspace, Lifecycle: result.Evidence}, nil
}

type refreshSource struct {
	app.AcceptedTreeSource
	identity app.WorkspaceSourceIdentity
}

func (s refreshSource) Identity() app.WorkspaceSourceIdentity { return s.identity }
