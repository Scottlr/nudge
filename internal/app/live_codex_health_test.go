package app

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Scottlr/nudge/internal/domain"
)

type liveHealthPortFake struct {
	observation LiveCodexHealthObservation
	err         error
	checks      int
	logins      int
}

func (f *liveHealthPortFake) CheckLiveCodex(context.Context, LiveCodexHealthRequest) (LiveCodexHealthObservation, error) {
	f.checks++
	return f.observation, f.err
}

func (f *liveHealthPortFake) StartCodexLogin(context.Context, CodexLoginMethod) (CodexLoginChallenge, error) {
	f.logins++
	return CodexLoginChallenge{Method: CodexLoginBrowser, LoginID: "login-1", AuthURL: "https://auth.example.test"}, nil
}

func (f *liveHealthPortFake) CancelCodexLogin(context.Context) error { return nil }

func TestLiveHealthOperationReturnsRedactedObservation(t *testing.T) {
	now := time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC)
	port := &liveHealthPortFake{observation: LiveCodexHealthObservation{Connection: ProviderConnectionConnected, Account: AccountHealthSummary{State: "authenticated"}}}
	operation := NewLiveCodexHealthOperation(port, fixedClock{when: now})
	id, err := domain.NewOperationID("live-health")
	if err != nil {
		t.Fatal(err)
	}
	report, err := operation.Check(context.Background(), LiveCodexHealthRequest{CorrelationID: id})
	if err != nil || !report.CheckedAt.Equal(now) || port.checks != 1 {
		t.Fatalf("report=%+v err=%v checks=%d", report, err, port.checks)
	}
}

func TestCodexSetupRequiresSeparateConfirmedLogin(t *testing.T) {
	port := &liveHealthPortFake{observation: LiveCodexHealthObservation{Connection: ProviderConnectionAuthRequired, LoginRequired: true}}
	controller := NewCodexSetupController(port, fixedClock{when: time.Unix(1, 0)})
	id, err := domain.NewOperationID("connect-codex")
	if err != nil {
		t.Fatal(err)
	}
	state, err := controller.ConnectCodex(context.Background(), id)
	if err != nil || state.Phase != CodexSetupLoginConfirmationNeeded || port.logins != 0 {
		t.Fatalf("state=%+v err=%v logins=%d", state, err, port.logins)
	}
	state, err = controller.ConfirmLogin(context.Background(), false, CodexLoginBrowser)
	if err != nil || state.Phase != CodexSetupLoginConfirmationNeeded || port.logins != 0 {
		t.Fatalf("unconfirmed state=%+v err=%v logins=%d", state, err, port.logins)
	}
	state, err = controller.ConfirmLogin(context.Background(), true, CodexLoginBrowser)
	if err != nil || state.Phase != CodexSetupLoginChallenge || port.logins != 1 {
		t.Fatalf("confirmed state=%+v err=%v logins=%d", state, err, port.logins)
	}
}

func TestLiveHealthFailureDoesNotOverwriteAsSuccess(t *testing.T) {
	failure := errors.New("provider unavailable")
	port := &liveHealthPortFake{err: failure}
	operation := NewLiveCodexHealthOperation(port, fixedClock{when: time.Unix(1, 0)})
	id, _ := domain.NewOperationID("live-health-failure")
	report, err := operation.Check(context.Background(), LiveCodexHealthRequest{CorrelationID: id})
	if !errors.Is(err, failure) || report.ErrorCode != "live_health_failed" || report.PersistedRevision != 0 {
		t.Fatalf("report=%+v err=%v", report, err)
	}
}
