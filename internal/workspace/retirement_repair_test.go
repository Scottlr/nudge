package workspace

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/Scottlr/nudge/internal/app"
	"github.com/Scottlr/nudge/internal/domain"
	"github.com/Scottlr/nudge/internal/domain/review"
)

func TestRetirementRepairOwnerPlansAndFinishesExactJournal(t *testing.T) {
	now := time.Date(2026, time.July, 16, 12, 0, 0, 0, time.UTC)
	candidate := app.WorkspaceRetentionCandidate{
		RepositoryID: "repository-1", WorktreeID: "worktree-1", SessionID: "session-1", WorkspaceID: "workspace-1", ThreadID: "thread-1", ProposalID: "proposal-1", ApplyOperationID: "apply-1",
		ThreadResolution: review.ResolutionResolved, ProposalState: review.ProposalApplied, WorkspaceState: review.WorkspaceReady, BasisTime: now.Add(-app.DefaultWorkspaceRetentionMinimumAge), EvaluatedRevision: 7,
		ProposalTerminal: true, ApplyTerminal: true, LifecycleTerminal: true, JournalCertain: true, OwnershipCertain: true, OwnershipDigest: strings.Repeat("a", 64), MarkerNonce: "nonce-1", HistoryCertain: true,
	}
	decision, err := app.EvaluateWorkspaceRetention(app.DefaultWorkspaceRetentionPolicy(), candidate, now)
	if err != nil {
		t.Fatal(err)
	}
	retirement := app.WorkspaceRetirement{Version: 1, OperationID: domain.OperationID("operation-1"), Candidate: candidate, Decision: decision, Phase: app.WorkspaceRetirementRepairRequired, Reason: "interrupted", CreatedAt: now.Add(-time.Hour), UpdatedAt: now}
	store := &retirementRepairTestStore{retirement: retirement}
	executor := &retirementRepairTestExecutor{proof: app.WorkspaceRetirementProof{WorkspaceID: candidate.WorkspaceID, OwnershipDigest: candidate.OwnershipDigest, MarkerNonce: candidate.MarkerNonce}}
	owner, err := NewRetirementRepairOwner(store, executor)
	if err != nil {
		t.Fatal(err)
	}
	report := app.HealthReport{HealthRevision: strings.Repeat("b", 64)}
	plans, err := owner.Plans(context.Background(), report)
	if err != nil || len(plans) != 1 {
		t.Fatalf("plans=%#v err=%v", plans, err)
	}
	if _, err := owner.Revalidate(context.Background(), plans[0]); err != nil {
		t.Fatalf("revalidate: %v", err)
	}
	effect, err := owner.Execute(context.Background(), app.RepairOperation{IdempotencyKey: "repair-key"}, plans[0])
	if err != nil || effect.IdempotencyKey != "repair-key" {
		t.Fatalf("effect=%#v err=%v", effect, err)
	}
	verification, err := owner.Verify(context.Background(), app.RepairOperation{}, plans[0])
	if err != nil || !verification.AlreadyRepaired || store.retirement.Phase != app.WorkspaceRetirementRemoved {
		t.Fatalf("verification=%#v err=%v retirement=%#v", verification, err, store.retirement)
	}
	registry := app.NewRepairRegistry()
	if err := RegisterRetirementRepairOwner(registry, owner); err != nil {
		t.Fatalf("register owner: %v", err)
	}
	if _, err := registry.Handler(RetirementRepairHandlerKind, RetirementRepairHandlerVersion); err != nil {
		t.Fatalf("registered handler: %v", err)
	}
}

type retirementRepairTestStore struct {
	retirement app.WorkspaceRetirement
}

func (s *retirementRepairTestStore) ListWorkspaceRetentionCandidates(context.Context, app.WorkspaceRetentionPage) (app.WorkspaceRetentionPageResult, error) {
	return app.WorkspaceRetentionPageResult{}, nil
}

func (s *retirementRepairTestStore) LoadWorkspaceRetentionCandidate(context.Context, domain.WorkspaceID) (app.WorkspaceRetentionCandidate, error) {
	return s.retirement.Candidate, nil
}

func (s *retirementRepairTestStore) LoadWorkspaceRetirement(context.Context, domain.WorkspaceID, domain.OperationID) (app.WorkspaceRetirement, error) {
	return s.retirement, nil
}

func (s *retirementRepairTestStore) SaveWorkspaceRetirement(_ context.Context, retirement app.WorkspaceRetirement) error {
	s.retirement = retirement
	return nil
}

func (s *retirementRepairTestStore) ListWorkspaceRetirements(_ context.Context, phase app.WorkspaceRetirementPhase, _ uint32) ([]app.WorkspaceRetirement, error) {
	if s.retirement.Phase != phase {
		return nil, nil
	}
	return []app.WorkspaceRetirement{s.retirement}, nil
}

type retirementRepairTestExecutor struct {
	proof   app.WorkspaceRetirementProof
	removed bool
}

func (e *retirementRepairTestExecutor) Verify(context.Context, app.WorkspaceRetirement) (app.WorkspaceRetirementProof, error) {
	proof := e.proof
	proof.AlreadyRemoved = e.removed
	return proof, nil
}

func (e *retirementRepairTestExecutor) Remove(context.Context, app.WorkspaceRetirement) (app.WorkspaceRetirementProof, error) {
	e.removed = true
	return e.proof, nil
}
