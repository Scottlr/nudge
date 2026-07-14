package gitcli

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Scottlr/nudge/internal/process"
)

func TestResolveNestedRepositoryPreservesGitMetadataAndIndex(t *testing.T) {
	root, gitPath := initializedRepository(t)
	nested := filepath.Join(root, "-leading", "unicode ü space")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	remote := filepath.Join(filepath.Dir(root), "remote bare")
	runGit(t, "", gitPath, "init", "--bare", remote)
	runGit(t, root, gitPath, "remote", "add", "origin", remote)
	runGit(t, root, gitPath, "update-ref", "refs/remotes/origin/main", "HEAD")
	runGit(t, root, gitPath, "symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/main")
	runGit(t, root, gitPath, "branch", "--set-upstream-to=origin/main", "main")

	indexPath := filepath.Join(root, ".git", "index")
	before, err := os.ReadFile(indexPath)
	if err != nil {
		t.Fatal(err)
	}
	resolver := newTestResolver(t, root, gitPath)
	repo, worktree, err := resolver.ResolveRepository(context.Background(), nested)
	if err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(indexPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatal("repository resolution modified the Git index")
	}
	if repo.DisplayName != filepath.Base(root) {
		t.Fatalf("display name = %q, want %q", repo.DisplayName, filepath.Base(root))
	}
	if repo.DefaultBranch != "main" {
		t.Fatalf("default branch = %q, want main", repo.DefaultBranch)
	}
	if len(repo.Remotes) != 1 || repo.Remotes[0].Name != "origin" {
		t.Fatalf("remotes = %#v, want origin", repo.Remotes)
	}
	if len(repo.Remotes[0].FetchURLs) != 1 || len(repo.Remotes[0].PushURLs) != 1 {
		t.Fatalf("remote URLs = %#v, want one fetch and push URL", repo.Remotes[0])
	}
	if worktree.BranchName != "main" || worktree.Detached || worktree.Upstream == nil {
		t.Fatalf("worktree branch state = %#v, want main with upstream", worktree)
	}
	if worktree.LaunchFocus != "-leading/unicode ü space" {
		t.Fatalf("launch focus = %q, want nested path", worktree.LaunchFocus)
	}
	if worktree.CurrentObjectID == "" {
		t.Fatal("current object ID is empty")
	}
	if repo.CommonGitDir != repo.Binding.CommonGitDir || worktree.RootPath != worktree.Binding.RootPath || worktree.GitDir != worktree.Binding.GitDir {
		t.Fatal("binding paths were not retained in the resolved values")
	}
}

func TestResolveLinkedWorktreesShareRepositoryIdentityButNotWorktreeIdentity(t *testing.T) {
	root, gitPath := initializedRepository(t)
	linked := filepath.Join(filepath.Dir(root), "linked worktree ü")
	runGit(t, root, gitPath, "worktree", "add", "-b", "feature", linked, "HEAD")
	resolver := newTestResolver(t, root, gitPath)
	mainRepo, mainWorktree, err := resolver.ResolveRepository(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	linkedRepo, linkedWorktree, err := resolver.ResolveRepository(context.Background(), linked)
	if err != nil {
		t.Fatal(err)
	}
	if mainRepo.ID != linkedRepo.ID || mainRepo.CommonGitDir != linkedRepo.CommonGitDir {
		t.Fatalf("linked repositories = %#v and %#v, want shared identity and common directory", mainRepo, linkedRepo)
	}
	if mainWorktree.ID == linkedWorktree.ID || mainWorktree.RootPath == linkedWorktree.RootPath || mainWorktree.GitDir == linkedWorktree.GitDir {
		t.Fatalf("linked worktrees were conflated: %#v and %#v", mainWorktree, linkedWorktree)
	}
	if linkedWorktree.BranchName != "feature" || linkedWorktree.Detached {
		t.Fatalf("linked worktree branch state = %#v", linkedWorktree)
	}
	if mainWorktree.Binding.GitDir == linkedWorktree.Binding.GitDir {
		t.Fatal("per-worktree Git directory evidence was shared")
	}
}

func TestResolveDetachedHead(t *testing.T) {
	root, gitPath := initializedRepository(t)
	runGit(t, root, gitPath, "checkout", "--detach", "HEAD")
	resolver := newTestResolver(t, root, gitPath)
	_, worktree, err := resolver.ResolveRepository(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	if !worktree.Detached || worktree.BranchName != "" || worktree.CurrentObjectID == "" {
		t.Fatalf("detached worktree = %#v", worktree)
	}
}

func TestResolveOutsideGitAndBareRepositoryAreTyped(t *testing.T) {
	base := t.TempDir()
	gitPath := testGitPath(t, base)
	resolver := newTestResolver(t, base, gitPath)
	outside := filepath.Join(base, "outside")
	if err := os.MkdirAll(outside, 0o755); err != nil {
		t.Fatal(err)
	}
	_, _, err := resolver.ResolveRepository(context.Background(), outside)
	if !errors.Is(err, &GitError{Code: ErrorOutsideRepository}) {
		t.Fatalf("outside error = %v, want typed outside-repository error", err)
	}
	bare := filepath.Join(base, "bare")
	runGit(t, "", gitPath, "init", "--bare", bare)
	_, _, err = resolver.ResolveRepository(context.Background(), bare)
	if !errors.Is(err, &GitError{Code: ErrorBareRepository}) {
		t.Fatalf("bare error = %v, want typed bare-repository error", err)
	}
}

func initializedRepository(t *testing.T) (string, string) {
	t.Helper()
	base := t.TempDir()
	root := filepath.Join(base, "nudge repository ü")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	gitPath := testGitPath(t, base)
	runGit(t, root, gitPath, "init")
	runGit(t, root, gitPath, "config", "user.name", "Nudge Test")
	runGit(t, root, gitPath, "config", "user.email", "nudge@example.invalid")
	file := filepath.Join(root, "tracked file.txt")
	if err := os.WriteFile(file, []byte("initial\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, root, gitPath, "add", "--", "tracked file.txt")
	runGit(t, root, gitPath, "commit", "--no-gpg-sign", "-m", "initial")
	runGit(t, root, gitPath, "branch", "-M", "main")
	return root, gitPath
}

func newTestResolver(t *testing.T, currentDir, gitPath string) *Resolver {
	t.Helper()
	identity, err := process.NewExecutableResolver().Resolve(context.Background(), process.ResolveExecutableRequest{
		Kind:       process.ExecutableGit,
		SearchPath: os.Getenv("PATH"),
		CurrentDir: currentDir,
	})
	if err != nil {
		t.Fatalf("resolve trusted Git executable: %v", err)
	}
	if identity.CanonicalPath != gitPath {
		t.Fatalf("test Git path = %q, trusted path = %q", gitPath, identity.CanonicalPath)
	}
	resolver, err := NewResolver(ResolverConfig{Executable: identity, Runner: process.NewRunner()})
	if err != nil {
		t.Fatal(err)
	}
	return resolver
}

func testGitPath(t *testing.T, currentDir string) string {
	t.Helper()
	identity, err := process.NewExecutableResolver().Resolve(context.Background(), process.ResolveExecutableRequest{
		Kind:       process.ExecutableGit,
		SearchPath: os.Getenv("PATH"),
		CurrentDir: currentDir,
	})
	if err != nil {
		t.Fatalf("resolve Git executable: %v", err)
	}
	return identity.CanonicalPath
}

func runGit(t *testing.T, dir, gitPath string, args ...string) []byte {
	t.Helper()
	command := exec.CommandContext(context.Background(), gitPath, args...)
	command.Dir = dir
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s failed: %v\n%s", strings.Join(args, " "), err, output)
	}
	return output
}
