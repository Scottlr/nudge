package sqlite

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Scottlr/nudge/internal/app"
	"github.com/Scottlr/nudge/internal/domain"
)

func TestOwnedStorageLedgerReservationSettlementIsIdempotent(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, testDatabasePath(t))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()

	record, artifact := testLedgerRecord(t, "reserve-1", "reservation-1", 100, 100)
	if err := store.RecordReservation(ctx, record); err != nil {
		t.Fatalf("record reservation: %v", err)
	}
	if err := store.RecordReservation(ctx, record); err != nil {
		t.Fatalf("repeat reservation: %v", err)
	}
	snapshot, err := store.Snapshot(ctx, app.StorageLedgerQuery{Limit: 10, IncludeReservations: true, IncludeArtifacts: true, RepositoryID: record.Plan.RepositoryID})
	if err != nil {
		t.Fatalf("reservation snapshot: %v", err)
	}
	if snapshot.Revision != 1 || snapshot.Repository.ReservedBytes != 100 || snapshot.Global.ReservedBytes != 100 || len(snapshot.Reservations) != 1 || len(snapshot.Artifacts) != 0 {
		t.Fatalf("reserved snapshot = %#v", snapshot)
	}

	settlement := app.ReservationSettlement{ReservationID: record.Reservation.Marker(), IdempotencyKey: "settle-1", ExpectedRevision: snapshot.Revision, Artifacts: []app.OwnedArtifact{artifact}}
	if err := store.SettleReservation(ctx, settlement); err != nil {
		t.Fatalf("settle reservation: %v", err)
	}
	if err := store.SettleReservation(ctx, settlement); err != nil {
		t.Fatalf("repeat settlement: %v", err)
	}
	snapshot, err = store.Snapshot(ctx, app.StorageLedgerQuery{Limit: 10, IncludeReservations: true, IncludeArtifacts: true, RepositoryID: record.Plan.RepositoryID})
	if err != nil {
		t.Fatalf("settled snapshot: %v", err)
	}
	if snapshot.Revision != 2 || snapshot.Repository.ReservedBytes != 0 || snapshot.Repository.ChargedBytes != 90 || len(snapshot.Artifacts) != 1 || snapshot.Artifacts[0].ArtifactID != artifact.ArtifactID || snapshot.Pressure.Publication != app.StorageDecisionAllowed {
		t.Fatalf("settled snapshot = %#v", snapshot)
	}
	if err := store.ReleaseReservation(ctx, app.ReservationRelease{ReservationID: record.Reservation.Marker(), IdempotencyKey: "release-after-settle"}); !errors.Is(err, app.ErrStorageLedgerConflict) {
		t.Fatalf("release consumed reservation error = %v, want conflict", err)
	}
}

func TestOwnedStorageLedgerReleaseAndBoundedSnapshot(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, testDatabasePath(t))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()

	record, _ := testLedgerRecord(t, "reserve-1", "reservation-1", 100, 100)
	if err := store.RecordReservation(ctx, record); err != nil {
		t.Fatalf("record reservation: %v", err)
	}
	record2, _ := testLedgerRecord(t, "reserve-2", "reservation-2", 200, 200)
	if err := store.RecordReservation(ctx, record2); err != nil {
		t.Fatalf("record second reservation: %v", err)
	}
	snapshot, err := store.Snapshot(ctx, app.StorageLedgerQuery{Limit: 1, IncludeReservations: true, RepositoryID: record.Plan.RepositoryID})
	if err != nil {
		t.Fatalf("bounded snapshot: %v", err)
	}
	if snapshot.Complete || len(snapshot.Reservations) != 1 {
		t.Fatalf("bounded snapshot = %#v", snapshot)
	}
	release := app.ReservationRelease{ReservationID: record.Reservation.Marker(), IdempotencyKey: "release-1"}
	if err := store.ReleaseReservation(ctx, release); err != nil {
		t.Fatalf("release reservation: %v", err)
	}
	if err := store.ReleaseReservation(ctx, release); err != nil {
		t.Fatalf("repeat release: %v", err)
	}
	snapshot, err = store.Snapshot(ctx, app.StorageLedgerQuery{Limit: 10, IncludeReservations: true, RepositoryID: record.Plan.RepositoryID})
	if err != nil {
		t.Fatalf("released snapshot: %v", err)
	}
	if snapshot.Repository.ReservedBytes != 200 || snapshot.Revision != 3 {
		t.Fatalf("released snapshot = %#v", snapshot)
	}
}

func TestOwnedStorageLedgerBlocksSoftGrowth(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, testDatabasePath(t))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()

	record, _ := testLedgerRecord(t, "reserve-1", "reservation-1", app.DefaultResourcePolicy().Storage.RepositorySoftBytes, app.DefaultResourcePolicy().Storage.RepositorySoftBytes)
	if err := store.RecordReservation(ctx, record); !errors.Is(err, app.ErrStoragePublicationBlocked) {
		t.Fatalf("soft growth error = %v, want publication blocked", err)
	}
	snapshot, err := store.Snapshot(ctx, app.StorageLedgerQuery{Limit: 10, IncludeReservations: true, RepositoryID: record.Plan.RepositoryID})
	if err != nil {
		t.Fatalf("pressure snapshot: %v", err)
	}
	if snapshot.Revision != 0 || snapshot.Repository.ReservedBytes != 0 || len(snapshot.Reservations) != 0 {
		t.Fatalf("pressure snapshot = %#v", snapshot)
	}
}

func TestOwnedStorageLedgerConcurrentReservationsPreserveSoftBudget(t *testing.T) {
	ctx := context.Background()
	path := testDatabasePath(t)
	first, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("open first store: %v", err)
	}
	defer first.Close()
	second, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("open second store: %v", err)
	}
	defer second.Close()

	retained := app.DefaultResourcePolicy().Storage.RepositorySoftBytes / 2
	record1, _ := testLedgerRecord(t, "reserve-1", "reservation-1", retained, retained)
	record2, _ := testLedgerRecord(t, "reserve-2", "reservation-2", retained, retained)
	results := make(chan error, 2)
	var group sync.WaitGroup
	group.Add(2)
	go func() {
		defer group.Done()
		results <- first.RecordReservation(ctx, record1)
	}()
	go func() {
		defer group.Done()
		results <- second.RecordReservation(ctx, record2)
	}()
	group.Wait()
	close(results)

	var accepted, blocked int
	for err := range results {
		switch {
		case err == nil:
			accepted++
		case errors.Is(err, app.ErrStoragePublicationBlocked):
			blocked++
		default:
			t.Fatalf("concurrent reservation error = %v", err)
		}
	}
	if accepted != 1 || blocked != 1 {
		t.Fatalf("concurrent reservation outcomes accepted=%d blocked=%d", accepted, blocked)
	}
	snapshot, err := first.Snapshot(ctx, app.StorageLedgerQuery{Limit: 10, IncludeReservations: true, RepositoryID: record1.Plan.RepositoryID})
	if err != nil {
		t.Fatalf("concurrent reservation snapshot: %v", err)
	}
	if snapshot.Repository.ReservedBytes != retained || len(snapshot.Reservations) != 1 {
		t.Fatalf("concurrent reservation snapshot = %#v", snapshot)
	}
}

func testLedgerRecord(t *testing.T, idempotency, reservationID string, retained, peak app.ByteSize) (app.CapacityReservationRecord, app.OwnedArtifact) {
	t.Helper()
	policy := app.DefaultResourcePolicy()
	repositoryID := domain.RepositoryID("repository-1")
	operationID := domain.OperationID("operation-" + reservationID)
	plan := app.CapacityPlan{
		OperationID:   operationID,
		RepositoryID:  &repositoryID,
		RetainedDelta: retained,
		PolicyVersion: policy.Version,
		VolumePeaks: []app.VolumePeak{{
			ID:            "volume-1",
			Finals:        peak - retained,
			Reserve:       1,
			RetainedDelta: retained,
		}},
	}
	digest, err := app.PlanDigest(plan)
	if err != nil {
		t.Fatalf("plan digest: %v", err)
	}
	reservation, err := app.NewCapacityReservation(reservationID, operationID, string(repositoryID), digest, policy.Version)
	if err != nil {
		t.Fatalf("reservation: %v", err)
	}
	now := time.Date(2026, time.July, 14, 20, 0, 0, 0, time.UTC)
	record := app.CapacityReservationRecord{Reservation: reservation, OwnerKind: app.OwnerCapture, OwnerID: "capture-1", Plan: plan, IdempotencyKey: idempotency, CreatedAt: now}
	charged, err := app.StorageAccountingCharge(app.CurrentStorageAccountingVersion, app.StorageClassCapture, 80, 90)
	if err != nil {
		t.Fatalf("artifact charge: %v", err)
	}
	artifact := app.OwnedArtifact{ArtifactID: "artifact-" + reservationID, OwnerKind: app.OwnerCapture, OwnerID: "capture-1", OperationID: operationID, ReservationID: reservationID, RepositoryID: &repositoryID, Class: app.StorageClassCapture, Lifecycle: app.OwnedArtifactAccepted, LogicalBytes: 80, ObservedBytes: 90, ChargedBytes: charged, VolumeID: "volume-1", ManifestHash: strings.Repeat("a", 64), AccountingVersion: app.CurrentStorageAccountingVersion, PolicyVersion: policy.Version, Complete: true, CreatedAt: now}
	return record, artifact
}
