// Package domain contains adapter-independent Nudge vocabulary and contracts.
package domain

import "errors"

// ErrEmptyID reports an attempt to construct an identity without a value.
var ErrEmptyID = errors.New("id must not be empty")

// RepositoryID identifies a repository without assigning meaning to its text.
type RepositoryID string

// WorktreeID identifies one checked-out worktree without assigning meaning to its text.
type WorktreeID string

// ReviewSessionID identifies one review session.
type ReviewSessionID string

// SessionLeaseID identifies one writable session lease.
type SessionLeaseID string

// ReviewThreadID identifies one review thread.
type ReviewThreadID string

// MessageID identifies one normalized review message.
type MessageID string

// ProposalID identifies one proposed change.
type ProposalID string

// WorkspaceID identifies one Nudge-owned workspace.
type WorkspaceID string

// CaptureID identifies one accepted immutable capture.
type CaptureID string

// ReviewSnapshotID identifies one immutable review snapshot.
type ReviewSnapshotID string

// ProviderConversationID identifies Nudge's local provider-conversation record.
type ProviderConversationID string

// ProviderTurnID identifies Nudge's local provider-turn record.
type ProviderTurnID string

// OperationID identifies one cancellable application operation.
type OperationID string

// NewRepositoryID constructs a repository identity from an opaque value.
func NewRepositoryID(value string) (RepositoryID, error) {
	if err := validateID(value); err != nil {
		return "", err
	}
	return RepositoryID(value), nil
}

// NewWorktreeID constructs a worktree identity from an opaque value.
func NewWorktreeID(value string) (WorktreeID, error) {
	if err := validateID(value); err != nil {
		return "", err
	}
	return WorktreeID(value), nil
}

// NewReviewSessionID constructs a review-session identity from an opaque value.
func NewReviewSessionID(value string) (ReviewSessionID, error) {
	if err := validateID(value); err != nil {
		return "", err
	}
	return ReviewSessionID(value), nil
}

// NewSessionLeaseID constructs a session-lease identity from an opaque value.
func NewSessionLeaseID(value string) (SessionLeaseID, error) {
	if err := validateID(value); err != nil {
		return "", err
	}
	return SessionLeaseID(value), nil
}

// NewReviewThreadID constructs a review-thread identity from an opaque value.
func NewReviewThreadID(value string) (ReviewThreadID, error) {
	if err := validateID(value); err != nil {
		return "", err
	}
	return ReviewThreadID(value), nil
}

// NewMessageID constructs a message identity from an opaque value.
func NewMessageID(value string) (MessageID, error) {
	if err := validateID(value); err != nil {
		return "", err
	}
	return MessageID(value), nil
}

// NewProposalID constructs a proposal identity from an opaque value.
func NewProposalID(value string) (ProposalID, error) {
	if err := validateID(value); err != nil {
		return "", err
	}
	return ProposalID(value), nil
}

// NewWorkspaceID constructs a workspace identity from an opaque value.
func NewWorkspaceID(value string) (WorkspaceID, error) {
	if err := validateID(value); err != nil {
		return "", err
	}
	return WorkspaceID(value), nil
}

// NewCaptureID constructs a capture identity from an opaque value.
func NewCaptureID(value string) (CaptureID, error) {
	if err := validateID(value); err != nil {
		return "", err
	}
	return CaptureID(value), nil
}

// NewReviewSnapshotID constructs a review-snapshot identity from an opaque value.
func NewReviewSnapshotID(value string) (ReviewSnapshotID, error) {
	if err := validateID(value); err != nil {
		return "", err
	}
	return ReviewSnapshotID(value), nil
}

// NewProviderConversationID constructs a local provider-conversation identity.
func NewProviderConversationID(value string) (ProviderConversationID, error) {
	if err := validateID(value); err != nil {
		return "", err
	}
	return ProviderConversationID(value), nil
}

// NewProviderTurnID constructs a local provider-turn identity from an opaque value.
func NewProviderTurnID(value string) (ProviderTurnID, error) {
	if err := validateID(value); err != nil {
		return "", err
	}
	return ProviderTurnID(value), nil
}

// NewOperationID constructs an operation identity from an opaque value.
func NewOperationID(value string) (OperationID, error) {
	if err := validateID(value); err != nil {
		return "", err
	}
	return OperationID(value), nil
}

func validateID(value string) error {
	if value == "" {
		return ErrEmptyID
	}
	return nil
}
