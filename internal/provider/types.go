// Package provider contains provider-neutral values exchanged through the
// application-owned review-provider port.
package provider

import (
	"time"

	"github.com/Scottlr/nudge/internal/domain"
)

// CorrelationID ties a provider operation and its events to one application
// request. It is distinct from provider-owned conversation and turn refs.
type CorrelationID string

// ProviderConversationRef is an opaque external conversation reference.
type ProviderConversationRef string

// ProviderTurnRef is an opaque external turn reference.
type ProviderTurnRef string

// ProviderRequestID is an opaque provider runtime-approval request identity.
type ProviderRequestID string

// TurnMode describes the two provider uses Nudge permits in v1.
type TurnMode string

const (
	// TurnDiscuss permits read-only discussion over an immutable snapshot or
	// prompt-only captured context.
	TurnDiscuss TurnMode = "discuss"
	// TurnPropose permits edits only inside the explicitly isolated proposal
	// result workspace.
	TurnPropose TurnMode = "propose"
)

// FilesystemMode describes the repository-readable and writable roots exposed
// to a turn.
type FilesystemMode string

const (
	// FilesystemPromptOnly gives the provider no repository-readable root.
	FilesystemPromptOnly FilesystemMode = "prompt_only"
	// FilesystemReviewSnapshot gives the provider read access to an immutable
	// review snapshot and no writable root.
	FilesystemReviewSnapshot FilesystemMode = "review_snapshot"
	// FilesystemProposalResult gives the provider exact write access to an
	// isolated proposal result root.
	FilesystemProposalResult FilesystemMode = "proposal_result"
)

// NetworkPolicy is intentionally a closed v1 enum. There is no enabled value
// in the provider contract; unknown values fail closed during validation.
type NetworkPolicy uint8

const (
	// NetworkDisabled is the only network policy admitted by v1.
	NetworkDisabled NetworkPolicy = 1
)

// RuntimeApprovalPolicy controls whether a turn may receive explicit
// one-shot runtime approvals. It never approves a proposed patch.
type RuntimeApprovalPolicy string

const (
	RuntimeApprovalsDisabled RuntimeApprovalPolicy = "disabled"
	RuntimeApprovalsExplicit RuntimeApprovalPolicy = "explicit"
)

// PermissionRoot is an exact absolute root granted to one provider turn.
// Containment and native path safety are proven by the owning adapter before
// this value is sent to a provider process.
type PermissionRoot struct {
	Path string
}

// ContainmentEvidence records the independent evidence required before a
// provider can receive repository filesystem roots.
type ContainmentEvidence struct {
	CanonicalRead    bool
	CanonicalWrite   bool
	NoSymlinkEscape  bool
	NoJunctionEscape bool
	NoMountEscape    bool
	NoHardLinkAlias  bool
	HandlesQuiescent bool
}

// TurnPermissionPolicy is the complete filesystem, runtime, and network
// policy for one provider turn.
type TurnPermissionPolicy struct {
	Filesystem         FilesystemMode
	ReadableRoots      []PermissionRoot
	WritableRoots      []PermissionRoot
	RuntimeRoots       []PermissionRoot
	ProposalResultRoot PermissionRoot
	Containment        ContainmentEvidence
	Network            NetworkPolicy
	RuntimeApprovals   RuntimeApprovalPolicy
}

// ProviderCapabilities describes supported provider operations without
// exposing any provider protocol or generic agent API.
type ProviderCapabilities struct {
	ResumeConversation bool
	Streaming          bool
	Steering           bool
	RuntimeApprovals   bool
	WritableWorkspace  bool
	ReadOnlyFilesystem bool
	ExactReadRoots     bool
	AccountLogin       bool
	RateLimits         bool
}

// StartConversationRequest contains Nudge-local identity and the policy that
// must govern the conversation's turns.
type StartConversationRequest struct {
	ThreadID      domain.ReviewThreadID
	OperationID   domain.OperationID
	CorrelationID CorrelationID
	Mode          TurnMode
	WorkingDir    string
	Permissions   TurnPermissionPolicy
}

// TurnRequest starts one focused discussion or explicitly authorized
// proposal turn.
type TurnRequest struct {
	ThreadID      domain.ReviewThreadID
	OperationID   domain.OperationID
	Mode          TurnMode
	Prompt        string
	WorkingDir    string
	Permissions   TurnPermissionPolicy
	CorrelationID CorrelationID
}

// RuntimeApprovalKind identifies the narrow ephemeral scope being approved.
type RuntimeApprovalKind string

const (
	RuntimeApprovalCommand RuntimeApprovalKind = "command"
	RuntimeApprovalFile    RuntimeApprovalKind = "file_change"
	RuntimeApprovalTool    RuntimeApprovalKind = "tool"
	RuntimeApprovalNetwork RuntimeApprovalKind = "network"
)

// RuntimeApprovalScope is deliberately structured to avoid carrying raw
// command arguments, prompt bodies, or URLs through the approval contract.
// ArgumentsDigest identifies the exact command argument set without exposing
// it in the normalized event stream.
type RuntimeApprovalScope struct {
	Kind            RuntimeApprovalKind
	Executable      string
	ArgumentsDigest string
	Path            PermissionRoot
	Tool            string
}

// ApprovalDecision is the user's one-shot response to one runtime request.
type ApprovalDecision string

const (
	ApprovalAllowOnce ApprovalDecision = "allow_once"
	ApprovalDeny      ApprovalDecision = "deny"
)

// RuntimeApprovalRequest identifies one expiring, exact-scope approval.
type RuntimeApprovalRequest struct {
	RequestID     ProviderRequestID
	ThreadID      domain.ReviewThreadID
	OperationID   domain.OperationID
	CorrelationID CorrelationID
	TurnRef       ProviderTurnRef
	Scope         RuntimeApprovalScope
	ExpiresAt     time.Time
}

// RuntimeApprovalResponse resolves one pending runtime approval. Scope and
// local identities are repeated so a stale or confused response fails closed.
type RuntimeApprovalResponse struct {
	RequestID     ProviderRequestID
	ThreadID      domain.ReviewThreadID
	OperationID   domain.OperationID
	CorrelationID CorrelationID
	TurnRef       ProviderTurnRef
	Scope         RuntimeApprovalScope
	Decision      ApprovalDecision
}
