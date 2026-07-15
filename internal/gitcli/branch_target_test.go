package gitcli

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Scottlr/nudge/internal/app"
	"github.com/Scottlr/nudge/internal/diff"
	"github.com/Scottlr/nudge/internal/domain/repository"
	"github.com/Scottlr/nudge/internal/process"
)

func TestResolveBranchTargetFreezesObjectsAndExcludesDirtyWorktree(t *testing.T) {
	root, gitPath := initializedRepository(t)
	runGit(t, root, gitPath, "switch", "-c", "feature")
	branchFile := filepath.Join(root, "branch.txt")
	if err := os.WriteFile(branchFile, []byte("branch\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, root, gitPath, "add", "--", "branch.txt")
	runGit(t, root, gitPath, "commit", "--no-gpg-sign", "-m", "feature")
	if err := os.WriteFile(filepath.Join(root, "untracked.txt"), []byte("not part of branch review\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	resolver := newTestResolver(t, root, gitPath)
	repo, worktree, err := resolver.ResolveRepository(context.Background(), root)
	if err != nil {
		t.Fatal(describeGitError(err))
	}
	target, err := resolver.ResolveBranchTarget(context.Background(), app.BranchTargetRequest{
		Repository: repo,
		Worktree:   worktree,
		Selection:  app.BaseBranchSelection{Expression: "refs/heads/main", Source: app.BaseFromExplicitFlag},
		Generation: 7,
	})
	if err != nil {
		t.Fatal(describeGitError(err))
	}
	if target.Generation != 7 || target.BranchRef != "refs/heads/feature" || target.BaseBranchSource != string(app.BaseFromExplicitFlag) {
		t.Fatalf("target identity = %#v", target)
	}
	if target.ResolvedBaseRef == "" || target.MergeBase == "" || target.Head.ObjectID != worktree.CurrentObjectID {
		t.Fatalf("target object identity = %#v", target)
	}
	if target.DirtyWorktree == false || target.NoFetchWarning || !target.Editable || target.EditDestination == nil {
		t.Fatalf("target disclosure/editability = %#v", target)
	}

	tree, err := NewTreeReader(TreeReaderConfig{Executable: resolver.executable, Runner: resolver.runner, StartPath: root, Policy: resolver.policy})
	if err != nil {
		t.Fatal(err)
	}
	changes, err := tree.ChangedFiles(context.Background(), target)
	if err != nil {
		t.Fatal(err)
	}
	if len(changes) != 1 || changes[0].NewPath == nil || string(changes[0].NewPath.Bytes()) != "branch.txt" {
		t.Fatalf("branch changes = %#v, want only branch.txt", changes)
	}
	loader, err := NewContentLoader(ContentLoaderConfig{
		Executable:      resolver.executable,
		Runner:          resolver.runner,
		StartPath:       root,
		Policy:          resolver.policy,
		MaxContentBytes: 1 << 20,
		PatchLimits:     diff.DefaultPatchParseLimits(),
	})
	if err != nil {
		t.Fatal(err)
	}
	fileDiff, err := loader.LoadTargetDiff(context.Background(), target, changes[0])
	if err != nil || len(fileDiff.Hunks) == 0 {
		var gitErr *GitError
		if errors.As(err, &gitErr) {
			t.Logf("git error code=%s exit=%d stderr=%q cause=%v", gitErr.Code, gitErr.ExitCode, gitErr.Stderr, gitErr.Cause)
		}
		t.Fatalf("branch file diff = %#v/%v", fileDiff, err)
	}
	content, err := loader.LoadFile(context.Background(), "", target.Head, *changes[0].NewPath)
	if err != nil || string(content.Bytes) != "branch\n" {
		t.Fatalf("branch file content = %q/%v", content.Bytes, err)
	}
}

func TestDiscoverBaseBranchReportsLocalRefAndNoFetch(t *testing.T) {
	root, gitPath := initializedRepository(t)
	runGit(t, root, gitPath, "switch", "-c", "feature")
	resolver := newTestResolver(t, root, gitPath)
	repo, worktree, err := resolver.ResolveRepository(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	discovery, err := resolver.DiscoverBaseBranch(context.Background(), repo, worktree)
	if err != nil {
		t.Fatal(describeGitError(err))
	}
	if discovery.Expression != "refs/heads/main" || discovery.RefName != "refs/heads/main" || discovery.Source != string(BaseDiscoveryLocalMain) || !discovery.NoFetch {
		t.Fatalf("discovery = %#v", discovery)
	}
}

func TestResolveBranchTargetRejectsDetachedHead(t *testing.T) {
	root, gitPath := initializedRepository(t)
	runGit(t, root, gitPath, "checkout", "--detach", "HEAD")
	resolver := newTestResolver(t, root, gitPath)
	repo, worktree, err := resolver.ResolveRepository(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	_, err = resolver.ResolveBranchTarget(context.Background(), app.BranchTargetRequest{
		Repository: repo,
		Worktree:   worktree,
		Selection:  app.BaseBranchSelection{Expression: "main", Source: app.BaseFromExplicitFlag},
		Generation: 1,
	})
	if !errors.Is(err, &GitError{Code: ErrorBranchDetached}) {
		t.Fatalf("detached error = %v, want typed branch-detached error", err)
	}
}

func TestSelectMergeBaseRequiresExplicitAmbiguousChoice(t *testing.T) {
	candidates := []repository.ObjectID{"b", "a"}
	_, err := selectMergeBase(candidates, "")
	var gitErr *GitError
	if !errors.As(err, &gitErr) || gitErr.Code != ErrorMergeBaseAmbiguous || len(gitErr.Candidates) != 2 {
		t.Fatalf("ambiguous error = %#v, want candidates", err)
	}
	selected, err := selectMergeBase(candidates, "a")
	if err != nil || selected != "a" {
		t.Fatalf("selected merge base = %q/%v", selected, err)
	}
	if _, err := selectMergeBase(candidates, "c"); !errors.Is(err, &GitError{Code: ErrorMergeBaseSelectionInvalid}) {
		t.Fatalf("invalid selection error = %v", err)
	}
}

func TestResolveBaseRejectsOptionLookingExpressionBeforeGit(t *testing.T) {
	root := t.TempDir()
	identity, err := process.NewExecutableResolver().Resolve(context.Background(), process.ResolveExecutableRequest{
		Kind: process.ExecutableGit, SearchPath: os.Getenv("PATH"), CurrentDir: root,
	})
	if err != nil {
		t.Fatal(err)
	}
	runner := &recordingRunner{}
	resolver, err := NewResolver(ResolverConfig{Executable: identity, Runner: runner})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := resolver.ResolveBaseExpression(context.Background(), root, "-main"); !errors.Is(err, &GitError{Code: ErrorInvalidInput}) {
		t.Fatalf("leading-dash error = %v", err)
	}
	if runner.spec.Args != nil {
		t.Fatalf("leading-dash expression invoked Git with %#v", runner.spec.Args)
	}
	if _, err := resolver.ResolveBaseExpression(context.Background(), root, "main"); err != nil {
		t.Fatal(err)
	}
	args := strings.Join(runner.spec.Args, "\x00")
	if !strings.Contains(args, "--end-of-options\x00main^{commit}") {
		t.Fatalf("resolved base args = %q", args)
	}
}
