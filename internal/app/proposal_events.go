package app

import (
	"github.com/Scottlr/nudge/internal/domain"
	"github.com/Scottlr/nudge/internal/domain/review"
)

// ProposalTurnPrepared reports that the exact intent and isolated workspace
// have been journaled before provider activity. It carries no prompt or path
// contents.
type ProposalTurnPrepared struct {
	Revision      uint64
	AttemptID     domain.OperationID
	ProposalID    domain.ProposalID
	WorkspaceID   domain.WorkspaceID
	ThreadID      domain.ReviewThreadID
	TurnID        domain.ProviderTurnID
	OperationID   domain.OperationID
	CorrelationID CorrelationID
}

// ProposalResultReady reports a quiescent result root ready for T110/T111.
// It is not a proposal version and cannot be approved or applied.
type ProposalResultReady struct {
	Revision      uint64
	AttemptID     domain.OperationID
	ProposalID    domain.ProposalID
	WorkspaceID   domain.WorkspaceID
	ThreadID      domain.ReviewThreadID
	TurnID        domain.ProviderTurnID
	OperationID   domain.OperationID
	CorrelationID CorrelationID
}

// ProposalTurnFailed reports a terminal non-ready proposal outcome. Reason is
// a bounded stable code, never provider prose or source content.
type ProposalTurnFailed struct {
	Revision      uint64
	AttemptID     domain.OperationID
	ProposalID    domain.ProposalID
	WorkspaceID   domain.WorkspaceID
	ThreadID      domain.ReviewThreadID
	TurnID        domain.ProviderTurnID
	OperationID   domain.OperationID
	CorrelationID CorrelationID
	Reason        string
}

func (ProposalTurnPrepared) isEvent() {}
func (ProposalResultReady) isEvent()  {}
func (ProposalTurnFailed) isEvent()   {}

func (e ProposalTurnPrepared) eventMetadata() EventMetadata {
	return EventMetadata{Revision: e.Revision, OperationID: e.OperationID, CorrelationID: e.CorrelationID}
}

func (e ProposalTurnPrepared) withRevision(revision uint64) Event {
	e.Revision = revision
	return e
}

func (e ProposalResultReady) eventMetadata() EventMetadata {
	return EventMetadata{Revision: e.Revision, OperationID: e.OperationID, CorrelationID: e.CorrelationID}
}

func (e ProposalResultReady) withRevision(revision uint64) Event {
	e.Revision = revision
	return e
}

func (e ProposalTurnFailed) eventMetadata() EventMetadata {
	return EventMetadata{Revision: e.Revision, OperationID: e.OperationID, CorrelationID: e.CorrelationID}
}

func (e ProposalTurnFailed) withRevision(revision uint64) Event {
	e.Revision = revision
	return e
}

// ProposalRejected reports a durable exact-version rejection after the
// rejection decision has been recorded. Reset completion is reported by the
// disposition service result and does not alter thread resolution.
type ProposalRejected struct {
	Revision      uint64
	ProposalID    domain.ProposalID
	WorkspaceID   domain.WorkspaceID
	ThreadID      domain.ReviewThreadID
	Version       review.ProposalVersionNumber
	OperationID   domain.OperationID
	CorrelationID CorrelationID
	Reason        string
}

// ProposalResultDiscarded reports a distinct failed-result discard decision;
// it is never a rejected proposal-version event.
type ProposalResultDiscarded struct {
	Revision      uint64
	ProposalID    domain.ProposalID
	WorkspaceID   domain.WorkspaceID
	ThreadID      domain.ReviewThreadID
	AttemptID     domain.OperationID
	OperationID   domain.OperationID
	CorrelationID CorrelationID
	Reason        string
}

// ProposalApplicationFinalized reports the one guarded terminal aggregate
// transition after T113 has classified the durable apply operation.
type ProposalApplicationFinalized struct {
	Revision      uint64
	ProposalID    domain.ProposalID
	WorkspaceID   domain.WorkspaceID
	ThreadID      domain.ReviewThreadID
	Version       review.ProposalVersionNumber
	OperationID   domain.OperationID
	CorrelationID CorrelationID
	Outcome       ProposalApplyOutcome
}

func (ProposalRejected) isEvent()             {}
func (ProposalResultDiscarded) isEvent()      {}
func (ProposalApplicationFinalized) isEvent() {}

func (e ProposalRejected) eventMetadata() EventMetadata {
	return EventMetadata{Revision: e.Revision, OperationID: e.OperationID, CorrelationID: e.CorrelationID}
}

func (e ProposalRejected) withRevision(revision uint64) Event {
	e.Revision = revision
	return e
}

func (e ProposalResultDiscarded) eventMetadata() EventMetadata {
	return EventMetadata{Revision: e.Revision, OperationID: e.OperationID, CorrelationID: e.CorrelationID}
}

func (e ProposalResultDiscarded) withRevision(revision uint64) Event {
	e.Revision = revision
	return e
}

func (e ProposalApplicationFinalized) eventMetadata() EventMetadata {
	return EventMetadata{Revision: e.Revision, OperationID: e.OperationID, CorrelationID: e.CorrelationID}
}

func (e ProposalApplicationFinalized) withRevision(revision uint64) Event {
	e.Revision = revision
	return e
}
