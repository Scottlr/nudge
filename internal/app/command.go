package app

import (
	"errors"

	"github.com/Scottlr/nudge/internal/domain"
	"github.com/Scottlr/nudge/internal/domain/repository"
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
func (Shutdown) isReducerInput()                 {}

func (OpenRepository) isCommand()           {}
func (SelectTarget) isCommand()             {}
func (SelectFile) isCommand()               {}
func (RefreshTarget) isCommand()            {}
func (OpenSession) isCommand()              {}
func (CloseSession) isCommand()             {}
func (CancelOperation) isCommand()          {}
func (RespondToRuntimeApproval) isCommand() {}
func (Shutdown) isCommand()                 {}
