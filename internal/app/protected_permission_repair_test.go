package app

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"
)

type protectedPermissionTargetStoreStub struct {
	target ProtectedPermissionTarget
}

func (s *protectedPermissionTargetStoreStub) ListProtectedPermissionTargets(context.Context) ([]ProtectedPermissionTarget, error) {
	return []ProtectedPermissionTarget{s.target}, nil
}

func (s *protectedPermissionTargetStoreStub) LoadProtectedPermissionTarget(_ context.Context, resourceID string) (ProtectedPermissionTarget, error) {
	if resourceID != s.target.ResourceID {
		return ProtectedPermissionTarget{}, ErrProtectedPermissionTarget
	}
	return s.target, nil
}

type protectedPermissionAdapterStub struct {
	repaired int
}

func (s *protectedPermissionAdapterStub) Inspect(_ context.Context, target ProtectedPermissionTarget) (ProtectedPermissionProof, error) {
	current := target.CurrentPermissionHash
	if s.repaired > 0 {
		current = target.DesiredPermissionHash
	}
	return ProtectedPermissionProof{
		ResourceID: target.ResourceID, PathHash: target.PathHash, NativeIdentityHash: target.NativeIdentityHash,
		OwnershipMarkerHash: target.OwnershipMarkerHash, BeforePermissionHash: current,
		AfterPermissionHash: current, DesiredPermissionHash: target.DesiredPermissionHash,
	}, nil
}

func (s *protectedPermissionAdapterStub) Repair(_ context.Context, target ProtectedPermissionTarget, expected ProtectedPermissionProof) (ProtectedPermissionProof, error) {
	s.repaired++
	return ProtectedPermissionProof{
		ResourceID: target.ResourceID, PathHash: target.PathHash, NativeIdentityHash: target.NativeIdentityHash,
		OwnershipMarkerHash: target.OwnershipMarkerHash, BeforePermissionHash: expected.BeforePermissionHash,
		AfterPermissionHash: target.DesiredPermissionHash, DesiredPermissionHash: target.DesiredPermissionHash,
	}, nil
}

type protectedPermissionLockStub struct {
	acquired int
}

func (s *protectedPermissionLockStub) Acquire(context.Context, string) (io.Closer, error) {
	s.acquired++
	return protectedPermissionCloser{}, nil
}

type protectedPermissionCloser struct{}

func (protectedPermissionCloser) Close() error { return nil }

func TestProtectedPermissionRepairOwnerPlansAndVerifies(t *testing.T) {
	target := protectedPermissionTestTarget()
	store := &protectedPermissionTargetStoreStub{target: target}
	adapter := &protectedPermissionAdapterStub{}
	locks := &protectedPermissionLockStub{}
	owner, err := NewProtectedPermissionRepairOwner(store, adapter, locks, fixedClock{when: time.Date(2026, time.July, 16, 12, 0, 0, 0, time.UTC)})
	if err != nil {
		t.Fatal(err)
	}
	report := HealthReport{HealthRevision: strings.Repeat("f", 64), Results: []HealthResult{{Code: HealthProtectedRootRejected, Severity: HealthWarning, Summary: "protected root is weak"}}}
	plans, err := owner.Plans(context.Background(), report)
	if err != nil {
		t.Fatal(err)
	}
	if len(plans) != 1 || plans[0].ID != "protected-permission-state-root" {
		t.Fatalf("plans = %#v", plans)
	}
	if _, err := owner.Revalidate(context.Background(), plans[0]); err != nil {
		t.Fatalf("Revalidate() error = %v", err)
	}
	effect, err := owner.Execute(context.Background(), RepairOperation{IdempotencyKey: "repair-key"}, plans[0])
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if effect.EffectID == "" || adapter.repaired != 1 {
		t.Fatalf("effect = %#v, repaired = %d", effect, adapter.repaired)
	}
	verification, err := owner.Verify(context.Background(), RepairOperation{}, plans[0])
	if err != nil {
		t.Fatalf("Verify() error = %v", err)
	}
	if verification.PostconditionHash == "" || locks.acquired != 3 {
		t.Fatalf("verification = %#v, lock acquisitions = %d", verification, locks.acquired)
	}
}

func TestProtectedPermissionRepairOwnerAcceptsAlreadyDesiredState(t *testing.T) {
	target := protectedPermissionTestTarget()
	store := &protectedPermissionTargetStoreStub{target: target}
	adapter := &protectedPermissionAdapterStub{repaired: 1}
	owner, err := NewProtectedPermissionRepairOwner(store, adapter, &protectedPermissionLockStub{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	report := HealthReport{HealthRevision: strings.Repeat("f", 64), Results: []HealthResult{{Code: HealthProtectedRootRejected, Severity: HealthWarning, Summary: "protected root is weak"}}}
	plans, err := owner.Plans(context.Background(), report)
	if err != nil {
		t.Fatal(err)
	}
	plan := plans[0]
	store.target.CurrentPermissionHash = store.target.DesiredPermissionHash
	if _, err := owner.Revalidate(context.Background(), plan); err != nil {
		t.Fatalf("Revalidate() already desired error = %v", err)
	}
	if _, err := owner.Execute(context.Background(), RepairOperation{IdempotencyKey: "repair-key"}, plan); err != nil {
		t.Fatalf("Execute() already desired error = %v", err)
	}
	if adapter.repaired != 1 {
		t.Fatalf("already desired state was mutated again: repaired = %d", adapter.repaired)
	}
}

func TestProtectedPermissionRepairOwnerRejectsChangedWeakPrecondition(t *testing.T) {
	target := protectedPermissionTestTarget()
	store := &protectedPermissionTargetStoreStub{target: target}
	owner, err := NewProtectedPermissionRepairOwner(store, &protectedPermissionAdapterStub{}, &protectedPermissionLockStub{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	report := HealthReport{HealthRevision: strings.Repeat("f", 64), Results: []HealthResult{{Code: HealthProtectedRootRejected, Severity: HealthWarning, Summary: "protected root is weak"}}}
	plans, err := owner.Plans(context.Background(), report)
	if err != nil {
		t.Fatal(err)
	}
	store.target.CurrentPermissionHash = strings.Repeat("1", 64)
	if _, err := owner.Revalidate(context.Background(), plans[0]); !errors.Is(err, ErrRepairPreconditions) {
		t.Fatalf("Revalidate() error = %v, want precondition error", err)
	}
}

func protectedPermissionTestTarget() ProtectedPermissionTarget {
	return ProtectedPermissionTarget{
		ResourceID: "state-root", Kind: ProtectedStateRoot,
		PathHash: strings.Repeat("a", 64), NativeIdentityHash: strings.Repeat("b", 64),
		OwnershipMarkerHash: strings.Repeat("c", 64), CurrentPermissionHash: strings.Repeat("d", 64),
		DesiredPermissionHash: strings.Repeat("e", 64), PolicyVersion: ProtectedPermissionPolicyVersion,
	}
}
