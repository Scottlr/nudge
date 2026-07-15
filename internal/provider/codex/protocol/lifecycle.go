package protocol

import "encoding/json"

// InitializeParams is the stable app-server initialize request. Capabilities
// is intentionally omitted by the adapter so experimental APIs are not
// negotiated implicitly.
type InitializeParams struct {
	ClientInfo ClientInfo `json:"clientInfo"`
}

// ClientInfo identifies the application speaking the app-server protocol.
type ClientInfo struct {
	Name    string `json:"name"`
	Title   string `json:"title,omitempty"`
	Version string `json:"version"`
}

// InitializeResponse contains the bounded non-secret server identity returned
// by the initialize handshake.
type InitializeResponse struct {
	CodexHome      string `json:"codexHome"`
	PlatformFamily string `json:"platformFamily"`
	PlatformOS     string `json:"platformOs"`
	UserAgent      string `json:"userAgent"`
}

// GetAccountParams requests the current account summary without asking the
// server to refresh credentials.
type GetAccountParams struct {
	RefreshToken bool `json:"refreshToken,omitempty"`
}

// GetAccountResponse contains only the account object needed for safe
// normalization. The adapter never stores or forwards unknown account fields.
type GetAccountResponse struct {
	Account            json.RawMessage `json:"account"`
	RequiresOpenAIAuth bool            `json:"requiresOpenaiAuth"`
}

// LoginAccountParams is the managed-login subset of the app-server union.
// API-key and externally supplied token forms are deliberately not modeled.
type LoginAccountParams struct {
	Type string `json:"type"`
}

// LoginAccountResponse contains only browser/device challenge fields. Secret
// token response variants are intentionally not represented.
type LoginAccountResponse struct {
	Type            string `json:"type"`
	LoginID         string `json:"loginId"`
	AuthURL         string `json:"authUrl,omitempty"`
	VerificationURL string `json:"verificationUrl,omitempty"`
	UserCode        string `json:"userCode,omitempty"`
}

// CancelLoginAccountParams identifies one in-memory managed login challenge.
type CancelLoginAccountParams struct {
	LoginID string `json:"loginId"`
}

// CancelLoginAccountResponse reports whether the challenge was cancelled.
type CancelLoginAccountResponse struct {
	Status string `json:"status"`
}

// AccountLoginCompletedNotification reports completion without retaining the
// provider's free-form error text.
type AccountLoginCompletedNotification struct {
	LoginID *string `json:"loginId"`
	Success bool    `json:"success"`
}

// AccountUpdatedNotification is the safe account state delta.
type AccountUpdatedNotification struct {
	AuthMode string `json:"authMode,omitempty"`
	PlanType string `json:"planType,omitempty"`
}

// ThreadStartParams contains only the stable thread-level cwd and legacy
// sandbox fields supported by the pinned app-server schema.
type ThreadStartParams struct {
	CWD     string `json:"cwd,omitempty"`
	Sandbox string `json:"sandbox,omitempty"`
}

// ThreadResumeParams identifies one opaque Codex thread.
type ThreadResumeParams struct {
	ThreadID string `json:"threadId"`
}

// ThreadSummary is the minimum stable response shape needed for identity
// mapping. Other thread history fields remain inside the protocol boundary.
type ThreadSummary struct {
	ID string `json:"id"`
}

// ThreadStartResponse is the stable response envelope.
type ThreadStartResponse struct {
	Thread ThreadSummary `json:"thread"`
}

// ThreadResumeResponse is the stable response envelope.
type ThreadResumeResponse struct {
	Thread ThreadSummary `json:"thread"`
}

// UserInput is the narrow text input form used by T029's turn mapping.
type UserInput struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// SandboxPolicy is the stable turn-level policy union used by the pinned
// app-server schema. The schema has no exact read-root field; the adapter
// therefore rejects filesystem snapshot turns unless a future supported
// provider explicitly proves that boundary.
type SandboxPolicy struct {
	Type          string   `json:"type"`
	NetworkAccess *bool    `json:"networkAccess,omitempty"`
	WritableRoots []string `json:"writableRoots,omitempty"`
}

// TurnStartParams starts one turn in one opaque Codex thread.
type TurnStartParams struct {
	ThreadID      string         `json:"threadId"`
	Input         []UserInput    `json:"input"`
	CWD           string         `json:"cwd,omitempty"`
	SandboxPolicy *SandboxPolicy `json:"sandboxPolicy,omitempty"`
}

// TurnSummary is the minimum stable response shape needed for identity
// mapping.
type TurnSummary struct {
	ID string `json:"id"`
}

// TurnStartResponse is the stable response envelope.
type TurnStartResponse struct {
	Turn TurnSummary `json:"turn"`
}

// TurnSteerParams supplies intentional guidance to one active turn.
type TurnSteerParams struct {
	ThreadID       string      `json:"threadId"`
	ExpectedTurnID string      `json:"expectedTurnId"`
	Input          []UserInput `json:"input"`
}

// TurnSteerResponse returns the provider's active turn identity.
type TurnSteerResponse struct {
	TurnID string `json:"turnId"`
}

// TurnInterruptParams identifies one active turn in one thread.
type TurnInterruptParams struct {
	ThreadID string `json:"threadId"`
	TurnID   string `json:"turnId"`
}

// TurnInterruptResponse is intentionally empty in the stable schema.
type TurnInterruptResponse struct{}
