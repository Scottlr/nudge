package app

import (
	"context"
	"errors"
	"testing"
	"time"
)

type failingProviderPort struct{}

func (failingProviderPort) Connect(context.Context) error { return errors.New("codex unavailable") }

func (failingProviderPort) Probe(context.Context) (ProviderStatus, error) {
	return ProviderStatus{}, errors.New("probe should not run")
}

func TestProviderFailureKeepsReviewUsable(t *testing.T) {
	state := NewState()
	initialDisclosure := state.Provider.Disclosure
	result := <-NewProviderStatusUseCase(failingProviderPort{}).Connect(context.Background(), state.Provider)
	if result.Err == nil || result.Status.Connection != ProviderUnavailable {
		t.Fatalf("result = %+v, want degraded provider status", result)
	}
	if result.Status.Disclosure != initialDisclosure {
		t.Fatalf("provider failure changed disclosure state: got %+v want %+v", result.Status.Disclosure, initialDisclosure)
	}
}

func TestDisclosureGateBlocksStaleOrDeclinedUse(t *testing.T) {
	status := NewState().Provider
	status.Connection = ProviderConnected
	status.Account.State = ProviderAccountAuthenticated
	if !errors.Is(CheckProviderTurn(status), ErrProviderDataDisclosureRequired) {
		t.Fatal("unacknowledged disclosure allowed provider turn")
	}
	if err := status.Disclosure.Acknowledge(ProviderDisclosureVersionV1, time.Now(), DisclosureProcessOnly); err != nil {
		t.Fatal(err)
	}
	if err := CheckProviderTurn(status); err != nil {
		t.Fatalf("acknowledged disclosure blocked turn: %v", err)
	}
	status.Disclosure.CurrentVersion = "provider-data-v2"
	if !errors.Is(CheckProviderTurn(status), ErrProviderDataDisclosureRequired) {
		t.Fatal("stale disclosure acknowledgement allowed provider turn")
	}
	status.Disclosure.Decline()
	if status.Disclosure.Acknowledged() {
		t.Fatal("declined disclosure remained acknowledged")
	}
}
