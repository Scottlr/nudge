package app

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Scottlr/nudge/internal/domain"
)

type repairMemoryStore struct {
	plans      map[RepairPlanID]RepairPlan
	operations map[domain.OperationID]RepairOperation
	byKey      map[string]domain.OperationID
	audits     []RepairAuditEntry
}

func newRepairMemoryStore() *repairMemoryStore {
	return &repairMemoryStore{plans: make(map[RepairPlanID]RepairPlan), operations: make(map[domain.OperationID]RepairOperation), byKey: make(map[string]domain.OperationID)}
}

func (s *repairMemoryStore) SaveRepairPlan(_ context.Context, plan RepairPlan) error {
	if existing, ok := s.plans[plan.ID]; ok && !reflect.DeepEqual(existing, plan) {
		return ErrRepairPlanConflict
	}
	s.plans[plan.ID] = plan
	return nil
}

func (s *repairMemoryStore) LoadRepairPlan(_ context.Context, id RepairPlanID) (RepairPlan, error) {
	plan, ok := s.plans[id]
	if !ok {
		return RepairPlan{}, ErrRepairPlanNotFound
	}
	return plan, nil
}

func (s *repairMemoryStore) SaveRepairOperation(_ context.Context, operation RepairOperation) error {
	if existingID, ok := s.byKey[operation.IdempotencyKey]; ok && existingID != operation.ID {
		return ErrRepairPlanConflict
	}
	s.operations[operation.ID] = operation
	s.byKey[operation.IdempotencyKey] = operation.ID
	return nil
}

func (s *repairMemoryStore) LoadRepairOperation(_ context.Context, id domain.OperationID) (RepairOperation, error) {
	operation, ok := s.operations[id]
	if !ok {
		return RepairOperation{}, ErrRepairOperationNotFound
	}
	return operation, nil
}

func (s *repairMemoryStore) LoadRepairOperationByIdempotency(_ context.Context, key string) (RepairOperation, error) {
	id, ok := s.byKey[key]
	if !ok {
		return RepairOperation{}, ErrRepairOperationNotFound
	}
	return s.operations[id], nil
}

func (s *repairMemoryStore) AppendRepairAudit(_ context.Context, audit RepairAuditEntry) error {
	s.audits = append(s.audits, audit)
	return nil
}

type repairHandler struct {
	revalidate int
	execute    int
	verify     int
}

func (h *repairHandler) Kind() RepairHandlerKind { return "test-owner" }
func (h *repairHandler) Version() string         { return "v1" }
func (h *repairHandler) Revalidate(context.Context, RepairPlan) (RepairRevalidation, error) {
	h.revalidate++
	return RepairRevalidation{PreconditionsHash: strings.Repeat("a", 64), LockProof: "lock", JournalID: "journal"}, nil
}
func (h *repairHandler) Execute(_ context.Context, _ RepairOperation, plan RepairPlan) (RepairEffect, error) {
	h.execute++
	return RepairEffect{EffectID: "effect", IdempotencyKey: "repair-key"}, nil
}
func (h *repairHandler) Verify(context.Context, RepairOperation, RepairPlan) (RepairVerification, error) {
	h.verify++
	return RepairVerification{PostconditionHash: strings.Repeat("b", 64)}, nil
}

func testRepairPlan() RepairPlan {
	now := time.Date(2026, time.July, 16, 12, 0, 0, 0, time.UTC)
	return RepairPlan{
		ID: "plan-1", HealthCode: HealthDatabaseCorrupt, HealthRevision: strings.Repeat("c", 64),
		PolicyVersion: 1, Summary: "Repair one registered database state", Effect: "Complete one registered state transition",
		OwnedResourceRefs: []string{"database-1"}, PreconditionsHash: strings.Repeat("a", 64),
		ConfirmationText: "repair plan-1", HandlerKind: "test-owner", HandlerVersion: "v1",
		CreatedAt: now, ExpiresAt: now.Add(time.Hour),
	}
}

func TestRepairExecutorRunsOneConfirmedIdempotentLifecycle(t *testing.T) {
	store := newRepairMemoryStore()
	plan := testRepairPlan()
	if err := store.SaveRepairPlan(context.Background(), plan); err != nil {
		t.Fatal(err)
	}
	handler := &repairHandler{}
	registry := NewRepairRegistry()
	if err := registry.RegisterHandler(handler); err != nil {
		t.Fatal(err)
	}
	executor, err := NewRepairExecutor(store, registry, fixedClock{when: plan.CreatedAt.Add(time.Minute)}, fixedIDSource{id: "operation-1"})
	if err != nil {
		t.Fatal(err)
	}
	request := ExecuteRepair{PlanID: plan.ID, HealthRevision: plan.HealthRevision, Confirmation: plan.ConfirmationText, IdempotencyKey: "repair-key"}
	first, err := executor.Execute(context.Background(), request)
	if err != nil {
		t.Fatalf("first execution: %v", err)
	}
	if first.Phase != RepairPhaseSucceeded || first.Outcome != RepairOutcomeSucceeded || handler.revalidate != 1 || handler.execute != 1 || handler.verify != 1 {
		t.Fatalf("first operation=%#v calls=%d/%d/%d", first, handler.revalidate, handler.execute, handler.verify)
	}
	second, err := executor.Execute(context.Background(), request)
	if err != nil {
		t.Fatalf("duplicate execution: %v", err)
	}
	if second.ID != first.ID || handler.execute != 1 || len(store.audits) != 6 {
		t.Fatalf("duplicate operation=%#v calls=%d audits=%d", second, handler.execute, len(store.audits))
	}
}

func TestRepairExecutorRejectsStaleAndUnconfirmedRequestsBeforeOwnerEffect(t *testing.T) {
	store := newRepairMemoryStore()
	plan := testRepairPlan()
	if err := store.SaveRepairPlan(context.Background(), plan); err != nil {
		t.Fatal(err)
	}
	handler := &repairHandler{}
	registry := NewRepairRegistry()
	if err := registry.RegisterHandler(handler); err != nil {
		t.Fatal(err)
	}
	executor, err := NewRepairExecutor(store, registry, fixedClock{when: plan.CreatedAt.Add(time.Minute)}, fixedIDSource{id: "operation-2"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = executor.Execute(context.Background(), ExecuteRepair{PlanID: plan.ID, HealthRevision: strings.Repeat("d", 64), Confirmation: plan.ConfirmationText, IdempotencyKey: "stale-key"})
	if !errors.Is(err, ErrRepairHealthRevision) || handler.execute != 0 {
		t.Fatalf("stale execution err=%v execute=%d", err, handler.execute)
	}
	_, err = executor.Execute(context.Background(), ExecuteRepair{PlanID: plan.ID, HealthRevision: plan.HealthRevision, Confirmation: "wrong", IdempotencyKey: "unconfirmed-key"})
	if !errors.Is(err, ErrRepairConfirmation) || handler.execute != 0 {
		t.Fatalf("unconfirmed execution err=%v execute=%d", err, handler.execute)
	}
}

func TestRepairRegistryRejectsDuplicateAndArbitraryPlanIdentity(t *testing.T) {
	registry := NewRepairRegistry()
	handler := &repairHandler{}
	if err := registry.RegisterHandler(handler); err != nil {
		t.Fatal(err)
	}
	if err := registry.RegisterHandler(handler); !errors.Is(err, ErrRepairHandlerConflict) {
		t.Fatalf("duplicate handler error=%v", err)
	}
	if _, err := registry.Handler(handler.Kind(), "v2"); !errors.Is(err, ErrRepairHandlerUnavailable) {
		t.Fatalf("incompatible handler error=%v", err)
	}
	plan := testRepairPlan()
	plan.OwnedResourceRefs = []string{"C:\\user\\repository"}
	if err := plan.Validate(); !errors.Is(err, ErrInvalidRepairPlan) {
		t.Fatalf("arbitrary resource ref error=%v", err)
	}
}
