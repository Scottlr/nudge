package app

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Scottlr/nudge/internal/domain"
	"github.com/Scottlr/nudge/internal/domain/repository"
	"github.com/Scottlr/nudge/internal/domain/review"
)

func TestSessionLeaseRepairOwnerFencesAndRetriesIdempotently(t *testing.T) {
	candidate := testSessionLeaseRepairCandidate()
	store := &sessionLeaseRepairStoreFake{candidate: candidate}
	leases := &sessionLeaseRepairLeaseFake{}
	owner, err := NewSessionLeaseRepairOwner(store, leases, sessionLeaseRepairIdentityFake{}, fixedClock{when: time.Date(2026, time.July, 16, 12, 0, 0, 0, time.UTC)})
	if err != nil {
		t.Fatal(err)
	}
	report := HealthReport{HealthRevision: strings.Repeat("a", 64), Results: []HealthResult{{Code: HealthSessionLeaseStale, Severity: HealthWarning, Summary: "candidate"}}}
	plans, err := owner.Plans(context.Background(), report)
	if err != nil || len(plans) != 1 {
		t.Fatalf("plans = %#v, err=%v", plans, err)
	}
	plan := plans[0]
	if _, err := owner.Revalidate(context.Background(), plan); err != nil {
		t.Fatalf("revalidate: %v", err)
	}
	operation := RepairOperation{IdempotencyKey: "repair-key"}
	effect, err := owner.Execute(context.Background(), operation, plan)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	operation.EffectID = effect.EffectID
	verification, err := owner.Verify(context.Background(), operation, plan)
	if err != nil || verification.AlreadyRepaired {
		t.Fatalf("first verification = %#v, err=%v", verification, err)
	}
	duplicate, err := owner.Execute(context.Background(), operation, plan)
	if err != nil {
		t.Fatalf("duplicate execute: %v", err)
	}
	operation.EffectID = duplicate.EffectID
	verification, err = owner.Verify(context.Background(), operation, plan)
	if err != nil || !verification.AlreadyRepaired {
		t.Fatalf("duplicate verification = %#v, err=%v", verification, err)
	}
	if leases.acquires != 2 || store.repairs != 1 {
		t.Fatalf("acquires=%d repairs=%d, want two lock proofs and one CAS", leases.acquires, store.repairs)
	}
}

func TestSessionLeaseRepairOwnerRefusesHeldLock(t *testing.T) {
	candidate := testSessionLeaseRepairCandidate()
	store := &sessionLeaseRepairStoreFake{candidate: candidate}
	leases := &sessionLeaseRepairLeaseFake{err: ErrSessionBusy}
	owner, err := NewSessionLeaseRepairOwner(store, leases, sessionLeaseRepairIdentityFake{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	report := HealthReport{HealthRevision: strings.Repeat("b", 64), Results: []HealthResult{{Code: HealthSessionLeaseStale, Severity: HealthWarning, Summary: "candidate"}}}
	plans, err := owner.Plans(context.Background(), report)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := owner.Revalidate(context.Background(), plans[0]); !errors.Is(err, ErrSessionBusy) {
		t.Fatalf("held lock error = %v, want ErrSessionBusy", err)
	}
	if store.repairs != 0 {
		t.Fatalf("held lock mutated store: %d", store.repairs)
	}
}

func testSessionLeaseRepairCandidate() SessionLeaseRepairCandidate {
	return SessionLeaseRepairCandidate{
		SessionID:       "session-repair",
		RepositoryID:    "repository-repair",
		WorktreeID:      "worktree-repair",
		Key:             review.SessionKey{RepositoryID: "repository-repair", WorktreeID: "worktree-repair", TargetKind: repository.TargetLocal, BaseIdentity: `{"kind":"empty"}`},
		LockIdentity:    strings.Repeat("c", 64),
		LeaseID:         "lease-old",
		FencingToken:    "lease-old",
		WriterEpoch:     7,
		LeaseRevision:   11,
		SessionRevision: 11,
		State:           SessionLeaseStateRecorded,
	}
}

type sessionLeaseRepairStoreFake struct {
	candidate SessionLeaseRepairCandidate
	repairs   int
}

func (s *sessionLeaseRepairStoreFake) ListSessionLeaseRepairCandidates(context.Context) ([]SessionLeaseRepairCandidate, error) {
	return []SessionLeaseRepairCandidate{s.candidate}, nil
}

func (s *sessionLeaseRepairStoreFake) LoadSessionLeaseRepairCandidate(context.Context, domain.ReviewSessionID) (SessionLeaseRepairCandidate, error) {
	return s.candidate, nil
}

func (s *sessionLeaseRepairStoreFake) RepairStaleSessionLease(_ context.Context, request SessionLeaseRepairRequest) (SessionLeaseRepairResult, error) {
	if request.Validate() != nil {
		return SessionLeaseRepairResult{}, ErrSessionLeaseRepairProof
	}
	marker := sessionLeaseRepairMarker(request.PreconditionsHash)
	if s.candidate.State == SessionLeaseStateRepaired && s.candidate.FencingToken == string(marker) {
		return SessionLeaseRepairResult{AlreadyRepaired: true, WriterEpoch: s.candidate.WriterEpoch, SessionRevision: s.candidate.SessionRevision}, nil
	}
	if s.candidate != request.Candidate {
		return SessionLeaseRepairResult{}, ErrRepairPreconditions
	}
	s.repairs++
	s.candidate.State = SessionLeaseStateRepaired
	s.candidate.LeaseID = marker
	s.candidate.FencingToken = string(marker)
	s.candidate.WriterEpoch++
	s.candidate.LeaseRevision++
	s.candidate.SessionRevision++
	return SessionLeaseRepairResult{WriterEpoch: s.candidate.WriterEpoch, SessionRevision: s.candidate.SessionRevision}, nil
}

type sessionLeaseRepairIdentityFake struct{}

func (sessionLeaseRepairIdentityFake) LockIdentity(SessionLeaseRequest) (string, error) {
	return strings.Repeat("c", 64), nil
}

type sessionLeaseRepairLeaseFake struct {
	err      error
	acquires int
}

func (f *sessionLeaseRepairLeaseFake) Acquire(context.Context, SessionLeaseRequest) (SessionLease, error) {
	f.acquires++
	if f.err != nil {
		return nil, f.err
	}
	return sessionLeaseRepairLease{}, nil
}

type sessionLeaseRepairLease struct{}

func (sessionLeaseRepairLease) LeaseID() domain.SessionLeaseID { return "lease-old" }
func (sessionLeaseRepairLease) Close() error                   { return nil }

var _ SessionLeaseRepairStore = (*sessionLeaseRepairStoreFake)(nil)
var _ SessionLeaseManager = (*sessionLeaseRepairLeaseFake)(nil)
