package app

import (
	"errors"
	"fmt"

	"github.com/Scottlr/nudge/internal/domain"
	"github.com/Scottlr/nudge/internal/provider"
)

var (
	ErrDiscussionUnavailable = errors.New("discussion unavailable")
	ErrDiscussionStale       = errors.New("discussion context is stale")
)

// DiscussionMode is the application dispatch mode for a focused discussion.
// Proposal authorization remains a separate provider turn mode.
type DiscussionMode string

const (
	DiscussionModeFilesystem DiscussionMode = "filesystem"
	DiscussionModePromptOnly DiscussionMode = "prompt_only"
	DiscussionModeDisabled   DiscussionMode = "disabled"

	// DiscussionModeReadOnly is retained as the source-compatible name for the
	// original read-only availability call site.
	DiscussionModeReadOnly DiscussionMode = DiscussionModeFilesystem
	DiscussionModeProposal DiscussionMode = "proposal"
)

// DiscussionAvailabilityInput is the last-mile composition of repository
// evidence and application/provider evidence. Repository capability decisions
// remain immutable and do not absorb provider or disclosure state.
type DiscussionAvailabilityInput struct {
	Decision            CapabilityDecision
	RepositoryEvidence  SessionEvidence
	ProviderEvidence    DiscussionEvidence
	PromptContextReady  bool
	EmptyRootPermission bool
}

// ResolveDiscussionAvailability chooses the strongest safe mode immediately
// before dispatch. Filesystem mode wins only with the exact leased snapshot;
// prompt-only mode has zero repository-readable roots and no lease.
func ResolveDiscussionAvailability(input DiscussionAvailabilityInput) DiscussionAvailability {
	availability := DiscussionAvailability{
		Mode:                  DiscussionModeDisabled,
		Materialization:       input.Decision.MaterializeReviewSnapshot,
		SnapshotLease:         input.RepositoryEvidence.SnapshotLease,
		ReadContainment:       input.RepositoryEvidence.ReadContainment,
		ProviderCompatibility: input.ProviderEvidence.ProviderCompatibility,
		Account:               input.ProviderEvidence.Account,
		Disclosure:            input.ProviderEvidence.Disclosure,
		TurnPermission:        input.ProviderEvidence.TurnPermission,
	}
	appendRepositoryReason := func(reason CapabilityReason) {
		availability.RepositoryReasons = append(availability.RepositoryReasons, reason)
		availability.Reasons = append(availability.Reasons, reason)
	}
	appendApplicationReason := func(reason CapabilityReason) {
		availability.ApplicationReasons = append(availability.ApplicationReasons, reason)
		availability.Reasons = append(availability.Reasons, reason)
	}
	if !input.Decision.Review {
		appendRepositoryReason(CapabilityReason{Code: ReasonCapabilityNotImplemented})
	}
	if !availability.Materialization {
		appendRepositoryReason(CapabilityReason{Code: ReasonCapabilityNotImplemented})
	}
	if !availability.SnapshotLease {
		appendRepositoryReason(CapabilityReason{Code: ReasonSessionUnavailable})
	}
	if !availability.ReadContainment {
		appendRepositoryReason(CapabilityReason{Code: ReasonPlatformUnqualified})
	}
	if !availability.ProviderCompatibility {
		appendApplicationReason(CapabilityReason{Code: ReasonProviderUnavailable})
	}
	if !availability.Account {
		appendApplicationReason(CapabilityReason{Code: ReasonAccountUnavailable})
	}
	if !availability.Disclosure {
		appendApplicationReason(CapabilityReason{Code: ReasonDisclosureUnavailable})
	}
	if !availability.TurnPermission {
		appendApplicationReason(CapabilityReason{Code: ReasonPermissionUnavailable})
	}

	filesystemReady := input.Decision.Review && availability.Materialization && availability.SnapshotLease && availability.ReadContainment && availability.ProviderCompatibility && availability.Account && availability.Disclosure && availability.TurnPermission
	if filesystemReady {
		availability.Mode = DiscussionModeFilesystem
		availability.Available = true
		return availability
	}
	if input.Decision.Review && input.PromptContextReady && input.EmptyRootPermission && availability.ProviderCompatibility && availability.Account && availability.Disclosure && availability.TurnPermission {
		availability.Mode = DiscussionModePromptOnly
		availability.Available = true
		return availability
	}
	return availability
}

// DiscussionDispatchRequest prepares one provider-neutral discussion turn.
// It does not contact the provider; callers persist the pending concern first
// and pass the returned command to the T029 lifecycle service.
type DiscussionDispatchRequest struct {
	Guard              SessionWriteGuard
	ThreadID           domain.ReviewThreadID
	ConversationID     domain.ProviderConversationID
	Context            DiscussionContext
	Availability       DiscussionAvailability
	Permissions        provider.TurnPermissionPolicy
	SnapshotLease      *ReviewSnapshotLease
	SourceCaptureID    domain.CaptureID
	SourceSnapshotRef  string
	CapabilityPolicy   CapabilityPolicyVersion
	ResourcePolicy     ResourcePolicyVersion
	Evidence           EvidenceVersion
	PermissionVersion  string
	ExpectedGeneration uint64
	CurrentGeneration  uint64
	OperationID        domain.OperationID
	CorrelationID      CorrelationID
}

// PrepareStartProviderTurn binds context, mode, lease, and provenance before
// the remote side effect. A stale generation or missing disclosure gate never
// reaches the provider lifecycle service.
func (r DiscussionDispatchRequest) PrepareStartProviderTurn() (StartProviderTurn, error) {
	if r.Availability.Mode == DiscussionModeDisabled || !r.Availability.Available {
		return StartProviderTurn{}, ErrDiscussionUnavailable
	}
	if r.ExpectedGeneration != 0 && r.CurrentGeneration != 0 && r.ExpectedGeneration != r.CurrentGeneration {
		return StartProviderTurn{}, ErrDiscussionStale
	}
	prompt, err := BuildDiscussionPrompt(r.Context)
	if err != nil {
		return StartProviderTurn{}, err
	}
	if r.CapabilityPolicy == 0 || r.ResourcePolicy == 0 || r.Evidence == 0 || r.PermissionVersion == "" {
		return StartProviderTurn{}, ErrInvalidDiscussionContext
	}
	if err := r.Permissions.Validate(provider.DefaultValidationLimits()); err != nil || r.Permissions.Filesystem == provider.FilesystemProposalResult {
		return StartProviderTurn{}, provider.ErrInvalidPermission
	}
	provenance := DiscussionTurnProvenance{
		Mode:                    r.Availability.Mode,
		ContextHash:             DiscussionPromptHash(prompt),
		CapabilityPolicyVersion: r.CapabilityPolicy,
		ResourcePolicyVersion:   r.ResourcePolicy,
		EvidenceVersion:         r.Evidence,
		PermissionVersion:       r.PermissionVersion,
		SourceSnapshotRef:       r.SourceSnapshotRef,
	}
	if r.Availability.Mode == DiscussionModeFilesystem {
		if r.SnapshotLease == nil || r.SnapshotLease.Validate() != nil || r.Permissions.Filesystem != provider.FilesystemReviewSnapshot || len(r.Permissions.ReadableRoots) != 1 || r.Context.WorkingDir != r.SnapshotLease.Root || r.Permissions.ReadableRoots[0].Path != r.SnapshotLease.Root {
			return StartProviderTurn{}, ErrDiscussionUnavailable
		}
		if r.SnapshotLease.CaptureID == "" || r.SnapshotLease.ManifestHash == "" {
			return StartProviderTurn{}, ErrDiscussionUnavailable
		}
		provenance.ReviewSnapshotID = r.SnapshotLease.SnapshotID
		provenance.SourceCaptureID = r.SnapshotLease.CaptureID
		provenance.ManifestHash = r.SnapshotLease.ManifestHash
	} else {
		if r.SnapshotLease != nil || r.Permissions.Filesystem != provider.FilesystemPromptOnly {
			return StartProviderTurn{}, ErrDiscussionUnavailable
		}
		provenance.SourceCaptureID = r.SourceCaptureID
	}
	if err := provenance.Validate(); err != nil {
		return StartProviderTurn{}, fmt.Errorf("discussion provenance: %w", err)
	}
	return StartProviderTurn{
		Guard:          r.Guard,
		ThreadID:       r.ThreadID,
		ConversationID: r.ConversationID,
		Mode:           provider.TurnDiscuss,
		Prompt:         prompt,
		WorkingDir:     r.Context.WorkingDir,
		Permissions:    r.Permissions,
		Provenance:     provenance,
		OperationID:    r.OperationID,
		CorrelationID:  r.CorrelationID,
	}, nil
}
