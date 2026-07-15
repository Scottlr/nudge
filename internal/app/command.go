package app

import (
	"errors"

	"github.com/Scottlr/nudge/internal/domain"
	"github.com/Scottlr/nudge/internal/domain/repository"
	"github.com/Scottlr/nudge/internal/domain/review"
	"github.com/Scottlr/nudge/internal/provider"
)

// CorrelationID ties a command, its asynchronous result, and emitted events
// together without assigning meaning to the underlying value.
type CorrelationID string

// ErrEmptyCorrelationID reports an invalid correlation identity.
var ErrEmptyCorrelationID = errors.New("correlation id must not be empty")

// Command is a sealed application intent. Adapter and frontend packages can
// submit the commands defined here but cannot add a second command family.
type Command interface {
	ReducerInput
	isCommand()
}

// ReducerInput is a sealed message accepted by Reducer.Handle. It includes
// commands from frontends and typed results from cancellable operations.
type ReducerInput interface {
	isReducerInput()
}

// OpenRepository starts loading a repository from the supplied user-selected
// path. The application records the operation; a later RepositoryLoaded result
// commits adapter-produced repository evidence.
type OpenRepository struct {
	Path          string
	CorrelationID CorrelationID
}

// SelectTarget starts resolving a review target for the loaded repository.
type SelectTarget struct {
	Spec          repository.ReviewTargetSpec
	CorrelationID CorrelationID
}

// SelectFile makes one repository-relative file the active file projection.
type SelectFile struct {
	Path          repository.RepoPath
	CorrelationID CorrelationID
}

// RefreshTarget starts a new resolution of the current target specification.
type RefreshTarget struct {
	CorrelationID CorrelationID
}

// OpenSession requests restoration or creation of the explicitly selected
// session mode after repository and target evidence are available.
type OpenSession struct {
	Target        repository.ResolvedTarget
	Mode          SessionOpenMode
	Persistence   PersistenceMode
	CorrelationID CorrelationID
}

// CloseSession requests explicit closure, which excludes the durable session
// from automatic restore. Normal quit uses release and leaves it unfinished.
type CloseSession struct {
	SessionID     domain.ReviewSessionID
	CorrelationID CorrelationID
}

// CancelOperation requests cancellation of one active application operation.
type CancelOperation struct {
	OperationID   domain.OperationID
	CorrelationID CorrelationID
}

// RespondToRuntimeApproval is an explicit one-shot runtime decision. It is
// independent from proposal approval and carries only provider-neutral scope
// identity; exact command text remains an ephemeral presentation value.
type RespondToRuntimeApproval struct {
	Response      provider.RuntimeApprovalResponse
	CorrelationID CorrelationID
}

// RequestProposal is the explicit user action that authorizes one proposal
// turn. The confirmed intent is copied at this boundary and validated again
// against the durable proposal lineage before any workspace or provider work.
type RequestProposal struct {
	Guard          SessionWriteGuard
	ThreadID       domain.ReviewThreadID
	ProposalID     domain.ProposalID
	ConversationID domain.ProviderConversationID
	Intent         review.ProposalIntent
	Context        ProposalTurnContext
	// Eligibility is required for pinned branch/commit proposal turns and is
	// rechecked against the durable workspace/head identity before the provider starts.
	Eligibility   *ProposalEligibility
	OperationID   domain.OperationID
	CorrelationID CorrelationID
}

// CancelProposal requests cancellation of one active proposal turn. It is
// deliberately separate from runtime approval and proposed-patch approval.
type CancelProposal struct {
	Guard         SessionWriteGuard
	AttemptID     domain.OperationID
	CorrelationID CorrelationID
}

// RejectProposal rejects one exact immutable ready version. The workspace
// reset is a separate durable phase and never touches the edit destination.
type RejectProposal struct {
	Guard         SessionWriteGuard
	ThreadID      domain.ReviewThreadID
	ProposalID    domain.ProposalID
	Version       review.ProposalVersionNumber
	Reason        string
	OperationID   domain.OperationID
	CorrelationID CorrelationID
}

// ApproveProposal authorizes one exact, fully reviewed proposal version for
// whole-proposal application. Destination evidence is checked again against
// the durable aggregate before T112 or T113 is called.
type ApproveProposal struct {
	Guard                      SessionWriteGuard
	ThreadID                   domain.ReviewThreadID
	ProposalID                 domain.ProposalID
	Version                    review.ProposalVersionNumber
	PatchSHA256                string
	ConfirmedReviewRevision    uint64
	ReviewCompletenessIdentity string
	Destination                review.DestinationConstraints
	Repository                 repository.Repository
	Worktree                   repository.WorktreeRef
	IdempotencyKey             string
	OperationID                domain.OperationID
	CorrelationID              CorrelationID
}

// DiscardProposalResult discards one exact terminal failed/non-ready result
// attempt after confirming the isolated result reset effect.
type DiscardProposalResult struct {
	Guard         SessionWriteGuard
	ProposalID    domain.ProposalID
	AttemptID     domain.OperationID
	Reason        string
	OperationID   domain.OperationID
	CorrelationID CorrelationID
}

// Shutdown stops the application runtime after committing cancellation of
// active operations.
type Shutdown struct {
	CorrelationID CorrelationID
}

func (OpenRepository) isReducerInput()           {}
func (SelectTarget) isReducerInput()             {}
func (SelectFile) isReducerInput()               {}
func (RefreshTarget) isReducerInput()            {}
func (OpenSession) isReducerInput()              {}
func (CloseSession) isReducerInput()             {}
func (CancelOperation) isReducerInput()          {}
func (RespondToRuntimeApproval) isReducerInput() {}
func (RequestProposal) isReducerInput()          {}
func (CancelProposal) isReducerInput()           {}
func (RejectProposal) isReducerInput()           {}
func (ApproveProposal) isReducerInput()          {}
func (DiscardProposalResult) isReducerInput()    {}
func (Shutdown) isReducerInput()                 {}

func (OpenRepository) isCommand()           {}
func (SelectTarget) isCommand()             {}
func (SelectFile) isCommand()               {}
func (RefreshTarget) isCommand()            {}
func (OpenSession) isCommand()              {}
func (CloseSession) isCommand()             {}
func (CancelOperation) isCommand()          {}
func (RespondToRuntimeApproval) isCommand() {}
func (RequestProposal) isCommand()          {}
func (CancelProposal) isCommand()           {}
func (RejectProposal) isCommand()           {}
func (ApproveProposal) isCommand()          {}
func (DiscardProposalResult) isCommand()    {}
func (Shutdown) isCommand()                 {}
