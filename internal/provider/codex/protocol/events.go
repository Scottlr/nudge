package protocol

import "encoding/json"

// ThreadStartedNotification is the stable thread lifecycle envelope emitted
// by the pinned app-server protocol.
type ThreadStartedNotification struct {
	Thread ThreadSummary `json:"thread"`
}

// TurnEventSummary is the bounded turn shape used by started/completed
// notifications. The complete provider turn payload remains protocol-owned.
type TurnEventSummary struct {
	ID     string          `json:"id"`
	Status string          `json:"status"`
	Error  json.RawMessage `json:"error,omitempty"`
}

// TurnStartedNotification is the stable turn-start lifecycle envelope.
type TurnStartedNotification struct {
	ThreadID string           `json:"threadId"`
	Turn     TurnEventSummary `json:"turn"`
}

// TurnCompletedNotification is the stable turn-terminal lifecycle envelope.
type TurnCompletedNotification struct {
	ThreadID string           `json:"threadId"`
	Turn     TurnEventSummary `json:"turn"`
}

// ItemStartedNotification carries an additive ThreadItem union. The mapper
// inspects only its stable id/type discriminator.
type ItemStartedNotification struct {
	Item        json.RawMessage `json:"item"`
	StartedAtMS int64           `json:"startedAtMs"`
	ThreadID    string          `json:"threadId"`
	TurnID      string          `json:"turnId"`
}

// ItemCompletedNotification carries an additive ThreadItem union. The mapper
// inspects only its stable id/type/status fields.
type ItemCompletedNotification struct {
	Item          json.RawMessage `json:"item"`
	CompletedAtMS int64           `json:"completedAtMs"`
	ThreadID      string          `json:"threadId"`
	TurnID        string          `json:"turnId"`
}

// AgentMessageDeltaNotification is the exact text-stream notification shape.
type AgentMessageDeltaNotification struct {
	Delta    string `json:"delta"`
	ItemID   string `json:"itemId"`
	ThreadID string `json:"threadId"`
	TurnID   string `json:"turnId"`
}

// ErrorNotification is the provider turn error envelope. Error is retained
// as raw JSON until the adapter extracts only a bounded human message.
type ErrorNotification struct {
	Error     json.RawMessage `json:"error"`
	ThreadID  string          `json:"threadId"`
	TurnID    string          `json:"turnId"`
	WillRetry bool            `json:"willRetry"`
}

// ThreadStatusChangedNotification is a safe connection/thread activity hint.
type ThreadStatusChangedNotification struct {
	Status   json.RawMessage `json:"status"`
	ThreadID string          `json:"threadId"`
}
