package setup

import (
	"errors"
	"testing"

	"github.com/Scottlr/nudge/internal/app"
)

func TestConnectCodexRequiresSeparateConfirmedLogin(t *testing.T) {
	model := NewModel()
	if intent, ok := model.Update(ConnectCodexMsg{}); !ok || intent.Kind != IntentCheckHealth || model.State().Phase != Checking {
		t.Fatalf("connect transition = %#v, %t, phase=%s", intent, ok, model.State().Phase)
	}
	model.Update(LiveHealthResultMsg{Report: app.LiveCodexHealthReport{LoginRequired: true}})
	if model.State().Phase != LoginConfirmationNeeded {
		t.Fatalf("phase = %s, want login confirmation", model.State().Phase)
	}
	if intent, ok := model.Update(ConfirmLoginMsg{Confirmed: false, Method: app.CodexLoginBrowser}); ok || intent.Kind != "" {
		t.Fatalf("unconfirmed login emitted intent %#v, %t", intent, ok)
	}
	if model.State().Phase != LoginConfirmationNeeded {
		t.Fatalf("unconfirmed login changed phase to %s", model.State().Phase)
	}
	if intent, ok := model.Update(ConfirmLoginMsg{Confirmed: true, Method: app.CodexLoginBrowser}); !ok || intent.Kind != IntentStartLogin || model.State().Phase != LoggingIn {
		t.Fatalf("confirmed login transition = %#v, %t, phase=%s", intent, ok, model.State().Phase)
	}
}

func TestSetupHealthFailureDoesNotBecomeLogin(t *testing.T) {
	model := NewModel()
	model.Update(ConnectCodexMsg{})
	failure := errors.New("provider unavailable")
	model.Update(LiveHealthResultMsg{Err: failure})
	if model.State().Phase != Failed || !errors.Is(model.State().Err, failure) {
		t.Fatalf("state = %#v, want failed provider state", model.State())
	}
	if _, ok := model.Update(ConfirmLoginMsg{Confirmed: true, Method: app.CodexLoginBrowser}); ok {
		t.Fatal("failed health state unexpectedly emitted login intent")
	}
}
