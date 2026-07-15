package app

import (
	"errors"
	"fmt"
	"strings"

	"github.com/Scottlr/nudge/internal/domain"
	"github.com/Scottlr/nudge/internal/domain/repository"
)

var ErrProposalTargetUnavailable = errors.New("proposal target unavailable")

// ProposalEligibilityReason is a stable explanation for why Request change
// is unavailable for one frozen target generation.
type ProposalEligibilityReason string

const (
	ProposalEligible               ProposalEligibilityReason = "eligible"
	ProposalTargetInvalid          ProposalEligibilityReason = "target_invalid"
	ProposalTargetKindReadOnly     ProposalEligibilityReason = "target_kind_read_only"
	ProposalHistoricalTarget       ProposalEligibilityReason = "historical_target"
	ProposalDestinationUnavailable ProposalEligibilityReason = "destination_unavailable"
	ProposalNonCurrentWorktree     ProposalEligibilityReason = "non_current_worktree"
	ProposalHeadMoved              ProposalEligibilityReason = "head_moved"
	ProposalObjectsUnavailable     ProposalEligibilityReason = "objects_unavailable"
	ProposalIsolationUnavailable   ProposalEligibilityReason = "isolation_unavailable"
)

// ProposalEligibility is the bounded target/destination decision consumed by
// Request change. It never grants provider permissions or application rights.
type ProposalEligibility struct {
	Eligible     bool
	Reason       ProposalEligibilityReason
	Message      string
	TargetKind   repository.TargetKind
	WorktreeID   domain.WorktreeID
	ExpectedHead repository.ObjectID
	ObservedHead repository.ObjectID
}

func (e ProposalEligibility) Validate() error {
	if e.Eligible {
		if e.Reason != ProposalEligible || e.Message != "" || e.TargetKind != repository.TargetCommit && e.TargetKind != repository.TargetBranch || e.WorktreeID == "" || e.ExpectedHead == "" || e.ObservedHead != e.ExpectedHead {
			return ErrProposalTargetUnavailable
		}
		return nil
	}
	if e.Reason == ProposalEligible || e.Message == "" || e.TargetKind == "" || !safeText(e.Message) {
		return ErrProposalTargetUnavailable
	}
	return nil
}

// CanPropose determines whether one frozen branch/commit target can enter the
// proposal workspace for the supplied current destination evidence.
func CanPropose(target repository.ResolvedTarget, worktree repository.WorktreeRef, objectsAvailable, isolationSupported bool) ProposalEligibility {
	result := ProposalEligibility{TargetKind: target.Spec.Kind, WorktreeID: worktree.ID, ExpectedHead: target.Head.ObjectID, ObservedHead: worktree.CurrentObjectID}
	fail := func(reason ProposalEligibilityReason, message string) ProposalEligibility {
		result.Reason = reason
		result.Message = message
		return result
	}
	if target.Validate() != nil || worktree.Validate() != nil {
		return fail(ProposalTargetInvalid, "the frozen target or destination worktree is invalid")
	}
	if target.Spec.Kind != repository.TargetCommit && target.Spec.Kind != repository.TargetBranch {
		return fail(ProposalTargetKindReadOnly, "only branch and commit targets can enter this proposal path")
	}
	if !target.Editable {
		return fail(ProposalHistoricalTarget, "the reviewed target is not the current checked-out HEAD")
	}
	if target.EditDestination == nil || *target.EditDestination != worktree.ID {
		return fail(ProposalDestinationUnavailable, "the target has no matching editable destination worktree")
	}
	if target.Spec.Kind == repository.TargetBranch {
		if worktree.Detached || worktree.BranchName == "" || target.BranchRef != "refs/heads/"+worktree.BranchName {
			return fail(ProposalNonCurrentWorktree, "the reviewed branch is not the checked-out current branch")
		}
	}
	if worktree.CurrentObjectID == "" || worktree.CurrentObjectID != target.Head.ObjectID {
		return fail(ProposalHeadMoved, "the destination HEAD moved since the target was reviewed")
	}
	if !objectsAvailable {
		return fail(ProposalObjectsUnavailable, "the pinned target objects are unavailable")
	}
	if !isolationSupported {
		return fail(ProposalIsolationUnavailable, "proposal workspace isolation is unavailable")
	}
	result.Eligible = true
	result.Reason = ProposalEligible
	return result
}

// ProposalTargetBaselineRequest binds a writable proposal baseline to the
// exact pinned object snapshot and the current editable destination proof.
type ProposalTargetBaselineRequest struct {
	Target             repository.ResolvedTarget
	Snapshot           ReviewSnapshot
	Worktree           repository.WorktreeRef
	ResourcePolicy     ResourcePolicy
	ObjectsAvailable   bool
	IsolationSupported bool
}

func (r ProposalTargetBaselineRequest) Eligibility() ProposalEligibility {
	return CanPropose(r.Target, r.Worktree, r.ObjectsAvailable, r.IsolationSupported)
}

func (r ProposalTargetBaselineRequest) Validate() error {
	if r.ResourcePolicy.Validate() != nil || r.Target.Validate() != nil || r.Worktree.Validate() != nil || r.Target.Spec.Kind != repository.TargetCommit && r.Target.Spec.Kind != repository.TargetBranch || r.Snapshot.Validate() != nil || r.Snapshot.State != ReviewSnapshotReady || r.Snapshot.HeadObjectID != r.Target.Head.ObjectID || r.Snapshot.ObjectFormat == "" {
		return ErrProposalTargetUnavailable
	}
	if eligibility := r.Eligibility(); !eligibility.Eligible {
		return fmt.Errorf("%w: %s", ErrProposalTargetUnavailable, eligibility.Message)
	}
	return nil
}

// ProposalTargetLabel returns the bounded reason text suitable for a
// read-only Request change control.
func ProposalTargetLabel(eligibility ProposalEligibility) string {
	if eligibility.Eligible {
		return "Request change"
	}
	if eligibility.Message == "" {
		return "Request change unavailable"
	}
	return "Request change unavailable: " + strings.TrimSpace(eligibility.Message)
}
