package app

import (
	"github.com/Scottlr/nudge/internal/domain"
	"github.com/Scottlr/nudge/internal/domain/repository"
	"github.com/Scottlr/nudge/internal/domain/review"
)

// EventMetadata identifies an emitted fact and declares whether it may be
// coalesced when a client is temporarily unable to consume progress.
type EventMetadata struct {
	Revision         uint64
	OperationID      domain.OperationID
	CorrelationID    CorrelationID
	TargetGeneration repository.TargetGeneration
	CoalescingKey    string
	Coalescible      bool
}

// Event is a normalized product fact emitted after a reducer commit.
type Event interface {
	eventMetadata() EventMetadata
	withRevision(uint64) Event
	isEvent()
}

// OperationStarted reports admission of a new operation.
type OperationStarted struct {
	Revision         uint64
	OperationID      domain.OperationID
	CorrelationID    CorrelationID
	TargetGeneration repository.TargetGeneration
	Kind             OperationKind
}

// OperationCompleted reports successful completion of an operation.
type OperationCompleted struct {
	Revision         uint64
	OperationID      domain.OperationID
	CorrelationID    CorrelationID
	TargetGeneration repository.TargetGeneration
	Kind             OperationKind
}

// FileSelected reports an active-file projection change.
type FileSelected struct {
	Revision         uint64
	OperationID      domain.OperationID
	CorrelationID    CorrelationID
	TargetGeneration repository.TargetGeneration
	Path             repository.RepoPath
}

// Progress reports replaceable operation progress. A non-empty coalescing key
// lets a slow client retain only the latest progress for that operation.
type Progress struct {
	Revision         uint64
	OperationID      domain.OperationID
	CorrelationID    CorrelationID
	TargetGeneration repository.TargetGeneration
	CoalescingKey    string
	Message          string
}

// ApplicationClosed reports that the final shutdown state has been committed.
type ApplicationClosed struct {
	Revision uint64
}

// SessionRestored reports the result of durable-session restoration without
// exposing provider or storage implementation details to a frontend.
type SessionRestored struct {
	Revision         uint64
	OperationID      domain.OperationID
	CorrelationID    CorrelationID
	SessionID        domain.ReviewSessionID
	TargetGeneration repository.TargetGeneration
	ReadOnly         bool
	Persistence      PersistenceMode
	Degraded         bool
}

// ThreadCreated reports a durable thread and its initial normalized message
// after their transaction has committed.
type ThreadCreated struct {
	Revision         uint64
	OperationID      domain.OperationID
	CorrelationID    CorrelationID
	TargetGeneration repository.TargetGeneration
	SessionID        domain.ReviewSessionID
	ThreadID         domain.ReviewThreadID
	InitialMessageID domain.MessageID
	Title            string
	AnchorPath       repository.RepoPath
}

// ThreadActivated reports an explicit active-thread projection change.
type ThreadActivated struct {
	Revision         uint64
	OperationID      domain.OperationID
	CorrelationID    CorrelationID
	TargetGeneration repository.TargetGeneration
	SessionID        domain.ReviewSessionID
	ThreadID         domain.ReviewThreadID
}

// MessageAppended reports a normalized message after its durable append.
type MessageAppended struct {
	Revision         uint64
	OperationID      domain.OperationID
	CorrelationID    CorrelationID
	TargetGeneration repository.TargetGeneration
	SessionID        domain.ReviewSessionID
	ThreadID         domain.ReviewThreadID
	MessageID        domain.MessageID
	Role             review.MessageRole
	Status           review.MessageStatus
}

// ThreadResolutionChanged reports an explicit resolution-axis mutation.
type ThreadResolutionChanged struct {
	Revision         uint64
	OperationID      domain.OperationID
	CorrelationID    CorrelationID
	TargetGeneration repository.TargetGeneration
	SessionID        domain.ReviewSessionID
	ThreadID         domain.ReviewThreadID
	Resolved         bool
}

// ThreadReadChanged reports an explicit read-state mutation.
type ThreadReadChanged struct {
	Revision         uint64
	OperationID      domain.OperationID
	CorrelationID    CorrelationID
	TargetGeneration repository.TargetGeneration
	SessionID        domain.ReviewSessionID
	ThreadID         domain.ReviewThreadID
	Read             bool
}

// ThreadReadStateChanged is the descriptive alias for ThreadReadChanged.
type ThreadReadStateChanged = ThreadReadChanged

func (OperationStarted) isEvent()             {}
func (OperationCompleted) isEvent()           {}
func (RepositoryLoaded) isEvent()             {}
func (TargetLoaded) isEvent()                 {}
func (SessionRestored) isEvent()              {}
func (FileSelected) isEvent()                 {}
func (Progress) isEvent()                     {}
func (OperationFailed) isEvent()              {}
func (OperationCancelled) isEvent()           {}
func (ApplicationClosed) isEvent()            {}
func (ThreadCreated) isEvent()                {}
func (ThreadActivated) isEvent()              {}
func (MessageAppended) isEvent()              {}
func (ThreadResolutionChanged) isEvent()      {}
func (ThreadReadChanged) isEvent()            {}
func (ProviderConversationAttached) isEvent() {}
func (ProviderTurnStateChanged) isEvent()     {}

func (e OperationStarted) eventMetadata() EventMetadata {
	return EventMetadata{Revision: e.Revision, OperationID: e.OperationID, CorrelationID: e.CorrelationID, TargetGeneration: e.TargetGeneration}
}

func (e OperationStarted) withRevision(revision uint64) Event {
	e.Revision = revision
	return e
}

func (e OperationCompleted) eventMetadata() EventMetadata {
	return EventMetadata{Revision: e.Revision, OperationID: e.OperationID, CorrelationID: e.CorrelationID, TargetGeneration: e.TargetGeneration}
}

func (e OperationCompleted) withRevision(revision uint64) Event {
	e.Revision = revision
	return e
}

func (e RepositoryLoaded) eventMetadata() EventMetadata {
	return EventMetadata{Revision: e.Revision, OperationID: e.OperationID, CorrelationID: e.CorrelationID, TargetGeneration: e.TargetGeneration}
}

func (e RepositoryLoaded) withRevision(revision uint64) Event {
	e.Revision = revision
	return e
}

func (e TargetLoaded) eventMetadata() EventMetadata {
	return EventMetadata{Revision: e.Revision, OperationID: e.OperationID, CorrelationID: e.CorrelationID, TargetGeneration: e.TargetGeneration}
}

func (e TargetLoaded) withRevision(revision uint64) Event {
	e.Revision = revision
	return e
}

func (e SessionRestored) eventMetadata() EventMetadata {
	return EventMetadata{Revision: e.Revision, OperationID: e.OperationID, CorrelationID: e.CorrelationID, TargetGeneration: e.TargetGeneration}
}

func (e SessionRestored) withRevision(revision uint64) Event {
	e.Revision = revision
	return e
}

func (e FileSelected) eventMetadata() EventMetadata {
	return EventMetadata{Revision: e.Revision, OperationID: e.OperationID, CorrelationID: e.CorrelationID, TargetGeneration: e.TargetGeneration}
}

func (e FileSelected) withRevision(revision uint64) Event {
	e.Revision = revision
	e.Path = repository.RepoPath(e.Path.Bytes())
	return e
}

func (e Progress) eventMetadata() EventMetadata {
	return EventMetadata{Revision: e.Revision, OperationID: e.OperationID, CorrelationID: e.CorrelationID, TargetGeneration: e.TargetGeneration, CoalescingKey: e.CoalescingKey, Coalescible: e.CoalescingKey != ""}
}

func (e Progress) withRevision(revision uint64) Event {
	e.Revision = revision
	return e
}

func (e OperationFailed) eventMetadata() EventMetadata {
	return EventMetadata{Revision: e.Revision, OperationID: e.OperationID, CorrelationID: e.CorrelationID, TargetGeneration: e.TargetGeneration}
}

func (e OperationFailed) withRevision(revision uint64) Event {
	e.Revision = revision
	return e
}

func (e OperationCancelled) eventMetadata() EventMetadata {
	return EventMetadata{Revision: e.Revision, OperationID: e.OperationID, CorrelationID: e.CorrelationID, TargetGeneration: e.TargetGeneration}
}

func (e OperationCancelled) withRevision(revision uint64) Event {
	e.Revision = revision
	return e
}

func (e ApplicationClosed) eventMetadata() EventMetadata {
	return EventMetadata{Revision: e.Revision}
}

func (e ApplicationClosed) withRevision(revision uint64) Event {
	e.Revision = revision
	return e
}

func (e ThreadCreated) eventMetadata() EventMetadata {
	return EventMetadata{Revision: e.Revision, OperationID: e.OperationID, CorrelationID: e.CorrelationID, TargetGeneration: e.TargetGeneration}
}

func (e ThreadCreated) withRevision(revision uint64) Event {
	e.Revision = revision
	e.AnchorPath = repository.RepoPath(e.AnchorPath.Bytes())
	return e
}

func (e ThreadActivated) eventMetadata() EventMetadata {
	return EventMetadata{Revision: e.Revision, OperationID: e.OperationID, CorrelationID: e.CorrelationID, TargetGeneration: e.TargetGeneration}
}

func (e ThreadActivated) withRevision(revision uint64) Event {
	e.Revision = revision
	return e
}

func (e MessageAppended) eventMetadata() EventMetadata {
	return EventMetadata{Revision: e.Revision, OperationID: e.OperationID, CorrelationID: e.CorrelationID, TargetGeneration: e.TargetGeneration}
}

func (e MessageAppended) withRevision(revision uint64) Event {
	e.Revision = revision
	return e
}

func (e ThreadResolutionChanged) eventMetadata() EventMetadata {
	return EventMetadata{Revision: e.Revision, OperationID: e.OperationID, CorrelationID: e.CorrelationID, TargetGeneration: e.TargetGeneration}
}

func (e ThreadResolutionChanged) withRevision(revision uint64) Event {
	e.Revision = revision
	return e
}

func (e ThreadReadChanged) eventMetadata() EventMetadata {
	return EventMetadata{Revision: e.Revision, OperationID: e.OperationID, CorrelationID: e.CorrelationID, TargetGeneration: e.TargetGeneration}
}

func (e ThreadReadChanged) withRevision(revision uint64) Event {
	e.Revision = revision
	return e
}

func (e ProviderConversationAttached) eventMetadata() EventMetadata {
	return EventMetadata{Revision: e.Revision, OperationID: e.OperationID, CorrelationID: e.CorrelationID}
}

func (e ProviderConversationAttached) withRevision(revision uint64) Event {
	e.Revision = revision
	return e
}

func (e ProviderTurnStateChanged) eventMetadata() EventMetadata {
	return EventMetadata{Revision: e.Revision, OperationID: e.OperationID, CorrelationID: e.CorrelationID}
}

func (e ProviderTurnStateChanged) withRevision(revision uint64) Event {
	e.Revision = revision
	return e
}
