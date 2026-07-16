package app

import (
	"context"
	"errors"
	"time"

	"github.com/Scottlr/nudge/internal/domain"
)

var (
	ErrInvalidLiveCodexHealthRequest  = errors.New("invalid live Codex health request")
	ErrLiveCodexHealthUnavailable     = errors.New("live Codex health is unavailable")
	ErrCodexLoginConfirmationRequired = errors.New("explicit Codex login confirmation required")
)

// LiveCodexHealthRequest identifies one explicit, bounded provider health
// observation. It carries no repository or provider-visible context.
type LiveCodexHealthRequest struct {
	CorrelationID domain.OperationID
	RequestedAt   time.Time
}

// ExecutableHealthSummary is the redacted executable identity used by the
// live health projection.
type ExecutableHealthSummary struct {
	Kind               string `json:"kind"`
	Source             string `json:"source"`
	CanonicalPath      string `json:"canonical_path"`
	Version            string `json:"version,omitempty"`
	IdentityHashPrefix string `json:"identity_hash_prefix"`
	Trusted            bool   `json:"trusted"`
}

// ProviderConnectionHealth is the bounded provider lifecycle state observed
// by an explicit live health check.
type ProviderConnectionHealth string

const (
	ProviderConnectionUnknown      ProviderConnectionHealth = "unknown"
	ProviderConnectionConnected    ProviderConnectionHealth = "connected"
	ProviderConnectionAuthRequired ProviderConnectionHealth = "auth_required"
	ProviderConnectionUnavailable  ProviderConnectionHealth = "unavailable"
	ProviderConnectionMissing      ProviderConnectionHealth = "missing"
	ProviderConnectionIncompatible ProviderConnectionHealth = "incompatible"
)

// ProtocolHealthSummary contains only the app-server handshake outcome.
type ProtocolHealthSummary struct {
	State        string   `json:"state"`
	Version      string   `json:"version,omitempty"`
	Initialized  bool     `json:"initialized"`
	Capabilities []string `json:"capabilities,omitempty"`
}

// AccountHealthSummary contains only non-secret account metadata.
type AccountHealthSummary struct {
	State        string `json:"state"`
	AuthMode     string `json:"auth_mode,omitempty"`
	PlanType     string `json:"plan_type,omitempty"`
	RequiresAuth bool   `json:"requires_auth"`
}

// LiveCodexHealthObservation is the provider-neutral result supplied by the
// Codex adapter after it has closed its health-only process.
type LiveCodexHealthObservation struct {
	Executable    ExecutableHealthSummary
	Connection    ProviderConnectionHealth
	Protocol      ProtocolHealthSummary
	Account       AccountHealthSummary
	LoginRequired bool
}

// LiveCodexHealthReport is the versioned application projection of one live
// Codex health check. PersistedRevision remains zero until a durable
// last-known-health store is explicitly composed.
type LiveCodexHealthReport struct {
	CheckedAt         time.Time                `json:"checked_at"`
	Executable        ExecutableHealthSummary  `json:"executable"`
	Connection        ProviderConnectionHealth `json:"connection"`
	Protocol          ProtocolHealthSummary    `json:"protocol"`
	Account           AccountHealthSummary     `json:"account"`
	LoginRequired     bool                     `json:"login_required"`
	PersistedRevision uint64                   `json:"persisted_revision"`
	ErrorCode         string                   `json:"error_code,omitempty"`
	Remediation       string                   `json:"remediation,omitempty"`
}

// LiveCodexHealthPort is the application-owned boundary for one health-only
// provider lifecycle. Implementations must resolve/revalidate the trusted
// executable, perform no model turn, and close the process before returning.
type LiveCodexHealthPort interface {
	CheckLiveCodex(context.Context, LiveCodexHealthRequest) (LiveCodexHealthObservation, error)
}

// CodexLoginMethod identifies a managed login surface without accepting
// pasted credentials or provider-owned token forms.
type CodexLoginMethod string

const (
	CodexLoginBrowser CodexLoginMethod = "chatgpt"
	CodexLoginDevice  CodexLoginMethod = "chatgptDeviceCode"
)

// CodexLoginChallenge contains only browser/device-code instructions.
type CodexLoginChallenge struct {
	Method          CodexLoginMethod `json:"method"`
	LoginID         string           `json:"login_id"`
	AuthURL         string           `json:"auth_url,omitempty"`
	VerificationURL string           `json:"verification_url,omitempty"`
	UserCode        string           `json:"user_code,omitempty"`
}

// CodexSetupPort is the explicit setup boundary. StartCodexLogin must only
// be called after the setup controller receives a confirmed login intent.
type CodexSetupPort interface {
	LiveCodexHealthPort
	StartCodexLogin(context.Context, CodexLoginMethod) (CodexLoginChallenge, error)
	CancelCodexLogin(context.Context) error
}

// CodexSetupPhase identifies the user-visible setup state machine.
type CodexSetupPhase string

const (
	CodexSetupIdle                    CodexSetupPhase = "idle"
	CodexSetupChecking                CodexSetupPhase = "checking"
	CodexSetupReady                   CodexSetupPhase = "ready"
	CodexSetupLoginConfirmationNeeded CodexSetupPhase = "login_confirmation_needed"
	CodexSetupLoggingIn               CodexSetupPhase = "logging_in"
	CodexSetupLoginChallenge          CodexSetupPhase = "login_challenge"
	CodexSetupFailed                  CodexSetupPhase = "failed"
	CodexSetupCanceled                CodexSetupPhase = "canceled"
)

// CodexSetupState is a small immutable projection for setup consumers.
type CodexSetupState struct {
	Phase     CodexSetupPhase
	Health    *LiveCodexHealthReport
	Challenge *CodexLoginChallenge
	Err       error
}

// CodexSetupController enforces that connecting and logging in are separate
// user intents while keeping local review independent of provider failures.
type CodexSetupController struct {
	port  CodexSetupPort
	clock Clock
	state CodexSetupState
}

// NewCodexSetupController constructs an idle setup controller.
func NewCodexSetupController(port CodexSetupPort, clock Clock) *CodexSetupController {
	if clock == nil {
		clock = SystemClock{}
	}
	return &CodexSetupController{port: port, clock: clock, state: CodexSetupState{Phase: CodexSetupIdle}}
}

// State returns a defensive setup projection.
func (c *CodexSetupController) State() CodexSetupState {
	if c == nil {
		return CodexSetupState{Phase: CodexSetupFailed, Err: ErrLiveCodexHealthUnavailable}
	}
	state := c.state
	if state.Health != nil {
		copyValue := *state.Health
		state.Health = &copyValue
	}
	if state.Challenge != nil {
		copyValue := *state.Challenge
		state.Challenge = &copyValue
	}
	return state
}

// ConnectCodex starts the explicit health/setup action. It never logs in.
func (c *CodexSetupController) ConnectCodex(ctx context.Context, correlationID domain.OperationID) (CodexSetupState, error) {
	if c == nil || c.port == nil || ctx == nil || correlationID == "" {
		return CodexSetupState{}, ErrInvalidLiveCodexHealthRequest
	}
	c.state = CodexSetupState{Phase: CodexSetupChecking}
	report, err := NewLiveCodexHealthOperation(c.port, c.clock).Check(ctx, LiveCodexHealthRequest{CorrelationID: correlationID, RequestedAt: c.clock.Now()})
	if err != nil {
		c.state = CodexSetupState{Phase: CodexSetupFailed, Health: &report, Err: err}
		return c.State(), err
	}
	phase := CodexSetupReady
	if report.LoginRequired {
		phase = CodexSetupLoginConfirmationNeeded
	}
	c.state = CodexSetupState{Phase: phase, Health: &report}
	return c.State(), nil
}

// ConfirmLogin starts managed login only after the caller explicitly confirms
// the separate Login to Codex action.
func (c *CodexSetupController) ConfirmLogin(ctx context.Context, confirmed bool, method CodexLoginMethod) (CodexSetupState, error) {
	if c == nil || c.port == nil || ctx == nil || c.state.Phase != CodexSetupLoginConfirmationNeeded || !confirmed {
		if c != nil && c.state.Phase == CodexSetupLoginConfirmationNeeded && !confirmed {
			return c.State(), nil
		}
		return CodexSetupState{}, ErrCodexLoginConfirmationRequired
	}
	if method != CodexLoginBrowser && method != CodexLoginDevice {
		return c.State(), ErrCodexLoginConfirmationRequired
	}
	c.state.Phase = CodexSetupLoggingIn
	challenge, err := c.port.StartCodexLogin(ctx, method)
	if err != nil {
		c.state = CodexSetupState{Phase: CodexSetupFailed, Health: c.state.Health, Err: err}
		return c.State(), err
	}
	c.state.Challenge = &challenge
	c.state.Phase = CodexSetupLoginChallenge
	c.state.Err = nil
	return c.State(), nil
}

// Cancel stops a pending managed login or closes the setup projection.
func (c *CodexSetupController) Cancel(ctx context.Context) error {
	if c == nil {
		return nil
	}
	if c.state.Phase == CodexSetupLoggingIn || c.state.Phase == CodexSetupLoginChallenge {
		if c.port != nil && ctx != nil {
			if err := c.port.CancelCodexLogin(ctx); err != nil {
				return err
			}
		}
	}
	c.state = CodexSetupState{Phase: CodexSetupCanceled}
	return nil
}

// LiveCodexHealthOperation owns the typed application operation while the
// provider adapter owns protocol and process details.
type LiveCodexHealthOperation struct {
	port  LiveCodexHealthPort
	clock Clock
}

// NewLiveCodexHealthOperation composes an explicit live health operation.
func NewLiveCodexHealthOperation(port LiveCodexHealthPort, clock Clock) *LiveCodexHealthOperation {
	if clock == nil {
		clock = SystemClock{}
	}
	return &LiveCodexHealthOperation{port: port, clock: clock}
}

// Check performs one cancellable live health observation and returns only the
// redacted application report.
func (o *LiveCodexHealthOperation) Check(ctx context.Context, request LiveCodexHealthRequest) (LiveCodexHealthReport, error) {
	if ctx == nil || o == nil || o.port == nil || request.CorrelationID == "" {
		return LiveCodexHealthReport{}, ErrInvalidLiveCodexHealthRequest
	}
	if request.RequestedAt.IsZero() {
		request.RequestedAt = o.clock.Now()
	}
	request.RequestedAt = request.RequestedAt.UTC()
	observation, err := o.port.CheckLiveCodex(ctx, request)
	report := LiveCodexHealthReport{
		CheckedAt:     request.RequestedAt,
		Executable:    observation.Executable,
		Connection:    observation.Connection,
		Protocol:      observation.Protocol,
		Account:       observation.Account,
		LoginRequired: observation.LoginRequired,
	}
	if err != nil {
		report.ErrorCode = "live_health_failed"
		report.Remediation = "Review the redacted provider state and retry Connect Codex explicitly."
		return report, err
	}
	if report.Connection == ProviderConnectionAuthRequired || report.LoginRequired {
		report.LoginRequired = true
		report.Remediation = "Choose Connect Codex, then confirm Login to Codex as a separate action."
	}
	return report, nil
}
