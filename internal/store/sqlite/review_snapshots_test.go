package sqlite

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Scottlr/nudge/internal/app"
	"github.com/Scottlr/nudge/internal/domain"
)

func TestReviewSnapshotStoreRoundTripAndLeaseClosure(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, testDatabasePath(t))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	now := time.Unix(20, 0).UTC()
	snapshot := app.ReviewSnapshot{ID: "snapshot-1", CaptureID: "capture-1", RepositoryID: "repository-1", WorktreeID: "worktree-1", Root: "C:\\nudge\\published\\snapshot-1", MarkerNonce: strings.Repeat("a", 64), ManifestHash: strings.Repeat("b", 64), PolicyVersion: app.CurrentResourcePolicyVersion, EvidenceVersion: app.CurrentCapabilityEvidenceVersion, State: app.ReviewSnapshotReady, CreatedAt: now}
	if err := store.SaveReviewSnapshot(ctx, snapshot); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.LoadReviewSnapshotByCapture(ctx, snapshot.CaptureID)
	if err != nil || loaded != snapshot {
		t.Fatalf("loaded snapshot = %#v, err=%v", loaded, err)
	}
	lease := app.ReviewSnapshotLease{ID: domain.ReviewSnapshotLeaseID("lease-1"), SnapshotID: snapshot.ID, CaptureID: snapshot.CaptureID, Root: snapshot.Root, ManifestHash: snapshot.ManifestHash, ProcessNonce: strings.Repeat("c", 64), AcquiredAt: now}
	if err := store.SaveReviewSnapshotLease(ctx, lease); err != nil {
		t.Fatal(err)
	}
	count, err := store.CountReviewSnapshotLeases(ctx, snapshot.ID)
	if err != nil || count != 1 {
		t.Fatalf("active lease count = %d, err=%v", count, err)
	}
	if err := store.ReleaseReviewSnapshotLease(ctx, lease.ID); err != nil {
		t.Fatal(err)
	}
	count, err = store.CountReviewSnapshotLeases(ctx, snapshot.ID)
	if err != nil || count != 0 {
		t.Fatalf("released lease count = %d, err=%v", count, err)
	}
	if err := store.DeleteReviewSnapshot(ctx, snapshot.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.LoadReviewSnapshot(ctx, snapshot.ID); !errors.Is(err, app.ErrReviewSnapshotNotFound) {
		t.Fatalf("deleted snapshot load = %v", err)
	}
}
