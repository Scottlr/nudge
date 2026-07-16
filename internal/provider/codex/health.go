package codex

import (
	"context"
	"errors"

	"github.com/Scottlr/nudge/internal/process"
)

// LiveHealthConfig composes one short-lived, health-only app-server check.
// The resolver is required so the executable is freshly resolved and
// revalidated immediately before every process start.
type LiveHealthConfig struct {
	Resolver       process.ExecutableResolver
	ResolveRequest process.ResolveExecutableRequest
	Connector      ConnectorConfig
}

// LiveHealthResult contains the adapter-owned safe lifecycle projection.
type LiveHealthResult struct {
	Executable        process.ExecutableHealth
	Status            ConnectionStatus
	UnexpectedRequest bool
}

// LiveHealthChecker owns no retained provider connection. It is intended for
// explicit health actions and always closes a successfully initialized child
// before returning.
type LiveHealthChecker struct {
	config LiveHealthConfig
}

// NewLiveHealthChecker validates the health-only composition.
func NewLiveHealthChecker(config LiveHealthConfig) (*LiveHealthChecker, error) {
	if config.Resolver == nil || config.Connector.Runner == nil {
		return nil, ErrInvalidConnectorConfig
	}
	return &LiveHealthChecker{config: config}, nil
}

// Check starts one bounded app-server, performs the normal handshake and
// account/read query, then deterministically closes the child.
func (h *LiveHealthChecker) Check(ctx context.Context) (LiveHealthResult, error) {
	if h == nil || h.config.Resolver == nil || h.config.Connector.Runner == nil {
		return LiveHealthResult{}, ErrInvalidConnectorConfig
	}
	if ctx == nil {
		return LiveHealthResult{}, context.Canceled
	}
	identity, err := h.config.Resolver.Resolve(ctx, h.config.ResolveRequest)
	if err != nil {
		return LiveHealthResult{}, err
	}
	identity, err = h.config.Resolver.RevalidateForLaunch(ctx, identity)
	if err != nil {
		return LiveHealthResult{Executable: identity.Health("")}, err
	}
	connectorConfig := h.config.Connector
	connectorConfig.Executable = identity
	connectorConfig.HealthOnly = true
	connector, err := NewConnector(connectorConfig)
	if err != nil {
		return LiveHealthResult{Executable: identity.Health("")}, err
	}
	attempt, err := connector.ConnectAsync(ctx)
	if err != nil {
		return LiveHealthResult{Executable: identity.Health("")}, err
	}
	result := attempt.Wait(ctx)
	if result.Err != nil && ctx.Err() != nil {
		// ConnectAsync owns a goroutine. Once the caller cancels, join it so
		// process and protocol cleanup is complete before this operation ends.
		result = attempt.Wait(context.Background())
	}
	if result.Connection != nil {
		defer result.Connection.Close()
	}
	if result.Err != nil {
		return LiveHealthResult{Executable: identity.Health(result.Status.Version.String()), Status: result.Status, UnexpectedRequest: connector.healthRequestObserved.Load()}, result.Err
	}
	status := result.Status
	if connector.healthRequestObserved.Load() {
		status.State = ConnectionIncompatible
		status.Message = "unexpected_health_request"
	}
	return LiveHealthResult{Executable: identity.Health(status.Version.String()), Status: status, UnexpectedRequest: connector.healthRequestObserved.Load()}, nil
}

// IsCancellation reports whether a live health failure was caused by the
// caller's bounded cancellation policy.
func IsCancellation(err error) bool {
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}
