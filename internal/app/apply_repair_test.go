package app

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"
)

type applyRepairTargetStoreStub struct{ target ApplyRepairTarget }

func (s applyRepairTargetStoreStub) ListApplyRepairTargets(context.Context) ([]ApplyRepairTarget, error) {
	return []ApplyRepairTarget{s.target}, nil
}

func (s applyRepairTargetStoreStub) LoadApplyRepairTarget(_ context.Context, resourceID string, closure ApplyRepairClosure) (ApplyRepairTarget, error) {
	if resourceID != s.target.ResourceID || closure != s.target.Closure {
		return ApplyRepairTarget{}, errors.New("target not found")
	}
	return s.target, nil
}

type applyRepairAdapterStub struct {
	proof  ApplyRepairProof
	base   int
	result int
}

func (s *applyRepairAdapterStub) Inspect(context.Context, ApplyRepairTarget) (ApplyRepairProof, error) {
	return s.proof, nil
}

func (s *applyRepairAdapterStub) CloseBaseline(_ context.Context, _ ApplyRepairTarget, _ ApplyRepairProof, operation RepairOperation) (RepairEffect, error) {
	s.base++
	return RepairEffect{EffectID: "apply-baseline-effect", IdempotencyKey: operation.IdempotencyKey}, nil
}

func (s *applyRepairAdapterStub) CloseResult(_ context.Context, _ ApplyRepairTarget, _ ApplyRepairProof, operation RepairOperation) (RepairEffect, error) {
	s.result++
	return RepairEffect{EffectID: "apply-result-effect", IdempotencyKey: operation.IdempotencyKey}, nil
}

type applyRepairLockStub struct{}

func (applyRepairLockStub) Acquire(context.Context, string) (io.Closer, error) {
	return io.NopCloser(strings.NewReader("")), nil
}

func TestApplyRepairOwnerPlansAndClosesOnlyTheVerifiedBaseline(t *testing.T) {
	target := testApplyRepairTarget(ApplyRepairAbandonBaseline, ApplyRepairAllBaseline, false)
	adapter := &applyRepairAdapterStub{proof: testApplyRepairProof(target)}
	owner, err := NewApplyRepairOwner(applyRepairTargetStoreStub{target: target}, adapter, applyRepairLockStub{}, fixedClock{when: time.Unix(10, 0).UTC()})
	if err != nil {
		t.Fatal(err)
	}
	plans, err := owner.Plans(context.Background(), HealthReport{HealthRevision: strings.Repeat("a", 64)})
	if err != nil || len(plans) != 1 {
		t.Fatalf("plans = %#v, err = %v", plans, err)
	}
	if plans[0].ConfirmationText != "abandon manually restored apply journal" {
		t.Fatalf("plan confirmation = %q", plans[0].ConfirmationText)
	}
	if _, err := owner.Revalidate(context.Background(), plans[0]); err != nil {
		t.Fatal(err)
	}
	operation := RepairOperation{IdempotencyKey: "apply-repair-idempotency"}
	effect, err := owner.Execute(context.Background(), operation, plans[0])
	if err != nil || effect.IdempotencyKey != operation.IdempotencyKey || adapter.base != 1 || adapter.result != 0 {
		t.Fatalf("effect/calls = %#v/%d/%d, err = %v", effect, adapter.base, adapter.result, err)
	}
	if _, err := owner.Verify(context.Background(), operation, plans[0]); err != nil {
		t.Fatal(err)
	}
}

func TestApplyRepairOwnerRejectsMixedEvidenceBeforeAnyEffect(t *testing.T) {
	target := testApplyRepairTarget(ApplyRepairAcceptResult, ApplyRepairAllResult, true)
	adapter := &applyRepairAdapterStub{proof: testApplyRepairProof(target)}
	owner, err := NewApplyRepairOwner(applyRepairTargetStoreStub{target: target}, adapter, applyRepairLockStub{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	plans, err := owner.Plans(context.Background(), HealthReport{HealthRevision: strings.Repeat("b", 64)})
	if err != nil {
		t.Fatal(err)
	}
	adapter.proof.Classification = ApplyRepairMixed
	if _, err := owner.Revalidate(context.Background(), plans[0]); !errors.Is(err, ErrRepairPreconditions) {
		t.Fatalf("revalidate error = %v", err)
	}
	if adapter.base != 0 || adapter.result != 0 {
		t.Fatalf("unexpected effects = %d/%d", adapter.base, adapter.result)
	}
}

func testApplyRepairTarget(closure ApplyRepairClosure, classification ApplyRepairClassification, applyOnce bool) ApplyRepairTarget {
	return ApplyRepairTarget{ResourceID: "apply-1", Closure: closure, OperationID: "operation-1", SessionID: "session-1", ProposalID: "proposal-1", WorkspaceID: "workspace-1", RepositoryID: "repository-1", WorktreeID: "worktree-1", JournalRevision: 2, ProposalRevision: 3, DestinationIdentityHash: strings.Repeat("a", 64), BaselineEvidenceHash: strings.Repeat("b", 64), ResultEvidenceHash: strings.Repeat("c", 64), Classification: classification, ApplyOnceAvailable: applyOnce, ExpectedPostcondition: strings.Repeat("d", 64)}
}

func testApplyRepairProof(target ApplyRepairTarget) ApplyRepairProof {
	return ApplyRepairProof{ResourceID: target.ResourceID, OperationID: target.OperationID, JournalRevision: target.JournalRevision, ProposalRevision: target.ProposalRevision, DestinationIdentityHash: target.DestinationIdentityHash, BaselineEvidenceHash: target.BaselineEvidenceHash, ResultEvidenceHash: target.ResultEvidenceHash, Classification: target.Classification, PostconditionHash: target.ExpectedPostcondition}
}
