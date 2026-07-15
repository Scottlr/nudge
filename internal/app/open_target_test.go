package app

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Scottlr/nudge/internal/domain"
	"github.com/Scottlr/nudge/internal/domain/repository"
)

type branchTargetTestResolver struct {
	request BranchTargetRequest
}

func (r *branchTargetTestResolver) ResolveBranchTarget(_ context.Context, request BranchTargetRequest) (repository.ResolvedTarget, error) {
	r.request = request
	spec, err := repository.NewBranchTargetSpec(request.Selection.Expression)
	if err != nil {
		return repository.ResolvedTarget{}, err
	}
	destination := request.Worktree.ID
	return repository.NewResolvedTarget(repository.ResolvedTarget{
		Spec:             spec,
		Generation:       request.Generation,
		Base:             repository.SnapshotRef{Kind: repository.SnapshotCommit, ObjectID: "merge-base"},
		Head:             repository.SnapshotRef{Kind: repository.SnapshotCommit, ObjectID: "head"},
		ResolvedCommit:   "head",
		ResolvedBaseRef:  "base-ref",
		MergeBase:        "merge-base",
		BaseBranchSource: string(request.Selection.Source),
		BranchRef:        "refs/heads/feature",
		Editable:         true,
		EditDestination:  &destination,
		ResolvedAt:       time.Unix(10, 0).UTC(),
	})
}

type branchTargetTestDiscoverer struct {
	calls int
}

func (d *branchTargetTestDiscoverer) DiscoverBaseBranch(context.Context, repository.Repository, repository.WorktreeRef) (BaseBranchDiscovery, error) {
	d.calls++
	return BaseBranchDiscovery{Expression: "refs/heads/discovered", RefName: "refs/heads/discovered", Source: "local_main", NoFetch: true}, nil
}

func TestOpenBranchTargetPreservesPrecedenceAndDiscoveryEvidence(t *testing.T) {
	repo, worktree := branchTargetTestBinding(t)
	discoverer := &branchTargetTestDiscoverer{}
	resolver := &branchTargetTestResolver{}
	target, err := OpenBranchTarget(context.Background(), OpenBranchTargetRequest{
		Repository:         repo,
		Worktree:           worktree,
		ExplicitExpression: "refs/heads/explicit",
		SessionExpression:  "refs/heads/session",
		Persistence:        PersistenceNoPersist,
		Discover:           discoverer,
		Resolver:           resolver,
		Generation:         9,
	})
	if err != nil {
		t.Fatal(err)
	}
	if resolver.request.Selection.Source != BaseFromExplicitFlag || resolver.request.Selection.Expression != "refs/heads/explicit" || discoverer.calls != 0 || target.Generation != 9 {
		t.Fatalf("explicit target = %#v, request = %#v, discovery calls = %d", target, resolver.request, discoverer.calls)
	}

	resolver = &branchTargetTestResolver{}
	target, err = OpenBranchTarget(context.Background(), OpenBranchTargetRequest{
		Repository:  repo,
		Worktree:    worktree,
		Persistence: PersistenceNoPersist,
		Discover:    discoverer,
		Resolver:    resolver,
	})
	if err != nil {
		t.Fatal(err)
	}
	if resolver.request.Selection.Source != BaseFromDiscovery || resolver.request.Discovery.RefName != "refs/heads/discovered" || !resolver.request.Discovery.NoFetch || target.Generation != 1 {
		t.Fatalf("discovered target = %#v, request = %#v", target, resolver.request)
	}
}

func TestOpenBranchTargetDoesNotFallThroughUnavailableSavedPreference(t *testing.T) {
	repo, worktree := branchTargetTestBinding(t)
	store := &preferenceTestStore{preference: &BaseBranchPreference{RepositoryID: repo.ID, Expression: "-unsafe", Revision: 1, UpdatedAt: time.Unix(10, 0).UTC()}}
	discoverer := &branchTargetTestDiscoverer{}
	_, err := OpenBranchTarget(context.Background(), OpenBranchTargetRequest{
		Repository:  repo,
		Worktree:    worktree,
		Preferences: store,
		Discover:    discoverer,
		Resolver:    &branchTargetTestResolver{},
	})
	if !errors.Is(err, ErrSavedBaseUnavailable) || discoverer.calls != 0 {
		t.Fatalf("saved preference error = %v, discovery calls = %d", err, discoverer.calls)
	}
}

func branchTargetTestBinding(t *testing.T) (repository.Repository, repository.WorktreeRef) {
	t.Helper()
	now := time.Unix(10, 0).UTC()
	repoID := domain.RepositoryID("repo-1")
	worktreeID := domain.WorktreeID("worktree-1")
	repo := repository.Repository{
		ID: repoID, CommonGitDir: "/repo/.git", DisplayName: "repo", CreatedAt: now, UpdatedAt: now,
		Binding: repository.RepositoryBindingEvidence{Version: 1, ObjectFormat: "sha1", CommonGitDir: "/repo/.git", CommonGitDirIdentity: "common"},
	}
	worktree := repository.WorktreeRef{
		ID: worktreeID, RepositoryID: repoID, RootPath: "/repo", GitDir: "/repo/.git", CurrentObjectID: "head", BranchName: "feature",
		Binding: repository.WorktreeBindingEvidence{Version: 1, ObjectFormat: "sha1", RootPath: "/repo", GitDir: "/repo/.git", RootIdentity: "root", GitDirIdentity: "git"},
	}
	if err := repo.Validate(); err != nil {
		t.Fatal(err)
	}
	if err := worktree.Validate(); err != nil {
		t.Fatal(err)
	}
	return repo, worktree
}
