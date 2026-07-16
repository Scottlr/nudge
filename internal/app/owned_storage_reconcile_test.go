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

type reconcileInspectorStub struct {
	artifact    OwnedStorageArtifactEvidence
	reservation OwnedStorageReservationEvidence
	discovery   OwnedStorageDiscovery
}

func (s reconcileInspectorStub) OwnerKind() OwnerKind { return OwnerCapture }

func (s reconcileInspectorStub) InspectArtifact(context.Context, OwnedArtifact) (OwnedStorageArtifactEvidence, error) {
	return s.artifact, nil
}

func (s reconcileInspectorStub) InspectReservation(context.Context, CapacityReservationSummary) (OwnedStorageReservationEvidence, error) {
	return s.reservation, nil
}

func (s reconcileInspectorStub) Discover(context.Context, OwnedStorageDiscoveryRequest) (OwnedStorageDiscovery, error) {
	return s.discovery, nil
}

func TestOwnedStorageReconcilerReportsMatchingLedgerAsHealthy(t *testing.T) {
	artifact := testReconcileArtifact()
	inspector := reconcileInspectorStub{
		artifact:  OwnedStorageArtifactEvidence{ArtifactID: artifact.ArtifactID, OwnerKind: artifact.OwnerKind, OwnerID: artifact.OwnerID, ReservationID: artifact.ReservationID, VolumeID: artifact.VolumeID, ManifestHash: artifact.ManifestHash, ObservedBytes: artifact.ObservedBytes, EvidenceBytes: 64, State: OwnedStorageEvidenceAccepted, Complete: true},
		discovery: OwnedStorageDiscovery{Complete: true},
	}
	page := OwnedStorageLedgerPage{Revision: 4, Global: StorageTotals{Revision: 4}, Pressure: StoragePressureState{RepositoryPressure: StoragePressureNone, GlobalPressure: StoragePressureNone}, Artifacts: []OwnedArtifact{artifact}, Complete: true}
	reconciler, err := NewOwnedStorageReconciler(reconcileLedgerStub{page: page}, nil, []OwnedStorageInspector{inspector}, DefaultResourcePolicy(), fixedClock{when: time.Unix(10, 0).UTC()})
	if err != nil {
		t.Fatal(err)
	}
	report, err := reconciler.Reconcile(context.Background(), OwnedStorageReconcileRequest{Mode: ReconcileQueryOnly})
	if err != nil {
		t.Fatal(err)
	}
	if report.Capacity.Status != CapacityHealthOK || len(report.Discrepancies) != 0 || !report.Complete {
		t.Fatalf("report = %#v", report)
	}
}

func TestOwnedStorageReconcilerClassifiesManifestMismatchAndPersistsOnlyAdvance(t *testing.T) {
	artifact := testReconcileArtifact()
	inspector := reconcileInspectorStub{
		artifact:  OwnedStorageArtifactEvidence{ArtifactID: artifact.ArtifactID, OwnerKind: artifact.OwnerKind, OwnerID: artifact.OwnerID, ReservationID: artifact.ReservationID, VolumeID: artifact.VolumeID, ManifestHash: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", ObservedBytes: artifact.ObservedBytes, EvidenceBytes: 64, State: OwnedStorageEvidenceAccepted, Complete: true, Rebuildable: true},
		discovery: OwnedStorageDiscovery{Complete: true},
	}
	store := &reconcileStoreStub{}
	reconciler, err := NewOwnedStorageReconciler(reconcileLedgerStub{page: OwnedStorageLedgerPage{Revision: 5, Global: StorageTotals{Revision: 5}, Complete: true, Artifacts: []OwnedArtifact{artifact}}}, store, []OwnedStorageInspector{inspector}, DefaultResourcePolicy(), fixedClock{when: time.Unix(10, 0).UTC()})
	if err != nil {
		t.Fatal(err)
	}
	queryReport, err := reconciler.Reconcile(context.Background(), OwnedStorageReconcileRequest{Mode: ReconcileQueryOnly})
	if err != nil {
		t.Fatal(err)
	}
	if store.saved != 0 || len(queryReport.Candidates) != 1 || queryReport.Discrepancies[0].Kind != ManifestMismatch {
		t.Fatalf("query report/store = %#v/%d", queryReport, store.saved)
	}
	_, err = reconciler.Reconcile(context.Background(), OwnedStorageReconcileRequest{Mode: ReconcileAdvance})
	if err != nil {
		t.Fatal(err)
	}
	if store.saved != 1 {
		t.Fatalf("saved = %d, want 1", store.saved)
	}
}

func TestOwnedStorageReconcilerFailsClosedOnMissingInspectorAndRevisionDrift(t *testing.T) {
	artifact := testReconcileArtifact()
	reconciler, err := NewOwnedStorageReconciler(reconcileLedgerStub{page: OwnedStorageLedgerPage{Revision: 6, Global: StorageTotals{Revision: 6}, Complete: true, Artifacts: []OwnedArtifact{artifact}}}, nil, nil, DefaultResourcePolicy(), nil)
	if err != nil {
		t.Fatal(err)
	}
	report, err := reconciler.Reconcile(context.Background(), OwnedStorageReconcileRequest{Mode: ReconcileQueryOnly})
	if err != nil {
		t.Fatal(err)
	}
	if report.Capacity.Status != CapacityHealthAccountingUncertain || len(report.Candidates) != 0 || report.Discrepancies[0].Kind != OwnershipUncertain {
		t.Fatalf("uncertain report = %#v", report)
	}
	_, err = reconciler.Reconcile(context.Background(), OwnedStorageReconcileRequest{Mode: ReconcileQueryOnly, ExpectedLedgerRevision: 7})
	if !errors.Is(err, ErrOwnedStorageReconcileDrift) {
		t.Fatalf("drift error = %v", err)
	}
}

func testReconcileArtifact() OwnedArtifact {
	return OwnedArtifact{ArtifactID: "artifact-1", OwnerKind: OwnerCapture, OwnerID: "capture-1", OperationID: domain.OperationID("operation-1"), ReservationID: "reservation-1", Class: StorageClassCapture, Lifecycle: OwnedArtifactAccepted, LogicalBytes: 32, ObservedBytes: 32, ChargedBytes: 32, VolumeID: "volume-1", ManifestHash: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", AccountingVersion: CurrentStorageAccountingVersion, PolicyVersion: DefaultResourcePolicy().Version, Complete: true, CreatedAt: time.Unix(1, 0).UTC()}
}
