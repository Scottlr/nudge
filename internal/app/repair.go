package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/Scottlr/nudge/internal/domain"
)

const (
	// RepairFrameworkVersion identifies the common repair lifecycle contract.
	RepairFrameworkVersion uint64 = 1
	// RepairConfirmationYes is the non-interactive confirmation token used by
	// the exact doctor --repair --yes command. The executor still compares the
	// stored plan identity and health revision before accepting it.
	RepairConfirmationYes = "__nudge_confirmed_yes__"

	maxRepairSummaryBytes     = 512
	maxRepairResourceRefs     = 32
	maxRepairResourceRefBytes = 128
	// MaxRepairAuditEntries bounds retained phase records for one operation.
	MaxRepairAuditEntries        = 256
	maxRepairIdempotencyBytes    = 128
	maxRepairHandlerKindBytes    = 128
	maxRepairHandlerVersionBytes = 64
)

var (
	ErrRepairPlanNotFound       = errors.New("repair plan not found")
	ErrRepairOperationNotFound  = errors.New("repair operation not found")
	ErrRepairPlanConflict       = errors.New("repair plan conflict")
	ErrRepairHandlerConflict    = errors.New("repair handler conflict")
	ErrRepairPlannerConflict    = errors.New("repair planner conflict")
	ErrRepairHealthRevision     = errors.New("repair health revision is stale")
	ErrRepairPreconditions      = errors.New("repair preconditions changed")
	ErrRepairConfirmation       = errors.New("repair confirmation required")
	ErrRepairHandlerUnavailable = errors.New("repair handler unavailable")
	ErrRepairNotReady           = errors.New("repair operation is not ready")
	ErrRepairAlreadyTerminal    = errors.New("repair operation is already terminal")
)

// RepairPlanID identifies one advertised, immutable repair plan.
type RepairPlanID string

// RepairHandlerKind identifies the owner of a repair effect. It is never a
// filesystem path, SQL statement, shell command, or user-selected selector.
type RepairHandlerKind string

// RepairPhase is the bounded common lifecycle shared by all owner handlers.
type RepairPhase string

const (
	RepairPhasePrepared       RepairPhase = "prepared"
	RepairPhaseRevalidated    RepairPhase = "revalidated"
	RepairPhaseConfirmed      RepairPhase = "confirmed"
	RepairPhaseExecuting      RepairPhase = "executing"
	RepairPhaseVerifying      RepairPhase = "verifying"
	RepairPhaseSucceeded      RepairPhase = "succeeded"
	RepairPhaseFailed         RepairPhase = "failed"
	RepairPhaseRepairRequired RepairPhase = "repair_required"
)

// RepairOutcome is the stable terminal result of one repair operation.
type RepairOutcome string

const (
	RepairOutcomeNone            RepairOutcome = ""
	RepairOutcomeSucceeded       RepairOutcome = "succeeded"
	RepairOutcomeAlreadyRepaired RepairOutcome = "already_repaired"
	RepairOutcomeFailed          RepairOutcome = "failed"
	RepairOutcomeRepairRequired  RepairOutcome = "repair_required"
)

// RepairPlan is an immutable, redacted authorization unit produced by a
// registered owner planner from bounded health evidence.
type RepairPlan struct {
	ID                RepairPlanID      `json:"id"`
	HealthCode        HealthCode        `json:"health_code"`
	HealthRevision    string            `json:"health_revision"`
	PolicyVersion     uint64            `json:"policy_version"`
	Summary           string            `json:"summary"`
	Effect            string            `json:"effect"`
	OwnedResourceRefs []string          `json:"owned_resource_refs"`
	PreconditionsHash string            `json:"preconditions_hash"`
	ConfirmationText  string            `json:"confirmation_text"`
	HandlerKind       RepairHandlerKind `json:"handler_kind"`
	HandlerVersion    string            `json:"handler_version"`
	CreatedAt         time.Time         `json:"created_at"`
	ExpiresAt         time.Time         `json:"expires_at"`
}

// Validate checks the plan's bounded, identity-bearing public contract.
func (p RepairPlan) Validate() error {
	if !validRepairToken(string(p.ID), maxRepairResourceRefBytes) || !validHealthCode(p.HealthCode) || !validHealthRevision(p.HealthRevision) || p.PolicyVersion == 0 || !validRepairText(p.Summary, maxRepairSummaryBytes) || !validRepairText(p.Effect, maxRepairSummaryBytes) || !validRepairHash(p.PreconditionsHash) || !validRepairText(p.ConfirmationText, maxRepairSummaryBytes) || !validRepairToken(string(p.HandlerKind), maxRepairHandlerKindBytes) || !validRepairToken(p.HandlerVersion, maxRepairHandlerVersionBytes) || p.CreatedAt.IsZero() || p.ExpiresAt.IsZero() || p.ExpiresAt.Before(p.CreatedAt) {
		return ErrInvalidRepairPlan
	}
	if len(p.OwnedResourceRefs) == 0 || len(p.OwnedResourceRefs) > maxRepairResourceRefs {
		return ErrInvalidRepairPlan
	}
	for _, ref := range p.OwnedResourceRefs {
		if !validRepairToken(ref, maxRepairResourceRefBytes) || strings.ContainsAny(ref, `/\\`) {
			return ErrInvalidRepairPlan
		}
	}
	return nil
}

// ExecuteRepair is the exact application request for one advertised plan.
type ExecuteRepair struct {
	PlanID         RepairPlanID `json:"plan_id"`
	HealthRevision string       `json:"health_revision"`
	Confirmation   string       `json:"confirmation"`
	IdempotencyKey string       `json:"idempotency_key"`
}

// Validate checks the request without interpreting arbitrary effect data.
func (r ExecuteRepair) Validate() error {
	if !validRepairToken(string(r.PlanID), maxRepairResourceRefBytes) || !validHealthRevision(r.HealthRevision) || r.Confirmation == "" || !validRepairToken(r.IdempotencyKey, maxRepairIdempotencyBytes) {
		return ErrInvalidRepairRequest
	}
	return nil
}

// RepairOperation records one bounded, resumable owner lifecycle.
type RepairOperation struct {
	Version           uint64             `json:"version"`
	ID                domain.OperationID `json:"id"`
	PlanID            RepairPlanID       `json:"plan_id"`
	HandlerKind       RepairHandlerKind  `json:"handler_kind"`
	HandlerVersion    string             `json:"handler_version"`
	HealthRevision    string             `json:"health_revision"`
	IdempotencyKey    string             `json:"idempotency_key"`
	Phase             RepairPhase        `json:"phase"`
	Outcome           RepairOutcome      `json:"outcome"`
	ErrorCode         string             `json:"error_code,omitempty"`
	PreconditionsHash string             `json:"preconditions_hash"`
	LockProof         string             `json:"lock_proof"`
	JournalID         string             `json:"journal_id"`
	EffectID          string             `json:"effect_id"`
	PostconditionHash string             `json:"postcondition_hash"`
	CreatedAt         time.Time          `json:"created_at"`
	UpdatedAt         time.Time          `json:"updated_at"`
}

// Validate checks the operation identity and lifecycle invariants.
func (o RepairOperation) Validate() error {
	if o.Version != RepairFrameworkVersion || o.ID == "" || !validRepairToken(string(o.PlanID), maxRepairResourceRefBytes) || !validRepairToken(string(o.HandlerKind), maxRepairHandlerKindBytes) || !validRepairToken(o.HandlerVersion, maxRepairHandlerVersionBytes) || !validHealthRevision(o.HealthRevision) || !validRepairToken(o.IdempotencyKey, maxRepairIdempotencyBytes) || !validRepairPhase(o.Phase) || !validRepairOutcome(o.Outcome) || !validRepairHash(o.PreconditionsHash) || o.CreatedAt.IsZero() || o.UpdatedAt.IsZero() || o.UpdatedAt.Before(o.CreatedAt) {
		return ErrInvalidRepairOperation
	}
	if o.Phase == RepairPhasePrepared || o.Phase == RepairPhaseRevalidated || o.Phase == RepairPhaseConfirmed || o.Phase == RepairPhaseExecuting || o.Phase == RepairPhaseVerifying {
		if o.Outcome != RepairOutcomeNone {
			return ErrInvalidRepairOperation
		}
	}
	if o.Phase == RepairPhaseSucceeded && o.Outcome != RepairOutcomeSucceeded && o.Outcome != RepairOutcomeAlreadyRepaired {
		return ErrInvalidRepairOperation
	}
	if o.Phase == RepairPhaseRepairRequired && o.Outcome != RepairOutcomeRepairRequired {
		return ErrInvalidRepairOperation
	}
	if o.Phase == RepairPhaseFailed && o.Outcome != RepairOutcomeFailed {
		return ErrInvalidRepairOperation
	}
	return nil
}

// RepairAuditEntry is a redacted, append-only lifecycle record.
type RepairAuditEntry struct {
	OperationID domain.OperationID `json:"operation_id"`
	PlanID      RepairPlanID       `json:"plan_id"`
	Phase       RepairPhase        `json:"phase"`
	Code        string             `json:"code"`
	At          time.Time          `json:"at"`
}

// Validate checks bounded audit metadata.
func (a RepairAuditEntry) Validate() error {
	if a.OperationID == "" || !validRepairToken(string(a.PlanID), maxRepairResourceRefBytes) || !validRepairPhase(a.Phase) || !validRepairToken(a.Code, maxRepairSummaryBytes) || a.At.IsZero() {
		return ErrInvalidRepairAudit
	}
	return nil
}

// RepairRevalidation is the owner proof required before confirmation. The
// common framework checks the plan hash; the owner proves lock and journal
// ownership in its own adapter.
type RepairRevalidation struct {
	PreconditionsHash string
	LockProof         string
	JournalID         string
}

// Validate checks that the owner supplied all common safety evidence.
func (r RepairRevalidation) Validate() error {
	if !validRepairHash(r.PreconditionsHash) || !validRepairToken(r.LockProof, maxRepairResourceRefBytes) || !validRepairToken(r.JournalID, maxRepairResourceRefBytes) {
		return ErrInvalidRepairRevalidation
	}
	return nil
}

// RepairEffect is the bounded result of an owner effect. It contains no
// generic mutation capability and must be verified by the same owner.
type RepairEffect struct {
	EffectID       string
	IdempotencyKey string
}

// Validate checks the owner effect identity.
func (e RepairEffect) Validate() error {
	if !validRepairToken(e.EffectID, maxRepairResourceRefBytes) || !validRepairToken(e.IdempotencyKey, maxRepairIdempotencyBytes) {
		return ErrInvalidRepairEffect
	}
	return nil
}

// RepairVerification is the owner proof of the exact postcondition.
type RepairVerification struct {
	PostconditionHash string
	AlreadyRepaired   bool
}

// Validate checks the postcondition identity.
func (v RepairVerification) Validate() error {
	if !validRepairHash(v.PostconditionHash) {
		return ErrInvalidRepairVerification
	}
	return nil
}

// RepairHandler owns one typed repair effect. The framework supplies no
// filesystem, SQL, shell, or repository mutation primitive to handlers.
type RepairHandler interface {
	Kind() RepairHandlerKind
	Version() string
	Revalidate(context.Context, RepairPlan) (RepairRevalidation, error)
	Execute(context.Context, RepairOperation, RepairPlan) (RepairEffect, error)
	Verify(context.Context, RepairOperation, RepairPlan) (RepairVerification, error)
}

// RepairPlanner creates redacted plans for one health code. It cannot execute
// a plan and receives only the current bounded report.
type RepairPlanner interface {
	Plans(context.Context, HealthReport) ([]RepairPlan, error)
}

// RepairRegistry holds the exact owner handler and planner registrations.
type RepairRegistry struct {
	handlers map[RepairHandlerKind]RepairHandler
	planners map[HealthCode]RepairPlanner
}

// NewRepairRegistry creates an empty registry. Owner tasks register their own
// typed planners and handlers when their effect is implemented.
func NewRepairRegistry() *RepairRegistry {
	return &RepairRegistry{handlers: make(map[RepairHandlerKind]RepairHandler), planners: make(map[HealthCode]RepairPlanner)}
}

// RegisterHandler adds one handler after validating its stable identity.
func (r *RepairRegistry) RegisterHandler(handler RepairHandler) error {
	if r == nil || handler == nil || !validRepairToken(string(handler.Kind()), maxRepairHandlerKindBytes) || !validRepairToken(handler.Version(), maxRepairHandlerVersionBytes) {
		return ErrInvalidRepairHandler
	}
	if _, exists := r.handlers[handler.Kind()]; exists {
		return ErrRepairHandlerConflict
	}
	r.handlers[handler.Kind()] = handler
	return nil
}

// RegisterPlanner adds one planner for a stable health finding.
func (r *RepairRegistry) RegisterPlanner(code HealthCode, planner RepairPlanner) error {
	if r == nil || !validHealthCode(code) || planner == nil {
		return ErrInvalidRepairPlanner
	}
	if _, exists := r.planners[code]; exists {
		return ErrRepairPlannerConflict
	}
	r.planners[code] = planner
	return nil
}

// Handler returns the exact registered owner handler for a plan.
func (r *RepairRegistry) Handler(kind RepairHandlerKind, version string) (RepairHandler, error) {
	if r == nil {
		return nil, ErrRepairHandlerUnavailable
	}
	handler, ok := r.handlers[kind]
	if !ok || handler.Version() != version {
		return nil, ErrRepairHandlerUnavailable
	}
	return handler, nil
}

// BuildPlans asks registered owners to create bounded plans and persists no
// data itself. Callers persist the returned plans through RepairPlanStore.
func (r *RepairRegistry) BuildPlans(ctx context.Context, report HealthReport) ([]RepairPlan, error) {
	if r == nil || ctx == nil || len(report.Results) == 0 {
		return nil, ErrInvalidRepairRequest
	}
	plans := make([]RepairPlan, 0)
	for _, result := range report.Results {
		planner := r.planners[result.Code]
		if planner == nil {
			continue
		}
		ownerPlans, err := planner.Plans(ctx, report)
		if err != nil {
			return nil, err
		}
		for _, plan := range ownerPlans {
			if err := plan.Validate(); err != nil || plan.HealthCode != result.Code || plan.HealthRevision != report.HealthRevision {
				return nil, ErrInvalidRepairPlan
			}
			plans = append(plans, plan)
		}
	}
	return plans, nil
}

// RepairPlanStore is the application-owned durable boundary for plans and
// lifecycle metadata. It does not expose SQL or owner state.
type RepairPlanStore interface {
	SaveRepairPlan(context.Context, RepairPlan) error
	LoadRepairPlan(context.Context, RepairPlanID) (RepairPlan, error)
	SaveRepairOperation(context.Context, RepairOperation) error
	LoadRepairOperation(context.Context, domain.OperationID) (RepairOperation, error)
	LoadRepairOperationByIdempotency(context.Context, string) (RepairOperation, error)
	AppendRepairAudit(context.Context, RepairAuditEntry) error
}

// RepairExecutor owns common authorization and lifecycle sequencing for one
// plan. Owner handlers remain the only source of mutation authority.
type RepairExecutor struct {
	store    RepairPlanStore
	registry *RepairRegistry
	clock    Clock
	ids      IDSource
}

// NewRepairExecutor validates the framework composition.
func NewRepairExecutor(store RepairPlanStore, registry *RepairRegistry, clock Clock, ids IDSource) (*RepairExecutor, error) {
	if store == nil || registry == nil {
		return nil, ErrRepairNotReady
	}
	if clock == nil {
		clock = SystemClock{}
	}
	if ids == nil {
		ids = RandomIDSource{}
	}
	return &RepairExecutor{store: store, registry: registry, clock: clock, ids: ids}, nil
}

// Execute performs one exact, confirmed, idempotent repair lifecycle.
func (e *RepairExecutor) Execute(ctx context.Context, request ExecuteRepair) (RepairOperation, error) {
	if e == nil || ctx == nil || request.Validate() != nil {
		return RepairOperation{}, ErrInvalidRepairRequest
	}
	plan, err := e.store.LoadRepairPlan(ctx, request.PlanID)
	if err != nil {
		return RepairOperation{}, err
	}
	if err := plan.Validate(); err != nil {
		return RepairOperation{}, ErrRepairPlanNotFound
	}
	if plan.HealthRevision != request.HealthRevision {
		return RepairOperation{}, ErrRepairHealthRevision
	}
	if e.clock.Now().UTC().After(plan.ExpiresAt) {
		return RepairOperation{}, ErrRepairPlanExpired
	}
	if existing, loadErr := e.store.LoadRepairOperationByIdempotency(ctx, request.IdempotencyKey); loadErr == nil {
		if existing.Validate() != nil || existing.PlanID != request.PlanID || existing.HealthRevision != request.HealthRevision {
			return RepairOperation{}, ErrRepairPlanConflict
		}
		if repairPhaseTerminal(existing.Phase) {
			return existing, nil
		}
		return e.resume(ctx, plan, existing, request)
	} else if !errors.Is(loadErr, ErrRepairOperationNotFound) {
		return RepairOperation{}, loadErr
	}
	handler, err := e.registry.Handler(plan.HandlerKind, plan.HandlerVersion)
	if err != nil {
		return RepairOperation{}, err
	}
	now := e.clock.Now().UTC()
	opID, err := domain.NewOperationID(e.ids.NewID())
	if err != nil {
		return RepairOperation{}, err
	}
	operation := RepairOperation{Version: RepairFrameworkVersion, ID: opID, PlanID: plan.ID, HandlerKind: plan.HandlerKind, HandlerVersion: plan.HandlerVersion, HealthRevision: plan.HealthRevision, IdempotencyKey: request.IdempotencyKey, Phase: RepairPhasePrepared, Outcome: RepairOutcomeNone, PreconditionsHash: plan.PreconditionsHash, CreatedAt: now, UpdatedAt: now}
	if err := operation.Validate(); err != nil {
		return RepairOperation{}, err
	}
	if err := e.persistPhase(ctx, &operation, "prepared"); err != nil {
		return RepairOperation{}, err
	}
	return e.run(ctx, handler, plan, operation, request)
}

func (e *RepairExecutor) resume(ctx context.Context, plan RepairPlan, operation RepairOperation, request ExecuteRepair) (RepairOperation, error) {
	handler, err := e.registry.Handler(operation.HandlerKind, operation.HandlerVersion)
	if err != nil {
		return RepairOperation{}, err
	}
	return e.run(ctx, handler, plan, operation, request)
}

func (e *RepairExecutor) run(ctx context.Context, handler RepairHandler, plan RepairPlan, operation RepairOperation, request ExecuteRepair) (RepairOperation, error) {
	proof, err := handler.Revalidate(ctx, plan)
	if err != nil {
		return e.fail(ctx, operation, "revalidation_failed", RepairPhaseFailed, RepairOutcomeFailed, err)
	}
	if err := proof.Validate(); err != nil || proof.PreconditionsHash != plan.PreconditionsHash {
		return e.fail(ctx, operation, "preconditions_changed", RepairPhaseFailed, RepairOutcomeFailed, ErrRepairPreconditions)
	}
	operation.PreconditionsHash = proof.PreconditionsHash
	operation.LockProof = proof.LockProof
	operation.JournalID = proof.JournalID
	if err := e.setPhase(ctx, &operation, RepairPhaseRevalidated, "revalidated"); err != nil {
		return RepairOperation{}, err
	}
	if request.Confirmation != plan.ConfirmationText && request.Confirmation != RepairConfirmationYes {
		return e.fail(ctx, operation, "confirmation_required", RepairPhaseFailed, RepairOutcomeFailed, ErrRepairConfirmation)
	}
	if err := e.setPhase(ctx, &operation, RepairPhaseConfirmed, "confirmed"); err != nil {
		return RepairOperation{}, err
	}
	if err := e.setPhase(ctx, &operation, RepairPhaseExecuting, "executing"); err != nil {
		return RepairOperation{}, err
	}
	effect, err := handler.Execute(ctx, operation, plan)
	if err != nil {
		return e.fail(ctx, operation, "effect_failed", RepairPhaseFailed, RepairOutcomeFailed, err)
	}
	if err := effect.Validate(); err != nil || effect.IdempotencyKey != operation.IdempotencyKey {
		return e.fail(ctx, operation, "effect_identity_invalid", RepairPhaseRepairRequired, RepairOutcomeRepairRequired, ErrInvalidRepairEffect)
	}
	operation.EffectID = effect.EffectID
	if err := e.setPhase(ctx, &operation, RepairPhaseVerifying, "verifying"); err != nil {
		return RepairOperation{}, err
	}
	verification, err := handler.Verify(ctx, operation, plan)
	if err != nil {
		return e.fail(ctx, operation, "verification_failed", RepairPhaseRepairRequired, RepairOutcomeRepairRequired, err)
	}
	if err := verification.Validate(); err != nil {
		return e.fail(ctx, operation, "verification_invalid", RepairPhaseRepairRequired, RepairOutcomeRepairRequired, err)
	}
	operation.PostconditionHash = verification.PostconditionHash
	operation.Outcome = RepairOutcomeSucceeded
	if verification.AlreadyRepaired {
		operation.Outcome = RepairOutcomeAlreadyRepaired
	}
	if err := e.setPhase(ctx, &operation, RepairPhaseSucceeded, "succeeded"); err != nil {
		return RepairOperation{}, err
	}
	return operation, nil
}

func (e *RepairExecutor) setPhase(ctx context.Context, operation *RepairOperation, phase RepairPhase, code string) error {
	operation.Phase = phase
	operation.UpdatedAt = e.clock.Now().UTC()
	if err := operation.Validate(); err != nil {
		return err
	}
	if err := e.store.SaveRepairOperation(ctx, *operation); err != nil {
		return err
	}
	return e.store.AppendRepairAudit(ctx, RepairAuditEntry{OperationID: operation.ID, PlanID: operation.PlanID, Phase: phase, Code: code, At: operation.UpdatedAt})
}

func (e *RepairExecutor) persistPhase(ctx context.Context, operation *RepairOperation, code string) error {
	if err := e.store.SaveRepairOperation(ctx, *operation); err != nil {
		return err
	}
	return e.store.AppendRepairAudit(ctx, RepairAuditEntry{OperationID: operation.ID, PlanID: operation.PlanID, Phase: operation.Phase, Code: code, At: operation.UpdatedAt})
}

func (e *RepairExecutor) fail(ctx context.Context, operation RepairOperation, code string, phase RepairPhase, outcome RepairOutcome, cause error) (RepairOperation, error) {
	operation.ErrorCode = code
	operation.Outcome = outcome
	if err := e.setPhase(ctx, &operation, phase, code); err != nil {
		return RepairOperation{}, err
	}
	return operation, fmt.Errorf("%s: %w", code, cause)
}

// ErrInvalidRepairPlan and related errors are stable input categories for
// planners, handlers, and CLI composition.
var (
	ErrInvalidRepairPlan         = errors.New("invalid repair plan")
	ErrInvalidRepairRequest      = errors.New("invalid repair request")
	ErrInvalidRepairOperation    = errors.New("invalid repair operation")
	ErrInvalidRepairAudit        = errors.New("invalid repair audit")
	ErrInvalidRepairHandler      = errors.New("invalid repair handler")
	ErrInvalidRepairPlanner      = errors.New("invalid repair planner")
	ErrInvalidRepairRevalidation = errors.New("invalid repair revalidation")
	ErrInvalidRepairEffect       = errors.New("invalid repair effect")
	ErrInvalidRepairVerification = errors.New("invalid repair verification")
	ErrRepairPlanExpired         = errors.New("repair plan expired")
)

func repairPhaseTerminal(phase RepairPhase) bool {
	return phase == RepairPhaseSucceeded || phase == RepairPhaseFailed || phase == RepairPhaseRepairRequired
}

func validRepairPhase(phase RepairPhase) bool {
	switch phase {
	case RepairPhasePrepared, RepairPhaseRevalidated, RepairPhaseConfirmed, RepairPhaseExecuting, RepairPhaseVerifying, RepairPhaseSucceeded, RepairPhaseFailed, RepairPhaseRepairRequired:
		return true
	default:
		return false
	}
}

func validRepairOutcome(outcome RepairOutcome) bool {
	return outcome == RepairOutcomeNone || outcome == RepairOutcomeSucceeded || outcome == RepairOutcomeAlreadyRepaired || outcome == RepairOutcomeFailed || outcome == RepairOutcomeRepairRequired
}

func validHealthRevision(value string) bool {
	return validRepairHash(value)
}

func validRepairHash(value string) bool {
	if len(value) != sha256.Size*2 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func validRepairToken(value string, limit int) bool {
	if value == "" || len(value) > limit || !utf8.ValidString(value) || strings.TrimSpace(value) != value {
		return false
	}
	for _, r := range value {
		if unicode.IsControl(r) || unicode.Is(unicode.Bidi_Control, r) {
			return false
		}
	}
	return true
}

func validRepairText(value string, limit int) bool {
	return value != "" && len(value) <= limit && utf8.ValidString(value) && safeHealthText(value)
}
