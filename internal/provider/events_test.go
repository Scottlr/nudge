package provider

import (
	"errors"
	"testing"

	"github.com/Scottlr/nudge/internal/domain"
)

func TestProviderEventRequiresLocalAndOpaqueIdentity(t *testing.T) {
	event := ProviderEvent{
		Kind:            EventTurnDelta,
		Sequence:        1,
		ThreadID:        domain.ReviewThreadID("thread-1"),
		OperationID:     domain.OperationID("operation-1"),
		CorrelationID:   CorrelationID("correlation-1"),
		ConversationID:  domain.ProviderConversationID("conversation-1"),
		ConversationRef: ProviderConversationRef("remote-conversation-1"),
		TurnID:          domain.ProviderTurnID("turn-1"),
		TurnRef:         ProviderTurnRef("remote-turn-1"),
		Text:            "delta",
	}
	if err := event.Validate(DefaultValidationLimits()); err != nil {
		t.Fatalf("valid event rejected: %v", err)
	}
	event.CorrelationID = ""
	if err := event.Validate(DefaultValidationLimits()); !errors.Is(err, ErrInvalidEvent) {
		t.Fatalf("missing correlation accepted: %v", err)
	}
}

func TestEventOrderRejectsRegression(t *testing.T) {
	base := ProviderEvent{
		Kind:     EventProviderError,
		Sequence: 2,
		Error:    "provider failed",
	}
	var order EventOrder
	if err := order.Accept(base); err != nil {
		t.Fatalf("first event: %v", err)
	}
	base.Sequence = 1
	if err := order.Accept(base); !errors.Is(err, ErrEventOutOfOrder) {
		t.Fatalf("regressing event accepted: %v", err)
	}
}
