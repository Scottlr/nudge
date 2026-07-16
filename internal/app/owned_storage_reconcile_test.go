package app

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Scottlr/nudge/internal/domain"
)

type reconcileLedgerStub struct {
	page OwnedStorageLedgerPage
}

func (s reconcileLedgerStub) ReconciliationPage(_ context.Context, query OwnedStorageLedgerPageQuery) (OwnedStorageLedgerPage, error) {
	page := s.page
	if strings.HasPrefix(query.Cursor, "owner:") {
		page.Artifacts = nil
		page.Reservations = nil
		page.NextCursor = ""
		page.Complete = true
	}
	return page, nil
}

func (s reconcileLedgerStub) ContainsArtifact(context.Context, *domain.RepositoryID, string) (bool, error) {
	return false, nil
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
	owner       OwnerKind
	cursorSeen  *[]string
}

func (s reconcileInspectorStub) OwnerKind() OwnerKind {
	if s.owner == "" {
		return OwnerCapture
	}
	return s.owner
}

func (s reconcileInspectorStub) InspectArtifact(context.Context, OwnedArtifact) (OwnedStorageArtifactEvidence, error) {
	return s.artifact, nil
}

func (s reconcileInspectorStub) InspectReservation(context.Context, CapacityReservationSummary) (OwnedStorageReservationEvidence, error) {
	return s.reservation, nil
}

func (s reconcileInspectorStub) Discover(_ context.Context, request OwnedStorageDiscoveryRequest) (OwnedStorageDiscovery, error) {
	if s.cursorSeen != nil {
		*s.cursorSeen = append(*s.cursorSeen, request.Cursor)
	}
	return s.discovery, nil
}

func allReconcileInspectors(base reconcileInspectorStub) []OwnedStorageInspector {
	owners := OwnedStorageOwnerKinds()
	result := make([]OwnedStorageInspector, 0, len(owners))
	for _, owner := range owners {
		inspector := base
		inspector.owner = owner
		if owner != OwnerCapture {
			inspector.artifact = OwnedStorageArtifactEvidence{}
		}
		result = append(result, inspector)
	}
	return result
}

func reconcileToComplete(t *testing.T, reconciler *OwnedStorageReconciler, request OwnedStorageReconcileRequest) OwnedStorageReconcileReport {
	t.Helper()
	for range 10 {
		report, err := reconciler.Reconcile(context.Background(), request)
		if err != nil {
			t.Fatal(err)
		}
		if report.Complete {
			return report
		}
		request.Cursor = report.NextCursor
	}
	t.Fatal("reconciliation did not complete")
	return OwnedStorageReconcileReport{}
}

func TestOwnedStorageReconcilerReportsMatchingLedgerAsHealthy(t *testing.T) {
	artifact := testReconcileArtifact()
	inspector := reconcileInspectorStub{
		artifact:  OwnedStorageArtifactEvidence{ArtifactID: artifact.ArtifactID, OwnerKind: artifact.OwnerKind, OwnerID: artifact.OwnerID, ReservationID: artifact.ReservationID, VolumeID: artifact.VolumeID, ManifestHash: artifact.ManifestHash, ObservedBytes: artifact.ObservedBytes, EvidenceBytes: 64, State: OwnedStorageEvidenceAccepted, Complete: true},
		discovery: OwnedStorageDiscovery{Complete: true},
	}
	page := OwnedStorageLedgerPage{Revision: 4, Global: StorageTotals{Revision: 4}, Pressure: StoragePressureState{RepositoryPressure: StoragePressureNone, GlobalPressure: StoragePressureNone}, Artifacts: []OwnedArtifact{artifact}, Complete: true}
	reconciler, err := NewOwnedStorageReconciler(reconcileLedgerStub{page: page}, nil, allReconcileInspectors(inspector), DefaultResourcePolicy(), fixedClock{when: time.Unix(10, 0).UTC()})
	if err != nil {
		t.Fatal(err)
	}
	report := reconcileToComplete(t, reconciler, OwnedStorageReconcileRequest{Mode: ReconcileQueryOnly})
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
	reconciler, err := NewOwnedStorageReconciler(reconcileLedgerStub{page: OwnedStorageLedgerPage{Revision: 5, Global: StorageTotals{Revision: 5}, Complete: true, Artifacts: []OwnedArtifact{artifact}}}, store, allReconcileInspectors(inspector), DefaultResourcePolicy(), fixedClock{when: time.Unix(10, 0).UTC()})
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

func TestOwnedStorageReconcilerPassesOwnerCursorAndKeepsDiscoveryBounded(t *testing.T) {
	seen := []string{}
	inspector := reconcileInspectorStub{discovery: OwnedStorageDiscovery{Complete: false, NextCursor: "owner-page-2"}, cursorSeen: &seen}
	page := OwnedStorageLedgerPage{Revision: 7, Global: StorageTotals{Revision: 7}, Complete: true}
	inspectors := allReconcileInspectors(inspector)
	reconciler, err := NewOwnedStorageReconciler(reconcileLedgerStub{page: page}, nil, inspectors, DefaultResourcePolicy(), nil)
	if err != nil {
		t.Fatal(err)
	}
	first, err := reconciler.Reconcile(context.Background(), OwnedStorageReconcileRequest{Mode: ReconcileQueryOnly, MaxItems: 1})
	if err != nil {
		t.Fatal(err)
	}
	if first.Complete || first.NextCursor != "owner:capture:owner-page-2" {
		t.Fatalf("first report = %#v", first)
	}
	second, err := reconciler.Reconcile(context.Background(), OwnedStorageReconcileRequest{Mode: ReconcileQueryOnly, Cursor: first.NextCursor, MaxItems: 1})
	if err != nil {
		t.Fatal(err)
	}
	if second.NextCursor != first.NextCursor || len(seen) != 2 || seen[0] != "" || seen[1] != "owner-page-2" {
		t.Fatalf("second report/cursors = %#v/%#v", second, seen)
	}
	if (OwnedStorageDiscovery{Items: []OwnedStorageArtifactEvidence{{EvidenceBytes: 1}, {EvidenceBytes: 1}}, Complete: true}).ValidateWithin(1, 2) == nil {
		t.Fatal("oversized owner discovery was accepted")
	}
}

func testReconcileArtifact() OwnedArtifact {
	return OwnedArtifact{ArtifactID: "artifact-1", OwnerKind: OwnerCapture, OwnerID: "capture-1", OperationID: domain.OperationID("operation-1"), ReservationID: "reservation-1", Class: StorageClassCapture, Lifecycle: OwnedArtifactAccepted, LogicalBytes: 32, ObservedBytes: 32, ChargedBytes: 32, VolumeID: "volume-1", ManifestHash: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", AccountingVersion: CurrentStorageAccountingVersion, PolicyVersion: DefaultResourcePolicy().Version, Complete: true, CreatedAt: time.Unix(1, 0).UTC()}
}
