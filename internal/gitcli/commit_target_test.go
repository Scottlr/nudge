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
)

func TestResolveCommitTargetFreezesNormalCommitAndLoadsObjects(t *testing.T) {
	root, gitPath := initializedRepository(t)
	file := filepath.Join(root, "new file.txt")
	if err := os.WriteFile(file, []byte("new content\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, root, gitPath, "add", "--", "new file.txt")
	runGit(t, root, gitPath, "commit", "--no-gpg-sign", "-m", "second")
	resolver := newTestResolver(t, root, gitPath)
	repo, worktree, err := resolver.ResolveRepository(context.Background(), root)
	if err != nil {
		t.Fatal(describeGitError(err))
	}
	target, err := resolver.ResolveCommitTarget(context.Background(), app.CommitTargetRequest{Repository: repo, Worktree: worktree, Expression: "HEAD", Generation: 7})
	if err != nil {
		t.Fatal(describeGitError(err))
	}
	parent := repository.ObjectID(strings.TrimSpace(string(runGit(t, root, gitPath, "rev-parse", "HEAD^"))))
	if target.Generation != 7 || target.Head.ObjectID != worktree.CurrentObjectID || target.Base.ObjectID != parent || target.ResolvedParent != parent || target.ParentLabel != "parent 1" {
		t.Fatalf("normal commit target = %#v, want parent %q", target, parent)
	}
	if !target.Editable || target.EditDestination == nil {
		t.Fatalf("current HEAD editability = %#v, want editable", target)
	}

	tree, err := NewTreeReader(TreeReaderConfig{Executable: resolver.executable, Runner: resolver.runner, StartPath: root, Policy: resolver.policy})
	if err != nil {
		t.Fatal(err)
	}
	changes, err := tree.ChangedFiles(context.Background(), target)
	if err != nil || len(changes) != 1 || changes[0].NewPath == nil || string(changes[0].NewPath.Bytes()) != "new file.txt" {
		t.Fatalf("commit changes = %#v/%v", changes, err)
	}
	loader, err := NewContentLoader(ContentLoaderConfig{Executable: resolver.executable, Runner: resolver.runner, StartPath: root, Policy: resolver.policy, MaxContentBytes: 1 << 20, PatchLimits: diff.DefaultPatchParseLimits()})
	if err != nil {
		t.Fatal(err)
	}
	fileDiff, err := loader.LoadTargetDiff(context.Background(), target, changes[0])
	if err != nil || len(fileDiff.Hunks) == 0 {
		t.Fatalf("commit diff = %#v/%v", fileDiff, err)
	}
	content, err := loader.LoadFile(context.Background(), "", target.Head, *changes[0].NewPath)
	if err != nil || string(content.Bytes) != "new content\n" {
		t.Fatalf("commit content = %q/%v", content.Bytes, err)
	}
}

func TestResolveCommitTargetUsesEmptyTreeForRootWithoutMutation(t *testing.T) {
	for _, format := range []string{"sha1", "sha256"} {
		t.Run(format, func(t *testing.T) {
			root, gitPath := initializedRepositoryWithFormat(t, format)
			indexPath := filepath.Join(root, ".git", "index")
			beforeIndex, err := os.ReadFile(indexPath)
			if err != nil {
				t.Fatal(err)
			}
			beforeObjects := string(runGit(t, root, gitPath, "count-objects", "-v"))
			resolver := newTestResolver(t, root, gitPath)
			repo, worktree, err := resolver.ResolveRepository(context.Background(), root)
			if err != nil {
				t.Fatal(describeGitError(err))
			}
			target, err := resolver.ResolveCommitTarget(context.Background(), app.CommitTargetRequest{Repository: repo, Worktree: worktree, Expression: "HEAD", Generation: 1})
			if err != nil {
				t.Fatal(describeGitError(err))
			}
			if target.Base.Kind != repository.SnapshotEmpty || target.Base.ObjectID == "" || target.ResolvedParent != "" || target.ParentLabel != "root (empty tree)" {
				t.Fatalf("root target = %#v", target)
			}
			if len(string(target.Base.ObjectID)) != len(string(target.Head.ObjectID)) {
				t.Fatalf("root object widths differ: base=%q head=%q", target.Base.ObjectID, target.Head.ObjectID)
			}
			expected := strings.TrimSpace(string(runGit(t, root, gitPath, "hash-object", "-t", "tree", "--stdin")))
			if string(target.Base.ObjectID) != expected {
				t.Fatalf("empty tree = %q, want Git-derived %q", target.Base.ObjectID, expected)
			}
			afterIndex, err := os.ReadFile(indexPath)
			if err != nil {
				t.Fatal(err)
			}
			if string(beforeIndex) != string(afterIndex) || beforeObjects != string(runGit(t, root, gitPath, "count-objects", "-v")) {
				t.Fatal("root target resolution mutated Git state")
			}
		})
	}
}

func TestResolveCommitTargetUsesFirstParentForMergeAndHistoricalIsReadOnly(t *testing.T) {
	root, gitPath := initializedRepository(t)
	runGit(t, root, gitPath, "switch", "-c", "feature")
	if err := os.WriteFile(filepath.Join(root, "feature.txt"), []byte("feature\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, root, gitPath, "add", "--", "feature.txt")
	runGit(t, root, gitPath, "commit", "--no-gpg-sign", "-m", "feature")
	runGit(t, root, gitPath, "switch", "main")
	if err := os.WriteFile(filepath.Join(root, "main.txt"), []byte("main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, root, gitPath, "add", "--", "main.txt")
	runGit(t, root, gitPath, "commit", "--no-gpg-sign", "-m", "main")
	runGit(t, root, gitPath, "merge", "--no-ff", "--no-edit", "feature")
	resolver := newTestResolver(t, root, gitPath)
	repo, worktree, err := resolver.ResolveRepository(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	merge, err := resolver.ResolveCommitTarget(context.Background(), app.CommitTargetRequest{Repository: repo, Worktree: worktree, Expression: "HEAD", Generation: 1})
	if err != nil {
		t.Fatal(describeGitError(err))
	}
	firstParent := strings.TrimSpace(string(runGit(t, root, gitPath, "rev-parse", "HEAD^1")))
	if len(strings.Fields(string(runGit(t, root, gitPath, "rev-list", "--parents", "--max-count=1", "HEAD")))) != 3 {
		t.Fatal("fixture did not produce a merge commit")
	}
	if string(merge.Base.ObjectID) != firstParent || merge.ResolvedParent != repository.ObjectID(firstParent) || merge.ParentLabel != "parent 1 (first-parent v1)" {
		t.Fatalf("merge target = %#v, want first parent %q", merge, firstParent)
	}

	historical, err := resolver.ResolveCommitTarget(context.Background(), app.CommitTargetRequest{Repository: repo, Worktree: worktree, Expression: "HEAD^1", Generation: 2})
	if err != nil {
		t.Fatal(describeGitError(err))
	}
	if historical.Editable || historical.EditDestination != nil {
		t.Fatalf("historical target editability = %#v, want read-only", historical)
	}
}

func TestResolveCommitTargetRejectsLeadingDashAndMissingObjectBeforeFetch(t *testing.T) {
	root, gitPath := initializedRepository(t)
	baseResolver := newTestResolver(t, root, gitPath)
	repo, worktree, err := baseResolver.ResolveRepository(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	runner := &recordingRunner{}
	resolver, err := NewResolver(ResolverConfig{Executable: baseResolver.executable, Runner: runner})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := resolver.ResolveCommitTarget(context.Background(), app.CommitTargetRequest{Repository: repo, Worktree: worktree, Expression: "-main", Generation: 1}); !errors.Is(err, &GitError{Code: ErrorInvalidInput}) {
		t.Fatalf("leading-dash error = %v", err)
	}
	if runner.spec.Args != nil {
		t.Fatalf("leading-dash expression invoked Git with %#v", runner.spec.Args)
	}

	resolver = newTestResolver(t, root, gitPath)
	_, err = resolver.ResolveCommitTarget(context.Background(), app.CommitTargetRequest{Repository: repo, Worktree: worktree, Expression: "0123456789012345678901234567890123456789", Generation: 1})
	if !errors.Is(err, &GitError{Code: ErrorCommitUnavailable}) {
		t.Fatalf("missing commit error = %v", err)
	}
}

func TestResolveCommitTargetFreezesMovedRef(t *testing.T) {
	root, gitPath := initializedRepository(t)
	resolver := newTestResolver(t, root, gitPath)
	repo, worktree, err := resolver.ResolveRepository(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	target, err := resolver.ResolveCommitTarget(context.Background(), app.CommitTargetRequest{Repository: repo, Worktree: worktree, Expression: "refs/heads/main", Generation: 3})
	if err != nil {
		t.Fatal(err)
	}
	old := target.Head.ObjectID
	if err := os.WriteFile(filepath.Join(root, "moved.txt"), []byte("moved\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, root, gitPath, "add", "--", "moved.txt")
	runGit(t, root, gitPath, "commit", "--no-gpg-sign", "-m", "moved ref")
	moved := strings.TrimSpace(string(runGit(t, root, gitPath, "rev-parse", "refs/heads/main")))
	if target.Head.ObjectID != old || repository.ObjectID(moved) == old {
		t.Fatalf("frozen target changed after ref movement: target=%q old=%q moved=%q", target.Head.ObjectID, old, moved)
	}
}

func initializedRepositoryWithFormat(t *testing.T, format string) (string, string) {
	t.Helper()
	base := t.TempDir()
	root := filepath.Join(base, "nudge "+format)
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	gitPath := testGitPath(t, base)
	runGit(t, root, gitPath, "init", "--object-format="+format)
	runGit(t, root, gitPath, "config", "user.name", "Nudge Test")
	runGit(t, root, gitPath, "config", "user.email", "nudge@example.invalid")
	if err := os.WriteFile(filepath.Join(root, "tracked.txt"), []byte("tracked\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, root, gitPath, "add", "--", "tracked.txt")
	runGit(t, root, gitPath, "commit", "--no-gpg-sign", "-m", "initial")
	runGit(t, root, gitPath, "branch", "-M", "main")
	return root, gitPath
}
