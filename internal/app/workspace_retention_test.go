package app

import (
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/Scottlr/nudge/internal/domain"
	"github.com/Scottlr/nudge/internal/domain/review"
)

func TestDefaultWorkspaceRetentionPolicy(t *testing.T) {
	policy := DefaultWorkspaceRetentionPolicy()
	if policy.Version != CurrentWorkspaceRetentionPolicyVersion || policy.MinimumAge != DefaultWorkspaceRetentionMinimumAge || policy.CandidatePageSize != MaxWorkspaceRetentionCandidatePage {
		t.Fatalf("default policy = %#v", policy)
	}
	if err := policy.Validate(); err != nil {
		t.Fatalf("default policy validation: %v", err)
	}
}

func TestWorkspaceRetentionPolicyRejectsInvalidBounds(t *testing.T) {
	base := DefaultWorkspaceRetentionPolicy()
	tests := []struct {
		name   string
		policy WorkspaceRetentionPolicy
	}{
		{name: "version", policy: WorkspaceRetentionPolicy{Version: 2, MinimumAge: base.MinimumAge, CandidatePageSize: base.CandidatePageSize}},
		{name: "zero age", policy: WorkspaceRetentionPolicy{Version: base.Version, CandidatePageSize: base.CandidatePageSize}},
		{name: "negative age", policy: WorkspaceRetentionPolicy{Version: base.Version, MinimumAge: -time.Nanosecond, CandidatePageSize: base.CandidatePageSize}},
		{name: "zero page", policy: WorkspaceRetentionPolicy{Version: base.Version, MinimumAge: base.MinimumAge}},
		{name: "page over hard maximum", policy: WorkspaceRetentionPolicy{Version: base.Version, MinimumAge: base.MinimumAge, CandidatePageSize: MaxWorkspaceRetentionCandidatePage + 1}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if !errors.Is(test.policy.Validate(), ErrInvalidWorkspaceRetentionPolicy) {
				t.Fatalf("Validate() error = %v", test.policy.Validate())
			}
		})
	}
}

func TestEvaluateWorkspaceRetentionUsesExactAgeBoundary(t *testing.T) {
	policy := DefaultWorkspaceRetentionPolicy()
	now := time.Date(2026, time.July, 16, 12, 0, 0, 0, time.UTC)
	candidate := validWorkspaceRetentionCandidate(now.Add(-policy.MinimumAge))

	before, err := EvaluateWorkspaceRetention(policy, candidate, now.Add(-time.Nanosecond))
	if err != nil {
		t.Fatalf("before boundary: %v", err)
	}
	if before.Kind != WorkspaceRetentionNotEligible || len(before.Reasons) != 0 || !before.BasisTime.Equal(candidate.BasisTime) {
		t.Fatalf("before boundary = %#v", before)
	}

	at, err := EvaluateWorkspaceRetention(policy, candidate, now)
	if err != nil {
		t.Fatalf("at boundary: %v", err)
	}
	if at.Kind != WorkspaceRetentionEligible || len(at.Reasons) != 0 || !at.EligibleAt.Equal(now) || !at.BasisTime.Equal(candidate.BasisTime) {
		t.Fatalf("at boundary = %#v", at)
	}

	after, err := EvaluateWorkspaceRetention(policy, candidate, now.Add(time.Nanosecond))
	if err != nil {
		t.Fatalf("after boundary: %v", err)
	}
	if after.Kind != WorkspaceRetentionEligible {
		t.Fatalf("after boundary = %#v", after)
	}
}

func TestEvaluateWorkspaceRetentionEveryBlocker(t *testing.T) {
	now := time.Date(2026, time.July, 16, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name   string
		mutate func(*WorkspaceRetentionCandidate)
		want   WorkspaceRetentionBlockReason
	}{
		{name: "unresolved thread", mutate: func(candidate *WorkspaceRetentionCandidate) { candidate.ThreadResolution = review.ResolutionOpen }, want: WorkspaceRetentionBlockUnresolvedThread},
		{name: "nonterminal proposal", mutate: func(candidate *WorkspaceRetentionCandidate) { candidate.ProposalTerminal = false }, want: WorkspaceRetentionBlockNonterminalProposal},
		{name: "generating proposal state", mutate: func(candidate *WorkspaceRetentionCandidate) { candidate.ProposalState = review.ProposalGenerating }, want: WorkspaceRetentionBlockNonterminalProposal},
		{name: "nonterminal apply", mutate: func(candidate *WorkspaceRetentionCandidate) { candidate.ApplyTerminal = false }, want: WorkspaceRetentionBlockNonterminalApply},
		{name: "nonterminal lifecycle", mutate: func(candidate *WorkspaceRetentionCandidate) { candidate.LifecycleTerminal = false }, want: WorkspaceRetentionBlockNonterminalLifecycle},
		{name: "running workspace state", mutate: func(candidate *WorkspaceRetentionCandidate) { candidate.WorkspaceState = review.WorkspaceTurnRunning }, want: WorkspaceRetentionBlockNonterminalLifecycle},
		{name: "repair required", mutate: func(candidate *WorkspaceRetentionCandidate) { candidate.RepairRequired = true }, want: WorkspaceRetentionBlockRepairRequired},
		{name: "repair-required workspace state", mutate: func(candidate *WorkspaceRetentionCandidate) {
			candidate.WorkspaceState = review.WorkspaceRepairRequired
		}, want: WorkspaceRetentionBlockRepairRequired},
		{name: "active lease", mutate: func(candidate *WorkspaceRetentionCandidate) { candidate.ActiveLease = true }, want: WorkspaceRetentionBlockActiveLease},
		{name: "uncertain journal", mutate: func(candidate *WorkspaceRetentionCandidate) { candidate.JournalCertain = false }, want: WorkspaceRetentionBlockJournalUncertain},
		{name: "ownership ambiguity", mutate: func(candidate *WorkspaceRetentionCandidate) { candidate.OwnershipCertain = false }, want: WorkspaceRetentionBlockOwnershipAmbiguous},
		{name: "history ambiguity", mutate: func(candidate *WorkspaceRetentionCandidate) { candidate.HistoryCertain = false }, want: WorkspaceRetentionBlockHistoryAmbiguous},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			candidate := validWorkspaceRetentionCandidate(now.Add(-DefaultWorkspaceRetentionMinimumAge))
			test.mutate(&candidate)
			decision, err := EvaluateWorkspaceRetention(DefaultWorkspaceRetentionPolicy(), candidate, now)
			if err != nil {
				t.Fatalf("EvaluateWorkspaceRetention() error = %v", err)
			}
			if decision.Kind != WorkspaceRetentionNotEligible || !reflect.DeepEqual(decision.Reasons, []WorkspaceRetentionBlockReason{test.want}) {
				t.Fatalf("decision = %#v, want reason %q", decision, test.want)
			}
		})
	}
}

func TestEvaluateWorkspaceRetentionFailsClosedWithoutTrustworthyBasis(t *testing.T) {
	now := time.Date(2026, time.July, 16, 12, 0, 0, 0, time.UTC)
	candidate := validWorkspaceRetentionCandidate(time.Time{})
	decision, err := EvaluateWorkspaceRetention(DefaultWorkspaceRetentionPolicy(), candidate, now)
	if err != nil {
		t.Fatalf("EvaluateWorkspaceRetention() error = %v", err)
	}
	if decision.Kind != WorkspaceRetentionNotEligible || !decision.BasisTime.IsZero() || !decision.EligibleAt.IsZero() || !reflect.DeepEqual(decision.Reasons, []WorkspaceRetentionBlockReason{WorkspaceRetentionBlockHistoryAmbiguous}) {
		t.Fatalf("decision = %#v", decision)
	}
	if err := decision.Validate(DefaultWorkspaceRetentionPolicy()); err != nil {
		t.Fatalf("decision validation: %v", err)
	}
}

func TestEvaluateWorkspaceRetentionOrdersReasonsDeterministically(t *testing.T) {
	now := time.Date(2026, time.July, 16, 12, 0, 0, 0, time.UTC)
	candidate := validWorkspaceRetentionCandidate(now.Add(-DefaultWorkspaceRetentionMinimumAge))
	candidate.ThreadResolution = review.ResolutionOpen
	candidate.ProposalTerminal = false
	candidate.ApplyTerminal = false
	candidate.LifecycleTerminal = false
	candidate.RepairRequired = true
	candidate.ActiveLease = true
	candidate.JournalCertain = false
	candidate.OwnershipCertain = false
	candidate.HistoryCertain = false
	want := []WorkspaceRetentionBlockReason{
		WorkspaceRetentionBlockUnresolvedThread,
		WorkspaceRetentionBlockNonterminalProposal,
		WorkspaceRetentionBlockNonterminalApply,
		WorkspaceRetentionBlockNonterminalLifecycle,
		WorkspaceRetentionBlockRepairRequired,
		WorkspaceRetentionBlockActiveLease,
		WorkspaceRetentionBlockJournalUncertain,
		WorkspaceRetentionBlockOwnershipAmbiguous,
		WorkspaceRetentionBlockHistoryAmbiguous,
	}
	decision, err := EvaluateWorkspaceRetention(DefaultWorkspaceRetentionPolicy(), candidate, now)
	if err != nil {
		t.Fatalf("EvaluateWorkspaceRetention() error = %v", err)
	}
	if !reflect.DeepEqual(decision.Reasons, want) {
		t.Fatalf("reasons = %#v, want %#v", decision.Reasons, want)
	}
}

func TestWorkspaceRetentionValidationRejectsInvalidEvidenceAndReasons(t *testing.T) {
	now := time.Date(2026, time.July, 16, 12, 0, 0, 0, time.UTC)
	base := validWorkspaceRetentionCandidate(now.Add(-DefaultWorkspaceRetentionMinimumAge))
	tests := []struct {
		name   string
		mutate func(*WorkspaceRetentionCandidate)
	}{
		{name: "missing workspace id", mutate: func(candidate *WorkspaceRetentionCandidate) { candidate.WorkspaceID = "" }},
		{name: "missing thread id", mutate: func(candidate *WorkspaceRetentionCandidate) { candidate.ThreadID = "" }},
		{name: "oversized proposal id", mutate: func(candidate *WorkspaceRetentionCandidate) {
			candidate.ProposalID = domain.ProposalID(string(make([]byte, 257)))
		}},
		{name: "control in apply id", mutate: func(candidate *WorkspaceRetentionCandidate) { candidate.ApplyOperationID = "apply\n1" }},
		{name: "invalid resolution", mutate: func(candidate *WorkspaceRetentionCandidate) { candidate.ThreadResolution = "unknown" }},
		{name: "invalid proposal state", mutate: func(candidate *WorkspaceRetentionCandidate) { candidate.ProposalState = "unknown" }},
		{name: "invalid workspace state", mutate: func(candidate *WorkspaceRetentionCandidate) { candidate.WorkspaceState = "unknown" }},
		{name: "missing revision", mutate: func(candidate *WorkspaceRetentionCandidate) { candidate.EvaluatedRevision = 0 }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			candidate := base
			test.mutate(&candidate)
			if !errors.Is(candidate.Validate(), ErrInvalidWorkspaceRetentionCandidate) {
				t.Fatalf("Validate() error = %v", candidate.Validate())
			}
		})
	}

	decision, err := EvaluateWorkspaceRetention(DefaultWorkspaceRetentionPolicy(), base, now)
	if err != nil {
		t.Fatalf("base evaluation: %v", err)
	}
	decision.PolicyVersion = 2
	if !errors.Is(decision.Validate(DefaultWorkspaceRetentionPolicy()), ErrInvalidWorkspaceRetentionDecision) {
		t.Fatal("policy version mismatch was accepted")
	}
	decision = WorkspaceRetentionDecision{
		PolicyVersion:     CurrentWorkspaceRetentionPolicyVersion,
		WorkspaceID:       base.WorkspaceID,
		ThreadID:          base.ThreadID,
		ProposalID:        base.ProposalID,
		ApplyOperationID:  base.ApplyOperationID,
		Kind:              WorkspaceRetentionNotEligible,
		BasisTime:         base.BasisTime,
		EligibleAt:        base.BasisTime.Add(DefaultWorkspaceRetentionMinimumAge),
		EvaluatedRevision: base.EvaluatedRevision,
		Reasons:           []WorkspaceRetentionBlockReason{WorkspaceRetentionBlockActiveLease, WorkspaceRetentionBlockUnresolvedThread},
	}
	if !errors.Is(decision.Validate(DefaultWorkspaceRetentionPolicy()), ErrInvalidWorkspaceRetentionDecision) {
		t.Fatal("non-canonical reason order was accepted")
	}
}

func validWorkspaceRetentionCandidate(basis time.Time) WorkspaceRetentionCandidate {
	return WorkspaceRetentionCandidate{
		RepositoryID:      "repository-1",
		WorktreeID:        "worktree-1",
		SessionID:         "session-1",
		WorkspaceID:       "workspace-1",
		ThreadID:          "thread-1",
		ProposalID:        "proposal-1",
		ApplyOperationID:  "apply-1",
		ThreadResolution:  review.ResolutionResolved,
		ProposalState:     review.ProposalApplied,
		WorkspaceState:    review.WorkspaceReady,
		BasisTime:         basis,
		EvaluatedRevision: 7,
		ProposalTerminal:  true,
		ApplyTerminal:     true,
		LifecycleTerminal: true,
		JournalCertain:    true,
		OwnershipCertain:  true,
		OwnershipDigest:   "digest-1",
		MarkerNonce:       "nonce-1",
		HistoryCertain:    true,
	}
}
