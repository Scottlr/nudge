package app

import (
	"errors"
	"time"

	"github.com/Scottlr/nudge/internal/domain"
	"github.com/Scottlr/nudge/internal/domain/repository"
	"github.com/Scottlr/nudge/internal/domain/review"
)

// StaleReason is the application-facing name for the bounded domain stale
// reason vocabulary. Keeping the alias here lets validity consumers avoid
// depending on the review package's storage details.
type StaleReason = review.StaleReason

const (
	StaleReasonValid                     = review.StaleReasonValid
	StaleReasonPathPreconditionChanged   = review.StaleReasonPathPreconditionChanged
	StaleReasonDestinationKindChanged    = review.StaleReasonDestinationKindChanged
	StaleReasonDestinationChanged        = review.StaleReasonDestinationChanged
	StaleReasonTargetHeadChanged         = review.StaleReasonTargetHeadChanged
	StaleReasonTargetGenerationChanged   = review.StaleReasonTargetGenerationChanged
	StaleReasonProposalSuperseded        = review.StaleReasonProposalSuperseded
	StaleReasonProposalApplied           = review.StaleReasonProposalApplied
	StaleReasonWorkspaceBaselineMismatch = review.StaleReasonWorkspaceBaselineMismatch
	StaleReasonIsolationLost             = review.StaleReasonIsolationLost
	StaleReasonUnsupportedCapability     = review.StaleReasonUnsupportedCapability
	StaleReasonAnchorNeedsConfirmation   = review.StaleReasonAnchorNeedsConfirmation
)

var (
	// ErrProposalValidityReason reports a missing or non-stale validity code.
	ErrProposalValidityReason = errors.New("invalid proposal validity reason")
	// ErrProposalValidityTransition reports an attempted rewrite of immutable
	// stale evidence with a different reason.
	ErrProposalValidityTransition = errors.New("invalid proposal validity transition")
)

// MarkProposalStale transitions one immutable version to stale while
// retaining its patch bytes, source generation, and original transcript.
// Repeating the same transition is idempotent; changing an existing stale
// reason is rejected because stale evidence is part of the version history.
func MarkProposalStale(patch *review.ProposedPatch, reason StaleReason, at time.Time) error {
	if patch == nil || patch.Validate() != nil || reason.Validate() != nil || !reason.IsStale() || at.IsZero() || at.Before(patch.CreatedAt) {
		return ErrProposalValidityReason
	}
	switch patch.Status {
	case review.ProposalVersionStale:
		if patch.StatusReason != "" && patch.StatusReason != string(reason) {
			return ErrProposalValidityTransition
		}
		return nil
	case review.ProposalVersionDeriving, review.ProposalVersionReady, review.ProposalVersionApplying:
		patch.Status = review.ProposalVersionStale
		patch.StatusReason = string(reason)
		patch.StatusChangedAt = &at
		return nil
	default:
		return ErrProposalValidityTransition
	}
}

// evaluateProposalValidity evaluates only the destination constraints that
// can invalidate a version. Unrelated local changes are intentionally not a
// reason to stale a proposal; every persisted touched-path precondition is
// checked independently.
func evaluateProposalValidity(candidate ProposalValidityCandidate, destination PostApplyDestinationState, operationID domain.OperationID, generation repository.TargetGeneration) ProposalValidityResult {
	result := ProposalValidityResult{
		ApplyOperationID: operationID,
		Generation:       generation,
		ProposalID:       candidate.ProposalID,
		Version:          candidate.Version,
		ExpectedStatus:   candidate.Status,
		Outcome:          ProposalValidityValid,
		Reason:           StaleReasonValid,
		EvidenceBytes:    1,
	}
	if candidate.Destination.WorktreeID != destination.WorktreeID || candidate.Destination.TargetKind != destination.TargetKind {
		result.Outcome = ProposalValidityStale
		result.Reason = StaleReasonDestinationKindChanged
		return result
	}
	if destination.TargetKind != repository.TargetLocal && candidate.Destination.ExpectedHead != destination.Head {
		result.Outcome = ProposalValidityStale
		result.Reason = StaleReasonTargetHeadChanged
		return result
	}
	current := make(map[repository.RepoPathKey]repository.PathPrecondition, len(destination.Paths))
	for _, value := range destination.Paths {
		current[value.Path.Key()] = value
	}
	for _, expected := range candidate.Preconditions {
		actual, ok := current[expected.Path.Key()]
		if !ok || !preconditionMatchesExpected(actual, expected) {
			path := repository.RepoPath(expected.Path.Bytes())
			result.Outcome = ProposalValidityStale
			result.Reason = StaleReasonPathPreconditionChanged
			result.ConflictPath = &path
			result.EvidenceBytes += ByteSize(len(path.Bytes()))
			return result
		}
	}
	return result
}
