package workspace

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Scottlr/nudge/internal/app"
	"github.com/Scottlr/nudge/internal/domain"
	"github.com/Scottlr/nudge/internal/domain/repository"
)

func TestPinnedBaselineMaterializesExactObjectSnapshot(t *testing.T) {
	repo, worktree, target := pinnedBaselineTarget(t)
	path := repository.RepoPath("main.go")
	const snapshotContent = "pinned object\n"
	snapshot := app.ReviewSnapshot{
		ID: "review-snapshot", RepositoryID: repo.ID, TargetKind: target.Spec.Kind, HeadObjectID: target.Head.ObjectID, BaseObjectID: target.Base.ObjectID,
		ObjectFormat: "sha1", FormatVersion: app.ReviewSnapshotFormatVersion, Root: filepath.Join(t.TempDir(), "review"), MarkerNonce: strings.Repeat("a", 64), ManifestHash: strings.Repeat("b", 64),
		PolicyVersion: app.CurrentResourcePolicyVersion, EvidenceVersion: app.CurrentCapabilityEvidenceVersion, State: app.ReviewSnapshotReady, CreatedAt: time.Now().UTC(),
	}
	source := workspaceTestSource{identity: app.WorkspaceSourceIdentity{Kind: "review_snapshot", ID: string(snapshot.ID), ManifestHash: snapshot.ManifestHash}, entries: []repository.TreeEntry{treeEntryForPath(path, repository.FileKindRegular, 0o100644)}, content: map[repository.RepoPathKey][]byte{path.Key(): []byte(snapshotContent)}}
	request := app.ProposalTargetBaselineRequest{Target: target, Snapshot: snapshot, Worktree: worktree, ResourcePolicy: app.DefaultResourcePolicy(), ObjectsAvailable: true, IsolationSupported: true}
	guardCalls := 0
	baseline, err := NewPinnedBaseline(request, source, func(context.Context, repository.ResolvedTarget, repository.WorktreeRef) error {
		guardCalls++
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	allocator, _, handle, lease := localBaselineWorkspace(t)
	defer lease.Close()
	_ = allocator
	result, err := baseline.Materialize(context.Background(), handle.Roots.Baseline)
	if err != nil {
		t.Fatal(err)
	}
	if result.Target == nil || result.Target.Head.ObjectID != target.Head.ObjectID || result.Generation != (app.CaptureGeneration{}) || guardCalls < 2 {
		t.Fatalf("pinned baseline = %#v, guard calls = %d", result, guardCalls)
	}
	content, err := os.ReadFile(filepath.Join(handle.Roots.Baseline.Path(), "main.go"))
	if err != nil || string(content) != snapshotContent {
		t.Fatalf("materialized pinned content = %q/%v", content, err)
	}
}

func TestPinnedBaselineStopsWhenDestinationHeadChanges(t *testing.T) {
	repo, worktree, target := pinnedBaselineTarget(t)
	path := repository.RepoPath("main.go")
	snapshot := app.ReviewSnapshot{ID: "review-snapshot-stale", RepositoryID: repo.ID, TargetKind: target.Spec.Kind, HeadObjectID: target.Head.ObjectID, BaseObjectID: target.Base.ObjectID, ObjectFormat: "sha1", FormatVersion: app.ReviewSnapshotFormatVersion, Root: filepath.Join(t.TempDir(), "review"), MarkerNonce: strings.Repeat("c", 64), ManifestHash: strings.Repeat("d", 64), PolicyVersion: app.CurrentResourcePolicyVersion, EvidenceVersion: app.CurrentCapabilityEvidenceVersion, State: app.ReviewSnapshotReady, CreatedAt: time.Now().UTC()}
	source := workspaceTestSource{identity: app.WorkspaceSourceIdentity{Kind: "review_snapshot", ID: string(snapshot.ID), ManifestHash: snapshot.ManifestHash}, entries: []repository.TreeEntry{treeEntryForPath(path, repository.FileKindRegular, 0o100644)}, content: map[repository.RepoPathKey][]byte{path.Key(): []byte("pinned\n")}}
	request := app.ProposalTargetBaselineRequest{Target: target, Snapshot: snapshot, Worktree: worktree, ResourcePolicy: app.DefaultResourcePolicy(), ObjectsAvailable: true, IsolationSupported: true}
	stale := false
	baseline, err := NewPinnedBaseline(request, source, func(context.Context, repository.ResolvedTarget, repository.WorktreeRef) error {
		if stale {
			return errors.New("head moved")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	stale = true
	if _, err := baseline.List(context.Background()); !errors.Is(err, app.ErrProposalBaselineStale) {
		t.Fatalf("stale pinned baseline error = %v", err)
	}
}

func pinnedBaselineTarget(t *testing.T) (repository.Repository, repository.WorktreeRef, repository.ResolvedTarget) {
	t.Helper()
	now := time.Unix(30, 0).UTC()
	repoID := domain.RepositoryID("repo-pinned")
	worktreeID := domain.WorktreeID("worktree-pinned")
	repo := repository.Repository{ID: repoID, CommonGitDir: "/repo/.git", DisplayName: "repo", CreatedAt: now, UpdatedAt: now, Binding: repository.RepositoryBindingEvidence{Version: 1, ObjectFormat: "sha1", CommonGitDir: "/repo/.git", CommonGitDirIdentity: "common"}}
	worktree := repository.WorktreeRef{ID: worktreeID, RepositoryID: repoID, RootPath: "/repo", GitDir: "/repo/.git", CurrentObjectID: "head-pinned", BranchName: "feature", Binding: repository.WorktreeBindingEvidence{Version: 1, ObjectFormat: "sha1", RootPath: "/repo", GitDir: "/repo/.git", RootIdentity: "root", GitDirIdentity: "git"}}
	if err := repo.Validate(); err != nil {
		t.Fatal(err)
	}
	if err := worktree.Validate(); err != nil {
		t.Fatal(err)
	}
	spec, err := repository.NewBranchTargetSpec("main")
	if err != nil {
		t.Fatal(err)
	}
	destination := worktree.ID
	target, err := repository.NewResolvedTarget(repository.ResolvedTarget{Spec: spec, Generation: 7, Base: repository.SnapshotRef{Kind: repository.SnapshotCommit, ObjectID: "merge-pinned"}, Head: repository.SnapshotRef{Kind: repository.SnapshotCommit, ObjectID: worktree.CurrentObjectID}, ResolvedCommit: worktree.CurrentObjectID, ResolvedBaseRef: "base-pinned", MergeBase: "merge-pinned", BaseBranchSource: "explicit_branch_flag", BranchRef: "refs/heads/feature", Editable: true, EditDestination: &destination, Fingerprint: strings.Repeat("e", 64), ResolvedAt: now})
	if err != nil {
		t.Fatal(err)
	}
	return repo, worktree, target
}
