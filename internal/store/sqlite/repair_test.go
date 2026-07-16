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

func TestRepairPlanAndOperationMetadataRoundTrip(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, testDatabasePath(t))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	now := time.Date(2026, time.July, 16, 12, 0, 0, 0, time.UTC)
	plan := app.RepairPlan{
		ID: "plan-sqlite", HealthCode: app.HealthDatabaseCorrupt, HealthRevision: strings.Repeat("a", 64), PolicyVersion: 1,
		Summary: "Repair one registered database state", Effect: "Complete one registered state transition",
		OwnedResourceRefs: []string{"database-1"}, PreconditionsHash: strings.Repeat("b", 64), ConfirmationText: "repair plan-sqlite",
		HandlerKind: "owner", HandlerVersion: "v1", CreatedAt: now, ExpiresAt: now.Add(time.Hour),
	}
	if err := store.SaveRepairPlan(ctx, plan); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveRepairPlan(ctx, plan); err != nil {
		t.Fatalf("idempotent plan save: %v", err)
	}
	changed := plan
	changed.Effect = "different effect"
	if err := store.SaveRepairPlan(ctx, changed); !errors.Is(err, app.ErrRepairPlanConflict) {
		t.Fatalf("changed plan error=%v", err)
	}
	loaded, err := store.LoadRepairPlan(ctx, plan.ID)
	if err != nil || loaded.ID != plan.ID || loaded.OwnedResourceRefs[0] != "database-1" {
		t.Fatalf("loaded plan=%#v err=%v", loaded, err)
	}
	operation := app.RepairOperation{
		Version: app.RepairFrameworkVersion, ID: domain.OperationID("repair-operation"), PlanID: plan.ID, HandlerKind: plan.HandlerKind,
		HandlerVersion: plan.HandlerVersion, HealthRevision: plan.HealthRevision, IdempotencyKey: "repair-idempotency",
		Phase: app.RepairPhasePrepared, Outcome: app.RepairOutcomeNone, PreconditionsHash: plan.PreconditionsHash, CreatedAt: now, UpdatedAt: now,
	}
	if err := store.SaveRepairOperation(ctx, operation); err != nil {
		t.Fatal(err)
	}
	loadedOperation, err := store.LoadRepairOperationByIdempotency(ctx, operation.IdempotencyKey)
	if err != nil || loadedOperation.ID != operation.ID {
		t.Fatalf("loaded operation=%#v err=%v", loadedOperation, err)
	}
	audit := app.RepairAuditEntry{OperationID: operation.ID, PlanID: plan.ID, Phase: app.RepairPhasePrepared, Code: "prepared", At: now}
	if err := store.AppendRepairAudit(ctx, audit); err != nil {
		t.Fatal(err)
	}
}
