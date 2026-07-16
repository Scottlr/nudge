package logging

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Scottlr/nudge/internal/app"
	"github.com/Scottlr/nudge/internal/domain"
	"github.com/Scottlr/nudge/internal/paths"
)

func TestLoggerWritesOnlyTypedSafeFieldsAndRotatesOwnedFiles(t *testing.T) {
	root := filepath.Join(t.TempDir(), "logs")
	if err := paths.EnsurePrivateDir(root); err != nil {
		t.Fatalf("EnsurePrivateDir() error = %v", err)
	}
	operation, err := app.FieldOperationID(domain.OperationID("operation-1"))
	if err != nil {
		t.Fatalf("FieldOperationID() error = %v", err)
	}
	evidence, err := app.FieldEvidence("redacted-code-hash")
	if err != nil {
		t.Fatalf("FieldEvidence() error = %v", err)
	}
	logger := New(context.Background(), Config{Root: root, ProcessID: "abc123", MaxBytes: 512, MaxFiles: 2, Retention: time.Hour})
	for index := 0; index < 8; index++ {
		logger.Log(context.Background(), app.LogEventOperationFinished, operation, evidence, app.FieldRetryable(index%2 == 0))
	}
	if health := logger.Health(); health.Disabled {
		t.Fatalf("logger disabled during bounded rotation: %#v", health)
	}
	if err := logger.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatalf("ReadDir() error = %v", err)
	}
	logFiles := make([]string, 0, len(entries))
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".jsonl") {
			logFiles = append(logFiles, entry.Name())
		}
	}
	if len(logFiles) == 0 || len(logFiles) > 2 {
		t.Fatalf("owned log files = %#v", logFiles)
	}
	data, err := paths.ReadProtectedFile(root, logFiles[0])
	if err != nil {
		t.Fatalf("ReadProtectedFile() error = %v", err)
	}
	line := strings.SplitN(string(data), "\n", 2)[0]
	var record map[string]json.RawMessage
	if err := json.Unmarshal([]byte(line), &record); err != nil {
		t.Fatalf("log record JSON error = %v", err)
	}
	if _, ok := record["operation_id"]; !ok {
		t.Fatalf("record omitted typed operation ID: %s", line)
	}
	if strings.Contains(string(data), "source excerpt") || strings.ContainsAny(string(data), "\x00\x1b") {
		t.Fatalf("record retained forbidden/sentinel content: %s", data)
	}
}

func TestLoggerFailureIsBoundedAndNonRecursive(t *testing.T) {
	logger := New(context.Background(), Config{Root: "relative", ProcessID: "abc123"})
	if health := logger.Health(); !health.Disabled || health.LastFailure != app.LogFailureOpen || health.FailureCount == 0 {
		t.Fatalf("failed logger health = %#v", health)
	}
	logger.Log(context.Background(), app.LogEventOperationStarted)
	if health := logger.Health(); health.FailureCount != 1 {
		t.Fatalf("logging to disabled sink recursively changed health: %#v", health)
	}
}

func TestLoggerRejectsInvalidFieldsWithoutDisablingSink(t *testing.T) {
	root := filepath.Join(t.TempDir(), "logs")
	logger := New(context.Background(), DefaultConfig(root))
	logger.Log(context.Background(), app.LogEventOperationStarted, app.SafeField{})
	logger.Log(context.Background(), app.LogEventOperationStarted)
	if health := logger.Health(); health.Disabled || health.FailureCount != 1 || health.LastFailure != app.LogFailureRejected {
		t.Fatalf("invalid field changed sink availability unexpectedly: %#v", health)
	}
	if err := logger.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
}

func TestLoggerSettlesThroughOwnedStorageContracts(t *testing.T) {
	root := filepath.Join(t.TempDir(), "logs")
	policy := app.DefaultResourcePolicy()
	plan := app.CapacityPlan{
		OperationID:   domain.OperationID("operation-1"),
		VolumePeaks:   []app.VolumePeak{{ID: "volume-1", Finals: 2048, RetainedDelta: 2048, Reserve: 1}},
		RetainedDelta: 2048,
		PolicyVersion: policy.Version,
	}
	digest, err := app.PlanDigest(plan)
	if err != nil {
		t.Fatalf("PlanDigest() error = %v", err)
	}
	reservation, err := app.NewCapacityReservation("reservation-1", plan.OperationID, "", digest, plan.PolicyVersion)
	if err != nil {
		t.Fatalf("NewCapacityReservation() error = %v", err)
	}
	ledger := &recordingLedger{}
	reservations := &recordingReservations{}
	logger := New(context.Background(), Config{
		Root: root, ProcessID: "abc123", MaxBytes: 1024, MaxFiles: 2,
		Capacity: &CapacityBinding{Ledger: ledger, Reservations: reservations, Reservation: reservation, Plan: plan, Policy: policy, VolumeID: "volume-1"},
	})
	logger.Log(context.Background(), app.LogEventOperationFinished)
	if health := logger.Health(); health.Disabled {
		t.Fatalf("logger disabled with valid capacity binding: %#v", health)
	}
	if err := logger.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	if ledger.records != 1 || len(ledger.settlements) != 1 || reservations.releases != 1 {
		t.Fatalf("capacity lifecycle = records %d, settlements %d, releases %d", ledger.records, len(ledger.settlements), reservations.releases)
	}
	if len(ledger.settlements[0].Artifacts) == 0 {
		t.Fatal("capacity settlement omitted retained log artifacts")
	}
}

func TestRepositoryScopedLogCleanupUsesMarkerIdentity(t *testing.T) {
	root := filepath.Join(t.TempDir(), "logs")
	repositoryID := domain.RepositoryID("repository-1")
	logger := New(context.Background(), Config{Root: root, ProcessID: "abc123", RepositoryID: repositoryID, MaxBytes: 4096, MaxFiles: 2, Retention: time.Hour})
	logger.Log(context.Background(), app.LogEventOperationStarted)
	if err := logger.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	scopeRoot := filepath.Join(root, "repositories", repositoryScopeID(repositoryID))
	if _, err := os.Stat(filepath.Join(scopeRoot, "owners", "abc123.json")); err != nil {
		t.Fatalf("repository marker was not scoped: %v", err)
	}
	owner, err := NewRepositoryCleanupOwner(root, repositoryID)
	if err != nil {
		t.Fatalf("NewRepositoryCleanupOwner() error = %v", err)
	}
	resources, blockers, err := owner.Resources(context.Background(), repositoryID)
	if err != nil || len(blockers) != 0 || len(resources) != 1 {
		t.Fatalf("cleanup enumeration = resources %d blockers %#v error %v", len(resources), blockers, err)
	}
	if err := owner.Remove(context.Background(), resources[0]); err != nil {
		t.Fatalf("Remove() error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(scopeRoot, "owners", "abc123.json")); !os.IsNotExist(err) {
		t.Fatalf("marker after cleanup error = %v", err)
	}
}

func TestDebugLoggerUsesSeparateExpiringSink(t *testing.T) {
	root := filepath.Join(t.TempDir(), "logs")
	current := time.Now().UTC()
	logger := NewDebug(context.Background(), Config{Root: root, ProcessID: "abc123", Now: func() time.Time { return current }}, current.Add(time.Hour))
	logger.Log(context.Background(), app.LogEventOperationStarted)
	current = current.Add(2 * time.Hour)
	logger.Log(context.Background(), app.LogEventOperationStarted)
	if health := logger.Health(); !health.Disabled || health.LastFailure != app.LogFailureExpired {
		t.Fatalf("expired debug sink health = %#v", health)
	}
	if _, err := os.Stat(filepath.Join(root, "debug")); err != nil {
		t.Fatalf("debug sink root was not separate: %v", err)
	}
	if err := logger.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
}

type recordingLedger struct {
	records     int
	settlements []app.ReservationSettlement
	releases    int
}

func (l *recordingLedger) RecordReservation(_ context.Context, record app.CapacityReservationRecord) error {
	if err := record.Validate(); err != nil {
		return err
	}
	l.records++
	return nil
}

func (l *recordingLedger) SettleReservation(_ context.Context, settlement app.ReservationSettlement) error {
	if err := settlement.Validate(); err != nil {
		return err
	}
	l.settlements = append(l.settlements, settlement)
	return nil
}

func (l *recordingLedger) ReleaseReservation(_ context.Context, release app.ReservationRelease) error {
	if err := release.Validate(); err != nil {
		return err
	}
	l.releases++
	return nil
}

func (l *recordingLedger) Snapshot(context.Context, app.StorageLedgerQuery) (app.StorageLedgerSnapshot, error) {
	return app.StorageLedgerSnapshot{}, nil
}

type recordingReservations struct {
	releases int
}

func (r *recordingReservations) Reserve(context.Context, app.CapacityPlan, app.ResourcePolicy, []app.VolumeEvidence) (app.CapacityReservation, error) {
	return app.CapacityReservation{}, nil
}

func (r *recordingReservations) Recheck(context.Context, app.CapacityReservation, app.CapacityPlan, app.ResourcePolicy, app.RecheckBounds, []app.VolumeEvidence) (app.CapacityCheck, error) {
	return app.CapacityCheck{}, nil
}

func (r *recordingReservations) Release(context.Context, app.CapacityReservation, app.CapacityPlan, app.ResourcePolicy) error {
	r.releases++
	return nil
}

func (r *recordingReservations) Reconcile(context.Context, app.CapacityReservation, app.CapacityPlan, app.ResourcePolicy, app.ReconciliationProof) error {
	return nil
}
