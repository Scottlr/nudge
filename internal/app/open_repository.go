package app

import (
	"context"

	"github.com/Scottlr/nudge/internal/domain"
	"github.com/Scottlr/nudge/internal/domain/repository"
)

// LocalCaptureSource computes one complete immutable local-capture candidate.
// The source owns Git and temporary artifact handles; the application owns
// adoption and generation assignment through LocalCaptureStore.
type LocalCaptureSource interface {
	Capture(context.Context, repository.Repository, repository.WorktreeRef) (LocalCaptureArtifacts, error)
}

// ContentLoader reads immutable file bytes and structured diff artifacts for
// the selected target. It never supplies a live-worktree fallback.
type ContentLoader interface {
	LoadFile(context.Context, domain.CaptureID, repository.SnapshotRef, repository.RepoPath) (repository.FileContent, error)
	LoadDiff(context.Context, domain.CaptureID, repository.ChangedFile) (repository.FileDiff, error)
}

// LocalReviewSource is the application-facing composition boundary for the
// local walking skeleton. Implementations may execute Git or native
// highlighting, but the LocalReview runtime invokes them outside Bubble Tea.
type LocalReviewSource struct {
	Resolver    RepositoryResolver
	Capture     LocalCaptureSource
	Store       LocalCaptureStore
	Tree        TreeReader
	Content     ContentLoader
	Highlighter Highlighter
}

// Validate checks that the local-review runtime has every required owner port.
func (s LocalReviewSource) Validate() error {
	if s.Resolver == nil || s.Capture == nil || s.Store == nil || s.Tree == nil || s.Content == nil || s.Highlighter == nil {
		return ErrInvalidLocalReviewSource
	}
	return nil
}
