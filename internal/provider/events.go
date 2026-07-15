package provider

import (
	"time"

	"github.com/Scottlr/nudge/internal/domain"
)

// EventDelivery is the explicit result of offering an event to the
// application-owned sink. Backpressure and closure are observable outcomes;
// neither permits silently dropping lifecycle truth.
type EventDelivery string

const (
	EventAccepted     EventDelivery = "accepted"
	EventBackpressure EventDelivery = "backpressure"
	EventClosed       EventDelivery = "closed"
)

// EventKind identifies normalized provider lifecycle and progress events.
type EventKind string

const (
	EventConnectionChanged        EventKind = "connection_changed"
	EventCapabilitiesChanged      EventKind = "capabilities_changed"
	EventAccountChanged           EventKind = "account_changed"
	EventRateLimitChanged         EventKind = "rate_limit_changed"
	EventDisconnected             EventKind = "disconnected"
	EventConversationStarted      EventKind = "conversation_started"
	EventConversationResumed      EventKind = "conversation_resumed"
	EventConversationFailed       EventKind = "conversation_failed"
	EventTurnStarted              EventKind = "turn_started"
	EventTurnDelta                EventKind = "turn_delta"
	EventTurnCompleted            EventKind = "turn_completed"
	EventTurnFailed               EventKind = "turn_failed"
	EventMessageStarted           EventKind = "message_started"
	EventMessageDelta             EventKind = "message_delta"
	EventMessageCompleted         EventKind = "message_completed"
	EventCommandStarted           EventKind = "command_started"
	EventCommandCompleted         EventKind = "command_completed"
	EventToolActivity             EventKind = "tool_activity"
	EventFileActivity             EventKind = "file_activity"
	EventRuntimeApprovalRequested EventKind = "runtime_approval_requested"
	EventRuntimeApprovalResolved  EventKind = "runtime_approval_resolved"
	EventProviderError            EventKind = "provider_error"
)

// ProviderEvent is a bounded, provider-neutral event. The local IDs identify
// Nudge records; refs remain opaque external values and are never substituted
// for those local identities.
type ProviderEvent struct {
	Kind            EventKind
	Sequence        uint64
	ThreadID        domain.ReviewThreadID
	OperationID     domain.OperationID
	CorrelationID   CorrelationID
	ConversationID  domain.ProviderConversationID
	ConversationRef ProviderConversationRef
	TurnID          domain.ProviderTurnID
	TurnRef         ProviderTurnRef
	// ItemRef is the opaque provider item identity used to correlate streamed
	// message, command, and file activity inside one turn.
	ItemRef       string
	RequestID     ProviderRequestID
	Capabilities  ProviderCapabilities
	Status        string
	Text          string
	Error         string
	ExpiresAt     time.Time
	Scope         RuntimeApprovalScope
	Approval      *RuntimeApproval
	Decision      ApprovalDecision
	CoalescingKey string
	Coalescible   bool
}

func validEventKind(kind EventKind) bool {
	switch kind {
	case EventConnectionChanged, EventCapabilitiesChanged, EventAccountChanged, EventRateLimitChanged, EventDisconnected, EventConversationStarted, EventConversationResumed, EventConversationFailed, EventTurnStarted, EventTurnDelta, EventTurnCompleted, EventTurnFailed, EventMessageStarted, EventMessageDelta, EventMessageCompleted, EventCommandStarted, EventCommandCompleted, EventToolActivity, EventFileActivity, EventRuntimeApprovalRequested, EventRuntimeApprovalResolved, EventProviderError:
		return true
	default:
		return false
	}
}

func eventNeedsIdentity(kind EventKind) bool {
	switch kind {
	case EventConversationStarted, EventConversationResumed, EventConversationFailed, EventTurnStarted, EventTurnDelta, EventTurnCompleted, EventTurnFailed, EventMessageStarted, EventMessageDelta, EventMessageCompleted, EventCommandStarted, EventCommandCompleted, EventToolActivity, EventFileActivity, EventRuntimeApprovalRequested, EventRuntimeApprovalResolved:
		return true
	default:
		return false
	}
}

func eventNeedsConversation(kind EventKind) bool {
	switch kind {
	case EventConversationStarted, EventConversationResumed, EventConversationFailed, EventTurnStarted, EventTurnDelta, EventTurnCompleted, EventTurnFailed, EventMessageStarted, EventMessageDelta, EventMessageCompleted, EventCommandStarted, EventCommandCompleted, EventToolActivity, EventFileActivity, EventRuntimeApprovalRequested, EventRuntimeApprovalResolved:
		return true
	default:
		return false
	}
}

func eventNeedsTurn(kind EventKind) bool {
	switch kind {
	case EventTurnStarted, EventTurnDelta, EventTurnCompleted, EventTurnFailed, EventMessageStarted, EventMessageDelta, EventMessageCompleted, EventCommandStarted, EventCommandCompleted, EventToolActivity, EventFileActivity, EventRuntimeApprovalRequested, EventRuntimeApprovalResolved:
		return true
	default:
		return false
	}
}

// Validate checks sequence, identity, content, and event-specific fields
// before an event enters an application queue.
func (e ProviderEvent) Validate(limits ValidationLimits) error {
	if err := limits.validate(); err != nil || !validEventKind(e.Kind) || e.Sequence == 0 {
		return ErrInvalidEvent
	}
	if e.ConversationID != "" && validateLocalID(string(e.ConversationID), "conversation id", limits) != nil {
		return ErrInvalidEvent
	}
	if e.ConversationRef != "" && e.ConversationRef.Validate() != nil {
		return ErrInvalidEvent
	}
	if e.TurnID != "" && validateLocalID(string(e.TurnID), "turn id", limits) != nil {
		return ErrInvalidEvent
	}
	if e.TurnRef != "" && e.TurnRef.Validate() != nil {
		return ErrInvalidEvent
	}
	if validateText(e.ItemRef, "event item ref", limits.OpaqueRefBytes, true) != nil {
		return ErrInvalidEvent
	}
	if e.RequestID != "" && e.RequestID.Validate() != nil {
		return ErrInvalidEvent
	}
	if e.CorrelationID != "" && e.CorrelationID.Validate() != nil {
		return ErrInvalidEvent
	}
	if validateText(e.Status, "event status", limits.DisplayBytes, true) != nil || validateText(e.Text, "event text", limits.TurnContentBytes, true) != nil || validateText(e.Error, "event error", limits.HumanErrorBytes, true) != nil || validateText(e.CoalescingKey, "event coalescing key", limits.CoalescingKeyBytes, true) != nil {
		return ErrInvalidEvent
	}
	if eventNeedsIdentity(e.Kind) {
		if validateLocalID(string(e.ThreadID), "thread id", limits) != nil || validateLocalID(string(e.OperationID), "operation id", limits) != nil || e.CorrelationID == "" {
			return ErrInvalidEvent
		}
	}
	if eventNeedsConversation(e.Kind) && (e.ConversationID == "" || e.ConversationRef == "") {
		return ErrInvalidEvent
	}
	if eventNeedsTurn(e.Kind) && (e.TurnID == "" || e.TurnRef == "") {
		return ErrInvalidEvent
	}
	if e.Coalescible && (e.Kind != EventMessageDelta || e.CoalescingKey == "") {
		return ErrInvalidEvent
	}
	switch e.Kind {
	case EventConversationStarted, EventConversationResumed:
		if e.Status == "" {
			return ErrInvalidEvent
		}
	case EventConversationFailed, EventTurnFailed, EventProviderError:
		if e.Error == "" {
			return ErrInvalidEvent
		}
	case EventTurnDelta, EventMessageDelta:
		if e.Text == "" {
			return ErrInvalidEvent
		}
	case EventRuntimeApprovalRequested:
		if e.RequestID == "" || e.ExpiresAt.IsZero() || e.Scope.Validate(limits) != nil || e.Approval == nil || e.Approval.Request.RequestID != e.RequestID || e.Approval.Request.Scope != e.Scope || !e.Approval.Request.ExpiresAt.Equal(e.ExpiresAt) {
			return ErrInvalidEvent
		}
		if uint64(len([]byte(e.Approval.Details.ExactCommandArgs))) > limits.TurnContentBytes || uint64(len([]byte(e.Approval.Details.NetworkTarget))) > limits.PathBytes || uint64(len([]byte(e.Approval.Details.ToolName))) > limits.MethodBytes {
			return ErrInvalidEvent
		}
	case EventRuntimeApprovalResolved:
		if e.RequestID == "" || e.Scope.Validate(limits) != nil || (e.Decision != ApprovalAllowOnce && e.Decision != ApprovalDeny) {
			return ErrInvalidEvent
		}
	}
	return nil
}

// EventOrder rejects duplicate or regressing provider sequence numbers while
// preserving the provider's serialized order for accepted events.
type EventOrder struct {
	last uint64
}

// Accept validates an event and advances the order cursor only on success.
func (o *EventOrder) Accept(event ProviderEvent) error {
	if o == nil {
		return ErrInvalidEvent
	}
	if err := event.Validate(DefaultValidationLimits()); err != nil {
		return err
	}
	if event.Sequence <= o.last {
		return ErrEventOutOfOrder
	}
	o.last = event.Sequence
	return nil
}
