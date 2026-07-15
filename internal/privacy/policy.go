// Package privacy owns Nudge's typed data-disclosure and retention policy.
// It does not perform persistence, logging, export, or provider I/O.
package privacy

import (
	"errors"
	"fmt"
	"strings"
)

var (
	ErrInvalidPolicy      = errors.New("invalid privacy policy")
	ErrInvalidValueClass  = errors.New("invalid privacy value class")
	ErrInvalidDestination = errors.New("invalid privacy destination")
)

// DisclosureVersion identifies the exact privacy wording and retention
// contract acknowledged before provider use.
type DisclosureVersion string

const DisclosureVersionV1 DisclosureVersion = "privacy-v1"

// PolicyVersion is the descriptive alias used by policy values.
type PolicyVersion = DisclosureVersion

const PolicyVersionV1 PolicyVersion = DisclosureVersionV1

// AnchorExcerptRetention controls whether selected code and bounded context
// are retained after the current process. The source bytes are never required
// for an anchor to remain identifiable by hash and location.
type AnchorExcerptRetention string

const (
	AnchorExcerptNone     AnchorExcerptRetention = "none"
	AnchorExcerptSession  AnchorExcerptRetention = "session"
	AnchorExcerptRetained AnchorExcerptRetention = "retained"
)

// DisclosurePersistence identifies where acknowledgement metadata may live.
type DisclosurePersistence string

const (
	DisclosureProcessOnly       DisclosurePersistence = "process_only"
	DisclosureProtectedSettings DisclosurePersistence = "protected_settings"
)

// ValueClass identifies a category of data before it crosses a policy
// boundary. It intentionally does not carry the value itself.
type ValueClass string

const (
	ValueIdentifier       ValueClass = "identifier"
	ValueRepositoryPath   ValueClass = "repository_path"
	ValueSourceExcerpt    ValueClass = "source_excerpt"
	ValuePrompt           ValueClass = "prompt"
	ValueProviderResponse ValueClass = "provider_response"
	ValuePatchBytes       ValueClass = "patch_bytes"
	ValueOpaqueReference  ValueClass = "opaque_reference"
	ValueAccountMetadata  ValueClass = "account_metadata"
	ValueRuntimeScope     ValueClass = "runtime_approval_scope"
	ValueCommandOutput    ValueClass = "command_output"
	ValueRedactedEvidence ValueClass = "redacted_evidence"
	ValueCredential       ValueClass = "credential"
)

// Destination identifies the owner that would receive a classified value.
type Destination string

const (
	DestinationDurableMetadata   Destination = "durable_metadata"
	DestinationAnchorExcerpt     Destination = "anchor_excerpt"
	DestinationTranscript        Destination = "transcript"
	DestinationReviewSnapshot    Destination = "review_snapshot"
	DestinationProposalWorkspace Destination = "proposal_workspace"
	DestinationProviderContext   Destination = "provider_context"
	DestinationOperationalLog    Destination = "operational_log"
	DestinationHumanExport       Destination = "human_export"
)

// Policy is the versioned local-data contract consumed by application and
// adapter owners. It is immutable after construction.
type Policy struct {
	Version                PolicyVersion
	AnchorExcerptRetention AnchorExcerptRetention
	WorkspaceRetentionDays int
}

// DefaultPolicy returns the documented v1 privacy defaults.
func DefaultPolicy() Policy {
	return Policy{Version: PolicyVersionV1, AnchorExcerptRetention: AnchorExcerptRetained, WorkspaceRetentionDays: 14}
}

// NewPolicy validates one effective configuration and returns its immutable
// policy value.
func NewPolicy(version PolicyVersion, retention AnchorExcerptRetention, workspaceRetentionDays int) (Policy, error) {
	value := Policy{Version: version, AnchorExcerptRetention: retention, WorkspaceRetentionDays: workspaceRetentionDays}
	if err := value.Validate(); err != nil {
		return Policy{}, err
	}
	return value, nil
}

// Validate checks the bounded, versioned policy values.
func (p Policy) Validate() error {
	if p.Version != PolicyVersionV1 || p.WorkspaceRetentionDays < 1 || p.WorkspaceRetentionDays > 14 {
		return fmt.Errorf("%w: version or workspace retention", ErrInvalidPolicy)
	}
	switch p.AnchorExcerptRetention {
	case AnchorExcerptNone, AnchorExcerptSession, AnchorExcerptRetained:
		return nil
	default:
		return fmt.Errorf("%w: anchor excerpt retention", ErrInvalidPolicy)
	}
}

// StoresAnchorExcerpt reports whether selected/context plaintext may be
// written to durable state.
func (p Policy) StoresAnchorExcerpt() bool {
	return p.AnchorExcerptRetention == AnchorExcerptRetained
}

// PersistAnchorExcerpt is the policy-named form used by persistence owners.
func (p Policy) PersistAnchorExcerpt() bool {
	return p.StoresAnchorExcerpt()
}

// SupportsReattachment reports whether stored excerpt evidence is available
// after the process ends. Hashes and locations remain available in all modes.
func (p Policy) SupportsReattachment() bool {
	return p.AnchorExcerptRetention != AnchorExcerptNone
}

// DisclosureVersion is the provider-facing acknowledgement version derived
// from the same policy contract.
func (p Policy) DisclosureVersion() string {
	if p.Validate() != nil {
		return ""
	}
	return string(p.Version)
}

// IsValid reports whether value is one of the closed privacy categories.
func (value ValueClass) IsValid() bool {
	switch value {
	case ValueIdentifier, ValueRepositoryPath, ValueSourceExcerpt, ValuePrompt, ValueProviderResponse, ValuePatchBytes, ValueOpaqueReference, ValueAccountMetadata, ValueRuntimeScope, ValueCommandOutput, ValueRedactedEvidence, ValueCredential:
		return true
	default:
		return false
	}
}

// IsValid reports whether destination is one of the closed policy boundaries.
func (destination Destination) IsValid() bool {
	switch destination {
	case DestinationDurableMetadata, DestinationAnchorExcerpt, DestinationTranscript, DestinationReviewSnapshot, DestinationProposalWorkspace, DestinationProviderContext, DestinationOperationalLog, DestinationHumanExport:
		return true
	default:
		return false
	}
}

// Allows reports whether a value class may cross a destination under this
// policy. Credential values and unredacted command output are never admitted.
func (p Policy) Allows(value ValueClass, destination Destination) bool {
	if p.Validate() != nil || !value.IsValid() || !destination.IsValid() || value == ValueCredential || value == ValueCommandOutput {
		return false
	}
	if destination == DestinationAnchorExcerpt && value == ValueSourceExcerpt {
		return p.StoresAnchorExcerpt()
	}
	if destination == DestinationOperationalLog {
		return value == ValueIdentifier || value == ValueOpaqueReference || value == ValueAccountMetadata || value == ValueRedactedEvidence
	}
	if destination == DestinationProviderContext {
		return value == ValueIdentifier || value == ValueRepositoryPath || value == ValueSourceExcerpt || value == ValuePrompt || value == ValueProviderResponse || value == ValuePatchBytes || value == ValueOpaqueReference || value == ValueAccountMetadata || value == ValueRuntimeScope
	}
	if destination == DestinationTranscript {
		return value == ValueIdentifier || value == ValueProviderResponse || value == ValuePrompt || value == ValueOpaqueReference
	}
	if destination == DestinationDurableMetadata {
		return value == ValueIdentifier || value == ValueRepositoryPath || value == ValueOpaqueReference || value == ValueRedactedEvidence || value == ValueAccountMetadata
	}
	return value == ValueIdentifier || value == ValueRepositoryPath || value == ValueOpaqueReference || value == ValueRedactedEvidence || value == ValuePatchBytes
}

// SensitiveValue is a typed privacy category without retaining the underlying
// source, prompt, patch, or credential bytes. Owners must authorize the class
// at the destination boundary before handling the actual value.
type SensitiveValue struct {
	class ValueClass
}

// NewSensitiveValue constructs one closed-vocabulary privacy category.
func NewSensitiveValue(class ValueClass) (SensitiveValue, error) {
	if !class.IsValid() {
		return SensitiveValue{}, fmt.Errorf("%w: %q", ErrInvalidValueClass, class)
	}
	return SensitiveValue{class: class}, nil
}

// Class returns the category without exposing or retaining the classified
// payload.
func (value SensitiveValue) Class() ValueClass {
	return value.class
}

// Allowed reports whether this classified value may cross destination.
func (p Policy) Allowed(value SensitiveValue, destination Destination) bool {
	return p.Allows(value.class, destination)
}

// ProtectedRootState describes evidence about a private Nudge-owned root.
// It intentionally distinguishes current permission checks from historical
// privacy claims and later repair eligibility.
type ProtectedRootState string

const (
	ProtectedRootSecure      ProtectedRootState = "secure"
	ProtectedRootLegacyWeak  ProtectedRootState = "legacy_weak"
	ProtectedRootUnsupported ProtectedRootState = "unsupported"
	ProtectedRootUnavailable ProtectedRootState = "unavailable"
)

// ProtectedRootEvidence is a bounded, payload-free result from a platform
// owner. A repaired root must not be relabelled historically secure.
type ProtectedRootEvidence struct {
	State               ProtectedRootState
	OwnerOnlyAtCreation bool
	CurrentlyOwnerOnly  bool
	RepairEligible      bool
}

// Validate checks the evidence state and its safety relationships.
func (e ProtectedRootEvidence) Validate() error {
	switch e.State {
	case ProtectedRootSecure:
		if !e.OwnerOnlyAtCreation || !e.CurrentlyOwnerOnly || e.RepairEligible {
			return ErrInvalidPolicy
		}
	case ProtectedRootLegacyWeak:
		if e.OwnerOnlyAtCreation || !e.RepairEligible {
			return ErrInvalidPolicy
		}
	case ProtectedRootUnsupported, ProtectedRootUnavailable:
		if e.OwnerOnlyAtCreation || e.CurrentlyOwnerOnly || e.RepairEligible {
			return ErrInvalidPolicy
		}
	default:
		return ErrInvalidPolicy
	}
	return nil
}

// NormalizeClass returns a stable class for policy lookup without treating
// display text as a privacy identity.
func NormalizeClass(value string) (ValueClass, error) {
	class := ValueClass(strings.TrimSpace(value))
	if !class.IsValid() {
		return "", fmt.Errorf("%w: %q", ErrInvalidValueClass, value)
	}
	return class, nil
}
