package app

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"
)

type proposalWorkspaceRepairTargetStoreStub struct {
	target ProposalWorkspaceRepairTarget
}

func (s proposalWorkspaceRepairTargetStoreStub) ListProposalWorkspaceRepairTargets(context.Context) ([]ProposalWorkspaceRepairTarget, error) {
	return []ProposalWorkspaceRepairTarget{s.target}, nil
}

func (s proposalWorkspaceRepairTargetStoreStub) LoadProposalWorkspaceRepairTarget(_ context.Context, resourceID string, action ProposalWorkspaceRepairAction) (ProposalWorkspaceRepairTarget, error) {
	if resourceID != s.target.ResourceID || action != s.target.Action {
		return ProposalWorkspaceRepairTarget{}, errors.New("target not found")
	}
	return s.target, nil
}

type proposalWorkspaceRepairAdapterStub struct {
	proof      ProposalWorkspaceRepairProof
	reset      int
	quarantine int
	replay     int
}

func (s *proposalWorkspaceRepairAdapterStub) Inspect(context.Context, ProposalWorkspaceRepairTarget) (ProposalWorkspaceRepairProof, error) {
	return s.proof, nil
}

func (s *proposalWorkspaceRepairAdapterStub) ResetResult(_ context.Context, _ ProposalWorkspaceRepairTarget, _ ProposalWorkspaceRepairProof, operation RepairOperation) (RepairEffect, error) {
	s.reset++
	return RepairEffect{EffectID: "workspace-reset-effect", IdempotencyKey: operation.IdempotencyKey}, nil
}

func (s *proposalWorkspaceRepairAdapterStub) Quarantine(_ context.Context, _ ProposalWorkspaceRepairTarget, _ ProposalWorkspaceRepairProof, operation RepairOperation) (RepairEffect, error) {
	s.quarantine++
	return RepairEffect{EffectID: "workspace-quarantine-effect", IdempotencyKey: operation.IdempotencyKey}, nil
}

func (s *proposalWorkspaceRepairAdapterStub) Replay(_ context.Context, _ ProposalWorkspaceRepairTarget, _ ProposalWorkspaceRepairProof, operation RepairOperation) (RepairEffect, error) {
	s.replay++
	return RepairEffect{EffectID: "workspace-replay-effect", IdempotencyKey: operation.IdempotencyKey}, nil
}

type proposalWorkspaceRepairLockStub struct{}

func (proposalWorkspaceRepairLockStub) Acquire(context.Context, string) (io.Closer, error) {
	return io.NopCloser(strings.NewReader("")), nil
}

func TestProposalWorkspaceRepairOwnerPlansAndExecutesOnlyResetEffect(t *testing.T) {
	target := testProposalWorkspaceRepairTarget()
	adapter := &proposalWorkspaceRepairAdapterStub{proof: testProposalWorkspaceRepairProof(target)}
	owner, err := NewProposalWorkspaceRepairOwner(proposalWorkspaceRepairTargetStoreStub{target: target}, adapter, proposalWorkspaceRepairLockStub{}, fixedClock{when: time.Unix(10, 0).UTC()})
	if err != nil {
		t.Fatal(err)
	}
	report := HealthReport{HealthRevision: strings.Repeat("a", 64), Results: []HealthResult{{Code: HealthWorkspaceRepairRequired}}}
	plans, err := owner.Plans(context.Background(), report)
	if err != nil || len(plans) != 1 {
		t.Fatalf("plans = %#v, err = %v", plans, err)
	}
	if plans[0].HandlerKind != ProposalWorkspaceRepairHandlerKind || plans[0].HealthRevision != report.HealthRevision {
		t.Fatalf("plan = %#v", plans[0])
	}
	if _, err := owner.Revalidate(context.Background(), plans[0]); err != nil {
		t.Fatal(err)
	}
	operation := RepairOperation{IdempotencyKey: "repair-idempotency"}
	effect, err := owner.Execute(context.Background(), operation, plans[0])
	if err != nil {
		t.Fatal(err)
	}
	if effect.IdempotencyKey != operation.IdempotencyKey || adapter.reset != 1 || adapter.quarantine != 0 || adapter.replay != 0 {
		t.Fatalf("effect/calls = %#v/%d/%d/%d", effect, adapter.reset, adapter.quarantine, adapter.replay)
	}
	verification, err := owner.Verify(context.Background(), operation, plans[0])
	if err != nil || verification.PostconditionHash == "" {
		t.Fatalf("verification = %#v, err = %v", verification, err)
	}
}

func TestProposalWorkspaceRepairOwnerRejectsChangedLifecycleEvidence(t *testing.T) {
	target := testProposalWorkspaceRepairTarget()
	adapter := &proposalWorkspaceRepairAdapterStub{proof: testProposalWorkspaceRepairProof(target)}
	owner, err := NewProposalWorkspaceRepairOwner(proposalWorkspaceRepairTargetStoreStub{target: target}, adapter, proposalWorkspaceRepairLockStub{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	plans, err := owner.Plans(context.Background(), HealthReport{HealthRevision: strings.Repeat("b", 64)})
	if err != nil {
		t.Fatal(err)
	}
	adapter.proof.LifecycleRevision++
	if _, err := owner.Revalidate(context.Background(), plans[0]); !errors.Is(err, ErrRepairPreconditions) {
		t.Fatalf("revalidate error = %v", err)
	}
}

func testProposalWorkspaceRepairTarget() ProposalWorkspaceRepairTarget {
	hash := strings.Repeat("a", 64)
	return ProposalWorkspaceRepairTarget{
		ResourceID: "workspace-1", Action: ProposalWorkspaceResultReset, RepositoryID: "repo-1", SessionID: "session-1", ThreadID: "thread-1", ProposalID: "proposal-1", WorkspaceID: "workspace-1", TargetGeneration: 2, MarkerNonce: "nonce-1", NativeIdentityHash: hash, OwnerLeaseHash: strings.Repeat("b", 64), LifecyclePhase: WorkspaceResultResetting, LifecycleRevision: 3, WorkspaceRevision: 4, ProposalRevision: 5, BaselineIdentityHash: strings.Repeat("c", 64), BaselineManifestHash: strings.Repeat("d", 64), ResultIdentityHash: strings.Repeat("e", 64), ResultManifestHash: strings.Repeat("f", 64), CapacityDisposition: "reserved", ResetEligible: true, ExpectedPostcondition: strings.Repeat("1", 64),
	}
}

func testProposalWorkspaceRepairProof(target ProposalWorkspaceRepairTarget) ProposalWorkspaceRepairProof {
	return ProposalWorkspaceRepairProof{ResourceID: target.ResourceID, MarkerNonce: target.MarkerNonce, NativeIdentityHash: target.NativeIdentityHash, OwnerLeaseHash: target.OwnerLeaseHash, LifecyclePhase: target.LifecyclePhase, LifecycleRevision: target.LifecycleRevision, WorkspaceRevision: target.WorkspaceRevision, ProposalRevision: target.ProposalRevision, BaselineManifestHash: target.BaselineManifestHash, ResultManifestHash: target.ResultManifestHash, PostconditionHash: target.ExpectedPostcondition}
}
