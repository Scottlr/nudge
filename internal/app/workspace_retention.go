package app

import (
	"errors"
	"time"

	"github.com/Scottlr/nudge/internal/domain"
	"github.com/Scottlr/nudge/internal/domain/review"
)

var (
	// ErrInvalidWorkspaceRetentionPolicy reports an invalid retention policy.
	ErrInvalidWorkspaceRetentionPolicy = errors.New("invalid workspace retention policy")
	// ErrInvalidWorkspaceRetentionCandidate reports incomplete or contradictory
	// workspace evidence.
	ErrInvalidWorkspaceRetentionCandidate = errors.New("invalid workspace retention candidate")
	// ErrInvalidWorkspaceRetentionDecision reports a decision that is not bound
	// to one valid policy and candidate evaluation.
	ErrInvalidWorkspaceRetentionDecision = errors.New("invalid workspace retention decision")
)

// WorkspaceRetentionPolicyVersion identifies the retention semantics used by
// an eligibility decision.
type WorkspaceRetentionPolicyVersion uint32

const (
	// CurrentWorkspaceRetentionPolicyVersion is the current retention contract.
	CurrentWorkspaceRetentionPolicyVersion WorkspaceRetentionPolicyVersion = 1
	// DefaultWorkspaceRetentionMinimumAge is the default time before a
	// trustworthy terminal or resolved workspace may be retired.
	DefaultWorkspaceRetentionMinimumAge = 14 * 24 * time.Hour
	// DefaultWorkspaceRetentionCandidatePage is the default candidate page size.
	DefaultWorkspaceRetentionCandidatePage uint32 = 100
	// MaxWorkspaceRetentionCandidatePage is the hard candidate page ceiling.
	MaxWorkspaceRetentionCandidatePage uint32 = 100
)

// WorkspaceRetentionPolicy is the versioned, application-owned retention
// policy. A smaller page is permitted, but a caller cannot raise the v1 page
// ceiling through this contract.
type WorkspaceRetentionPolicy struct {
	Version           WorkspaceRetentionPolicyVersion
	MinimumAge        time.Duration
	CandidatePageSize uint32
}

// DefaultWorkspaceRetentionPolicy returns the conservative v1 defaults.
func DefaultWorkspaceRetentionPolicy() WorkspaceRetentionPolicy {
	return WorkspaceRetentionPolicy{
		Version:           CurrentWorkspaceRetentionPolicyVersion,
		MinimumAge:        DefaultWorkspaceRetentionMinimumAge,
		CandidatePageSize: DefaultWorkspaceRetentionCandidatePage,
	}
}

// Validate checks policy version, positive age, and bounded paging.
func (p WorkspaceRetentionPolicy) Validate() error {
	if p.Version != CurrentWorkspaceRetentionPolicyVersion || p.MinimumAge <= 0 || p.CandidatePageSize == 0 || p.CandidatePageSize > MaxWorkspaceRetentionCandidatePage {
		return ErrInvalidWorkspaceRetentionPolicy
	}
	return nil
}

// WorkspaceRetentionBlockReason is a stable explanation for why a candidate
// cannot be retired automatically.
type WorkspaceRetentionBlockReason string

const (
	WorkspaceRetentionBlockUnresolvedThread     WorkspaceRetentionBlockReason = "unresolved_thread"
	WorkspaceRetentionBlockNonterminalProposal  WorkspaceRetentionBlockReason = "nonterminal_proposal"
	WorkspaceRetentionBlockNonterminalApply     WorkspaceRetentionBlockReason = "nonterminal_apply"
	WorkspaceRetentionBlockNonterminalLifecycle WorkspaceRetentionBlockReason = "nonterminal_lifecycle"
	WorkspaceRetentionBlockRepairRequired       WorkspaceRetentionBlockReason = "repair_required"
	WorkspaceRetentionBlockActiveLease          WorkspaceRetentionBlockReason = "active_lease"
	WorkspaceRetentionBlockJournalUncertain     WorkspaceRetentionBlockReason = "journal_uncertain"
	WorkspaceRetentionBlockOwnershipAmbiguous   WorkspaceRetentionBlockReason = "ownership_ambiguous"
	WorkspaceRetentionBlockHistoryAmbiguous     WorkspaceRetentionBlockReason = "history_ambiguous"
)

var workspaceRetentionBlockReasons = [...]WorkspaceRetentionBlockReason{
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

// Validate checks that a blocking reason belongs to the closed v1 set.
func (r WorkspaceRetentionBlockReason) Validate() error {
	for _, known := range workspaceRetentionBlockReasons {
		if r == known {
			return nil
		}
	}
	return ErrInvalidWorkspaceRetentionDecision
}

// WorkspaceRetentionDecisionKind identifies whether automatic retirement is
// permitted at the evaluated time.
type WorkspaceRetentionDecisionKind string

const (
	WorkspaceRetentionEligible    WorkspaceRetentionDecisionKind = "eligible"
	WorkspaceRetentionNotEligible WorkspaceRetentionDecisionKind = "not_eligible"
)

func (k WorkspaceRetentionDecisionKind) validate() error {
	if k != WorkspaceRetentionEligible && k != WorkspaceRetentionNotEligible {
		return ErrInvalidWorkspaceRetentionDecision
	}
	return nil
}

// WorkspaceRetentionCandidate is bounded, path-free evidence for one managed
// proposal workspace. ProposalID and ApplyOperationID are empty only when the
// corresponding workflow has no durable identity.
type WorkspaceRetentionCandidate struct {
	RepositoryID     domain.RepositoryID
	WorktreeID       domain.WorktreeID
	SessionID        domain.ReviewSessionID
	WorkspaceID      domain.WorkspaceID
	ThreadID         domain.ReviewThreadID
	ProposalID       domain.ProposalID
	ApplyOperationID domain.OperationID

	ThreadResolution review.ResolutionState
	ProposalState    review.ProposalState
	WorkspaceState   review.WorkspaceState

	// BasisTime is the latest trustworthy terminal or resolved timestamp. A
	// zero value means that no trustworthy retention basis exists.
	BasisTime         time.Time
	EvaluatedRevision uint64

	ProposalTerminal  bool
	ApplyTerminal     bool
	LifecycleTerminal bool
	RepairRequired    bool
	ActiveLease       bool
	JournalCertain    bool
	OwnershipCertain  bool
	OwnershipDigest   string
	MarkerNonce       string
	HistoryCertain    bool
}

// Validate checks identities, bounded state, revision, and timestamp shape.
func (c WorkspaceRetentionCandidate) Validate() error {
	if !validWorkspaceRetentionRequiredID(string(c.RepositoryID)) || !validWorkspaceRetentionRequiredID(string(c.WorktreeID)) || !validWorkspaceRetentionRequiredID(string(c.SessionID)) || !validWorkspaceRetentionRequiredID(string(c.WorkspaceID)) || !validWorkspaceRetentionRequiredID(string(c.ThreadID)) ||
		!validWorkspaceRetentionOptionalID(string(c.ProposalID)) || !validWorkspaceRetentionOptionalID(string(c.ApplyOperationID)) ||
		c.ThreadResolution.Validate() != nil || c.ProposalState.Validate() != nil || c.WorkspaceState.Validate() != nil ||
		c.EvaluatedRevision == 0 || c.OwnershipCertain && (c.OwnershipDigest == "" || c.MarkerNonce == "") {
		return ErrInvalidWorkspaceRetentionCandidate
	}
	return nil
}

// WorkspaceRetentionDecision is the immutable result of evaluating one
// candidate against one retention policy. It retains only stable identities,
// bounded state, timestamps, and safe reason codes.
type WorkspaceRetentionDecision struct {
	PolicyVersion    WorkspaceRetentionPolicyVersion
	WorkspaceID      domain.WorkspaceID
	ThreadID         domain.ReviewThreadID
	ProposalID       domain.ProposalID
	ApplyOperationID domain.OperationID

	Kind              WorkspaceRetentionDecisionKind
	BasisTime         time.Time
	EligibleAt        time.Time
	EvaluatedRevision uint64
	Reasons           []WorkspaceRetentionBlockReason
}

// Validate checks policy binding, timestamps, reason membership, and the
// canonical reason order.
func (d WorkspaceRetentionDecision) Validate(policy WorkspaceRetentionPolicy) error {
	if policy.Validate() != nil || d.PolicyVersion != policy.Version ||
		!validWorkspaceRetentionRequiredID(string(d.WorkspaceID)) || !validWorkspaceRetentionRequiredID(string(d.ThreadID)) ||
		!validWorkspaceRetentionOptionalID(string(d.ProposalID)) || !validWorkspaceRetentionOptionalID(string(d.ApplyOperationID)) ||
		d.Kind.validate() != nil || d.EvaluatedRevision == 0 || !workspaceRetentionReasonsAreOrdered(d.Reasons) {
		return ErrInvalidWorkspaceRetentionDecision
	}
	if d.BasisTime.IsZero() {
		if !d.EligibleAt.IsZero() || d.Kind != WorkspaceRetentionNotEligible || !containsWorkspaceRetentionReason(d.Reasons, WorkspaceRetentionBlockHistoryAmbiguous) {
			return ErrInvalidWorkspaceRetentionDecision
		}
	} else {
		eligibleAt := d.BasisTime.Add(policy.MinimumAge)
		if eligibleAt.IsZero() || eligibleAt.Before(d.BasisTime) || !d.EligibleAt.Equal(eligibleAt) {
			return ErrInvalidWorkspaceRetentionDecision
		}
	}
	if d.Kind == WorkspaceRetentionEligible && len(d.Reasons) != 0 {
		return ErrInvalidWorkspaceRetentionDecision
	}
	return nil
}

// EvaluateWorkspaceRetention applies the exact age boundary after all
// application-owned blockers have been checked. Equality with EligibleAt is
// eligible; a missing basis remains conservatively ineligible.
func EvaluateWorkspaceRetention(policy WorkspaceRetentionPolicy, candidate WorkspaceRetentionCandidate, now time.Time) (WorkspaceRetentionDecision, error) {
	if policy.Validate() != nil || candidate.Validate() != nil || now.IsZero() {
		return WorkspaceRetentionDecision{}, ErrInvalidWorkspaceRetentionCandidate
	}

	decision := WorkspaceRetentionDecision{
		PolicyVersion:     policy.Version,
		WorkspaceID:       candidate.WorkspaceID,
		ThreadID:          candidate.ThreadID,
		ProposalID:        candidate.ProposalID,
		ApplyOperationID:  candidate.ApplyOperationID,
		Kind:              WorkspaceRetentionNotEligible,
		BasisTime:         candidate.BasisTime,
		EvaluatedRevision: candidate.EvaluatedRevision,
	}
	if !candidate.BasisTime.IsZero() {
		decision.EligibleAt = candidate.BasisTime.Add(policy.MinimumAge)
		if decision.EligibleAt.IsZero() || decision.EligibleAt.Before(candidate.BasisTime) {
			return WorkspaceRetentionDecision{}, ErrInvalidWorkspaceRetentionCandidate
		}
	}

	reasons := make([]WorkspaceRetentionBlockReason, 0, len(workspaceRetentionBlockReasons))
	if candidate.ThreadResolution != review.ResolutionResolved {
		reasons = append(reasons, WorkspaceRetentionBlockUnresolvedThread)
	}
	if !candidate.ProposalTerminal || workspaceRetentionProposalIsNonterminal(candidate.ProposalState) {
		reasons = append(reasons, WorkspaceRetentionBlockNonterminalProposal)
	}
	if !candidate.ApplyTerminal {
		reasons = append(reasons, WorkspaceRetentionBlockNonterminalApply)
	}
	if !candidate.LifecycleTerminal || workspaceRetentionLifecycleIsNonterminal(candidate.WorkspaceState) {
		reasons = append(reasons, WorkspaceRetentionBlockNonterminalLifecycle)
	}
	if candidate.RepairRequired || candidate.WorkspaceState == review.WorkspaceRepairRequired {
		reasons = append(reasons, WorkspaceRetentionBlockRepairRequired)
	}
	if candidate.ActiveLease {
		reasons = append(reasons, WorkspaceRetentionBlockActiveLease)
	}
	if !candidate.JournalCertain {
		reasons = append(reasons, WorkspaceRetentionBlockJournalUncertain)
	}
	if !candidate.OwnershipCertain {
		reasons = append(reasons, WorkspaceRetentionBlockOwnershipAmbiguous)
	}
	if !candidate.HistoryCertain || candidate.BasisTime.IsZero() {
		reasons = append(reasons, WorkspaceRetentionBlockHistoryAmbiguous)
	}
	decision.Reasons = reasons
	if len(reasons) == 0 && !now.Before(decision.EligibleAt) {
		decision.Kind = WorkspaceRetentionEligible
	}
	if err := decision.Validate(policy); err != nil {
		return WorkspaceRetentionDecision{}, err
	}
	return decision, nil
}

func workspaceRetentionReasonsAreOrdered(reasons []WorkspaceRetentionBlockReason) bool {
	previous := -1
	for _, reason := range reasons {
		if reason.Validate() != nil {
			return false
		}
		rank := workspaceRetentionReasonRank(reason)
		if rank <= previous {
			return false
		}
		previous = rank
	}
	return true
}

func workspaceRetentionReasonRank(reason WorkspaceRetentionBlockReason) int {
	for rank, known := range workspaceRetentionBlockReasons {
		if reason == known {
			return rank
		}
	}
	return -1
}

func containsWorkspaceRetentionReason(reasons []WorkspaceRetentionBlockReason, wanted WorkspaceRetentionBlockReason) bool {
	for _, reason := range reasons {
		if reason == wanted {
			return true
		}
	}
	return false
}

func validWorkspaceRetentionRequiredID(value string) bool {
	return value != "" && validWorkspaceRetentionOptionalID(value)
}

func validWorkspaceRetentionOptionalID(value string) bool {
	return value == "" || len(value) <= 256 && stableText(value)
}

func workspaceRetentionProposalIsNonterminal(state review.ProposalState) bool {
	switch state {
	case review.ProposalGenerating, review.ProposalReady, review.ProposalApplying:
		return true
	default:
		return false
	}
}

func workspaceRetentionLifecycleIsNonterminal(state review.WorkspaceState) bool {
	switch state {
	case review.WorkspaceCreating, review.WorkspaceTurnRunning, review.WorkspaceResultReady, review.WorkspaceResetting:
		return true
	default:
		return false
	}
}
