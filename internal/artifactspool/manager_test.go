package artifactspool

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/Scottlr/nudge/internal/app"
	"github.com/Scottlr/nudge/internal/capacitystore"
	"github.com/Scottlr/nudge/internal/domain"
	"github.com/Scottlr/nudge/internal/paths"
)

func TestSpoolVerifiesAndPublishesFileWithoutReplacement(t *testing.T) {
	manager, reservationManager, reservation, plan, policy := testSpoolInputs(t)
	defer func() { _ = reservationManager.Release(context.Background(), reservation, plan, policy) }()
	handle, err := manager.Create(context.Background(), app.SpoolSpec{OperationID: plan.OperationID, OwnerKind: app.OwnerCapture, Reservation: reservation, Limits: testSpoolLimits()})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	stream, err := handle.WriteFrom(context.Background(), "data", bytes.NewReader([]byte("hello")))
	if err != nil {
		t.Fatalf("WriteFrom() error = %v", err)
	}
	if stream.Bytes != 5 || len(stream.SHA256) != 64 {
		t.Fatalf("stream identity = %+v", stream)
	}
	identity, err := handle.CloseAndVerify(context.Background())
	if err != nil {
		t.Fatalf("CloseAndVerify() error = %v", err)
	}
	if identity.Bytes != 5 || identity.Entries != 1 || !identity.Complete {
		t.Fatalf("identity = %+v", identity)
	}
	target := app.PublishTarget{OwnerKind: app.OwnerCapture, RelativePath: "final.bin", SourceRelativePath: "data"}
	published, err := handle.Publish(context.Background(), identity, target)
	if err != nil {
		t.Fatalf("Publish() error = %v", err)
	}
	if published.Target != target {
		t.Fatalf("published target = %+v, want %+v", published.Target, target)
	}
	destination := filepath.Join(manager.publishRoot, digestID(string(app.OwnerCapture)), "final.bin")
	content, err := os.ReadFile(destination)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	if string(content) != "hello" {
		t.Fatalf("published content = %q", content)
	}
	if _, err := os.Stat(manager.spoolPath(app.OwnerCapture, string(plan.OperationID), handle.Descriptor().RootNonce)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("spool residue error = %v, want absent root", err)
	}
}

func TestSpoolLimitAndCancellationCannotBeVerified(t *testing.T) {
	manager, reservationManager, reservation, plan, policy := testSpoolInputs(t)
	defer func() { _ = reservationManager.Release(context.Background(), reservation, plan, policy) }()
	handle, err := manager.Create(context.Background(), app.SpoolSpec{OperationID: plan.OperationID, OwnerKind: app.OwnerCapture, Reservation: reservation, Limits: testSpoolLimits()})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := handle.WriteFrom(context.Background(), "too-large", bytes.NewReader([]byte("123456789"))); !errors.Is(err, app.ErrSpoolLimitExceeded) {
		t.Fatalf("WriteFrom() error = %v, want limit exceeded", err)
	}
	if _, err := handle.CloseAndVerify(context.Background()); !errors.Is(err, app.ErrSpoolNotReady) {
		t.Fatalf("CloseAndVerify() error = %v, want not ready", err)
	}

	second, err := manager.Create(context.Background(), app.SpoolSpec{OperationID: plan.OperationID, OwnerKind: app.OwnerCache, Reservation: reservation, Limits: testSpoolLimits()})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := second.WriteFrom(ctx, "cancelled", bytes.NewReader([]byte("data"))); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled WriteFrom() error = %v", err)
	}
	if _, err := second.CloseAndVerify(context.Background()); !errors.Is(err, app.ErrSpoolNotReady) {
		t.Fatalf("cancelled CloseAndVerify() error = %v, want not ready", err)
	}
}

func TestPublishDestinationRacePreservesExistingBytes(t *testing.T) {
	manager, reservationManager, reservation, plan, policy := testSpoolInputs(t)
	defer func() { _ = reservationManager.Release(context.Background(), reservation, plan, policy) }()
	handle, err := manager.Create(context.Background(), app.SpoolSpec{OperationID: plan.OperationID, OwnerKind: app.OwnerCapture, Reservation: reservation, Limits: testSpoolLimits()})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := handle.WriteFrom(context.Background(), "data", bytes.NewReader([]byte("owned"))); err != nil {
		t.Fatal(err)
	}
	identity, err := handle.CloseAndVerify(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	target := app.PublishTarget{OwnerKind: app.OwnerCapture, RelativePath: "race.bin", SourceRelativePath: "data"}
	destination := filepath.Join(manager.publishRoot, digestID(string(app.OwnerCapture)), target.RelativePath)
	if err := paths.EnsurePrivateDir(filepath.Dir(destination)); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(destination, []byte("attacker"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := handle.Publish(context.Background(), identity, target); !errors.Is(err, app.ErrSpoolDestinationExists) {
		t.Fatalf("Publish() error = %v, want destination exists", err)
	}
	content, err := os.ReadFile(destination)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "attacker" {
		t.Fatalf("destination content = %q", content)
	}
	if err := handle.Recover(context.Background(), app.SpoolRecoveryProof{OwnerLockReconciled: true, OperationJournalDone: true, NoActiveHandles: true, Action: app.SpoolRecoveryRemove, Expected: identity, Target: target}); !errors.Is(err, app.ErrSpoolResidueAmbiguous) {
		t.Fatalf("Recover(remove) error = %v, want ambiguous residue", err)
	}
	if _, err := os.Stat(manager.spoolPath(app.OwnerCapture, string(plan.OperationID), handle.Descriptor().RootNonce)); err != nil {
		t.Fatalf("owned spool was removed after destination race: %v", err)
	}
}

func TestSpoolPublishesCompleteDirectory(t *testing.T) {
	manager, reservationManager, reservation, plan, policy := testSpoolInputs(t)
	defer func() { _ = reservationManager.Release(context.Background(), reservation, plan, policy) }()
	handle, err := manager.Create(context.Background(), app.SpoolSpec{OperationID: plan.OperationID, OwnerKind: app.OwnerWorkspace, Reservation: reservation, Limits: testSpoolLimits()})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := handle.WriteFrom(context.Background(), filepath.Join("nested", "data"), bytes.NewReader([]byte("dir-data"))); err != nil {
		t.Fatal(err)
	}
	identity, err := handle.CloseAndVerify(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if identity.Entries != 2 {
		t.Fatalf("directory identity entries = %d, want 2", identity.Entries)
	}
	if _, err := handle.Publish(context.Background(), identity, app.PublishTarget{OwnerKind: app.OwnerWorkspace, RelativePath: "artifact"}); err != nil {
		t.Fatalf("Publish() error = %v", err)
	}
	destination := filepath.Join(manager.publishRoot, digestID(string(app.OwnerWorkspace)), "artifact", "nested", "data")
	content, err := os.ReadFile(destination)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	if string(content) != "dir-data" {
		t.Fatalf("published directory content = %q", content)
	}
}

func TestSpoolSymlinkResidueCannotBeVerifiedOrRemoved(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("creating symlinks requires elevated Windows test permissions")
	}
	manager, reservationManager, reservation, plan, policy := testSpoolInputs(t)
	defer func() { _ = reservationManager.Release(context.Background(), reservation, plan, policy) }()
	handle, err := manager.Create(context.Background(), app.SpoolSpec{OperationID: plan.OperationID, OwnerKind: app.OwnerCache, Reservation: reservation, Limits: testSpoolLimits()})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := handle.WriteFrom(context.Background(), "data", bytes.NewReader([]byte("safe"))); err != nil {
		t.Fatal(err)
	}
	descriptor := handle.Descriptor()
	payload := filepath.Join(manager.spoolPath(descriptor.OwnerKind, string(descriptor.OperationID), descriptor.RootNonce), payloadName)
	if err := os.Symlink("data", filepath.Join(payload, "alias")); err != nil {
		t.Fatal(err)
	}
	if _, err := handle.CloseAndVerify(context.Background()); !errors.Is(err, app.ErrSpoolResidueAmbiguous) {
		t.Fatalf("CloseAndVerify() error = %v, want ambiguous residue", err)
	}
	if err := handle.Recover(context.Background(), app.SpoolRecoveryProof{OwnerLockReconciled: true, OperationJournalDone: true, NoActiveHandles: true, Action: app.SpoolRecoveryRemove}); !errors.Is(err, app.ErrSpoolResidueAmbiguous) {
		t.Fatalf("Recover(remove) error = %v, want ambiguous residue", err)
	}
	if _, err := os.Lstat(filepath.Join(payload, "alias")); err != nil {
		t.Fatalf("symlink residue disappeared: %v", err)
	}
}

func TestCorruptMarkerRemainsVisible(t *testing.T) {
	manager, reservationManager, reservation, plan, policy := testSpoolInputs(t)
	defer func() { _ = reservationManager.Release(context.Background(), reservation, plan, policy) }()
	handle, err := manager.Create(context.Background(), app.SpoolSpec{OperationID: plan.OperationID, OwnerKind: app.OwnerCapture, Reservation: reservation, Limits: testSpoolLimits()})
	if err != nil {
		t.Fatal(err)
	}
	descriptor := handle.Descriptor()
	spoolPath := manager.spoolPath(descriptor.OwnerKind, string(descriptor.OperationID), descriptor.RootNonce)
	if err := os.WriteFile(filepath.Join(spoolPath, markerName), []byte(`{"version":99}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Open(context.Background(), descriptor); !errors.Is(err, app.ErrSpoolResidueAmbiguous) {
		t.Fatalf("Open() error = %v, want ambiguous residue", err)
	}
	if _, err := os.Stat(filepath.Join(spoolPath, markerName)); err != nil {
		t.Fatalf("corrupt marker was not preserved: %v", err)
	}
}

func testSpoolInputs(t *testing.T) (*Manager, *capacitystore.Manager, app.CapacityReservation, app.CapacityPlan, app.ResourcePolicy) {
	t.Helper()
	root := filepath.Join(t.TempDir(), "nudge")
	manager, err := NewManager(root)
	if err != nil {
		t.Fatal(err)
	}
	capacityManager, err := capacitystore.NewManager(filepath.Join(t.TempDir(), "capacity"))
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
	plan := app.CapacityPlan{OperationID: operation, RepositoryID: &repository, PolicyVersion: policy.Version, VolumePeaks: []app.VolumePeak{{ID: "volume", Inputs: 1, Reserve: 5}}}
	reservation, err := capacityManager.Reserve(context.Background(), plan, policy, []app.VolumeEvidence{{ID: "volume", Free: 1 << 20, Mode: app.VolumeCapacityMonitored, Stable: true}})
	if err != nil {
		t.Fatal(err)
	}
	return manager, capacityManager, reservation, plan, policy
}

func testSpoolLimits() app.SpoolLimits {
	return app.SpoolLimits{MaxBytes: 8, MaxEntries: 8, MaxPathBytes: 64, MaxManifestBytes: 4 * 1024, BufferBytes: 2, CheckEveryBytes: 2}
}
