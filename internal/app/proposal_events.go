package app

import (
	"github.com/Scottlr/nudge/internal/domain"
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
