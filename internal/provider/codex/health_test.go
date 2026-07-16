package codex

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/Scottlr/nudge/internal/process"
)

func TestLiveHealthHandshakeAccountAndClose(t *testing.T) {
	checker := newTestLiveHealthChecker(t, "lifecycle")
	result, err := checker.Check(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if result.Status.State != ConnectionConnected || result.Status.Account.State != AccountAuthenticated {
		t.Fatalf("result = %+v, want connected authenticated health", result)
	}
	if !result.Executable.Trusted || result.UnexpectedRequest {
		t.Fatalf("executable/request health = %+v/%t", result.Executable, result.UnexpectedRequest)
	}
}

func TestLiveHealthNeverLogsIn(t *testing.T) {
	checker := newTestLiveHealthChecker(t, "login")
	result, err := checker.Check(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if result.Status.Account.State != AccountAuthRequired || result.Status.Account.LoginInProgress {
		t.Fatalf("account health = %+v, want auth required without login", result.Status.Account)
	}
}

func TestLiveHealthReportsIncompatibleVersion(t *testing.T) {
	checker := newTestLiveHealthChecker(t, "unsupported_lifecycle")
	result, err := checker.Check(context.Background())
	if !errors.Is(err, ErrUnsupportedVersion) {
		t.Fatalf("Check() error = %v, want unsupported version", err)
	}
	if result.Status.State != ConnectionIncompatible || result.Status.Message != "version_incompatible" {
		t.Fatalf("status = %+v, want incompatible health", result.Status)
	}
}

func newTestLiveHealthChecker(t *testing.T, mode string) *LiveHealthChecker {
	t.Helper()
	resolver := process.NewExecutableResolver()
	executablePath, err := filepath.Abs(os.Args[0])
	if err != nil {
		t.Fatal(err)
	}
	config := DefaultConfig()
	config.Environment = process.EnvironmentPolicy{Mode: process.EnvironmentMinimal, Set: map[string]string{helperEnv: "1", helperMode: mode}}
	checker, err := NewLiveHealthChecker(LiveHealthConfig{
		Resolver: resolver,
		ResolveRequest: process.ResolveExecutableRequest{
			Kind:           process.ExecutableCodex,
			ConfiguredPath: executablePath,
			CurrentDir:     t.TempDir(),
		},
		Connector: ConnectorConfig{Runner: process.NewRunner(), Client: config},
	})
	if err != nil {
		t.Fatal(err)
	}
	return checker
}
