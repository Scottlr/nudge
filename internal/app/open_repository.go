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

// TargetContentLoader loads a structured diff directly from immutable Git
// object snapshots. It is used by branch/commit targets, which have no local
// capture artifact to consult.
type TargetContentLoader interface {
	LoadTargetDiff(context.Context, repository.ResolvedTarget, repository.ChangedFile) (repository.FileDiff, error)
}

// ChangedFileReader enumerates the frozen target diff independently of the
// tree projection. Local captures obtain this from their accepted candidate;
// branch/commit targets obtain it from Git object IDs.
type ChangedFileReader interface {
	ChangedFiles(context.Context, repository.ResolvedTarget) ([]repository.ChangedFile, error)
}

// LocalReviewSource is the application-facing composition boundary for the
// local walking skeleton. Implementations may execute Git or native
// highlighting, but the LocalReview runtime invokes them outside Bubble Tea.
type LocalReviewSource struct {
	Resolver    RepositoryResolver
	Capture     LocalCaptureSource
	Store       LocalCaptureStore
	Tree        TreeReader
	Changed     ChangedFileReader
	Content     ContentLoader
	Highlighter Highlighter
}

// BranchReviewConfig selects the application ports needed for an object-backed
// current-branch review. The local capture store remains unused in this mode.
type BranchReviewConfig struct {
	ExplicitBaseExpression string
	SessionBaseExpression  string
	Preferences            RepositoryPreferenceStore
	Discover               BranchBaseDiscoverer
	Resolver               BranchTargetResolver
}

// CommitReviewConfig selects the application port for one frozen commit
// review. The local capture store remains unused in this mode.
type CommitReviewConfig struct {
	Expression string
	Resolver   CommitTargetResolver
}

func (c *CommitReviewConfig) validate() error {
	if c == nil || c.Resolver == nil || ValidateCommitExpression(c.Expression) != nil {
		return ErrInvalidLocalReviewSource
	}
	return nil
}

func (c *BranchReviewConfig) validate() error {
	if c == nil || c.Resolver == nil {
		return ErrInvalidLocalReviewSource
	}
	if c.ExplicitBaseExpression != "" && ValidateBaseBranchExpression(c.ExplicitBaseExpression) != nil || c.SessionBaseExpression != "" && ValidateBaseBranchExpression(c.SessionBaseExpression) != nil {
		return ErrInvalidLocalReviewSource
	}
	return nil
}

// Validate checks that the local-review runtime has every required owner port.
func (s LocalReviewSource) Validate() error {
	if s.Resolver == nil || s.Capture == nil || s.Store == nil || s.Tree == nil || s.Content == nil || s.Highlighter == nil {
		return ErrInvalidLocalReviewSource
	}
	return nil
}
