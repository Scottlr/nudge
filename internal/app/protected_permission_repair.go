package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"time"
)

const (
	ProtectedPermissionPolicyVersion  uint64 = 1
	ProtectedPermissionHandlerKind           = RepairHandlerKind("protected-path.permission")
	ProtectedPermissionHandlerVersion        = "v1"
)

var (
	ErrProtectedPermissionRepair = errors.New("protected permission repair rejected")
	ErrProtectedPermissionTarget = errors.New("invalid protected permission target")
	ErrProtectedPermissionProof  = errors.New("invalid protected permission proof")
)

// ProtectedPermissionRootKind identifies one T005-owned Nudge root.
type ProtectedPermissionRootKind string

const (
	ProtectedConfigRoot    ProtectedPermissionRootKind = "config"
	ProtectedStateRoot     ProtectedPermissionRootKind = "state"
	ProtectedCacheRoot     ProtectedPermissionRootKind = "cache"
	ProtectedLogRoot       ProtectedPermissionRootKind = "log"
	ProtectedWorkspaceRoot ProtectedPermissionRootKind = "workspace"
)

func (k ProtectedPermissionRootKind) Validate() error {
	switch k {
	case ProtectedConfigRoot, ProtectedStateRoot, ProtectedCacheRoot, ProtectedLogRoot, ProtectedWorkspaceRoot:
		return nil
	default:
		return ErrProtectedPermissionTarget
	}
}

// ProtectedPermissionTarget is path-free evidence for one Nudge-owned root.
// The platform adapter retains the native path; plans contain only hashes and
// a stable resource identity.
type ProtectedPermissionTarget struct {
	ResourceID            string
	Kind                  ProtectedPermissionRootKind
	PathHash              string
	NativeIdentityHash    string
	OwnershipMarkerHash   string
	CurrentPermissionHash string
	DesiredPermissionHash string
	PolicyVersion         uint64
}

func (t ProtectedPermissionTarget) Validate() error {
	if !safeRepairToken(t.ResourceID, 128) || t.Kind.Validate() != nil || !validRepairHash(t.PathHash) || !validRepairHash(t.NativeIdentityHash) || !validRepairHash(t.OwnershipMarkerHash) || !validRepairHash(t.CurrentPermissionHash) || !validRepairHash(t.DesiredPermissionHash) || t.PolicyVersion != ProtectedPermissionPolicyVersion {
		return ErrProtectedPermissionTarget
	}
	return nil
}

// ProtectedPermissionProof is the adapter's identity and permission proof
// while the owner lock is held.
type ProtectedPermissionProof struct {
	ResourceID            string
	PathHash              string
	NativeIdentityHash    string
	OwnershipMarkerHash   string
	BeforePermissionHash  string
	AfterPermissionHash   string
	DesiredPermissionHash string
}

func (p ProtectedPermissionProof) Validate() error {
	if !safeRepairToken(p.ResourceID, 128) || !validRepairHash(p.PathHash) || !validRepairHash(p.NativeIdentityHash) || !validRepairHash(p.OwnershipMarkerHash) || !validRepairHash(p.BeforePermissionHash) || !validRepairHash(p.AfterPermissionHash) || !validRepairHash(p.DesiredPermissionHash) {
		return ErrProtectedPermissionProof
	}
	return nil
}

// ProtectedPermissionTargetStore supplies fresh path-free evidence for the
// exact stable resource selected by a repair plan.
type ProtectedPermissionTargetStore interface {
	ListProtectedPermissionTargets(context.Context) ([]ProtectedPermissionTarget, error)
	LoadProtectedPermissionTarget(context.Context, string) (ProtectedPermissionTarget, error)
}

// ProtectedPermissionAdapter owns native path handles and permission effects.
// Calls are made only while the corresponding owner lock is held.
type ProtectedPermissionAdapter interface {
	Inspect(context.Context, ProtectedPermissionTarget) (ProtectedPermissionProof, error)
	Repair(context.Context, ProtectedPermissionTarget, ProtectedPermissionProof) (ProtectedPermissionProof, error)
}

// ProtectedPermissionLockManager provides the stable cross-process owner lock
// for one protected root without exposing lock-file paths to the plan.
type ProtectedPermissionLockManager interface {
	Acquire(context.Context, string) (io.Closer, error)
}

// ProtectedPermissionRepairOwner is the T092 planner and typed T058 handler.
type ProtectedPermissionRepairOwner struct {
	targets ProtectedPermissionTargetStore
	adapter ProtectedPermissionAdapter
	locks   ProtectedPermissionLockManager
	clock   Clock
}

// NewProtectedPermissionRepairOwner validates the owner-specific seams.
func NewProtectedPermissionRepairOwner(targets ProtectedPermissionTargetStore, adapter ProtectedPermissionAdapter, locks ProtectedPermissionLockManager, clock Clock) (*ProtectedPermissionRepairOwner, error) {
	if targets == nil || adapter == nil || locks == nil {
		return nil, ErrProtectedPermissionRepair
	}
	if clock == nil {
		clock = SystemClock{}
	}
	return &ProtectedPermissionRepairOwner{targets: targets, adapter: adapter, locks: locks, clock: clock}, nil
}

// RegisterProtectedPermissionRepairOwner registers the exact health planner
// and handler. Composition decides whether explicit repair is available.
func RegisterProtectedPermissionRepairOwner(registry *RepairRegistry, owner *ProtectedPermissionRepairOwner) error {
	if registry == nil || owner == nil {
		return ErrProtectedPermissionRepair
	}
	if err := registry.RegisterHandler(owner); err != nil {
		return err
	}
	return registry.RegisterPlanner(HealthProtectedRootRejected, owner)
}

func (o *ProtectedPermissionRepairOwner) Kind() RepairHandlerKind {
	return ProtectedPermissionHandlerKind
}

func (o *ProtectedPermissionRepairOwner) Version() string { return ProtectedPermissionHandlerVersion }

// Plans returns one bounded plan per currently weak protected root.
func (o *ProtectedPermissionRepairOwner) Plans(ctx context.Context, report HealthReport) ([]RepairPlan, error) {
	if o == nil || ctx == nil || !validHealthRevision(report.HealthRevision) {
		return nil, ErrProtectedPermissionRepair
	}
	targets, err := o.targets.ListProtectedPermissionTargets(ctx)
	if err != nil {
		return nil, err
	}
	now := o.clock.Now().UTC()
	plans := make([]RepairPlan, 0, len(targets))
	for _, target := range targets {
		if err := target.Validate(); err != nil {
			return nil, err
		}
		if target.CurrentPermissionHash == target.DesiredPermissionHash {
			continue
		}
		preconditions, err := protectedPermissionPreconditionHash(target)
		if err != nil {
			return nil, err
		}
		plan := RepairPlan{
			ID:                RepairPlanID("protected-permission-" + target.ResourceID),
			HealthCode:        HealthProtectedRootRejected,
			HealthRevision:    report.HealthRevision,
			PolicyVersion:     ProtectedPermissionPolicyVersion,
			Summary:           "A Nudge protected root has weak permissions.",
			Effect:            "Tighten one verified Nudge-owned root to the owner-only policy.",
			OwnedResourceRefs: []string{"protected-root:" + target.ResourceID},
			PreconditionsHash: preconditions,
			ConfirmationText:  "repair protected root permissions",
			HandlerKind:       ProtectedPermissionHandlerKind,
			HandlerVersion:    ProtectedPermissionHandlerVersion,
			CreatedAt:         now,
			ExpiresAt:         now.Add(24 * time.Hour),
		}
		if err := plan.Validate(); err != nil {
			return nil, err
		}
		plans = append(plans, plan)
	}
	return plans, nil
}

func (o *ProtectedPermissionRepairOwner) Revalidate(ctx context.Context, plan RepairPlan) (RepairRevalidation, error) {
	target, err := o.loadTarget(ctx, plan)
	if err != nil {
		return RepairRevalidation{}, err
	}
	hash, err := protectedPermissionPreconditionHash(target)
	if err != nil || (hash != plan.PreconditionsHash && target.CurrentPermissionHash != target.DesiredPermissionHash) {
		return RepairRevalidation{}, ErrRepairPreconditions
	}
	lock, err := o.locks.Acquire(ctx, target.ResourceID)
	if err != nil {
		return RepairRevalidation{}, err
	}
	defer lock.Close()
	proof, err := o.adapter.Inspect(ctx, target)
	if err != nil || proof.Validate() != nil || !proofMatchesTarget(proof, target) || proof.BeforePermissionHash != target.CurrentPermissionHash {
		return RepairRevalidation{}, ErrRepairPreconditions
	}
	return RepairRevalidation{PreconditionsHash: plan.PreconditionsHash, LockProof: "protected-permission-lock:" + target.ResourceID, JournalID: "protected-permission:" + target.ResourceID}, nil
}

func (o *ProtectedPermissionRepairOwner) Execute(ctx context.Context, operation RepairOperation, plan RepairPlan) (RepairEffect, error) {
	target, err := o.loadTarget(ctx, plan)
	if err != nil {
		return RepairEffect{}, err
	}
	hash, err := protectedPermissionPreconditionHash(target)
	if err != nil || (hash != plan.PreconditionsHash && target.CurrentPermissionHash != target.DesiredPermissionHash) {
		return RepairEffect{}, ErrRepairPreconditions
	}
	lock, err := o.locks.Acquire(ctx, target.ResourceID)
	if err != nil {
		return RepairEffect{}, err
	}
	defer lock.Close()
	proof, err := o.adapter.Inspect(ctx, target)
	if err != nil || proof.Validate() != nil || !proofMatchesTarget(proof, target) {
		return RepairEffect{}, ErrRepairPreconditions
	}
	if proof.BeforePermissionHash != target.DesiredPermissionHash {
		proof, err = o.adapter.Repair(ctx, target, proof)
		if err != nil {
			return RepairEffect{}, err
		}
	}
	if err := proof.Validate(); err != nil || proof.AfterPermissionHash != target.DesiredPermissionHash {
		return RepairEffect{}, ErrProtectedPermissionProof
	}
	return RepairEffect{EffectID: "protected-permission-effect:" + target.ResourceID, IdempotencyKey: operation.IdempotencyKey}, nil
}

func (o *ProtectedPermissionRepairOwner) Verify(ctx context.Context, _ RepairOperation, plan RepairPlan) (RepairVerification, error) {
	target, err := o.loadTarget(ctx, plan)
	if err != nil {
		return RepairVerification{}, err
	}
	lock, err := o.locks.Acquire(ctx, target.ResourceID)
	if err != nil {
		return RepairVerification{}, err
	}
	defer lock.Close()
	proof, err := o.adapter.Inspect(ctx, target)
	if err != nil || proof.Validate() != nil || !proofMatchesTarget(proof, target) || proof.BeforePermissionHash != target.DesiredPermissionHash {
		return RepairVerification{}, ErrProtectedPermissionProof
	}
	value, err := json.Marshal(struct {
		ResourceID string
		PathHash   string
		Identity   string
		Permission string
	}{target.ResourceID, target.PathHash, target.NativeIdentityHash, proof.BeforePermissionHash})
	if err != nil {
		return RepairVerification{}, err
	}
	digest := sha256.Sum256(value)
	return RepairVerification{PostconditionHash: hex.EncodeToString(digest[:])}, nil
}

func (o *ProtectedPermissionRepairOwner) loadTarget(ctx context.Context, plan RepairPlan) (ProtectedPermissionTarget, error) {
	if o == nil || ctx == nil || plan.Validate() != nil || plan.HandlerKind != ProtectedPermissionHandlerKind || plan.HandlerVersion != ProtectedPermissionHandlerVersion || len(plan.OwnedResourceRefs) != 1 {
		return ProtectedPermissionTarget{}, ErrProtectedPermissionRepair
	}
	const prefix = "protected-root:"
	if !strings.HasPrefix(plan.OwnedResourceRefs[0], prefix) {
		return ProtectedPermissionTarget{}, ErrProtectedPermissionRepair
	}
	resourceID := strings.TrimPrefix(plan.OwnedResourceRefs[0], prefix)
	if !safeRepairToken(resourceID, 128) {
		return ProtectedPermissionTarget{}, ErrProtectedPermissionRepair
	}
	target, err := o.targets.LoadProtectedPermissionTarget(ctx, resourceID)
	if err != nil {
		return ProtectedPermissionTarget{}, err
	}
	if err := target.Validate(); err != nil || target.ResourceID != resourceID {
		return ProtectedPermissionTarget{}, ErrProtectedPermissionTarget
	}
	return target, nil
}

func proofMatchesTarget(proof ProtectedPermissionProof, target ProtectedPermissionTarget) bool {
	return proof.ResourceID == target.ResourceID && proof.PathHash == target.PathHash && proof.NativeIdentityHash == target.NativeIdentityHash && proof.OwnershipMarkerHash == target.OwnershipMarkerHash && proof.DesiredPermissionHash == target.DesiredPermissionHash
}

func protectedPermissionPreconditionHash(target ProtectedPermissionTarget) (string, error) {
	value, err := json.Marshal(target)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(value)
	return hex.EncodeToString(digest[:]), nil
}

func safeRepairToken(value string, limit int) bool {
	return value != "" && len(value) <= limit && strings.TrimSpace(value) == value && !strings.ContainsAny(value, "/\\\r\n\x00") && stableText(value)
}
