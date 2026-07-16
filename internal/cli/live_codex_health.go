package cli

import (
	"context"
	"os"
	"path/filepath"

	"github.com/Scottlr/nudge/internal/app"
	"github.com/Scottlr/nudge/internal/config"
	"github.com/Scottlr/nudge/internal/paths"
	"github.com/Scottlr/nudge/internal/process"
	"github.com/Scottlr/nudge/internal/provider"
	"github.com/Scottlr/nudge/internal/provider/codex"
)

// codexLiveHealthPort adapts the provider-owned lifecycle projection to the
// application-owned live-health operation without making app import protocol
// DTOs or provider lifecycle types.
type codexLiveHealthPort struct {
	checker *codex.LiveHealthChecker
}

func (p codexLiveHealthPort) CheckLiveCodex(ctx context.Context, _ app.LiveCodexHealthRequest) (app.LiveCodexHealthObservation, error) {
	result, err := p.checker.Check(ctx)
	status := result.Status
	connection := app.ProviderConnectionUnavailable
	switch status.State {
	case codex.ConnectionConnected:
		connection = app.ProviderConnectionConnected
		if status.Account.State == codex.AccountAuthRequired {
			connection = app.ProviderConnectionAuthRequired
		}
	case codex.ConnectionMissing:
		connection = app.ProviderConnectionMissing
	case codex.ConnectionIncompatible:
		connection = app.ProviderConnectionIncompatible
	case codex.ConnectionDisconnected:
		connection = app.ProviderConnectionUnavailable
	}
	if result.UnexpectedRequest {
		connection = app.ProviderConnectionIncompatible
	}
	account := app.AccountHealthSummary{
		State:        string(status.Account.State),
		AuthMode:     app.NormalizeHealthToken(status.Account.AuthMode, 64),
		PlanType:     app.NormalizeHealthToken(status.Account.PlanType, 64),
		RequiresAuth: status.Account.RequiresAuth,
	}
	return app.LiveCodexHealthObservation{
		Executable: app.ExecutableHealthSummary{
			Kind:               string(result.Executable.Kind),
			Source:             string(result.Executable.Source),
			CanonicalPath:      app.NormalizeHealthToken(filepath.Base(result.Executable.CanonicalPath), 256),
			Version:            app.NormalizeHealthToken(result.Executable.Version, 96),
			IdentityHashPrefix: app.NormalizeHealthToken(result.Executable.IdentityHashPrefix, 32),
			Trusted:            result.Executable.Trusted,
		},
		Connection: connection,
		Protocol: app.ProtocolHealthSummary{
			State:        app.NormalizeHealthToken(string(status.State), 64),
			Version:      app.NormalizeHealthToken(status.Version.String(), 96),
			Initialized:  status.Version.String() != "0.0.0",
			Capabilities: capabilityNames(status.Capabilities),
		},
		Account:       account,
		LoginRequired: status.Account.State == codex.AccountAuthRequired || status.Account.RequiresAuth,
	}, err
}

func capabilityNames(value provider.ProviderCapabilities) []string {
	result := make([]string, 0, 9)
	if value.AccountLogin {
		result = append(result, "account_login")
	}
	if value.RateLimits {
		result = append(result, "rate_limits")
	}
	if value.ResumeConversation {
		result = append(result, "resume_conversation")
	}
	if value.Streaming {
		result = append(result, "streaming")
	}
	if value.Steering {
		result = append(result, "steering")
	}
	if value.ReadOnlyFilesystem {
		result = append(result, "read_only_filesystem")
	}
	return result
}

func newLiveCodexHealthOperation(ctx context.Context, startPath string, locations paths.Locations, loaded config.LoadedConfig) (*app.LiveCodexHealthOperation, error) {
	if ctx == nil {
		return nil, app.ErrInvalidLiveCodexHealthRequest
	}
	configured := ""
	if loaded.Config.Codex.Executable != "" && filepath.IsAbs(loaded.Config.Codex.Executable) {
		configured = loaded.Config.Codex.Executable
	}
	resolver := process.NewExecutableResolver()
	checker, err := codex.NewLiveHealthChecker(codex.LiveHealthConfig{
		Resolver: resolver,
		ResolveRequest: process.ResolveExecutableRequest{
			Kind:               process.ExecutableCodex,
			ConfiguredPath:     configured,
			SearchPath:         doctorEnvironmentValue(os.Environ(), "PATH"),
			CurrentDir:         startPath,
			WorkspaceRoots:     rootsIfValid(locations.WorkspaceRoot),
			NudgeWritableRoots: validRoots(locations.ConfigRoot, locations.StateRoot, locations.CacheRoot, locations.LogRoot),
		},
		Connector: codex.ConnectorConfig{
			Runner:        process.NewRunner(),
			Client:        codex.DefaultConfig(),
			ClientName:    "nudge",
			ClientTitle:   "Nudge",
			ClientVersion: "0.1.0-dev",
			Compatibility: codex.DefaultCompatibilityPolicy(),
		},
	})
	if err != nil {
		return nil, err
	}
	return app.NewLiveCodexHealthOperation(codexLiveHealthPort{checker: checker}, app.SystemClock{}), nil
}
