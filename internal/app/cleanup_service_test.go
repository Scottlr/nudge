package app

import (
	"context"
	"io"
	"testing"
	"time"

	"github.com/Scottlr/nudge/internal/domain"
)

func TestCleanupServiceJournalsCompletion(t *testing.T) {
	ctx := context.Background()
	inventory := CleanupInventory{RepositoryID: "repo-1", RepositoryDisplay: "repo", ObservedRevision: cleanupTestHash('a')}
	store := &cleanupServiceInventory{inventory: inventory}
	journal := &cleanupServiceJournal{}
	service, err := NewCleanupService(CleanupService{
		Inventory: store,
		Journal:   journal,
		Gate:      cleanupServiceGate{},
		Quiescer:  cleanupServiceQuiescer{},
		IDs:       &sequenceIDs{values: []string{"plan-1", "operation-1"}},
		Clock:     fixedClock{when: time.Date(2026, time.July, 16, 12, 0, 0, 0, time.UTC)},
	})
	if err != nil {
		t.Fatalf("new cleanup service: %v", err)
	}
	plan, err := service.PlanRepositoryCleanup(ctx, inventory.RepositoryID)
	if err != nil {
		t.Fatalf("plan cleanup: %v", err)
	}
	operation, err := service.Execute(ctx, CleanupRequest{PlanID: plan.ID, Confirmation: RepairConfirmationYes})
	if err != nil {
		t.Fatalf("execute cleanup: %v", err)
	}
	if operation.Phase != CleanupPhaseComplete || operation.Outcome != CleanupOutcomeSucceeded || !store.deleted {
		t.Fatalf("cleanup operation = %#v, deleted = %v", operation, store.deleted)
	}
}

type cleanupServiceInventory struct {
	inventory CleanupInventory
	deleted   bool
}

func (s *cleanupServiceInventory) LoadCleanupInventory(context.Context, domain.RepositoryID) (CleanupInventory, error) {
	return s.inventory, nil
}

func (s *cleanupServiceInventory) DeleteRepositoryRows(context.Context, domain.RepositoryID, CleanupPlan) error {
	s.deleted = true
	return nil
}

type cleanupServiceJournal struct {
	plan CleanupPlan
	op   CleanupOperation
}

func (s *cleanupServiceJournal) SaveCleanupPlan(_ context.Context, plan CleanupPlan) error {
	s.plan = plan
	return nil
}

func (s *cleanupServiceJournal) LoadCleanupPlan(_ context.Context, id string) (CleanupPlan, error) {
	if s.plan.ID != id {
		return CleanupPlan{}, ErrCleanupNotFound
	}
	return s.plan, nil
}

func (s *cleanupServiceJournal) SaveCleanupOperation(_ context.Context, operation CleanupOperation) error {
	s.op = operation
	return nil
}

func (s *cleanupServiceJournal) LoadCleanupOperation(context.Context, domain.OperationID) (CleanupOperation, error) {
	return CleanupOperation{}, ErrCleanupNotFound
}

func (s *cleanupServiceJournal) LoadCleanupOperationByPlan(context.Context, string) (CleanupOperation, error) {
	if s.op.ID == "" {
		return CleanupOperation{}, ErrCleanupNotFound
	}
	return s.op, nil
}

type cleanupServiceGate struct{}

func (cleanupServiceGate) Acquire(context.Context, domain.RepositoryID) (io.Closer, error) {
	return cleanupServiceCloser{}, nil
}

type cleanupServiceQuiescer struct{}

func (cleanupServiceQuiescer) Acquire(context.Context, domain.RepositoryID) (io.Closer, error) {
	return cleanupServiceCloser{}, nil
}

type cleanupServiceCloser struct{}

func (cleanupServiceCloser) Close() error { return nil }
