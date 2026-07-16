package app

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Scottlr/nudge/internal/domain"
)

type reconcileLedgerStub struct {
	page OwnedStorageLedgerPage
}

func (s reconcileLedgerStub) ReconciliationPage(context.Context, OwnedStorageLedgerPageQuery) (OwnedStorageLedgerPage, error) {
	return s.page, nil
}

type reconcileStoreStub struct {
	saved int
}

func (s *reconcileStoreStub) SaveOwnedStorageReconciliation(context.Context, OwnedStorageReconciliationEpoch, []OwnedStorageDiscrepancy) error {
	s.saved++
	return nil
}

func (*reconcileStoreStub) LoadOwnedStorageReconciliation(context.Context, string) (OwnedStorageReconciliationEpoch, []OwnedStorageDiscrepancy, error) {
	return OwnedStorageReconciliationEpoch{}, nil, errors.New("not used")
}

func TestOwnedStorageReconcilerReportsLedgerOnlyHealthAsUncertain(t *testing.T) {
	page := OwnedStorageLedgerPage{Revision: 4, Global: StorageTotals{Revision: 4}, Pressure: StoragePressureState{RepositoryPressure: StoragePressureNone, GlobalPressure: StoragePressureNone}, Artifacts: []OwnedArtifact{testReconcileArtifact()}, Complete: true}
	reconciler, err := NewOwnedStorageReconciler(reconcileLedgerStub{page: page}, nil, DefaultResourcePolicy(), fixedClock{when: time.Unix(10, 0).UTC()})
	if err != nil {
		t.Fatal(err)
	}
	report, err := reconciler.Reconcile(context.Background(), OwnedStorageReconcileRequest{Mode: ReconcileQueryOnly})
	if err != nil {
		t.Fatal(err)
	}
	if report.Capacity.Status != CapacityHealthAccountingUncertain || report.Capacity.UncertaintyCount != 1 || len(report.Discrepancies) != 0 || len(report.Candidates) != 0 || !report.Complete {
		t.Fatalf("report = %#v", report)
	}
}

func TestOwnedStorageReconcilerPersistsOnlyAdvance(t *testing.T) {
	store := &reconcileStoreStub{}
	reconciler, err := NewOwnedStorageReconciler(reconcileLedgerStub{page: OwnedStorageLedgerPage{Revision: 5, Global: StorageTotals{Revision: 5}, Complete: true}}, store, DefaultResourcePolicy(), fixedClock{when: time.Unix(10, 0).UTC()})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reconciler.Reconcile(context.Background(), OwnedStorageReconcileRequest{Mode: ReconcileQueryOnly}); err != nil {
		t.Fatal(err)
	}
	if store.saved != 0 {
		t.Fatalf("query saved = %d", store.saved)
	}
	if _, err := reconciler.Reconcile(context.Background(), OwnedStorageReconcileRequest{Mode: ReconcileAdvance}); err != nil {
		t.Fatal(err)
	}
	if store.saved != 1 {
		t.Fatalf("advance saved = %d, want 1", store.saved)
	}
}

func TestOwnedStorageReconcilerResumesLedgerCursor(t *testing.T) {
	page := OwnedStorageLedgerPage{Revision: 6, Global: StorageTotals{Revision: 6}, NextCursor: "artifact:artifact-1", Complete: false}
	reconciler, err := NewOwnedStorageReconciler(reconcileLedgerStub{page: page}, nil, DefaultResourcePolicy(), nil)
	if err != nil {
		t.Fatal(err)
	}
	report, err := reconciler.Reconcile(context.Background(), OwnedStorageReconcileRequest{Mode: ReconcileQueryOnly, MaxItems: 1})
	if err != nil {
		t.Fatal(err)
	}
	if report.Complete || report.NextCursor != page.NextCursor || report.Capacity.Complete || report.Capacity.Status != CapacityHealthAccountingUncertain {
		t.Fatalf("report = %#v", report)
	}
}

func TestOwnedStorageReconcilerRejectsRevisionDrift(t *testing.T) {
	reconciler, err := NewOwnedStorageReconciler(reconcileLedgerStub{page: OwnedStorageLedgerPage{Revision: 6, Global: StorageTotals{Revision: 6}, Complete: true}}, nil, DefaultResourcePolicy(), nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = reconciler.Reconcile(context.Background(), OwnedStorageReconcileRequest{Mode: ReconcileQueryOnly, ExpectedLedgerRevision: 7})
	if !errors.Is(err, ErrOwnedStorageReconcileDrift) {
		t.Fatalf("drift error = %v", err)
	}
}

func TestCapacityHealthFromLedgerRemainsUncertainWithoutOwnerEvidence(t *testing.T) {
	health := CapacityHealthFromLedger(StorageLedgerSnapshot{Revision: 3, Global: StorageTotals{Revision: 3}}, nil, "epoch", "", true)
	if health.Status != CapacityHealthAccountingUncertain || health.UncertaintyCount != 1 || !health.Complete {
		t.Fatalf("health = %#v", health)
	}
}

func testReconcileArtifact() OwnedArtifact {
	return OwnedArtifact{ArtifactID: "artifact-1", OwnerKind: OwnerCapture, OwnerID: "capture-1", OperationID: domain.OperationID("operation-1"), ReservationID: "reservation-1", Class: StorageClassCapture, Lifecycle: OwnedArtifactAccepted, LogicalBytes: 32, ObservedBytes: 32, ChargedBytes: 32, VolumeID: "volume-1", ManifestHash: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", AccountingVersion: CurrentStorageAccountingVersion, PolicyVersion: DefaultResourcePolicy().Version, Complete: true, CreatedAt: time.Unix(1, 0).UTC()}
}
