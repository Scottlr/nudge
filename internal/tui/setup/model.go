// Package setup owns the disposable Connect Codex setup projection. It emits
// intents for the root/application boundary and never starts provider work.
package setup

import "github.com/Scottlr/nudge/internal/app"

// Phase aliases the application setup phase for rendering and tests.
type Phase = app.CodexSetupPhase

const (
	Idle                    = app.CodexSetupIdle
	Checking                = app.CodexSetupChecking
	Ready                   = app.CodexSetupReady
	LoginConfirmationNeeded = app.CodexSetupLoginConfirmationNeeded
	LoggingIn               = app.CodexSetupLoggingIn
	LoginChallenge          = app.CodexSetupLoginChallenge
	Failed                  = app.CodexSetupFailed
	Canceled                = app.CodexSetupCanceled
)

// IntentKind identifies the only provider actions the setup projection may
// request from its root owner.
type IntentKind string

const (
	IntentCheckHealth IntentKind = "check_health"
	IntentStartLogin  IntentKind = "start_login"
	IntentCancel      IntentKind = "cancel"
)

// Intent is a provider-neutral setup action emitted after a user intent.
type Intent struct {
	Kind   IntentKind
	Method app.CodexLoginMethod
}

// Messages are produced by the root's asynchronous application commands.
type ConnectCodexMsg struct{}

type LiveHealthResultMsg struct {
	Report app.LiveCodexHealthReport
	Err    error
}

type ConfirmLoginMsg struct {
	Confirmed bool
	Method    app.CodexLoginMethod
}

type LoginResultMsg struct {
	Challenge app.CodexLoginChallenge
	Err       error
}

type CancelMsg struct{}

// Model is a bounded, transition-only setup projection.
type Model struct {
	state app.CodexSetupState
}

// NewModel creates an idle setup projection.
func NewModel() Model {
	return Model{state: app.CodexSetupState{Phase: app.CodexSetupIdle}}
}

// State returns the current application-owned setup projection.
func (m Model) State() app.CodexSetupState { return m.state }

// Update applies one setup message and emits zero or one root intent.
func (m *Model) Update(message any) (Intent, bool) {
	if m == nil {
		return Intent{}, false
	}
	switch value := message.(type) {
	case ConnectCodexMsg:
		if m.state.Phase != app.CodexSetupIdle && m.state.Phase != app.CodexSetupFailed && m.state.Phase != app.CodexSetupCanceled {
			return Intent{}, false
		}
		m.state = app.CodexSetupState{Phase: app.CodexSetupChecking}
		return Intent{Kind: IntentCheckHealth}, true
	case LiveHealthResultMsg:
		m.state = app.CodexSetupState{Phase: app.CodexSetupReady, Health: &value.Report, Err: value.Err}
		if value.Err != nil {
			m.state.Phase = app.CodexSetupFailed
		} else if value.Report.LoginRequired {
			m.state.Phase = app.CodexSetupLoginConfirmationNeeded
		}
		return Intent{}, false
	case ConfirmLoginMsg:
		if m.state.Phase != app.CodexSetupLoginConfirmationNeeded {
			return Intent{}, false
		}
		if !value.Confirmed {
			return Intent{}, false
		}
		if value.Method != app.CodexLoginBrowser && value.Method != app.CodexLoginDevice {
			return Intent{}, false
		}
		m.state.Phase = app.CodexSetupLoggingIn
		return Intent{Kind: IntentStartLogin, Method: value.Method}, true
	case LoginResultMsg:
		if value.Err != nil {
			m.state.Phase = app.CodexSetupFailed
			m.state.Err = value.Err
			return Intent{}, false
		}
		m.state.Challenge = &value.Challenge
		m.state.Phase = app.CodexSetupLoginChallenge
		m.state.Err = nil
	case CancelMsg:
		m.state = app.CodexSetupState{Phase: app.CodexSetupCanceled}
		return Intent{Kind: IntentCancel}, true
	}
	return Intent{}, false
}
