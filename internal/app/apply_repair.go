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
	ApplyRepairPolicyVersion  uint64 = 1
	ApplyRepairHandlerKind           = RepairHandlerKind("apply-journal.manual-closure")
	ApplyRepairHandlerVersion        = "v1"
)

var (
	ErrApplyRepair       = errors.New("apply repair rejected")
	ErrApplyRepairTarget = errors.New("invalid apply repair target")
	ErrApplyRepairProof  = errors.New("invalid apply repair proof")
)

// ApplyRepairClosure identifies one of the two provable manual terminal
// states. Neither action writes the user repository.
type ApplyRepairClosure string

const (
	ApplyRepairAbandonBaseline ApplyRepairClosure = "abandon_baseline"
	ApplyRepairAcceptResult    ApplyRepairClosure = "accept_result"
)

func (c ApplyRepairClosure) Validate() error {
	if c != ApplyRepairAbandonBaseline && c != ApplyRepairAcceptResult {
		return ErrApplyRepairTarget
	}
	return nil
}

// ApplyRepairClassification is produced by the same destination observation
// policy used by normal apply verification.
type ApplyRepairClassification string

const (
	ApplyRepairAllBaseline     ApplyRepairClassification = "all_baseline"
	ApplyRepairAllResult       ApplyRepairClassification = "all_result"
	ApplyRepairMixed           ApplyRepairClassification = "mixed"
	ApplyRepairMissingEvidence ApplyRepairClassification = "missing_evidence"
	ApplyRepairUnsupported     ApplyRepairClassification = "unsupported_type"
	ApplyRepairChanged         ApplyRepairClassification = "changed_during_observation"
)

func (c ApplyRepairClassification) Validate() error {
	switch c {
	case ApplyRepairAllBaseline, ApplyRepairAllResult, ApplyRepairMixed, ApplyRepairMissingEvidence, ApplyRepairUnsupported, ApplyRepairChanged:
		return nil
	default:
		return ErrApplyRepairTarget
	}
}

// ApplyRepairTarget is path-free evidence for one interrupted apply journal.
// The adapter owns complete per-path and index observations.
type ApplyRepairTarget struct {
	ResourceID              string
	Closure                 ApplyRepairClosure
	OperationID             string
	SessionID               string
	ProposalID              string
	WorkspaceID             string
	RepositoryID            string
	WorktreeID              string
	JournalRevision         uint64
	ProposalRevision        uint64
	DestinationIdentityHash string
	BaselineEvidenceHash    string
	ResultEvidenceHash      string
	Classification          ApplyRepairClassification
	ApplyOnceAvailable      bool
	ExpectedPostcondition   string
}

func (t ApplyRepairTarget) Validate() error {
	if !safeRepairToken(t.ResourceID, 128) || t.Closure.Validate() != nil || !safeRepairToken(t.OperationID, 128) || !safeRepairToken(t.SessionID, 128) || !safeRepairToken(t.ProposalID, 128) || !safeRepairToken(t.WorkspaceID, 128) || !safeRepairToken(t.RepositoryID, 128) || !safeRepairToken(t.WorktreeID, 128) || t.JournalRevision == 0 || t.ProposalRevision == 0 || !validRepairHash(t.DestinationIdentityHash) || !validRepairHash(t.BaselineEvidenceHash) || !validRepairHash(t.ResultEvidenceHash) || t.Classification.Validate() != nil || !validRepairHash(t.ExpectedPostcondition) {
		return ErrApplyRepairTarget
	}
	if t.Closure == ApplyRepairAbandonBaseline && t.Classification != ApplyRepairAllBaseline {
		return ErrApplyRepairTarget
	}
	if t.Closure == ApplyRepairAcceptResult && (t.Classification != ApplyRepairAllResult || !t.ApplyOnceAvailable) {
		return ErrApplyRepairTarget
	}
	return nil
}

// ApplyRepairProof is fresh complete evidence captured while the destination
// lock is held.
type ApplyRepairProof struct {
	ResourceID              string
	OperationID             string
	JournalRevision         uint64
	ProposalRevision        uint64
	DestinationIdentityHash string
	BaselineEvidenceHash    string
	ResultEvidenceHash      string
	Classification          ApplyRepairClassification
	PostconditionHash       string
}

func (p ApplyRepairProof) Validate() error {
	if !safeRepairToken(p.ResourceID, 128) || !safeRepairToken(p.OperationID, 128) || p.JournalRevision == 0 || p.ProposalRevision == 0 || !validRepairHash(p.DestinationIdentityHash) || !validRepairHash(p.BaselineEvidenceHash) || !validRepairHash(p.ResultEvidenceHash) || p.Classification.Validate() != nil || !validRepairHash(p.PostconditionHash) {
		return ErrApplyRepairProof
	}
	return nil
}

type ApplyRepairTargetStore interface {
	ListApplyRepairTargets(context.Context) ([]ApplyRepairTarget, error)
	LoadApplyRepairTarget(context.Context, string, ApplyRepairClosure) (ApplyRepairTarget, error)
}

type ApplyRepairLockManager interface {
	Acquire(context.Context, string) (io.Closer, error)
}

// ApplyRepairAdapter owns exact Git/raw/index/conversion observations and the
// metadata-only terminal transition. It never writes user files.
type ApplyRepairAdapter interface {
	Inspect(context.Context, ApplyRepairTarget) (ApplyRepairProof, error)
	CloseBaseline(context.Context, ApplyRepairTarget, ApplyRepairProof, RepairOperation) (RepairEffect, error)
	CloseResult(context.Context, ApplyRepairTarget, ApplyRepairProof, RepairOperation) (RepairEffect, error)
}

type ApplyRepairOwner struct {
	targets ApplyRepairTargetStore
	adapter ApplyRepairAdapter
	locks   ApplyRepairLockManager
	clock   Clock
}

func NewApplyRepairOwner(targets ApplyRepairTargetStore, adapter ApplyRepairAdapter, locks ApplyRepairLockManager, clock Clock) (*ApplyRepairOwner, error) {
	if targets == nil || adapter == nil || locks == nil {
		return nil, ErrApplyRepair
	}
	if clock == nil {
		clock = SystemClock{}
	}
	return &ApplyRepairOwner{targets: targets, adapter: adapter, locks: locks, clock: clock}, nil
}

func RegisterApplyRepairOwner(registry *RepairRegistry, owner *ApplyRepairOwner) error {
	if registry == nil || owner == nil {
		return ErrApplyRepair
	}
	if err := registry.RegisterHandler(owner); err != nil {
		return err
	}
	return registry.RegisterPlanner(HealthApplyRepairRequired, owner)
}

func (o *ApplyRepairOwner) Kind() RepairHandlerKind { return ApplyRepairHandlerKind }

func (o *ApplyRepairOwner) Version() string { return ApplyRepairHandlerVersion }

func (o *ApplyRepairOwner) Plans(ctx context.Context, report HealthReport) ([]RepairPlan, error) {
	if o == nil || ctx == nil || !validHealthRevision(report.HealthRevision) {
		return nil, ErrApplyRepair
	}
	targets, err := o.targets.ListApplyRepairTargets(ctx)
	if err != nil {
		return nil, err
	}
	now := o.clock.Now().UTC()
	plans := make([]RepairPlan, 0, len(targets))
	for _, target := range targets {
		if err := target.Validate(); err != nil {
			return nil, err
		}
		preconditions, err := applyRepairPreconditions(target)
		if err != nil {
			return nil, err
		}
		confirmation := "abandon manually restored apply journal"
		effect := "Close one interrupted apply journal as abandoned from complete baseline evidence."
		if target.Closure == ApplyRepairAcceptResult {
			confirmation = "accept manually verified apply result without Nudge file writes"
			effect = "Close one interrupted apply journal as an explicitly accepted manual result."
		}
		plan := RepairPlan{ID: RepairPlanID("apply-repair-" + string(target.Closure) + "-" + target.ResourceID), HealthCode: HealthApplyRepairRequired, HealthRevision: report.HealthRevision, PolicyVersion: ApplyRepairPolicyVersion, Summary: "An interrupted apply journal has one exact manual terminal state.", Effect: effect, OwnedResourceRefs: []string{"apply-journal:" + target.ResourceID + ":" + string(target.Closure)}, PreconditionsHash: preconditions, ConfirmationText: confirmation, HandlerKind: ApplyRepairHandlerKind, HandlerVersion: ApplyRepairHandlerVersion, CreatedAt: now, ExpiresAt: now.Add(24 * time.Hour)}
		if err := plan.Validate(); err != nil {
			return nil, err
		}
		plans = append(plans, plan)
	}
	return plans, nil
}

func (o *ApplyRepairOwner) Revalidate(ctx context.Context, plan RepairPlan) (RepairRevalidation, error) {
	target, err := o.loadTarget(ctx, plan)
	if err != nil {
		return RepairRevalidation{}, err
	}
	hash, err := applyRepairPreconditions(target)
	if err != nil || hash != plan.PreconditionsHash {
		return RepairRevalidation{}, ErrRepairPreconditions
	}
	lock, err := o.locks.Acquire(ctx, target.ResourceID)
	if err != nil {
		return RepairRevalidation{}, err
	}
	defer lock.Close()
	proof, err := o.adapter.Inspect(ctx, target)
	if err != nil || proof.Validate() != nil || !applyRepairProofMatches(proof, target) {
		return RepairRevalidation{}, ErrRepairPreconditions
	}
	return RepairRevalidation{PreconditionsHash: hash, LockProof: "apply-destination-lock:" + target.ResourceID, JournalID: "apply-repair:" + target.ResourceID}, nil
}

func (o *ApplyRepairOwner) Execute(ctx context.Context, operation RepairOperation, plan RepairPlan) (RepairEffect, error) {
	target, err := o.loadTarget(ctx, plan)
	if err != nil {
		return RepairEffect{}, err
	}
	hash, err := applyRepairPreconditions(target)
	if err != nil || hash != plan.PreconditionsHash {
		return RepairEffect{}, ErrRepairPreconditions
	}
	lock, err := o.locks.Acquire(ctx, target.ResourceID)
	if err != nil {
		return RepairEffect{}, err
	}
	defer lock.Close()
	proof, err := o.adapter.Inspect(ctx, target)
	if err != nil || proof.Validate() != nil || !applyRepairProofMatches(proof, target) {
		return RepairEffect{}, ErrRepairPreconditions
	}
	if target.Closure == ApplyRepairAbandonBaseline {
		return o.adapter.CloseBaseline(ctx, target, proof, operation)
	}
	return o.adapter.CloseResult(ctx, target, proof, operation)
}

func (o *ApplyRepairOwner) Verify(ctx context.Context, _ RepairOperation, plan RepairPlan) (RepairVerification, error) {
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
	if err != nil || proof.Validate() != nil || proof.ResourceID != target.ResourceID || proof.OperationID != target.OperationID || proof.PostconditionHash != target.ExpectedPostcondition {
		return RepairVerification{}, ErrApplyRepairProof
	}
	data, err := json.Marshal(struct {
		ResourceID string
		Closure    ApplyRepairClosure
		Post       string
	}{target.ResourceID, target.Closure, proof.PostconditionHash})
	if err != nil {
		return RepairVerification{}, err
	}
	digest := sha256.Sum256(data)
	return RepairVerification{PostconditionHash: hex.EncodeToString(digest[:])}, nil
}

func (o *ApplyRepairOwner) loadTarget(ctx context.Context, plan RepairPlan) (ApplyRepairTarget, error) {
	if o == nil || ctx == nil || plan.Validate() != nil || plan.HealthCode != HealthApplyRepairRequired || plan.HandlerKind != ApplyRepairHandlerKind || plan.HandlerVersion != ApplyRepairHandlerVersion || len(plan.OwnedResourceRefs) != 1 {
		return ApplyRepairTarget{}, ErrApplyRepair
	}
	const prefix = "apply-journal:"
	if !strings.HasPrefix(plan.OwnedResourceRefs[0], prefix) {
		return ApplyRepairTarget{}, ErrApplyRepair
	}
	parts := strings.Split(strings.TrimPrefix(plan.OwnedResourceRefs[0], prefix), ":")
	if len(parts) != 2 || !safeRepairToken(parts[0], 128) {
		return ApplyRepairTarget{}, ErrApplyRepair
	}
	target, err := o.targets.LoadApplyRepairTarget(ctx, parts[0], ApplyRepairClosure(parts[1]))
	if err != nil || target.Validate() != nil || target.ResourceID != parts[0] || target.Closure != ApplyRepairClosure(parts[1]) {
		return ApplyRepairTarget{}, ErrApplyRepairTarget
	}
	return target, nil
}

func applyRepairPreconditions(target ApplyRepairTarget) (string, error) {
	if err := target.Validate(); err != nil {
		return "", err
	}
	data, err := json.Marshal(target)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:]), nil
}

func applyRepairProofMatches(proof ApplyRepairProof, target ApplyRepairTarget) bool {
	return proof.ResourceID == target.ResourceID && proof.OperationID == target.OperationID && proof.JournalRevision == target.JournalRevision && proof.ProposalRevision == target.ProposalRevision && proof.DestinationIdentityHash == target.DestinationIdentityHash && proof.BaselineEvidenceHash == target.BaselineEvidenceHash && proof.ResultEvidenceHash == target.ResultEvidenceHash && proof.Classification == target.Classification
}
