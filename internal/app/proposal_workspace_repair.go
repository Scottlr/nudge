package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
)

const (
	ProposalWorkspaceRepairPolicyVersion  uint64 = 1
	ProposalWorkspaceRepairHandlerKind           = RepairHandlerKind("proposal-workspace.lifecycle")
	ProposalWorkspaceRepairHandlerVersion        = "v1"
)

var (
	ErrProposalWorkspaceRepair       = errors.New("proposal workspace repair rejected")
	ErrProposalWorkspaceRepairTarget = errors.New("invalid proposal workspace repair target")
	ErrProposalWorkspaceRepairProof  = errors.New("invalid proposal workspace repair proof")
)

// ProposalWorkspaceRepairAction identifies one exact T035-owned transition.
type ProposalWorkspaceRepairAction string

const (
	ProposalWorkspaceResultReset ProposalWorkspaceRepairAction = "result_reset"
	ProposalWorkspaceQuarantine  ProposalWorkspaceRepairAction = "workspace_quarantine"
	ProposalWorkspaceReplay      ProposalWorkspaceRepairAction = "lifecycle_replay"
)

func (a ProposalWorkspaceRepairAction) Validate() error {
	switch a {
	case ProposalWorkspaceResultReset, ProposalWorkspaceQuarantine, ProposalWorkspaceReplay:
		return nil
	default:
		return ErrProposalWorkspaceRepairTarget
	}
}

// ProposalWorkspaceRepairTarget is path-free evidence for one exact inactive
// workspace lifecycle failure. Native paths and leases remain adapter-owned.
type ProposalWorkspaceRepairTarget struct {
	ResourceID            string
	Action                ProposalWorkspaceRepairAction
	RepositoryID          string
	SessionID             string
	ThreadID              string
	ProposalID            string
	WorkspaceID           string
	TargetGeneration      uint64
	MarkerNonce           string
	NativeIdentityHash    string
	OwnerLeaseHash        string
	LifecyclePhase        ProposalWorkspaceLifecyclePhase
	LifecycleRevision     uint64
	WorkspaceRevision     uint64
	ProposalRevision      uint64
	BaselineIdentityHash  string
	BaselineManifestHash  string
	ResultIdentityHash    string
	ResultManifestHash    string
	CapacityDisposition   string
	ActiveTurn            bool
	ReadyDependency       bool
	ResetEligible         bool
	QuarantineEligible    bool
	ReplayEligible        bool
	SameRootQuarantine    bool
	QuarantineRootHash    string
	ExpectedPostcondition string
}

func (t ProposalWorkspaceRepairTarget) Validate() error {
	if !safeRepairToken(t.ResourceID, 128) || t.Action.Validate() != nil || !safeRepairToken(t.RepositoryID, 128) || !safeRepairToken(t.SessionID, 128) || !safeRepairToken(t.ThreadID, 128) || !safeRepairToken(t.ProposalID, 128) || !safeRepairToken(t.WorkspaceID, 128) || t.TargetGeneration == 0 || !safeRepairToken(t.MarkerNonce, 128) || !validRepairHash(t.NativeIdentityHash) || !validRepairHash(t.OwnerLeaseHash) || t.LifecyclePhase.Validate() != nil || t.LifecycleRevision == 0 || t.WorkspaceRevision == 0 || t.ProposalRevision == 0 || !validRepairHash(t.BaselineIdentityHash) || !validRepairHash(t.BaselineManifestHash) || !validRepairHash(t.ResultIdentityHash) || !validRepairHash(t.ResultManifestHash) || !safeRepairToken(t.CapacityDisposition, 64) || !validRepairHash(t.ExpectedPostcondition) {
		return ErrProposalWorkspaceRepairTarget
	}
	if t.Action == ProposalWorkspaceResultReset && (!t.ResetEligible || t.ActiveTurn || t.ReadyDependency) {
		return ErrProposalWorkspaceRepairTarget
	}
	if t.Action == ProposalWorkspaceQuarantine && (!t.QuarantineEligible || t.ActiveTurn || t.ReadyDependency || !t.SameRootQuarantine || !validRepairHash(t.QuarantineRootHash)) {
		return ErrProposalWorkspaceRepairTarget
	}
	if t.Action == ProposalWorkspaceReplay && !t.ReplayEligible {
		return ErrProposalWorkspaceRepairTarget
	}
	return nil
}

// ProposalWorkspaceRepairProof is fresh adapter evidence captured while the
// workspace owner lock is held.
type ProposalWorkspaceRepairProof struct {
	ResourceID           string
	MarkerNonce          string
	NativeIdentityHash   string
	OwnerLeaseHash       string
	LifecyclePhase       ProposalWorkspaceLifecyclePhase
	LifecycleRevision    uint64
	WorkspaceRevision    uint64
	ProposalRevision     uint64
	BaselineManifestHash string
	ResultManifestHash   string
	ActiveTurn           bool
	ReadyDependency      bool
	SameRootQuarantine   bool
	PostconditionHash    string
}

func (p ProposalWorkspaceRepairProof) Validate() error {
	if !safeRepairToken(p.ResourceID, 128) || !safeRepairToken(p.MarkerNonce, 128) || !validRepairHash(p.NativeIdentityHash) || !validRepairHash(p.OwnerLeaseHash) || p.LifecyclePhase.Validate() != nil || p.LifecycleRevision == 0 || p.WorkspaceRevision == 0 || p.ProposalRevision == 0 || !validRepairHash(p.BaselineManifestHash) || !validRepairHash(p.ResultManifestHash) || !validRepairHash(p.PostconditionHash) {
		return ErrProposalWorkspaceRepairProof
	}
	return nil
}

// ProposalWorkspaceRepairTargetStore supplies fresh path-free lifecycle
// evidence. It never returns workspace paths or file content.
type ProposalWorkspaceRepairTargetStore interface {
	ListProposalWorkspaceRepairTargets(context.Context) ([]ProposalWorkspaceRepairTarget, error)
	LoadProposalWorkspaceRepairTarget(context.Context, string, ProposalWorkspaceRepairAction) (ProposalWorkspaceRepairTarget, error)
}

// ProposalWorkspaceRepairLockManager owns the stable T035 workspace lock.
type ProposalWorkspaceRepairLockManager interface {
	Acquire(context.Context, string) (io.Closer, error)
}

// ProposalWorkspaceRepairAdapter owns T035 containment, baseline/result
// manifests, lifecycle journals, and the exact reset/quarantine/replay effect.
type ProposalWorkspaceRepairAdapter interface {
	Inspect(context.Context, ProposalWorkspaceRepairTarget) (ProposalWorkspaceRepairProof, error)
	ResetResult(context.Context, ProposalWorkspaceRepairTarget, ProposalWorkspaceRepairProof, RepairOperation) (RepairEffect, error)
	Quarantine(context.Context, ProposalWorkspaceRepairTarget, ProposalWorkspaceRepairProof, RepairOperation) (RepairEffect, error)
	Replay(context.Context, ProposalWorkspaceRepairTarget, ProposalWorkspaceRepairProof, RepairOperation) (RepairEffect, error)
}

// ProposalWorkspaceRepairOwner is the T103 planner and typed T058 handler.
type ProposalWorkspaceRepairOwner struct {
	targets ProposalWorkspaceRepairTargetStore
	adapter ProposalWorkspaceRepairAdapter
	locks   ProposalWorkspaceRepairLockManager
	clock   Clock
}

func NewProposalWorkspaceRepairOwner(targets ProposalWorkspaceRepairTargetStore, adapter ProposalWorkspaceRepairAdapter, locks ProposalWorkspaceRepairLockManager, clock Clock) (*ProposalWorkspaceRepairOwner, error) {
	if targets == nil || adapter == nil || locks == nil {
		return nil, ErrProposalWorkspaceRepair
	}
	if clock == nil {
		clock = SystemClock{}
	}
	return &ProposalWorkspaceRepairOwner{targets: targets, adapter: adapter, locks: locks, clock: clock}, nil
}

// RegisterProposalWorkspaceRepairOwner registers the exact health planner and
// handler. Composition decides when T035 owner evidence is available.
func RegisterProposalWorkspaceRepairOwner(registry *RepairRegistry, owner *ProposalWorkspaceRepairOwner) error {
	if registry == nil || owner == nil {
		return ErrProposalWorkspaceRepair
	}
	if err := registry.RegisterHandler(owner); err != nil {
		return err
	}
	return registry.RegisterPlanner(HealthWorkspaceRepairRequired, owner)
}

func (o *ProposalWorkspaceRepairOwner) Kind() RepairHandlerKind {
	return ProposalWorkspaceRepairHandlerKind
}

func (o *ProposalWorkspaceRepairOwner) Version() string { return ProposalWorkspaceRepairHandlerVersion }

func (o *ProposalWorkspaceRepairOwner) Plans(ctx context.Context, report HealthReport) ([]RepairPlan, error) {
	if o == nil || ctx == nil || !validHealthRevision(report.HealthRevision) {
		return nil, ErrProposalWorkspaceRepair
	}
	targets, err := o.targets.ListProposalWorkspaceRepairTargets(ctx)
	if err != nil {
		return nil, err
	}
	now := o.clock.Now().UTC()
	plans := make([]RepairPlan, 0, len(targets))
	for _, target := range targets {
		if err := target.Validate(); err != nil {
			return nil, err
		}
		preconditions, err := proposalWorkspaceRepairPreconditions(target)
		if err != nil {
			return nil, err
		}
		plan := RepairPlan{
			ID:                RepairPlanID("workspace-repair-" + string(target.Action) + "-" + target.ResourceID),
			HealthCode:        HealthWorkspaceRepairRequired,
			HealthRevision:    report.HealthRevision,
			PolicyVersion:     ProposalWorkspaceRepairPolicyVersion,
			Summary:           "A proposal workspace lifecycle transition requires exact owner repair.",
			Effect:            workspaceRepairEffectText(target.Action),
			OwnedResourceRefs: []string{"proposal-workspace:" + target.ResourceID + ":" + string(target.Action)},
			PreconditionsHash: preconditions,
			ConfirmationText:  "repair proposal workspace lifecycle",
			HandlerKind:       ProposalWorkspaceRepairHandlerKind,
			HandlerVersion:    ProposalWorkspaceRepairHandlerVersion,
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

func (o *ProposalWorkspaceRepairOwner) Revalidate(ctx context.Context, plan RepairPlan) (RepairRevalidation, error) {
	target, err := o.loadTarget(ctx, plan)
	if err != nil {
		return RepairRevalidation{}, err
	}
	hash, err := proposalWorkspaceRepairPreconditions(target)
	if err != nil || hash != plan.PreconditionsHash {
		return RepairRevalidation{}, ErrRepairPreconditions
	}
	lock, err := o.locks.Acquire(ctx, target.ResourceID)
	if err != nil {
		return RepairRevalidation{}, err
	}
	defer lock.Close()
	proof, err := o.adapter.Inspect(ctx, target)
	if err != nil || proof.Validate() != nil || !proposalWorkspaceProofMatches(proof, target) {
		return RepairRevalidation{}, ErrRepairPreconditions
	}
	return RepairRevalidation{PreconditionsHash: hash, LockProof: "proposal-workspace-lock:" + target.ResourceID, JournalID: "proposal-workspace-repair:" + target.ResourceID}, nil
}

func (o *ProposalWorkspaceRepairOwner) Execute(ctx context.Context, operation RepairOperation, plan RepairPlan) (RepairEffect, error) {
	target, err := o.loadTarget(ctx, plan)
	if err != nil {
		return RepairEffect{}, err
	}
	hash, err := proposalWorkspaceRepairPreconditions(target)
	if err != nil || hash != plan.PreconditionsHash {
		return RepairEffect{}, ErrRepairPreconditions
	}
	lock, err := o.locks.Acquire(ctx, target.ResourceID)
	if err != nil {
		return RepairEffect{}, err
	}
	defer lock.Close()
	proof, err := o.adapter.Inspect(ctx, target)
	if err != nil || proof.Validate() != nil || !proposalWorkspaceProofMatches(proof, target) {
		return RepairEffect{}, ErrRepairPreconditions
	}
	switch target.Action {
	case ProposalWorkspaceResultReset:
		return o.adapter.ResetResult(ctx, target, proof, operation)
	case ProposalWorkspaceQuarantine:
		return o.adapter.Quarantine(ctx, target, proof, operation)
	case ProposalWorkspaceReplay:
		return o.adapter.Replay(ctx, target, proof, operation)
	default:
		return RepairEffect{}, ErrProposalWorkspaceRepairTarget
	}
}

func (o *ProposalWorkspaceRepairOwner) Verify(ctx context.Context, _ RepairOperation, plan RepairPlan) (RepairVerification, error) {
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
	if err != nil || proof.Validate() != nil || proof.ResourceID != target.ResourceID || proof.MarkerNonce != target.MarkerNonce || proof.NativeIdentityHash != target.NativeIdentityHash {
		return RepairVerification{}, ErrProposalWorkspaceRepairProof
	}
	if proof.PostconditionHash != target.ExpectedPostcondition {
		return RepairVerification{}, ErrProposalWorkspaceRepairProof
	}
	digest, err := json.Marshal(struct {
		ResourceID string
		Action     ProposalWorkspaceRepairAction
		Proof      string
	}{target.ResourceID, target.Action, proof.PostconditionHash})
	if err != nil {
		return RepairVerification{}, err
	}
	hash := sha256.Sum256(digest)
	return RepairVerification{PostconditionHash: hex.EncodeToString(hash[:])}, nil
}

func (o *ProposalWorkspaceRepairOwner) loadTarget(ctx context.Context, plan RepairPlan) (ProposalWorkspaceRepairTarget, error) {
	if o == nil || ctx == nil || plan.Validate() != nil || plan.HealthCode != HealthWorkspaceRepairRequired || plan.HandlerKind != ProposalWorkspaceRepairHandlerKind || plan.HandlerVersion != ProposalWorkspaceRepairHandlerVersion || len(plan.OwnedResourceRefs) != 1 {
		return ProposalWorkspaceRepairTarget{}, ErrProposalWorkspaceRepair
	}
	const prefix = "proposal-workspace:"
	if !strings.HasPrefix(plan.OwnedResourceRefs[0], prefix) {
		return ProposalWorkspaceRepairTarget{}, ErrProposalWorkspaceRepair
	}
	parts := strings.Split(strings.TrimPrefix(plan.OwnedResourceRefs[0], prefix), ":")
	if len(parts) != 2 || !safeRepairToken(parts[0], 128) {
		return ProposalWorkspaceRepairTarget{}, ErrProposalWorkspaceRepair
	}
	target, err := o.targets.LoadProposalWorkspaceRepairTarget(ctx, parts[0], ProposalWorkspaceRepairAction(parts[1]))
	if err != nil || target.Validate() != nil || target.ResourceID != parts[0] || target.Action != ProposalWorkspaceRepairAction(parts[1]) {
		return ProposalWorkspaceRepairTarget{}, ErrProposalWorkspaceRepairTarget
	}
	return target, nil
}

func proposalWorkspaceRepairPreconditions(target ProposalWorkspaceRepairTarget) (string, error) {
	if err := target.Validate(); err != nil {
		return "", err
	}
	value := struct {
		ResourceID, Action, RepositoryID, SessionID, ThreadID, ProposalID, WorkspaceID string
		TargetGeneration                                                               uint64
		MarkerNonce, NativeIdentityHash, OwnerLeaseHash                                string
		LifecyclePhase                                                                 ProposalWorkspaceLifecyclePhase
		LifecycleRevision, WorkspaceRevision, ProposalRevision                         uint64
		BaselineIdentityHash, BaselineManifestHash                                     string
		ResultIdentityHash, ResultManifestHash, CapacityDisposition                    string
		ActiveTurn, ReadyDependency, ResetEligible, QuarantineEligible, ReplayEligible bool
		SameRootQuarantine, QuarantineRootHash, ExpectedPostcondition                  interface{}
	}{target.ResourceID, string(target.Action), target.RepositoryID, target.SessionID, target.ThreadID, target.ProposalID, target.WorkspaceID, target.TargetGeneration, target.MarkerNonce, target.NativeIdentityHash, target.OwnerLeaseHash, target.LifecyclePhase, target.LifecycleRevision, target.WorkspaceRevision, target.ProposalRevision, target.BaselineIdentityHash, target.BaselineManifestHash, target.ResultIdentityHash, target.ResultManifestHash, target.CapacityDisposition, target.ActiveTurn, target.ReadyDependency, target.ResetEligible, target.QuarantineEligible, target.ReplayEligible, target.SameRootQuarantine, target.QuarantineRootHash, target.ExpectedPostcondition}
	encoded, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}

func proposalWorkspaceProofMatches(proof ProposalWorkspaceRepairProof, target ProposalWorkspaceRepairTarget) bool {
	return proof.ResourceID == target.ResourceID && proof.MarkerNonce == target.MarkerNonce && proof.NativeIdentityHash == target.NativeIdentityHash && proof.OwnerLeaseHash == target.OwnerLeaseHash && proof.LifecyclePhase == target.LifecyclePhase && proof.LifecycleRevision == target.LifecycleRevision && proof.WorkspaceRevision == target.WorkspaceRevision && proof.ProposalRevision == target.ProposalRevision && proof.BaselineManifestHash == target.BaselineManifestHash && proof.ResultManifestHash == target.ResultManifestHash && proof.ActiveTurn == target.ActiveTurn && proof.ReadyDependency == target.ReadyDependency && (!target.SameRootQuarantine || proof.SameRootQuarantine)
}

func workspaceRepairEffectText(action ProposalWorkspaceRepairAction) string {
	switch action {
	case ProposalWorkspaceResultReset:
		return "Reset one provider-writable result from its verified immutable baseline."
	case ProposalWorkspaceQuarantine:
		return "Quarantine one inactive, positively owned proposal workspace within its verified root."
	case ProposalWorkspaceReplay:
		return "Replay one enumerated non-apply workspace lifecycle transition."
	default:
		return fmt.Sprintf("Repair proposal workspace action %q.", action)
	}
}
