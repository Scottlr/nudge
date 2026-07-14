package app

import (
	"errors"
	"math"
	"testing"
)

func TestDefaultResourcePolicyAndRelationships(t *testing.T) {
	t.Parallel()

	policy := DefaultResourcePolicy()
	if err := policy.Validate(); err != nil {
		t.Fatalf("default policy invalid: %v", err)
	}
	if policy.Version != CurrentResourcePolicyVersion || policy.TreePage.Default != 200 || policy.TreePage.Hard != 1000 || policy.HistoryPage.Default != 100 || policy.HistoryPage.Hard != 200 {
		t.Fatalf("unexpected page policy: %+v", policy)
	}
	if policy.Provider.MaxFrameBytes != 16*MiB || policy.Process.MaxOutputBytes != 256*MiB || policy.HistoryPageEncodedBytes != 4*MiB || policy.Symlink.ReferentFollowHops != 0 || policy.Symlink.NULAllowed {
		t.Fatalf("unexpected process/provider/symlink policy: %+v", policy)
	}
	if policy.Storage.RepositorySoftBytes != 32*GiB || policy.Storage.RepositoryHardBytes != 64*GiB || policy.Storage.GlobalSoftBytes != 128*GiB || policy.Storage.GlobalHardBytes != 256*GiB {
		t.Fatalf("unexpected storage policy: %+v", policy.Storage)
	}
}

func TestCheckedLimitArithmetic(t *testing.T) {
	t.Parallel()

	if got, err := ByteSize(4).Add(6); err != nil || got != 10 {
		t.Fatalf("ByteSize.Add() = %v, %v", got, err)
	}
	if got, err := ByteSize(4).Mul(Count(3)); err != nil || got != 12 {
		t.Fatalf("ByteSize.Mul() = %v, %v", got, err)
	}
	if _, err := ByteSize(math.MaxUint64).Add(1); !errors.Is(err, ErrLimitArithmeticOverflow) {
		t.Fatalf("addition overflow error = %v", err)
	}
	if _, err := ByteSize(math.MaxUint64).Mul(Count(2)); !errors.Is(err, ErrLimitArithmeticOverflow) {
		t.Fatalf("multiplication overflow error = %v", err)
	}
	if _, err := Count(math.MaxUint64).Add(1); !errors.Is(err, ErrLimitArithmeticOverflow) {
		t.Fatalf("count overflow error = %v", err)
	}
}

func TestResourcePolicyRejectsBoundaryViolations(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		mutate func(*ResourcePolicy)
	}{
		{name: "page default above hard", mutate: func(p *ResourcePolicy) { p.TreePage.Default = p.TreePage.Hard + 1 }},
		{name: "symlink referent follow", mutate: func(p *ResourcePolicy) { p.Symlink.ReferentFollowHops = 1 }},
		{name: "network enabled", mutate: func(p *ResourcePolicy) { p.Provider.NetworkAllowed = true }},
		{name: "storage soft above hard", mutate: func(p *ResourcePolicy) { p.Storage.RepositorySoftBytes = p.Storage.RepositoryHardBytes + 1 }},
		{name: "zero limit", mutate: func(p *ResourcePolicy) { p.Provider.MaxFrameBytes = 0 }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			policy := DefaultResourcePolicy()
			test.mutate(&policy)
			if err := policy.Validate(); !errors.Is(err, ErrInvalidResourcePolicy) {
				t.Fatalf("Validate() error = %v", err)
			}
		})
	}
}

func TestPolicyTuningIsLowerOnlyOrReserveHigherOnly(t *testing.T) {
	t.Parallel()

	policy := DefaultResourcePolicy()
	treeDefault := PageSize(100)
	reserve := ByteSize(3 * GiB)
	updated, err := policy.WithTuning(PolicyTuning{TreePageDefault: &treeDefault, MinimumFreeBytes: &reserve})
	if err != nil {
		t.Fatalf("WithTuning() error = %v", err)
	}
	if updated.TreePage.Default != treeDefault || updated.Storage.MinimumFreeBytes != reserve || updated.Version != policy.Version {
		t.Fatalf("tuned policy = %+v", updated)
	}
	raisePage := PageSize(201)
	if _, err := policy.WithTuning(PolicyTuning{TreePageDefault: &raisePage}); !errors.Is(err, ErrInvalidResourcePolicy) {
		t.Fatalf("raised page error = %v", err)
	}
	lowerReserve := ByteSize(1 * GiB)
	if _, err := policy.WithTuning(PolicyTuning{MinimumFreeBytes: &lowerReserve}); !errors.Is(err, ErrInvalidResourcePolicy) {
		t.Fatalf("lowered reserve error = %v", err)
	}
}

func TestLimitEvidenceAndStoragePressure(t *testing.T) {
	t.Parallel()

	policy := DefaultResourcePolicy()
	accepted, err := policy.Admit("repo.path", 10, 10, LimitReviewOnly, true)
	if err != nil || accepted.Outcome != LimitAccepted || !accepted.Complete || accepted.PolicyVersion != CurrentResourcePolicyVersion {
		t.Fatalf("accepted evidence = %+v, error = %v", accepted, err)
	}
	hit, err := policy.Admit("repo.path", 11, 10, LimitReviewOnly, true)
	if !errors.Is(err, ErrLimitExceeded) || hit.Outcome != LimitReviewOnly || hit.Complete || !hit.PriorStateUsable {
		t.Fatalf("limit evidence = %+v, error = %v", hit, err)
	}
	if pressure, err := ClassifyStoragePressure(32*GiB, 32*GiB, 64*GiB); err != nil || pressure != StoragePressureSoft {
		t.Fatalf("soft pressure = %v, %v", pressure, err)
	}
	if pressure, err := ClassifyStoragePressure(64*GiB, 32*GiB, 64*GiB); err != nil || pressure != StoragePressureHard {
		t.Fatalf("hard pressure = %v, %v", pressure, err)
	}
}
