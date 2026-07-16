package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/Scottlr/nudge/internal/domain"
	"github.com/Scottlr/nudge/internal/domain/review"
)

const (
	SessionLeaseRepairPolicyVersion  uint64 = 1
	SessionLeaseRepairHandlerKind           = RepairHandlerKind("session-lease.stale")
	SessionLeaseRepairHandlerVersion        = "v1"
	SessionLeaseStateRecorded               = "writer_recorded"
	SessionLeaseStateRepaired               = "terminal_fenced"
)

var (
	ErrSessionLeaseRepair      = errors.New("session lease repair rejected")
	ErrSessionLeaseRepairProof = errors.New("session lease repair proof invalid")
)

// SessionLeaseRepairCandidate is bounded evidence read from one durable
// session row. It never treats age, PID, hostname, or a process list as proof.
type SessionLeaseRepairCandidate struct {
	SessionID       domain.ReviewSessionID
	RepositoryID    domain.RepositoryID
	WorktreeID      domain.WorktreeID
	Key             review.SessionKey
	LockIdentity    string
	Distinct        bool
	LeaseID         domain.SessionLeaseID
	FencingToken    string
	WriterEpoch     uint64
	LeaseRevision   uint64
	SessionRevision uint64
	State           string
}

func (c SessionLeaseRepairCandidate) Validate() error {
	if c.SessionID == "" || c.RepositoryID == "" || c.Key.Validate() != nil || c.Key.RepositoryID != c.RepositoryID || c.Key.WorktreeID != c.WorktreeID || !ValidateSessionLeaseLockIdentity(c.LockIdentity) || c.LeaseID == "" || c.FencingToken == "" || c.FencingToken != string(c.LeaseID) || c.WriterEpoch == 0 || c.LeaseRevision == 0 || c.SessionRevision == 0 || c.LeaseRevision != c.SessionRevision {
		return ErrSessionLeaseRepairProof
	}
	if c.State != SessionLeaseStateRecorded && c.State != SessionLeaseStateRepaired {
		return ErrSessionLeaseRepairProof
	}
	return nil
}

// ValidateSessionLeaseLockIdentity validates the stable path-free lock
// identity returned by the native lease adapter.
func ValidateSessionLeaseLockIdentity(value string) bool {
	if len(value) != sha256.Size*2 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

// SessionLeaseRepairStore is the consumer-owned persistence seam for T100.
// It exposes no SQL and performs the fenced transition in one transaction.
type SessionLeaseRepairStore interface {
	ListSessionLeaseRepairCandidates(context.Context) ([]SessionLeaseRepairCandidate, error)
	LoadSessionLeaseRepairCandidate(context.Context, domain.ReviewSessionID) (SessionLeaseRepairCandidate, error)
	RepairStaleSessionLease(context.Context, SessionLeaseRepairRequest) (SessionLeaseRepairResult, error)
}

// SessionLeaseRepairRequest binds the one compare-and-swap repair transition.
type SessionLeaseRepairRequest struct {
	Candidate         SessionLeaseRepairCandidate
	PreconditionsHash string
	RepairLeaseID     domain.SessionLeaseID
}

func (r SessionLeaseRepairRequest) Validate() error {
	if r.Candidate.Validate() != nil || r.Candidate.State != SessionLeaseStateRecorded || !validRepairHash(r.PreconditionsHash) || r.RepairLeaseID != sessionLeaseRepairMarker(r.PreconditionsHash) {
		return ErrSessionLeaseRepairProof
	}
	return nil
}

// SessionLeaseRepairResult confirms the durable transition while the native
// lock is still held by the owner handler.
type SessionLeaseRepairResult struct {
	AlreadyRepaired bool
	WriterEpoch     uint64
	SessionRevision uint64
}

// SessionLeaseRepairOwner is the T100 planner and T058 owner handler.
type SessionLeaseRepairOwner struct {
	store      SessionLeaseRepairStore
	leases     SessionLeaseManager
	identities SessionLeaseIdentityProvider
	clock      Clock
}

func NewSessionLeaseRepairOwner(store SessionLeaseRepairStore, leases SessionLeaseManager, identities SessionLeaseIdentityProvider, clock Clock) (*SessionLeaseRepairOwner, error) {
	if store == nil || leases == nil || identities == nil {
		return nil, ErrSessionLeaseRepair
	}
	if clock == nil {
		clock = SystemClock{}
	}
	return &SessionLeaseRepairOwner{store: store, leases: leases, identities: identities, clock: clock}, nil
}

// RegisterSessionLeaseRepairOwner registers the exact T049 finding and T058
// handler. Candidate health is advisory; the OS lock and CAS are authoritative.
func RegisterSessionLeaseRepairOwner(registry *RepairRegistry, owner *SessionLeaseRepairOwner) error {
	if registry == nil || owner == nil {
		return ErrSessionLeaseRepair
	}
	if err := registry.RegisterHandler(owner); err != nil {
		return err
	}
	return registry.RegisterPlanner(HealthSessionLeaseStale, owner)
}

func (o *SessionLeaseRepairOwner) Kind() RepairHandlerKind { return SessionLeaseRepairHandlerKind }
func (o *SessionLeaseRepairOwner) Version() string         { return SessionLeaseRepairHandlerVersion }

func (o *SessionLeaseRepairOwner) Plans(ctx context.Context, report HealthReport) ([]RepairPlan, error) {
	if o == nil || ctx == nil || !validHealthRevision(report.HealthRevision) {
		return nil, ErrSessionLeaseRepair
	}
	candidates, err := o.store.ListSessionLeaseRepairCandidates(ctx)
	if err != nil {
		return nil, err
	}
	now := o.clock.Now().UTC()
	plans := make([]RepairPlan, 0, len(candidates))
	for _, candidate := range candidates {
		if err := candidate.Validate(); err != nil || candidate.State != SessionLeaseStateRecorded {
			return nil, ErrSessionLeaseRepairProof
		}
		lockIdentity, err := o.lockIdentity(candidate)
		if err != nil || lockIdentity != candidate.LockIdentity {
			return nil, ErrRepairPreconditions
		}
		candidate.LockIdentity = lockIdentity
		preconditions, err := sessionLeasePreconditionHash(candidate)
		if err != nil {
			return nil, err
		}
		plan := RepairPlan{
			ID:                RepairPlanID("session-lease-stale-" + string(candidate.SessionID)),
			HealthCode:        HealthSessionLeaseStale,
			HealthRevision:    report.HealthRevision,
			PolicyVersion:     SessionLeaseRepairPolicyVersion,
			Summary:           "A durable Nudge session lease requires explicit lock proof before repair.",
			Effect:            "Fence one inactive session writer after its exact native lock is proven free.",
			OwnedResourceRefs: []string{"session-lease:" + string(candidate.SessionID)},
			PreconditionsHash: preconditions,
			ConfirmationText:  "repair stale session lease",
			HandlerKind:       SessionLeaseRepairHandlerKind,
			HandlerVersion:    SessionLeaseRepairHandlerVersion,
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

func (o *SessionLeaseRepairOwner) Revalidate(ctx context.Context, plan RepairPlan) (RepairRevalidation, error) {
	candidate, err := o.loadCandidate(ctx, plan)
	if err != nil {
		return RepairRevalidation{}, err
	}
	if o.isTerminalCandidate(plan, candidate) {
		return o.revalidation(plan, candidate.LockIdentity), nil
	}
	if err := o.requireCandidate(plan, candidate); err != nil {
		return RepairRevalidation{}, err
	}
	lock, err := o.leases.Acquire(ctx, SessionLeaseRequest{Key: candidate.Key, SessionID: candidate.SessionID, LeaseID: candidate.LeaseID, Distinct: candidate.Distinct})
	if err != nil {
		return RepairRevalidation{}, err
	}
	defer lock.Close()
	return o.revalidation(plan, candidate.LockIdentity), nil
}

func (o *SessionLeaseRepairOwner) Execute(ctx context.Context, operation RepairOperation, plan RepairPlan) (RepairEffect, error) {
	candidate, err := o.loadCandidate(ctx, plan)
	if err != nil {
		return RepairEffect{}, err
	}
	marker := sessionLeaseRepairMarker(plan.PreconditionsHash)
	if o.isTerminalCandidate(plan, candidate) {
		return RepairEffect{EffectID: sessionLeaseAlreadyEffectID(candidate.SessionID), IdempotencyKey: operation.IdempotencyKey}, nil
	}
	if err := o.requireCandidate(plan, candidate); err != nil {
		return RepairEffect{}, err
	}
	lock, err := o.leases.Acquire(ctx, SessionLeaseRequest{Key: candidate.Key, SessionID: candidate.SessionID, LeaseID: candidate.LeaseID, Distinct: candidate.Distinct})
	if err != nil {
		return RepairEffect{}, err
	}
	defer lock.Close()
	current, err := o.store.LoadSessionLeaseRepairCandidate(ctx, candidate.SessionID)
	if err != nil {
		return RepairEffect{}, err
	}
	if err := o.requireCandidate(plan, current); err != nil {
		return RepairEffect{}, err
	}
	result, err := o.store.RepairStaleSessionLease(ctx, SessionLeaseRepairRequest{Candidate: current, PreconditionsHash: plan.PreconditionsHash, RepairLeaseID: marker})
	if err != nil {
		return RepairEffect{}, err
	}
	if result.WriterEpoch != candidate.WriterEpoch+1 || result.SessionRevision != candidate.SessionRevision+1 {
		return RepairEffect{}, ErrSessionLeaseRepairProof
	}
	effectID := sessionLeaseEffectID(candidate.SessionID)
	if result.AlreadyRepaired {
		effectID = sessionLeaseAlreadyEffectID(candidate.SessionID)
	}
	return RepairEffect{EffectID: effectID, IdempotencyKey: operation.IdempotencyKey}, nil
}

func (o *SessionLeaseRepairOwner) Verify(ctx context.Context, operation RepairOperation, plan RepairPlan) (RepairVerification, error) {
	candidate, err := o.loadCandidate(ctx, plan)
	if err != nil {
		return RepairVerification{}, err
	}
	if !o.isTerminalCandidate(plan, candidate) {
		return RepairVerification{}, ErrSessionLeaseRepairProof
	}
	postcondition, err := sessionLeasePostconditionHash(candidate)
	if err != nil {
		return RepairVerification{}, err
	}
	return RepairVerification{PostconditionHash: postcondition, AlreadyRepaired: operation.EffectID == sessionLeaseAlreadyEffectID(candidate.SessionID)}, nil
}

func (o *SessionLeaseRepairOwner) loadCandidate(ctx context.Context, plan RepairPlan) (SessionLeaseRepairCandidate, error) {
	if o == nil || ctx == nil || plan.Validate() != nil || plan.HandlerKind != SessionLeaseRepairHandlerKind || plan.HandlerVersion != SessionLeaseRepairHandlerVersion || len(plan.OwnedResourceRefs) != 1 || !strings.HasPrefix(plan.OwnedResourceRefs[0], "session-lease:") {
		return SessionLeaseRepairCandidate{}, ErrSessionLeaseRepair
	}
	id := strings.TrimPrefix(plan.OwnedResourceRefs[0], "session-lease:")
	if id == "" {
		return SessionLeaseRepairCandidate{}, ErrSessionLeaseRepair
	}
	candidate, err := o.store.LoadSessionLeaseRepairCandidate(ctx, domain.ReviewSessionID(id))
	if err != nil {
		return SessionLeaseRepairCandidate{}, err
	}
	if candidate.Validate() != nil || candidate.SessionID != domain.ReviewSessionID(id) {
		return SessionLeaseRepairCandidate{}, ErrSessionLeaseRepairProof
	}
	return candidate, nil
}

func (o *SessionLeaseRepairOwner) lockIdentity(candidate SessionLeaseRepairCandidate) (string, error) {
	return o.identities.LockIdentity(SessionLeaseRequest{Key: candidate.Key, SessionID: candidate.SessionID, LeaseID: candidate.LeaseID, Distinct: candidate.Distinct})
}

func (o *SessionLeaseRepairOwner) requireCandidate(plan RepairPlan, candidate SessionLeaseRepairCandidate) error {
	if candidate.State != SessionLeaseStateRecorded || candidate.LockIdentity == "" {
		return ErrRepairPreconditions
	}
	identity, err := o.lockIdentity(candidate)
	if err != nil || identity != candidate.LockIdentity {
		return ErrRepairPreconditions
	}
	hash, err := sessionLeasePreconditionHash(candidate)
	if err != nil || hash != plan.PreconditionsHash {
		return ErrRepairPreconditions
	}
	return nil
}

func (o *SessionLeaseRepairOwner) isTerminalCandidate(plan RepairPlan, candidate SessionLeaseRepairCandidate) bool {
	if candidate.State != SessionLeaseStateRepaired || candidate.FencingToken != string(sessionLeaseRepairMarker(plan.PreconditionsHash)) {
		return false
	}
	identity, err := o.lockIdentity(candidate)
	return err == nil && identity == candidate.LockIdentity
}

func (o *SessionLeaseRepairOwner) revalidation(plan RepairPlan, lockIdentity string) RepairRevalidation {
	return RepairRevalidation{PreconditionsHash: plan.PreconditionsHash, LockProof: "session-lease-lock:" + lockIdentity, JournalID: "session-lease-repair:" + string(plan.ID)}
}

func sessionLeasePreconditionHash(candidate SessionLeaseRepairCandidate) (string, error) {
	value, err := json.Marshal(candidate)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(value)
	return hex.EncodeToString(digest[:]), nil
}

func sessionLeasePostconditionHash(candidate SessionLeaseRepairCandidate) (string, error) {
	value, err := json.Marshal(struct {
		SessionID       domain.ReviewSessionID
		LockIdentity    string
		WriterEpoch     uint64
		SessionRevision uint64
		FencingToken    string
	}{candidate.SessionID, candidate.LockIdentity, candidate.WriterEpoch, candidate.SessionRevision, candidate.FencingToken})
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(value)
	return hex.EncodeToString(digest[:]), nil
}

func sessionLeaseRepairMarker(preconditions string) domain.SessionLeaseID {
	return domain.SessionLeaseID("repair-stale-" + preconditions)
}

func sessionLeaseEffectID(sessionID domain.ReviewSessionID) string {
	return "session-lease-effect:" + string(sessionID)
}

func sessionLeaseAlreadyEffectID(sessionID domain.ReviewSessionID) string {
	return sessionLeaseEffectID(sessionID) + ":already"
}

var _ RepairHandler = (*SessionLeaseRepairOwner)(nil)
