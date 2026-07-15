package gitcli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Scottlr/nudge/internal/app"
	"github.com/Scottlr/nudge/internal/domain/repository"
)

func TestTreePageHierarchy(t *testing.T) {
	root, gitPath := initializedRepository(t)
	if err := os.MkdirAll(filepath.Join(root, "dir", "nested"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "dir", "child.txt"), []byte("child\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "dir", "nested", "deep.txt"), []byte("deep\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, root, gitPath, "add", "--", "dir")
	runGit(t, root, gitPath, "commit", "--no-gpg-sign", "-m", "tree")
	resolver := newTestResolver(t, root, gitPath)
	_, worktree, err := resolver.ResolveRepository(context.Background(), root)
	if err != nil {
		t.Fatal(describeGitError(err))
	}
	target := commitTreeTarget(t, worktree.CurrentObjectID)
	reader := newTestTreeReader(t, root, gitPath)
	page, err := reader.ListTree(context.Background(), target, app.TreeQuery{Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Entries) != 1 || string(page.Entries[0].Path) != "dir" || page.Entries[0].Kind != repository.FileKindDirectory || !page.Entries[0].LazyChild || page.NextCursor == "" {
		t.Fatalf("root page = %#v", page)
	}
	next, err := reader.ListTree(context.Background(), target, app.TreeQuery{Cursor: page.NextCursor, Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(next.Entries) != 1 || string(next.Entries[0].Path) != "tracked file.txt" || next.NextCursor != "" {
		t.Fatalf("second root page = %#v", next)
	}
	children, err := reader.ListTree(context.Background(), target, app.TreeQuery{ParentPath: repoPathPtr("dir"), Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(children.Entries) != 2 || string(children.Entries[0].Path) != "dir/child.txt" || string(children.Entries[1].Path) != "dir/nested" {
		t.Fatalf("directory children = %#v", children.Entries)
	}
	if _, err := reader.ListTree(context.Background(), target, app.TreeQuery{ParentPath: repoPathPtr("other"), Cursor: page.NextCursor, Limit: 1}); !errors.Is(err, ErrTreeCursor) {
		t.Fatalf("cross-parent cursor error = %v", err)
	}
}

func TestChangedTreeIncludesDeletedAncestors(t *testing.T) {
	root, gitPath := initializedRepository(t)
	if err := os.MkdirAll(filepath.Join(root, "dir", "deep"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "dir", "deep", "gone.txt"), []byte("gone\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, root, gitPath, "add", "--", "dir")
	runGit(t, root, gitPath, "commit", "--no-gpg-sign", "-m", "deleted tree")
	resolver := newTestResolver(t, root, gitPath)
	_, worktree, err := resolver.ResolveRepository(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(root, "dir", "deep", "gone.txt")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "loose.txt"), []byte("loose\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	spec, err := repository.NewLocalTargetSpec()
	if err != nil {
		t.Fatal(err)
	}
	target := repository.ResolvedTarget{
		Spec: spec, Generation: 1,
		Base:       repository.SnapshotRef{Kind: repository.SnapshotTree, ObjectID: worktree.CurrentObjectID},
		Head:       repository.SnapshotRef{Kind: repository.SnapshotWorkingTree, WorktreeID: worktree.ID, Fingerprint: strings.Repeat("a", 64)},
		ResolvedAt: time.Now().UTC(),
	}
	reader := newTestTreeReader(t, root, gitPath)
	all, err := reader.ListTree(context.Background(), target, app.TreeQuery{Filter: app.TreeFilterAll, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(all.Entries) != 3 || string(all.Entries[0].Path) != "dir" || string(all.Entries[1].Path) != "loose.txt" || string(all.Entries[2].Path) != "tracked file.txt" {
		t.Fatalf("local all entries = %#v", all.Entries)
	}
	page, err := reader.ListTree(context.Background(), target, app.TreeQuery{Filter: app.TreeFilterChanged, Limit: 10})
	if err != nil {
		t.Fatal(describeGitError(err))
	}
	if len(page.Entries) != 2 || string(page.Entries[0].Path) != "dir" || !page.Entries[0].LazyChild || string(page.Entries[1].Path) != "loose.txt" {
		t.Fatalf("changed root entries = %#v", page.Entries)
	}
	deep, err := reader.ListTree(context.Background(), target, app.TreeQuery{Filter: app.TreeFilterChanged, ParentPath: repoPathPtr("dir"), Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(deep.Entries) != 1 || string(deep.Entries[0].Path) != "dir/deep" || !deep.Entries[0].LazyChild {
		t.Fatalf("changed directory entries = %#v", deep.Entries)
	}
	deletedPage, err := reader.ListTree(context.Background(), target, app.TreeQuery{Filter: app.TreeFilterChanged, ParentPath: repoPathPtr("dir/deep"), Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(deletedPage.Entries) != 1 || string(deletedPage.Entries[0].Path) != "dir/deep/gone.txt" {
		t.Fatalf("changed leaf entries = %#v", deletedPage.Entries)
	}
	deleted := deletedPage.Entries[0]
	if deleted.ChangedSummary == nil || deleted.ChangedSummary.Kind != repository.ChangeDeleted {
		t.Fatalf("deleted summary = %#v", deleted.ChangedSummary)
	}
}

func TestTreePagingLargeRepository(t *testing.T) {
	root, gitPath := initializedRepository(t)
	for index := 0; index < 2100; index++ {
		path := filepath.Join(root, "generated", "file-"+formatTreeIndex(index)+".txt")
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("x\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	runGit(t, root, gitPath, "add", "--", "generated")
	runGit(t, root, gitPath, "commit", "--no-gpg-sign", "-m", "large tree")
	resolver := newTestResolver(t, root, gitPath)
	_, worktree, err := resolver.ResolveRepository(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	reader := newTestTreeReader(t, root, gitPath)
	target := commitTreeTarget(t, worktree.CurrentObjectID)
	query := app.TreeQuery{ParentPath: repoPathPtr("generated"), Limit: 200}
	count := 0
	pages := 0
	for {
		page, pageErr := reader.ListTree(context.Background(), target, query)
		if pageErr != nil {
			t.Fatal(pageErr)
		}
		if len(page.Entries) == 0 || len(page.Entries) > 200 {
			t.Fatalf("page size = %d", len(page.Entries))
		}
		count += len(page.Entries)
		pages++
		if page.NextCursor == "" {
			break
		}
		query.Cursor = page.NextCursor
		if pages > 20 {
			t.Fatal("paging did not terminate")
		}
	}
	if count != 2100 || pages != 11 {
		t.Fatalf("paged tree count/pages = %d/%d, want 2100/11", count, pages)
	}
}

func commitTreeTarget(t *testing.T, head repository.ObjectID) repository.ResolvedTarget {
	t.Helper()
	spec, err := repository.NewCommitTargetSpec(string(head), "")
	if err != nil {
		t.Fatal(err)
	}
	target := repository.ResolvedTarget{
		Spec: spec, Generation: 1,
		Base:           repository.SnapshotRef{Kind: repository.SnapshotCommit, ObjectID: head},
		Head:           repository.SnapshotRef{Kind: repository.SnapshotCommit, ObjectID: head},
		ResolvedCommit: head, ResolvedParent: head, ParentLabel: "parent 1", ResolvedAt: time.Now().UTC(),
	}
	if err := target.Validate(); err != nil {
		t.Fatal(err)
	}
	return target
}

func newTestTreeReader(t *testing.T, root, gitPath string) *GitTreeReader {
	t.Helper()
	resolver := newTestResolver(t, root, gitPath)
	reader, err := NewTreeReader(TreeReaderConfig{Executable: resolver.executable, StartPath: root})
	if err != nil {
		t.Fatal(err)
	}
	return reader
}

func repoPathPtr(value string) *repository.RepoPath {
	path := repository.RepoPath(value)
	return &path
}

func formatTreeIndex(value int) string {
	return fmt.Sprintf("%04d", value)
}
