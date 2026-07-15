package codex

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/Scottlr/nudge/internal/provider"
	"github.com/Scottlr/nudge/internal/provider/codex/protocol"
)

var (
	ErrInvalidAccountResponse = errors.New("invalid codex account response")
	ErrUnsupportedLogin       = errors.New("codex managed login method is unsupported")
	ErrInvalidLoginResponse   = errors.New("invalid codex managed login response")
	ErrNoLoginInProgress      = errors.New("no codex login is in progress")
)

// AccountState describes authentication without carrying credentials.
type AccountState string

const (
	AccountUnknown       AccountState = "unknown"
	AccountAuthenticated AccountState = "authenticated"
	AccountAuthRequired  AccountState = "auth_required"
	AccountUnavailable   AccountState = "unavailable"
	AccountLoggingIn     AccountState = "logging_in"
)

// AccountStatus is the non-secret application-facing account summary.
type AccountStatus struct {
	State           AccountState
	AuthMode        string
	PlanType        string
	RequiresAuth    bool
	LoginInProgress bool
}

// LoginMethod identifies one supported managed Codex login surface.
type LoginMethod string

const (
	LoginBrowser LoginMethod = "chatgpt"
	LoginDevice  LoginMethod = "chatgptDeviceCode"
)

// LoginChallenge contains the URL or device-code instructions returned by
// Codex. It never contains an access token or credential file path.
type LoginChallenge struct {
	Method          LoginMethod
	LoginID         string
	AuthURL         string
	VerificationURL string
	UserCode        string
}

type accountFields struct {
	Type     string `json:"type"`
	PlanType string `json:"planType"`
}

// MapAccountResponse projects the app-server response into non-secret state.
// Unknown fields, including any provider token fields, are discarded.
func MapAccountResponse(response protocol.GetAccountResponse) (AccountStatus, error) {
	status := AccountStatus{RequiresAuth: response.RequiresOpenAIAuth}
	if len(response.Account) == 0 || string(response.Account) == "null" {
		if response.RequiresOpenAIAuth {
			status.State = AccountAuthRequired
		} else {
			status.State = AccountUnavailable
		}
		return status, nil
	}
	var fields accountFields
	if err := json.Unmarshal(response.Account, &fields); err != nil || fields.Type == "" {
		return AccountStatus{}, ErrInvalidAccountResponse
	}
	switch fields.Type {
	case "apiKey", "chatgpt", "amazonBedrock":
		status.AuthMode = fields.Type
	default:
		return AccountStatus{}, ErrInvalidAccountResponse
	}
	status.PlanType = fields.PlanType
	status.State = AccountAuthenticated
	if err := status.validate(); err != nil {
		return AccountStatus{}, err
	}
	return status, nil
}

func (c *Connection) accountStatus() AccountStatus {
	c.accountMu.RLock()
	defer c.accountMu.RUnlock()
	return c.account
}

func (c *Connection) setAccount(status AccountStatus) {
	c.accountMu.Lock()
	c.account = status
	c.accountMu.Unlock()
}

// Account returns the last normalized account state.
func (c *Connection) Account() AccountStatus {
	if c == nil {
		return AccountStatus{State: AccountUnavailable}
	}
	return c.accountStatus()
}

// Login starts a managed browser or device-code login. API-key and externally
// supplied token flows are intentionally unavailable through this method.
func (c *Connection) Login(ctx context.Context, method LoginMethod) (LoginChallenge, error) {
	if c == nil || c.client == nil {
		return LoginChallenge{}, ErrClientClosed
	}
	if method != LoginBrowser && method != LoginDevice {
		return LoginChallenge{}, ErrUnsupportedLogin
	}
	var response protocol.LoginAccountResponse
	if err := c.client.Call(ctx, "account/login/start", protocol.LoginAccountParams{Type: string(method)}, &response); err != nil {
		return LoginChallenge{}, err
	}
	if response.LoginID == "" || (method == LoginBrowser && response.AuthURL == "") || (method == LoginDevice && (response.VerificationURL == "" || response.UserCode == "")) {
		return LoginChallenge{}, ErrInvalidLoginResponse
	}
	challenge := LoginChallenge{
		Method:          method,
		LoginID:         response.LoginID,
		AuthURL:         response.AuthURL,
		VerificationURL: response.VerificationURL,
		UserCode:        response.UserCode,
	}
	c.accountMu.Lock()
	c.account.State = AccountLoggingIn
	c.account.LoginInProgress = true
	c.loginID = response.LoginID
	c.accountMu.Unlock()
	return challenge, nil
}

// CancelLogin cancels the active managed login challenge.
func (c *Connection) CancelLogin(ctx context.Context) error {
	if c == nil || c.client == nil {
		return ErrClientClosed
	}
	c.accountMu.RLock()
	loginID := c.loginID
	c.accountMu.RUnlock()
	if loginID == "" {
		return ErrNoLoginInProgress
	}
	var response protocol.CancelLoginAccountResponse
	if err := c.client.Call(ctx, "account/login/cancel", protocol.CancelLoginAccountParams{LoginID: loginID}, &response); err != nil {
		return err
	}
	c.accountMu.Lock()
	c.account.LoginInProgress = false
	c.account.State = accountStateAfterLogin(c.account)
	c.loginID = ""
	c.accountMu.Unlock()
	return nil
}

func accountStateAfterLogin(status AccountStatus) AccountState {
	if status.RequiresAuth {
		return AccountAuthRequired
	}
	return AccountUnavailable
}

func (c *Connection) handleAccountUpdated(notification protocol.Notification) error {
	var update protocol.AccountUpdatedNotification
	if err := json.Unmarshal(notification.Params, &update); err != nil {
		return ErrInvalidAccountResponse
	}
	c.accountMu.Lock()
	if update.AuthMode != "" {
		c.account.AuthMode = update.AuthMode
	}
	if update.PlanType != "" {
		c.account.PlanType = update.PlanType
	}
	c.account.State = AccountAuthenticated
	c.account.RequiresAuth = false
	c.account.LoginInProgress = false
	c.loginID = ""
	c.accountMu.Unlock()
	return c.publishProviderEvent(provider.ProviderEvent{Kind: provider.EventAccountChanged, Status: "updated"})
}

func (c *Connection) handleLoginCompleted(notification protocol.Notification) error {
	var completion protocol.AccountLoginCompletedNotification
	if err := json.Unmarshal(notification.Params, &completion); err != nil {
		return ErrInvalidAccountResponse
	}
	c.accountMu.Lock()
	if completion.Success {
		c.account.State = AccountAuthenticated
		c.account.RequiresAuth = false
	} else {
		c.account.State = accountStateAfterLogin(c.account)
	}
	c.account.LoginInProgress = false
	c.loginID = ""
	c.accountMu.Unlock()
	return c.publishProviderEvent(provider.ProviderEvent{Kind: provider.EventAccountChanged, Status: "login_completed"})
}

func (c *Connection) handleRateLimitsUpdated(notification protocol.Notification) error {
	var value struct {
		RateLimits json.RawMessage `json:"rateLimits"`
	}
	if err := json.Unmarshal(notification.Params, &value); err != nil || len(value.RateLimits) == 0 || !json.Valid(value.RateLimits) {
		return ErrInvalidAccountResponse
	}
	return c.publishProviderEvent(provider.ProviderEvent{Kind: provider.EventRateLimitChanged, Status: "updated"})
}

func (status AccountStatus) validate() error {
	if status.AuthMode != "" && strings.ContainsAny(status.AuthMode, "\r\n\x00") {
		return ErrInvalidAccountResponse
	}
	if status.PlanType != "" && strings.ContainsAny(status.PlanType, "\r\n\x00") {
		return ErrInvalidAccountResponse
	}
	return nil
}
