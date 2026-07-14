package app

import (
	"errors"
	"testing"
	"time"

	"github.com/Scottlr/nudge/internal/domain"
)

func TestValidateCapacityPlanUsesPolicyReserveAndMultiVolumeMapping(t *testing.T) {
	policy := reducedCapacityPolicy()
	operation, err := domain.NewOperationID("operation")
	if err != nil {
		t.Fatal(err)
	}
	plan := CapacityPlan{
		OperationID:   operation,
		PolicyVersion: policy.Version,
		Artifacts: []ArtifactEstimate{{
			Class:       ArtifactCapture,
			Entries:     2,
			Bytes:       10,
			LargestItem: 4,
		}},
		VolumePeaks: []VolumePeak{
			{ID: "volume-a", Inputs: 10, Finals: 10, Reserve: 5},
			{ID: "volume-b", Temporaries: 4, DatabaseWAL: 3, Reserve: 5},
		},
	}
	evidence := []VolumeEvidence{
		{ID: "volume-a", Free: 30, Mode: VolumeCapacityMonitored, Stable: true},
		{ID: "volume-b", Free: 20, Mode: VolumeCapacityHard, Stable: true},
	}
	if err := ValidateCapacityPlan(policy, plan, evidence); err != nil {
		t.Fatalf("ValidateCapacityPlan() error = %v", err)
	}

	evidence[1].Free = 11
	if err := ValidateCapacityPlan(policy, plan, evidence); !errors.Is(err, ErrCapacityPressure) {
		t.Fatalf("ValidateCapacityPlan() error = %v, want capacity pressure", err)
	}
}

func TestValidateCapacityPlanRejectsRetentionBudgetOverflow(t *testing.T) {
	policy := reducedCapacityPolicy()
	operation, _ := domain.NewOperationID("operation")
	plan := CapacityPlan{
		OperationID:   operation,
		PolicyVersion: policy.Version,
		Budget:        CapacityBudget{RepositoryUsed: policy.Storage.RepositoryHardBytes - 1},
		RetainedDelta: 2,
		VolumePeaks:   []VolumePeak{{ID: "volume", Reserve: 5, RetainedDelta: 2}},
	}
	if err := ValidateCapacityPlan(policy, plan, nil); !errors.Is(err, ErrLimitExceeded) {
		t.Fatalf("ValidateCapacityPlan() error = %v, want ErrLimitExceeded", err)
	}
}

func TestRecheckBoundsRequireByteAndTimeLimits(t *testing.T) {
	if err := (RecheckBounds{MaxBytes: 1, MaxInterval: time.Second}).Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
	if err := (RecheckBounds{MaxBytes: 0, MaxInterval: time.Second}).Validate(); !errors.Is(err, ErrInvalidCapacityPlan) {
		t.Fatalf("Validate() error = %v, want invalid plan", err)
	}
}

func reducedCapacityPolicy() ResourcePolicy {
	policy := DefaultResourcePolicy()
	policy.Storage.MinimumFreeBytes = 5
	policy.Storage.RecoveryFileBytes = 2
	policy.Storage.RepositorySoftBytes = 100
	policy.Storage.RepositoryHardBytes = 200
	policy.Storage.GlobalSoftBytes = 100
	policy.Storage.GlobalHardBytes = 200
	return policy
}
