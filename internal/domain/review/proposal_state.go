package review

import "errors"

// WorkspaceState identifies the isolated proposal workspace lifecycle.
type WorkspaceState string

const (
	WorkspaceCreating       WorkspaceState = "creating"
	WorkspaceReady          WorkspaceState = "ready"
	WorkspaceTurnRunning    WorkspaceState = "turn_running"
	WorkspaceResultReady    WorkspaceState = "result_ready"
	WorkspaceResetting      WorkspaceState = "resetting"
	WorkspaceRepairRequired WorkspaceState = "repair_required"
	WorkspaceRemoved        WorkspaceState = "removed"
)

func (s WorkspaceState) Validate() error {
	switch s {
	case WorkspaceCreating, WorkspaceReady, WorkspaceTurnRunning, WorkspaceResultReady, WorkspaceResetting, WorkspaceRepairRequired, WorkspaceRemoved:
		return nil
	default:
		return errors.New("invalid workspace state")
	}
}

// CanTransitionTo returns the lifecycle transitions available to workspace
// owners. Filesystem effects are implemented outside the domain package.
func (s WorkspaceState) CanTransitionTo(next WorkspaceState) bool {
	if s == next {
		return s != WorkspaceRemoved
	}
	switch s {
	case WorkspaceCreating:
		return next == WorkspaceReady || next == WorkspaceRepairRequired || next == WorkspaceRemoved
	case WorkspaceReady:
		return next == WorkspaceTurnRunning || next == WorkspaceResetting || next == WorkspaceRepairRequired || next == WorkspaceRemoved
	case WorkspaceTurnRunning:
		return next == WorkspaceResultReady || next == WorkspaceResetting || next == WorkspaceRepairRequired
	case WorkspaceResultReady:
		return next == WorkspaceReady || next == WorkspaceResetting || next == WorkspaceRepairRequired
	case WorkspaceResetting:
		return next == WorkspaceReady || next == WorkspaceRepairRequired || next == WorkspaceRemoved
	case WorkspaceRepairRequired:
		return next == WorkspaceRemoved
	case WorkspaceRemoved:
		return false
	default:
		return false
	}
}

// Transition changes only durable state metadata. The owner must perform and
// verify any corresponding workspace filesystem operation separately.
func (w *ProposalWorkspace) Transition(next WorkspaceState) error {
	if w == nil || w.Validate() != nil || next.Validate() != nil || !w.State.CanTransitionTo(next) {
		return ErrInvalidProposalTransition
	}
	w.State = next
	return nil
}

// ProposalStatus is the lifecycle of one immutable proposal version or its
// aggregate's current version projection.
type ProposalStatus string

const (
	ProposalVersionDeriving ProposalStatus = "deriving"
	ProposalVersionReady    ProposalStatus = "ready"
	ProposalVersionStale    ProposalStatus = "stale"
	ProposalVersionRejected ProposalStatus = "rejected"
	ProposalVersionApplying ProposalStatus = "applying"
	ProposalVersionApplied  ProposalStatus = "applied"
	ProposalVersionFailed   ProposalStatus = "failed"
)

func (s ProposalStatus) Validate() error {
	switch s {
	case ProposalVersionDeriving, ProposalVersionReady, ProposalVersionStale, ProposalVersionRejected, ProposalVersionApplying, ProposalVersionApplied, ProposalVersionFailed:
		return nil
	default:
		return errors.New("invalid proposal status")
	}
}

func (s ProposalStatus) CanTransitionTo(next ProposalStatus) bool {
	if s == next {
		return s == ProposalVersionDeriving || s == ProposalVersionFailed
	}
	switch s {
	case ProposalVersionDeriving:
		return next == ProposalVersionReady || next == ProposalVersionStale || next == ProposalVersionRejected || next == ProposalVersionFailed
	case ProposalVersionReady:
		return next == ProposalVersionStale || next == ProposalVersionRejected || next == ProposalVersionApplying
	case ProposalVersionStale:
		return next == ProposalVersionRejected
	case ProposalVersionApplying:
		return next == ProposalVersionApplied || next == ProposalVersionFailed
	case ProposalVersionFailed:
		return next == ProposalVersionDeriving
	case ProposalVersionRejected, ProposalVersionApplied:
		return false
	default:
		return false
	}
}

// ProposalFailurePhase records which owner stopped a failed proposal action.
type ProposalFailurePhase string

const (
	ProposalFailureNone        ProposalFailurePhase = ""
	ProposalFailureValidation  ProposalFailurePhase = "validation"
	ProposalFailureWorkspace   ProposalFailurePhase = "workspace"
	ProposalFailureProvider    ProposalFailurePhase = "provider"
	ProposalFailureReset       ProposalFailurePhase = "reset"
	ProposalFailureDerivation  ProposalFailurePhase = "derivation"
	ProposalFailurePersistence ProposalFailurePhase = "persistence"
	ProposalFailurePatch       ProposalFailurePhase = "patch"
	ProposalFailureDestination ProposalFailurePhase = "destination"
)

func (p ProposalFailurePhase) Validate() error {
	switch p {
	case ProposalFailureNone, ProposalFailureValidation, ProposalFailureWorkspace, ProposalFailureProvider, ProposalFailureReset, ProposalFailureDerivation, ProposalFailurePersistence, ProposalFailurePatch, ProposalFailureDestination:
		return nil
	default:
		return errors.New("invalid proposal failure phase")
	}
}

// ProposalAttemptOutcome records whether an attempt published a version, was
// reset after a verified zero delta, or failed.
type ProposalAttemptOutcome string

const (
	ProposalAttemptDeriving           ProposalAttemptOutcome = "deriving"
	ProposalAttemptVersionPublished   ProposalAttemptOutcome = "version_published"
	ProposalAttemptNoChangesResetting ProposalAttemptOutcome = "no_changes_resetting"
	ProposalAttemptNoChanges          ProposalAttemptOutcome = "no_changes"
	ProposalAttemptFailed             ProposalAttemptOutcome = "failed"
)

func (o ProposalAttemptOutcome) Validate() error {
	switch o {
	case ProposalAttemptDeriving, ProposalAttemptVersionPublished, ProposalAttemptNoChangesResetting, ProposalAttemptNoChanges, ProposalAttemptFailed:
		return nil
	default:
		return errors.New("invalid proposal attempt outcome")
	}
}

func (o ProposalAttemptOutcome) CanTransitionTo(next ProposalAttemptOutcome) bool {
	if o == next {
		return o == ProposalAttemptDeriving || o == ProposalAttemptNoChangesResetting
	}
	switch o {
	case ProposalAttemptDeriving:
		return next == ProposalAttemptVersionPublished || next == ProposalAttemptNoChangesResetting || next == ProposalAttemptFailed
	case ProposalAttemptNoChangesResetting:
		return next == ProposalAttemptNoChanges || next == ProposalAttemptFailed
	case ProposalAttemptVersionPublished, ProposalAttemptNoChanges, ProposalAttemptFailed:
		return false
	default:
		return false
	}
}

// ProposalResultDisposition records whether the isolated result root remains
// available while an attempt is being reset or retired.
type ProposalResultDisposition string

const (
	ProposalResultNone       ProposalResultDisposition = "none"
	ProposalResultPresent    ProposalResultDisposition = "present"
	ProposalResultDiscarding ProposalResultDisposition = "discarding"
	ProposalResultDiscarded  ProposalResultDisposition = "discarded"
)

func (d ProposalResultDisposition) Validate() error {
	switch d {
	case ProposalResultNone, ProposalResultPresent, ProposalResultDiscarding, ProposalResultDiscarded:
		return nil
	default:
		return errors.New("invalid proposal result disposition")
	}
}

// ProposalScope classifies whether a derived patch stays within the confirmed
// request or requires broader review disclosure.
type ProposalScope string

const (
	ProposalScopeFocused ProposalScope = "focused"
	ProposalScopeBroader ProposalScope = "broader"
)

func (s ProposalScope) Validate() error {
	switch s {
	case ProposalScopeFocused, ProposalScopeBroader:
		return nil
	default:
		return errors.New("invalid proposal scope")
	}
}

// ProposalVersionNumber is monotonic within one proposal lineage.
type ProposalVersionNumber uint64
