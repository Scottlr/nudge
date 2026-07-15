package workspace

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Scottlr/nudge/internal/app"
	"github.com/Scottlr/nudge/internal/domain"
	"github.com/Scottlr/nudge/internal/domain/repository"
)

func TestReviewSnapshotFreezesAcceptedCaptureAndProtectsLeases(t *testing.T) {
	const value = "accepted capture"
	committer := &captureCommitter{manifests: make(map[domain.CaptureID]app.CaptureManifest)}
	reader := &captureReader{data: map[string][]byte{"blob-00000000": []byte(value)}}
	captures, err := NewCaptureStore(CaptureStoreConfig{Committer: committer, Manifests: committer, Reader: reader, Releaser: &captureReleaser{}})
	if err != nil {
		t.Fatal(err)
	}
	adoption, err := captures.Adopt(context.Background(), testCaptureArtifacts(t, value, "patch-snapshot", "blob-snapshot"), testCaptureSessionState())
	if err != nil {
		t.Fatal(err)
	}
	policy := app.DefaultResourcePolicy()
	root := filepath.Join(t.TempDir(), "snapshots")
	manager, err := NewReviewSnapshotManager(ReviewSnapshotConfig{
		Root: root, Source: emptyReviewSnapshotSource{}, Captures: captures, IDs: snapshotIDs{}, Policy: policy,
		Persist: false, ProcessNonce: strings.Repeat("d", 64),
		FreeSpace: func(context.Context, string) (app.ByteSize, error) { return ^app.ByteSize(0), nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	request := app.ReviewSnapshotEnsureRequest{CaptureID: adoption.Generation.CaptureID, RepositoryID: "repository", WorktreeID: "worktree", PolicyVersion: policy.Version, EvidenceVersion: app.CurrentCapabilityEvidenceVersion}
	snapshot, err := manager.Ensure(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.State != app.ReviewSnapshotReady {
		t.Fatalf("snapshot state = %q", snapshot.State)
	}
	content, err := os.ReadFile(filepath.Join(snapshot.Root, "file.txt"))
	if err != nil || string(content) != value {
		t.Fatalf("snapshot content = %q, err=%v", string(content), err)
	}
	reader.data["blob-00000000"] = []byte("live worktree changed")
	content, err = os.ReadFile(filepath.Join(snapshot.Root, "file.txt"))
	if err != nil || string(content) != value {
		t.Fatalf("snapshot changed after source mutation = %q, err=%v", string(content), err)
	}
	reused, err := manager.Ensure(context.Background(), request)
	if err != nil || reused.ID != snapshot.ID {
		t.Fatalf("snapshot reuse = %#v, err=%v", reused, err)
	}
	lease, err := manager.AcquireReadLease(context.Background(), snapshot.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.Remove(context.Background(), snapshot.ID); !errors.Is(err, app.ErrReviewSnapshotBusy) {
		t.Fatalf("Remove() with active lease = %v", err)
	}
	if err := manager.Release(context.Background(), lease); err != nil {
		t.Fatal(err)
	}
	if err := manager.Remove(context.Background(), snapshot.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(snapshot.Root); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("removed root stat = %v", err)
	}
}

func TestReviewSnapshotLimitLeavesNoPublishedRoot(t *testing.T) {
	committer := &captureCommitter{manifests: make(map[domain.CaptureID]app.CaptureManifest)}
	reader := &captureReader{data: map[string][]byte{"blob-00000000": []byte("bounded")}}
	captures, err := NewCaptureStore(CaptureStoreConfig{Committer: committer, Manifests: committer, Reader: reader, Releaser: &captureReleaser{}})
	if err != nil {
		t.Fatal(err)
	}
	adoption, err := captures.Adopt(context.Background(), testCaptureArtifacts(t, "bounded", "patch-limit", "blob-limit"), testCaptureSessionState())
	if err != nil {
		t.Fatal(err)
	}
	policy := app.DefaultResourcePolicy()
	root := filepath.Join(t.TempDir(), "snapshots")
	manager, err := NewReviewSnapshotManager(ReviewSnapshotConfig{
		Root: root, Source: emptyReviewSnapshotSource{}, Captures: captures, IDs: snapshotIDs{}, Policy: policy,
		Persist: false, ProcessNonce: strings.Repeat("e", 64),
		FreeSpace: func(context.Context, string) (app.ByteSize, error) { return policy.Storage.MinimumFreeBytes, nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = manager.Ensure(context.Background(), app.ReviewSnapshotEnsureRequest{CaptureID: adoption.Generation.CaptureID, RepositoryID: "repository", WorktreeID: "worktree", PolicyVersion: policy.Version, EvidenceVersion: app.CurrentCapabilityEvidenceVersion})
	if !errors.Is(err, app.ErrReviewSnapshotLimit) {
		t.Fatalf("Ensure() error = %v, want review snapshot limit", err)
	}
	entries, readErr := os.ReadDir(filepath.Join(root, "ephemeral"))
	if readErr != nil {
		t.Fatal(readErr)
	}
	if len(entries) != 0 {
		t.Fatalf("ephemeral entries after failed publication = %d", len(entries))
	}
}

type emptyReviewSnapshotSource struct{}

func (emptyReviewSnapshotSource) ListBase(context.Context, repository.LocalCaptureBase) ([]repository.TreeEntry, error) {
	return nil, nil
}

func (emptyReviewSnapshotSource) OpenBase(context.Context, repository.LocalCaptureBase, repository.TreeEntry) (io.ReadCloser, error) {
	return io.NopCloser(bytes.NewReader(nil)), nil
}

type snapshotIDs struct{}

func (snapshotIDs) NewID() string { return "snapshot-id" }
