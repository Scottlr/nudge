package sqlite

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/Scottlr/nudge/internal/app"
)

func TestOwnedStorageReconciliationPageResumesByStableCursor(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, testDatabasePath(t))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	first, artifact := testLedgerRecord(t, "reserve-1", "reservation-1", 100, 100)
	second, _ := testLedgerRecord(t, "reserve-2", "reservation-2", 100, 100)
	if err := store.RecordReservation(ctx, first); err != nil {
		t.Fatal(err)
	}
	if err := store.RecordReservation(ctx, second); err != nil {
		t.Fatal(err)
	}
	if err := store.SettleReservation(ctx, app.ReservationSettlement{ReservationID: first.Reservation.Marker(), IdempotencyKey: "settle-1", Artifacts: []app.OwnedArtifact{artifact}}); err != nil {
		t.Fatal(err)
	}

	page, err := store.ReconciliationPage(ctx, app.OwnedStorageLedgerPageQuery{Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if page.ActiveReservations != 1 || len(page.Artifacts) != 1 || page.Artifacts[0].ArtifactID != artifact.ArtifactID || page.Complete || page.NextCursor == "" {
		t.Fatalf("first page = %#v", page)
	}
	page, err = store.ReconciliationPage(ctx, app.OwnedStorageLedgerPageQuery{Cursor: page.NextCursor, Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Reservations) != 1 || page.Reservations[0].ReservationID != second.Reservation.Marker() || !page.Complete {
		t.Fatalf("second page = %#v", page)
	}
}

func TestOwnedStorageReconciliationPersistenceIsIdempotentAndRedacted(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, testDatabasePath(t))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	epoch := app.OwnedStorageReconciliationEpoch{Epoch: strings.Repeat("a", 64), LedgerRevision: 1, PolicyVersion: app.DefaultResourcePolicy().Version, BatchKey: strings.Repeat("b", 64), Complete: true, UpdatedAt: time.Unix(10, 0).UTC()}
	discrepancy := app.OwnedStorageDiscrepancy{Kind: app.ManifestMismatch, OwnerKind: app.OwnerCapture, OwnerID: "capture-1", ArtifactID: "artifact-1", VolumeID: "volume-1", ExpectedManifestHash: strings.Repeat("c", 64), ObservedManifestHash: strings.Repeat("d", 64), ExpectedBytes: 3, ObservedBytes: 4, EvidenceCode: "manifest_mismatch"}
	if err := store.SaveOwnedStorageReconciliation(ctx, epoch, []app.OwnedStorageDiscrepancy{discrepancy}); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveOwnedStorageReconciliation(ctx, epoch, []app.OwnedStorageDiscrepancy{discrepancy}); err != nil {
		t.Fatal(err)
	}
	loaded, discrepancies, err := store.LoadOwnedStorageReconciliation(ctx, epoch.Epoch)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Epoch != epoch.Epoch || len(discrepancies) != 1 || discrepancies[0].Kind != app.ManifestMismatch || discrepancies[0].ExpectedManifestHash != discrepancy.ExpectedManifestHash {
		t.Fatalf("loaded epoch/discrepancies = %#v/%#v", loaded, discrepancies)
	}
}
