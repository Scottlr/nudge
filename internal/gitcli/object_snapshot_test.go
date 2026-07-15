package gitcli

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Scottlr/nudge/internal/domain/repository"
)

func TestObjectSnapshotSourceReadsPinnedTreeAfterWorktreeChanges(t *testing.T) {
	root, gitPath := initializedRepository(t)
	resolver := newTestResolver(t, root, gitPath)
	head := repository.ObjectID(strings.TrimSpace(string(runGit(t, root, gitPath, "rev-parse", "HEAD"))))
	source, err := NewObjectSnapshotSource(ObjectSnapshotSourceConfig{
		Executable: resolver.executable,
		Runner:     resolver.runner,
		StartPath:  root,
		Policy:     resolver.policy,
		MaxEntries: 16,
	})
	if err != nil {
		t.Fatal(err)
	}

	entries, err := source.ListBase(context.Background(), repository.LocalCaptureBase{ObjectFormat: "sha1", ObjectID: head})
	if err != nil {
		t.Fatal(err)
	}
	var tracked repository.TreeEntry
	for _, entry := range entries {
		if string(entry.Path.Bytes()) == "tracked file.txt" {
			tracked = entry
			break
		}
	}
	if tracked.Path == nil || tracked.Kind != repository.FileKindRegular {
		t.Fatalf("pinned tree entries = %#v", entries)
	}

	if err := os.WriteFile(filepath.Join(root, "tracked file.txt"), []byte("dirty worktree\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "untracked.txt"), []byte("untracked\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, root, gitPath, "commit", "--allow-empty", "--no-gpg-sign", "-m", "newer ref")
	content, err := source.OpenBase(context.Background(), repository.LocalCaptureBase{ObjectFormat: "sha1", ObjectID: head}, tracked)
	if err != nil {
		t.Fatal(err)
	}
	defer content.Close()
	data, err := io.ReadAll(content)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "initial\n" {
		t.Fatalf("pinned content = %q, want committed content", data)
	}
	for _, entry := range entries {
		if string(entry.Path.Bytes()) == "untracked.txt" {
			t.Fatal("pinned tree included an untracked worktree path")
		}
	}

	_, err = source.ListBase(context.Background(), repository.LocalCaptureBase{ObjectFormat: "sha1", ObjectID: "missing-object"})
	var gitErr *GitError
	if !errors.As(err, &gitErr) || gitErr.Code != ErrorObjectUnavailableNoFetch {
		t.Fatalf("missing object error = %v, want typed no-fetch unavailable", err)
	}

}
