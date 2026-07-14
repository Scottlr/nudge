package app

import (
	"context"

	"github.com/Scottlr/nudge/internal/domain/repository"
)

// RepositoryResolver resolves one enclosing Git worktree from a launch path.
// The application owns this port; Git CLI behavior remains in the adapter.
type RepositoryResolver interface {
	ResolveRepository(ctx context.Context, startPath string) (repository.Repository, repository.WorktreeRef, error)
}
