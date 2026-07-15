package codex

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/Scottlr/nudge/internal/domain"
	"github.com/Scottlr/nudge/internal/provider"
	"github.com/Scottlr/nudge/internal/provider/codex/protocol"
)

var ErrInvalidConversationResponse = errors.New("invalid codex conversation response")
var ErrInvalidTurnResponse = errors.New("invalid codex turn response")

var ErrInvalidNotification = errors.New("invalid codex notification")

// NotificationContext carries local Nudge ownership for one provider
// conversation/turn. Raw Codex IDs never become Nudge IDs; the connection
// supplies this context after its local journal has been fenced.
type NotificationContext struct {
	Sequence        uint64
	ThreadID        domain.ReviewThreadID
	OperationID     domain.OperationID
	CorrelationID   provider.CorrelationID
	ConversationID  domain.ProviderConversationID
	ConversationRef provider.ProviderConversationRef
	TurnID          domain.ProviderTurnID
	TurnRef         provider.ProviderTurnRef
}

const (
	notificationError         = "error"
	notificationThreadStarted = "thread/started"
	notificationTurnStarted   = "turn/started"
	notificationTurnCompleted = "turn/completed"
	notificationItemStarted   = "item/started"
	notificationItemCompleted = "item/completed"
	notificationMessageDelta  = "item/agentMessage/delta"
	notificationThreadStatus  = "thread/status/changed"
)

// MapNotification converts only pinned, supported app-server notifications
// into bounded neutral events. Additive or unknown methods return no event.
func MapNotification(notification protocol.Notification, context NotificationContext) ([]provider.ProviderEvent, error) {
	base := provider.ProviderEvent{
		Sequence:        context.Sequence,
		ThreadID:        context.ThreadID,
		OperationID:     context.OperationID,
		CorrelationID:   context.CorrelationID,
		ConversationID:  context.ConversationID,
		ConversationRef: context.ConversationRef,
		TurnID:          context.TurnID,
		TurnRef:         context.TurnRef,
	}
	switch notification.Method {
	case notificationThreadStarted:
		var value protocol.ThreadStartedNotification
		if err := json.Unmarshal(notification.Params, &value); err != nil || value.Thread.ID == "" {
			return nil, fmt.Errorf("%w: thread started", ErrInvalidNotification)
		}
		base.Kind = provider.EventConversationStarted
		base.ConversationRef = provider.ProviderConversationRef(value.Thread.ID)
		base.Status = "started"
		return []provider.ProviderEvent{base}, nil
	case notificationTurnStarted:
		var value protocol.TurnStartedNotification
		if err := json.Unmarshal(notification.Params, &value); err != nil || value.ThreadID == "" || value.Turn.ID == "" || value.Turn.Status == "" {
			return nil, fmt.Errorf("%w: turn started", ErrInvalidNotification)
		}
		base.Kind = provider.EventTurnStarted
		base.TurnRef = provider.ProviderTurnRef(value.Turn.ID)
		base.Status = value.Turn.Status
		return []provider.ProviderEvent{base}, nil
	case notificationTurnCompleted:
		var value protocol.TurnCompletedNotification
		if err := json.Unmarshal(notification.Params, &value); err != nil || value.ThreadID == "" || value.Turn.ID == "" || value.Turn.Status == "" {
			return nil, fmt.Errorf("%w: turn completed", ErrInvalidNotification)
		}
		base.TurnRef = provider.ProviderTurnRef(value.Turn.ID)
		base.Status = value.Turn.Status
		if value.Turn.Status == "completed" {
			base.Kind = provider.EventTurnCompleted
		} else {
			base.Kind = provider.EventTurnFailed
			base.Error = turnErrorMessage(value.Turn.Error, value.Turn.Status)
		}
		return []provider.ProviderEvent{base}, nil
	case notificationMessageDelta:
		var value protocol.AgentMessageDeltaNotification
		if err := json.Unmarshal(notification.Params, &value); err != nil || value.ThreadID == "" || value.TurnID == "" || value.ItemID == "" || value.Delta == "" {
			return nil, fmt.Errorf("%w: message delta", ErrInvalidNotification)
		}
		base.Kind = provider.EventMessageDelta
		base.TurnRef = provider.ProviderTurnRef(value.TurnID)
		base.ItemRef = value.ItemID
		base.Text = value.Delta
		base.CoalescingKey = value.ItemID
		base.Coalescible = true
		return []provider.ProviderEvent{base}, nil
	case notificationItemStarted, notificationItemCompleted:
		var value struct {
			Item     json.RawMessage `json:"item"`
			ThreadID string          `json:"threadId"`
			TurnID   string          `json:"turnId"`
		}
		if err := json.Unmarshal(notification.Params, &value); err != nil || value.ThreadID == "" || value.TurnID == "" {
			return nil, fmt.Errorf("%w: item lifecycle", ErrInvalidNotification)
		}
		item, err := mapItem(value.Item)
		if err != nil {
			return nil, err
		}
		base.ItemRef = item.ID
		base.TurnRef = provider.ProviderTurnRef(value.TurnID)
		base.Status = item.Status
		switch item.Type {
		case "agentMessage":
			if notification.Method == notificationItemStarted {
				base.Kind = provider.EventMessageStarted
			} else {
				base.Kind = provider.EventMessageCompleted
			}
		case "commandExecution":
			if notification.Method == notificationItemStarted {
				base.Kind = provider.EventCommandStarted
			} else {
				base.Kind = provider.EventCommandCompleted
			}
		case "mcpToolCall", "dynamicToolCall", "collabAgentToolCall", "webSearch":
			base.Kind = provider.EventToolActivity
		case "fileChange":
			base.Kind = provider.EventFileActivity
		default:
			return nil, nil
		}
		return []provider.ProviderEvent{base}, nil
	case notificationError:
		var value protocol.ErrorNotification
		if err := json.Unmarshal(notification.Params, &value); err != nil || len(value.Error) == 0 {
			return nil, fmt.Errorf("%w: error", ErrInvalidNotification)
		}
		base.Kind = provider.EventProviderError
		base.Error = turnErrorMessage(value.Error, "provider_error")
		if value.TurnID != "" {
			base.TurnRef = provider.ProviderTurnRef(value.TurnID)
		}
		return []provider.ProviderEvent{base}, nil
	case notificationThreadStatus:
		var value protocol.ThreadStatusChangedNotification
		if err := json.Unmarshal(notification.Params, &value); err != nil || value.ThreadID == "" {
			return nil, fmt.Errorf("%w: thread status", ErrInvalidNotification)
		}
		base.Kind = provider.EventConnectionChanged
		base.Status = "thread_status_changed"
		return []provider.ProviderEvent{base}, nil
	default:
		return nil, nil
	}
}

type mappedItem struct {
	ID     string `json:"id"`
	Type   string `json:"type"`
	Status string `json:"status"`
}

func mapItem(data json.RawMessage) (mappedItem, error) {
	var item mappedItem
	if err := json.Unmarshal(data, &item); err != nil || item.ID == "" || item.Type == "" {
		return mappedItem{}, fmt.Errorf("%w: item", ErrInvalidNotification)
	}
	return item, nil
}

func turnErrorMessage(data json.RawMessage, fallback string) string {
	var object struct {
		Message string `json:"message"`
	}
	if json.Unmarshal(data, &object) == nil && strings.TrimSpace(object.Message) != "" {
		return object.Message
	}
	var text string
	if json.Unmarshal(data, &text) == nil && strings.TrimSpace(text) != "" {
		return text
	}
	return fallback
}

func mapConversationResponse(response protocol.ThreadStartResponse) (provider.ProviderConversationRef, error) {
	ref := provider.ProviderConversationRef(response.Thread.ID)
	if ref.Validate() != nil {
		return "", ErrInvalidConversationResponse
	}
	return ref, nil
}

func mapTurnResponse(response protocol.TurnStartResponse) (provider.ProviderTurnRef, error) {
	ref := provider.ProviderTurnRef(response.Turn.ID)
	if ref.Validate() != nil {
		return "", ErrInvalidTurnResponse
	}
	return ref, nil
}
