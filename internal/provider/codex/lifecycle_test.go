package codex

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Scottlr/nudge/internal/process"
	"github.com/Scottlr/nudge/internal/provider/codex/protocol"
)

func TestConnectHandshakeOrder(t *testing.T) {
	connector := newTestConnector(t, "lifecycle")
	connection := connectorConnect(t, connector)
	defer connection.Close()

	status := connection.Status()
	if status.State != ConnectionConnected || status.Version.String() != "0.144.0-alpha.4" {
		t.Fatalf("status = %+v, want connected pinned version", status)
	}
	if !status.Capabilities.AccountLogin || !status.Capabilities.RateLimits {
		t.Fatalf("capabilities = %+v, want stable account capabilities", status.Capabilities)
	}
	if status.Account.State != AccountAuthenticated || status.Account.AuthMode != "chatgpt" || status.Account.PlanType != "plus" {
		t.Fatalf("account = %+v, want authenticated non-secret summary", status.Account)
	}
	if err := connector.Connect(context.Background()); !errors.Is(err, ErrConnectionExists) {
		t.Fatalf("second Connect() error = %v, want ErrConnectionExists", err)
	}
	if err := connection.Close(); err != nil {
		t.Fatal(err)
	}
	if err := connector.Connect(context.Background()); err != nil {
		t.Fatalf("reconnect after Close() error = %v", err)
	}
	connector.mu.Lock()
	reconnected := connector.live
	connector.mu.Unlock()
	if reconnected == nil {
		t.Fatal("reconnect did not install a live connection")
	}
	defer reconnected.Close()
}

func TestConnectRejectsUnsupportedVersion(t *testing.T) {
	connector := newTestConnector(t, "unsupported_lifecycle")
	if err := connector.Connect(context.Background()); !errors.Is(err, ErrUnsupportedVersion) {
		t.Fatalf("Connect() error = %v, want ErrUnsupportedVersion", err)
	}
	status, _ := connector.Probe(context.Background())
	if status.State != ConnectionIncompatible || status.Message != "version_incompatible" {
		t.Fatalf("status = %+v, want incompatible", status)
	}
}

func TestAccountMappingDropsSecrets(t *testing.T) {
	status, err := MapAccountResponse(protocol.GetAccountResponse{
		Account: json.RawMessage(`{"type":"chatgpt","planType":"pro","email":"user@example.com","accessToken":"do-not-retain"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(status)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "do-not-retain") || strings.Contains(string(encoded), "user@example.com") {
		t.Fatalf("normalized account retained secret or email: %s", encoded)
	}
	if status.AuthMode != "chatgpt" || status.PlanType != "pro" || status.State != AccountAuthenticated {
		t.Fatalf("status = %+v", status)
	}
}

func TestManagedBrowserLoginUsesChallengeOnly(t *testing.T) {
	connector := newTestConnector(t, "login")
	connection := connectorConnect(t, connector)
	defer connection.Close()

	challenge, err := connection.Login(context.Background(), LoginBrowser)
	if err != nil {
		t.Fatal(err)
	}
	if challenge.LoginID != "login-1" || challenge.AuthURL == "" || challenge.UserCode != "" {
		t.Fatalf("challenge = %+v, want browser URL without token fields", challenge)
	}
	if connection.Account().State != AccountLoggingIn || !connection.Account().LoginInProgress {
		t.Fatalf("account after login = %+v, want logging_in", connection.Account())
	}
}

func TestDisclosureVersionParsing(t *testing.T) {
	policy := DefaultCompatibilityPolicy()
	for _, userAgent := range []string{"codex-cli 0.144.0-alpha.4", "codex_cli_rs/0.144.0-alpha.5 (windows)"} {
		if _, err := policy.Check(userAgent); err != nil {
			t.Fatalf("Check(%q) error = %v", userAgent, err)
		}
	}
}

func connectorConnect(t *testing.T, connector *Connector) *Connection {
	t.Helper()
	if err := connector.Connect(context.Background()); err != nil {
		t.Fatal(err)
	}
	connector.mu.Lock()
	connection := connector.live
	connector.mu.Unlock()
	if connection == nil {
		t.Fatal("Connect() did not retain live connection")
	}
	return connection
}

func newTestConnector(t *testing.T, mode string) *Connector {
	t.Helper()
	resolver := process.NewExecutableResolver()
	executablePath, err := filepath.Abs(os.Args[0])
	if err != nil {
		t.Fatal(err)
	}
	executable, err := resolver.Resolve(context.Background(), process.ResolveExecutableRequest{
		Kind:           process.ExecutableCodex,
		ConfiguredPath: executablePath,
		CurrentDir:     t.TempDir(),
	})
	if err != nil {
		t.Fatal(err)
	}
	config := DefaultConfig()
	config.Environment = process.EnvironmentPolicy{Mode: process.EnvironmentMinimal, Set: map[string]string{helperEnv: "1", helperMode: mode}}
	connector, err := NewConnector(ConnectorConfig{Runner: process.NewRunner(), Executable: executable, Client: config})
	if err != nil {
		t.Fatal(err)
	}
	return connector
}
