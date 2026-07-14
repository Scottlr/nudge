package capacitystore

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/Scottlr/nudge/internal/app"
	"github.com/Scottlr/nudge/internal/domain"
)

func TestManagerExcludesConcurrentReservationsAndReleasesMatchingMarker(t *testing.T) {
	manager, policy, plan, evidence := testManagerInputs(t)
	first, err := manager.Reserve(context.Background(), plan, policy, evidence)
	if err != nil {
		t.Fatalf("first Reserve() error = %v", err)
	}

	secondManager, err := NewManager(manager.root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := secondManager.Reserve(context.Background(), plan, policy, []app.VolumeEvidence{{ID: "volume", Free: 49, Mode: app.VolumeCapacityMonitored, Stable: true}}); !errors.Is(err, app.ErrCapacityPressure) {
		t.Fatalf("second Reserve() error = %v, want capacity pressure", err)
	}
	if _, err := manager.Recheck(context.Background(), first, plan, policy, app.RecheckBounds{MaxBytes: 1, MaxInterval: 1}, evidence); err != nil {
		t.Fatalf("Recheck() error = %v", err)
	}
	if err := manager.Release(context.Background(), first, plan, policy); err != nil {
		t.Fatalf("Release() error = %v", err)
	}
	if _, err := secondManager.Reserve(context.Background(), plan, policy, evidence); err != nil {
		t.Fatalf("Reserve() after release error = %v", err)
	}
}

func TestManagerRequiresExplicitReconciliationForCrashMarker(t *testing.T) {
	manager, policy, plan, evidence := testManagerInputs(t)
	reservation, err := manager.Reserve(context.Background(), plan, policy, evidence)
	if err != nil {
		t.Fatalf("Reserve() error = %v", err)
	}
	if err := manager.Reconcile(context.Background(), reservation, plan, policy, app.ReconciliationProof{}); !errors.Is(err, app.ErrReservationNotReady) {
		t.Fatalf("Reconcile() error = %v, want not ready", err)
	}
	if err := manager.Reconcile(context.Background(), reservation, plan, policy, app.ReconciliationProof{OwnerLockReconciled: true, OperationJournalDone: true}); err != nil {
		t.Fatalf("Reconcile() with proof error = %v", err)
	}
	if _, err := manager.Reserve(context.Background(), plan, policy, evidence); err != nil {
		t.Fatalf("Reserve() after reconciliation error = %v", err)
	}
}

func TestManagerBlocksUnknownMarker(t *testing.T) {
	manager, policy, plan, evidence := testManagerInputs(t)
	name := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa.json"
	if err := os.WriteFile(filepath.Join(manager.markerRoot, name), []byte(`{"version":99}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Reserve(context.Background(), plan, policy, evidence); !errors.Is(err, ErrUnknownMarker) {
		t.Fatalf("Reserve() error = %v, want ErrUnknownMarker", err)
	}
}

func testManagerInputs(t *testing.T) (*Manager, app.ResourcePolicy, app.CapacityPlan, []app.VolumeEvidence) {
	t.Helper()
	manager, err := NewManager(filepath.Join(t.TempDir(), "reservations"))
	if err != nil {
		t.Fatal(err)
	}
	policy := app.DefaultResourcePolicy()
	policy.Storage.MinimumFreeBytes = 5
	policy.Storage.RecoveryFileBytes = 2
	policy.Storage.RepositorySoftBytes = 100
	policy.Storage.RepositoryHardBytes = 200
	policy.Storage.GlobalSoftBytes = 100
	policy.Storage.GlobalHardBytes = 200
	operation, err := domain.NewOperationID("operation")
	if err != nil {
		t.Fatal(err)
	}
	repository, err := domain.NewRepositoryID("repository")
	if err != nil {
		t.Fatal(err)
	}
	plan := app.CapacityPlan{
		OperationID:   operation,
		RepositoryID:  &repository,
		PolicyVersion: policy.Version,
		VolumePeaks:   []app.VolumePeak{{ID: "volume", Inputs: 10, Finals: 10, Reserve: 5}},
	}
	evidence := []app.VolumeEvidence{{ID: "volume", Free: 100, Mode: app.VolumeCapacityMonitored, Stable: true}}
	return manager, policy, plan, evidence
}
