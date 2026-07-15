package codex

import (
	"encoding/json"
	"testing"

	"github.com/Scottlr/nudge/internal/domain"
	"github.com/Scottlr/nudge/internal/provider"
	"github.com/Scottlr/nudge/internal/provider/codex/protocol"
)

func TestMapperNormalizesStream(t *testing.T) {
	context := NotificationContext{
		Sequence:        1,
		ThreadID:        domain.ReviewThreadID("thread-1"),
		OperationID:     domain.OperationID("operation-1"),
		CorrelationID:   provider.CorrelationID("correlation-1"),
		ConversationID:  domain.ProviderConversationID("conversation-1"),
		ConversationRef: provider.ProviderConversationRef("remote-thread-1"),
		TurnID:          domain.ProviderTurnID("turn-1"),
		TurnRef:         provider.ProviderTurnRef("remote-turn-1"),
	}
	events, err := MapNotification(protocol.Notification{Method: "item/agentMessage/delta", Params: json.RawMessage(`{"delta":"hello ","itemId":"item-1","threadId":"remote-thread-1","turnId":"remote-turn-1"}`)}, context)
	if err != nil || len(events) != 1 {
		t.Fatalf("mapped events = %#v, err=%v", events, err)
	}
	event := events[0]
	if event.Kind != provider.EventMessageDelta || event.Text != "hello " || event.ItemRef != "item-1" || !event.Coalescible || event.CoalescingKey != "item-1" {
		t.Fatalf("event = %#v", event)
	}
	if err := event.Validate(provider.DefaultValidationLimits()); err != nil {
		t.Fatalf("mapped event invalid: %v", err)
	}
	unknown, err := MapNotification(protocol.Notification{Method: "future/notification", Params: json.RawMessage(`{"newField":true}`)}, context)
	if err != nil || len(unknown) != 0 {
		t.Fatalf("unknown notification = %#v, err=%v", unknown, err)
	}
}

func TestMapperMapsInterruptedTurnAsFailure(t *testing.T) {
	context := NotificationContext{Sequence: 2, ThreadID: "thread-1", OperationID: "operation-1", CorrelationID: "correlation-1", ConversationID: "conversation-1", ConversationRef: "remote-thread-1", TurnID: "turn-1", TurnRef: "remote-turn-1"}
	events, err := MapNotification(protocol.Notification{Method: "turn/completed", Params: json.RawMessage(`{"threadId":"remote-thread-1","turn":{"id":"remote-turn-1","status":"interrupted"}}`)}, context)
	if err != nil || len(events) != 1 {
		t.Fatalf("mapped events = %#v, err=%v", events, err)
	}
	if events[0].Kind != provider.EventTurnFailed || events[0].Status != "interrupted" || events[0].Error == "" {
		t.Fatalf("event = %#v", events[0])
	}
}
