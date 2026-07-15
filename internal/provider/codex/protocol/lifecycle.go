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
