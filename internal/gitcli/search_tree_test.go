package gitcli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Scottlr/nudge/internal/app"
	"github.com/Scottlr/nudge/internal/domain"
	"github.com/Scottlr/nudge/internal/domain/repository"
)

func TestSearchTreeEnumeratesUnloadedPathsAndBindsCursor(t *testing.T) {
	root, gitPath := initializedRepository(t)
	if err := os.MkdirAll(filepath.Join(root, "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "docs"), 0o755); err != nil {
		t.Fatal(err)
	}
	for path, content := range map[string]string{
		"src/main.go":        "package main\n",
		"src/main_test.go":   "package main\n",
		"docs/main-notes.md": "notes\n",
	} {
		if err := os.WriteFile(filepath.Join(root, filepath.FromSlash(path)), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	runGit(t, root, gitPath, "add", "--", "src", "docs")
	runGit(t, root, gitPath, "commit", "--no-gpg-sign", "-m", "search tree")
	resolver := newTestResolver(t, root, gitPath)
	_, worktree, err := resolver.ResolveRepository(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	searcher, err := NewTreeSearcher(TreeSearcherConfig{Executable: resolver.executable, StartPath: root})
	if err != nil {
		t.Fatal(err)
	}
	target := commitTreeTarget(t, worktree.CurrentObjectID)
	query := app.SearchTreeQuery{Snapshot: target.Head, Query: "main", Limit: 1}
	page, err := searcher.SearchTree(context.Background(), query)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Matches) != 1 || string(page.Matches[0].Entry.Path) != "src/main.go" || page.NextCursor == "" || !page.Complete {
		t.Fatalf("first search page = %#v", page)
	}
	firstCursor := page.NextCursor
	page, err = searcher.SearchTree(context.Background(), app.SearchTreeQuery{Snapshot: target.Head, Query: "main", Cursor: firstCursor, Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Matches) != 1 || string(page.Matches[0].Entry.Path) != "src/main_test.go" {
		t.Fatalf("second search page = %#v", page)
	}
	if _, err := searcher.SearchTree(context.Background(), app.SearchTreeQuery{Snapshot: target.Head, Query: "notes", Cursor: firstCursor, Limit: 1}); !errors.Is(err, ErrTreeSearchCursor) {
		t.Fatalf("cross-query cursor error = %v", err)
	}
}

func TestSearchTreeUsesInjectedImmutableWorkingTreeEntries(t *testing.T) {
	root, gitPath := initializedRepository(t)
	resolver := newTestResolver(t, root, gitPath)
	path := repository.RepoPath([]byte("bad\xff.go"))
	entry := repository.TreeEntry{Path: path, Name: path, Kind: repository.FileKindRegular, Mode: 0o100644}
	if err := entry.Validate(); err != nil {
		t.Fatal(err)
	}
	worktreeID := domain.WorktreeID("worktree")
	snapshot := repository.SnapshotRef{Kind: repository.SnapshotWorkingTree, WorktreeID: worktreeID, Fingerprint: strings.Repeat("a", 64)}
	searcher, err := NewTreeSearcher(TreeSearcherConfig{
		Executable: resolver.executable,
		StartPath:  root,
		WorkingTree: func(_ context.Context, got repository.SnapshotRef, visit func(repository.TreeEntry) error) error {
			if got != snapshot {
				t.Fatalf("working snapshot = %#v", got)
			}
			return visit(entry)
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	page, err := searcher.SearchTree(context.Background(), app.SearchTreeQuery{Snapshot: snapshot, Query: "bad", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Matches) != 1 || string(page.Matches[0].Entry.Path.Bytes()) != string(path.Bytes()) {
		t.Fatalf("raw working-tree result = %#v", page.Matches)
	}
}

func TestSearchTreeCancellationNeverPublishesCompletePage(t *testing.T) {
	root, gitPath := initializedRepository(t)
	resolver := newTestResolver(t, root, gitPath)
	worktreeID := domain.WorktreeID("worktree")
	snapshot := repository.SnapshotRef{Kind: repository.SnapshotWorkingTree, WorktreeID: worktreeID, Fingerprint: strings.Repeat("b", 64)}
	searcher, err := NewTreeSearcher(TreeSearcherConfig{
		Executable: resolver.executable,
		StartPath:  root,
		WorkingTree: func(ctx context.Context, _ repository.SnapshotRef, _ func(repository.TreeEntry) error) error {
			return ctx.Err()
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = searcher.SearchTree(ctx, app.SearchTreeQuery{Snapshot: snapshot, Query: "x", Limit: 10})
	if !errors.Is(err, app.ErrTreeSearchIncomplete) || !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation error = %v", err)
	}
}

func TestSearchTreeStreamsAcceptedHundredThousandEntryCeiling(t *testing.T) {
	root, gitPath := initializedRepository(t)
	resolver := newTestResolver(t, root, gitPath)
	worktreeID := domain.WorktreeID("worktree")
	snapshot := repository.SnapshotRef{Kind: repository.SnapshotWorkingTree, WorktreeID: worktreeID, Fingerprint: strings.Repeat("c", 64)}
	searcher, err := NewTreeSearcher(TreeSearcherConfig{
		Executable: resolver.executable,
		StartPath:  root,
		WorkingTree: func(_ context.Context, _ repository.SnapshotRef, visit func(repository.TreeEntry) error) error {
			for index := 0; index < 100_000; index++ {
				path, pathErr := repository.NewRepoPath([]byte(fmt.Sprintf("generated/file-%05d.go", index)))
				if pathErr != nil {
					return pathErr
				}
				entry := repository.TreeEntry{Path: path, Name: repository.RepoPath(path.Bytes()[10:]), Parent: repository.RepoPath([]byte("generated")), Kind: repository.FileKindRegular, Mode: 0o100644}
				if visitErr := visit(entry); visitErr != nil {
					return visitErr
				}
			}
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	page, err := searcher.SearchTree(context.Background(), app.SearchTreeQuery{Snapshot: snapshot, Query: "file", Limit: 50})
	if err != nil {
		t.Fatal(err)
	}
	if page.ScannedEntries != 100_000 || len(page.Matches) != 50 || page.NextCursor == "" {
		t.Fatalf("bounded search page = scanned %d matches %d cursor %t", page.ScannedEntries, len(page.Matches), page.NextCursor != "")
	}
}
