package review

import "errors"

// ResolutionState identifies whether a review thread remains actionable.
type ResolutionState string

const (
	ResolutionOpen     ResolutionState = "open"
	ResolutionResolved ResolutionState = "resolved"
)

// ConversationState identifies the provider-conversation lifecycle.
type ConversationState string

const (
	ConversationIdle                    ConversationState = "idle"
	ConversationStreaming               ConversationState = "streaming"
	ConversationAwaitingRuntimeApproval ConversationState = "awaiting_runtime_approval"
	ConversationFailed                  ConversationState = "failed"
)

// ProposalState identifies the proposal lifecycle without embedding proposal
// aggregate data in the review thread.
type ProposalState string

const (
	ProposalNone       ProposalState = "none"
	ProposalGenerating ProposalState = "generating"
	ProposalReady      ProposalState = "ready"
	ProposalStale      ProposalState = "stale"
	ProposalApplying   ProposalState = "applying"
	ProposalApplied    ProposalState = "applied"
	ProposalRejected   ProposalState = "rejected"
	ProposalFailed     ProposalState = "failed"
)

// AnchorState identifies the current reconciliation disposition of an anchor.
type AnchorState string

const (
	AnchorValid     AnchorState = "valid"
	AnchorRelocated AnchorState = "relocated"
	AnchorAmbiguous AnchorState = "ambiguous"
	AnchorOrphaned  AnchorState = "orphaned"
)

// ReadState tracks notification state independently of every other thread
// status axis.
type ReadState string

const (
	Read   ReadState = "read"
	Unread ReadState = "unread"
)

// MessageStatus identifies the normalized message lifecycle.
type MessageStatus string

const (
	MessagePending   MessageStatus = "pending"
	MessageStreaming MessageStatus = "streaming"
	MessageCompleted MessageStatus = "completed"
	MessageFailed    MessageStatus = "failed"
	MessageCancelled MessageStatus = "cancelled"
)

// MessageRole identifies the source of normalized transcript content.
type MessageRole string

const (
	RoleUser      MessageRole = "user"
	RoleAssistant MessageRole = "assistant"
	RoleSystem    MessageRole = "system"
	RoleTool      MessageRole = "tool"
)

// FailurePhase records where a failed lifecycle operation stopped. It is
// deliberately separate from the status enum so a consumer can retain both
// coarse state and safe failure detail.
type FailurePhase string

const (
	FailurePhaseNone           FailurePhase = ""
	FailurePhaseValidation     FailurePhase = "validation"
	FailurePhaseProvider       FailurePhase = "provider"
	FailurePhasePermission     FailurePhase = "permission"
	FailurePhasePersistence    FailurePhase = "persistence"
	FailurePhaseReconciliation FailurePhase = "reconciliation"
)

// ErrorCode is a stable, domain-owned failure category. Application adapters
// may map their richer errors to this bounded value at the consumer boundary.
type ErrorCode string

// ThreadStatus composes the independent status axes of one review thread.
// There is intentionally no single enum representing their cross-product.
type ThreadStatus struct {
	Resolution   ResolutionState
	Conversation ConversationState
	Proposal     ProposalState
	Anchor       AnchorState
	Read         ReadState
	FailurePhase FailurePhase
	ErrorCode    ErrorCode
}

// ComposedStatus is a descriptive alias for callers that prefer the domain
// language used in the product design.
type ComposedStatus = ThreadStatus

func (s ResolutionState) Validate() error {
	switch s {
	case ResolutionOpen, ResolutionResolved:
		return nil
	default:
		return errors.New("invalid resolution state")
	}
}

func (s ConversationState) Validate() error {
	switch s {
	case ConversationIdle, ConversationStreaming, ConversationAwaitingRuntimeApproval, ConversationFailed:
		return nil
	default:
		return errors.New("invalid conversation state")
	}
}

func (s ProposalState) Validate() error {
	switch s {
	case ProposalNone, ProposalGenerating, ProposalReady, ProposalStale, ProposalApplying, ProposalApplied, ProposalRejected, ProposalFailed:
		return nil
	default:
		return errors.New("invalid proposal state")
	}
}

func (s AnchorState) Validate() error {
	switch s {
	case AnchorValid, AnchorRelocated, AnchorAmbiguous, AnchorOrphaned:
		return nil
	default:
		return errors.New("invalid anchor state")
	}
}

func (s ReadState) Validate() error {
	switch s {
	case Read, Unread:
		return nil
	default:
		return errors.New("invalid read state")
	}
}

func (s MessageStatus) Validate() error {
	switch s {
	case MessagePending, MessageStreaming, MessageCompleted, MessageFailed, MessageCancelled:
		return nil
	default:
		return errors.New("invalid message status")
	}
}

func (r MessageRole) Validate() error {
	switch r {
	case RoleUser, RoleAssistant, RoleSystem, RoleTool:
		return nil
	default:
		return errors.New("invalid message role")
	}
}
