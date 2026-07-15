package app

import (
	"context"
	"errors"

	"github.com/Scottlr/nudge/internal/domain"
	"github.com/Scottlr/nudge/internal/domain/repository"
)

var (
	// ErrInvalidBranchTargetRequest reports incomplete target-resolution input.
	ErrInvalidBranchTargetRequest = errors.New("invalid branch target request")
	// ErrBranchTargetResolverUnavailable reports a missing Git adapter port.
	ErrBranchTargetResolverUnavailable = errors.New("branch target resolver unavailable")
)

// BaseBranchDiscovery is the bounded explanation returned by deterministic
// local-ref discovery. The expression is still re-resolved for each target
// generation; RefName is presentation evidence, never the frozen identity.
type BaseBranchDiscovery struct {
	Expression string
	RefName    string
	Source     string
	NoFetch    bool
}

// Validate checks discovery evidence before it crosses the application port.
func (d BaseBranchDiscovery) Validate() error {
	if ValidateBaseBranchExpression(d.Expression) != nil || d.Source == "" || !safeText(d.Source) {
		return ErrInvalidBranchTargetRequest
	}
	if d.RefName != "" && !safeText(d.RefName) {
		return ErrInvalidBranchTargetRequest
	}
	return nil
}

// BranchTargetRequest is the adapter-facing frozen branch-resolution input.
// MergeBaseSelection is required only when Git reports multiple best common
// ancestors; the application never chooses the first output implicitly.
type BranchTargetRequest struct {
	Repository         repository.Repository
	Worktree           repository.WorktreeRef
	Selection          BaseBranchSelection
	Discovery          BaseBranchDiscovery
	Generation         repository.TargetGeneration
	MergeBaseSelection repository.ObjectID
}

// Validate checks repository binding, current worktree identity, and base
// selection before any adapter starts Git I/O.
func (r BranchTargetRequest) Validate() error {
	if r.Repository.Validate() != nil || r.Worktree.Validate() != nil || r.Worktree.RepositoryID != r.Repository.ID || r.Selection.Validate() != nil {
		return ErrInvalidBranchTargetRequest
	}
	if r.Generation == 0 {
		return ErrInvalidBranchTargetRequest
	}
	if r.Discovery.Expression != "" {
		if err := r.Discovery.Validate(); err != nil || r.Discovery.Expression != r.Selection.Expression {
			return ErrInvalidBranchTargetRequest
		}
	}
	if r.MergeBaseSelection != "" {
		if _, err := repository.NewObjectID(string(r.MergeBaseSelection)); err != nil {
			return ErrInvalidBranchTargetRequest
		}
	}
	return nil
}

// BranchTargetResolver resolves one current-branch generation from pinned
// Git object IDs. The application owns this consumer port; Git semantics stay
// in the adapter.
type BranchTargetResolver interface {
	ResolveBranchTarget(context.Context, BranchTargetRequest) (repository.ResolvedTarget, error)
}

// BranchBaseDiscoverer supplies deterministic local discovery without making
// the application depend on a Git implementation.
type BranchBaseDiscoverer interface {
	DiscoverBaseBranch(context.Context, repository.Repository, repository.WorktreeRef) (BaseBranchDiscovery, error)
}

// OpenBranchTargetRequest composes precedence selection with branch target
// resolution. Explicit save/clear actions remain separate and are never
// performed as a side effect of opening a target.
type OpenBranchTargetRequest struct {
	Repository         repository.Repository
	Worktree           repository.WorktreeRef
	ExplicitExpression string
	SessionExpression  string
	Persistence        PersistenceMode
	Preferences        RepositoryPreferenceStore
	Discover           BranchBaseDiscoverer
	Resolver           BranchTargetResolver
	Generation         repository.TargetGeneration
	MergeBaseSelection repository.ObjectID
}

// OpenBranchTarget applies T083 precedence and resolves one frozen current
// branch generation. An invalid saved preference is returned to the caller so
// the UI can ask the user to clear or replace it.
func OpenBranchTarget(ctx context.Context, request OpenBranchTargetRequest) (repository.ResolvedTarget, error) {
	if ctx == nil || request.Repository.Validate() != nil || request.Worktree.Validate() != nil || request.Worktree.RepositoryID != request.Repository.ID {
		return repository.ResolvedTarget{}, ErrInvalidBranchTargetRequest
	}
	if request.Resolver == nil {
		return repository.ResolvedTarget{}, ErrBranchTargetResolverUnavailable
	}
	generation := request.Generation
	if generation == 0 {
		generation = 1
	}
	var discovery BaseBranchDiscovery
	selection, err := SelectBaseBranch(ctx, BaseBranchSelectionRequest{
		RepositoryID:       request.Repository.ID,
		ExplicitExpression: request.ExplicitExpression,
		SessionExpression:  request.SessionExpression,
		Persistence:        request.Persistence,
		Store:              request.Preferences,
		Discover: func(ctx context.Context) (string, error) {
			if request.Discover == nil {
				return "", ErrBaseBranchDiscoveryUnavailable
			}
			value, discoverErr := request.Discover.DiscoverBaseBranch(ctx, request.Repository, request.Worktree)
			if discoverErr != nil {
				return "", discoverErr
			}
			discovery = value
			if err := value.Validate(); err != nil {
				return "", err
			}
			return value.Expression, nil
		},
	})
	if err != nil {
		return repository.ResolvedTarget{}, err
	}
	if selection.Source != BaseFromDiscovery {
		discovery = BaseBranchDiscovery{}
	}
	return request.Resolver.ResolveBranchTarget(ctx, BranchTargetRequest{
		Repository:         request.Repository,
		Worktree:           request.Worktree,
		Selection:          selection,
		Discovery:          discovery,
		Generation:         generation,
		MergeBaseSelection: request.MergeBaseSelection,
	})
}

// BranchTargetSessionExpression returns the reusable raw base expression from
// an existing branch target. It intentionally does not return its frozen OID.
func BranchTargetSessionExpression(target repository.ResolvedTarget) string {
	if target.Spec.Kind != repository.TargetBranch || target.Spec.Validate() != nil {
		return ""
	}
	return target.Spec.BaseBranch
}

// BranchTargetRepositoryBinding is a compact identity used by target
// selectors when preserving per-target UI state.
type BranchTargetRepositoryBinding struct {
	RepositoryID domain.RepositoryID
	WorktreeID   domain.WorktreeID
	BranchRef    string
}
