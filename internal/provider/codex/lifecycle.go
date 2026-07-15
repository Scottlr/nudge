package codex

import (
	"context"
	"errors"
	"sync"

	"github.com/Scottlr/nudge/internal/process"
	"github.com/Scottlr/nudge/internal/provider"
	"github.com/Scottlr/nudge/internal/provider/codex/protocol"
)

var (
	ErrInvalidConnectorConfig = errors.New("invalid codex connector configuration")
	ErrConnectionInProgress   = errors.New("codex app-server connection is already starting")
	ErrConnectionExists       = errors.New("codex app-server connection already exists")
	ErrMissingExecutable      = errors.New("codex app-server executable is missing")
	ErrInvalidInitialize      = errors.New("invalid codex initialize response")
)

// ConnectionState is the adapter-level projection used by status consumers.
type ConnectionState string

const (
	ConnectionDisconnected ConnectionState = "disconnected"
	ConnectionConnecting   ConnectionState = "connecting"
	ConnectionConnected    ConnectionState = "connected"
	ConnectionRestarting   ConnectionState = "restarting"
	ConnectionUnavailable  ConnectionState = "unavailable"
	ConnectionMissing      ConnectionState = "missing"
	ConnectionIncompatible ConnectionState = "incompatible"
)

// ConnectionStatus is safe operational state; it contains no protocol body,
// prompt, account secret, or credential path.
type ConnectionStatus struct {
	State        ConnectionState
	Version      Version
	Capabilities provider.ProviderCapabilities
	Account      AccountStatus
	Message      string
}

// ConnectorConfig supplies the trusted process identity and stable client
// metadata for one Codex app-server connection.
type ConnectorConfig struct {
	Runner        process.Runner
	Executable    process.ExecutableIdentity
	Client        Config
	ClientName    string
	ClientTitle   string
	ClientVersion string
	Compatibility CompatibilityPolicy
}

func (c ConnectorConfig) withDefaults() (ConnectorConfig, error) {
	if c.Runner == nil || c.Executable.CanonicalPath == "" {
		return ConnectorConfig{}, ErrInvalidConnectorConfig
	}
	if c.ClientName == "" {
		c.ClientName = "nudge"
	}
	if c.ClientTitle == "" {
		c.ClientTitle = "Nudge"
	}
	if c.ClientVersion == "" {
		c.ClientVersion = "0.1.0-dev"
	}
	if c.Compatibility == (CompatibilityPolicy{}) {
		c.Compatibility = DefaultCompatibilityPolicy()
	}
	if err := c.Compatibility.validate(); err != nil {
		return ConnectorConfig{}, err
	}
	return c, nil
}

// Connector owns the one-live-connection invariant.
type Connector struct {
	config ConnectorConfig

	mu         sync.Mutex
	connecting bool
	live       *Connection
	status     ConnectionStatus
}

// NewConnector constructs a connection manager without starting Codex.
func NewConnector(config ConnectorConfig) (*Connector, error) {
	config, err := config.withDefaults()
	if err != nil {
		return nil, err
	}
	return &Connector{
		config: config,
		status: ConnectionStatus{State: ConnectionDisconnected, Account: AccountStatus{State: AccountUnknown}},
	}, nil
}

// Connect starts an asynchronous handshake and waits for its result. Callers
// that own a local review shell should use ConnectAsync so the shell remains
// usable while Codex starts or degrades.
func (c *Connector) Connect(ctx context.Context) error {
	attempt, err := c.ConnectAsync(ctx)
	if err != nil {
		return err
	}
	return attempt.Wait(ctx).Err
}

// ConnectResult is the joined outcome of one asynchronous connection attempt.
type ConnectResult struct {
	Connection *Connection
	Status     ConnectionStatus
	Err        error
}

// ConnectAttempt is a joinable, bounded asynchronous connection operation.
type ConnectAttempt struct {
	done chan ConnectResult
}

// ConnectAsync begins exactly one initialize/initialized handshake in a
// connector-owned goroutine and returns without blocking the review shell.
func (c *Connector) ConnectAsync(ctx context.Context) (*ConnectAttempt, error) {
	if c == nil {
		return nil, ErrInvalidConnectorConfig
	}
	if ctx == nil {
		ctx = context.Background()
	}
	c.mu.Lock()
	if c.live != nil {
		c.mu.Unlock()
		return nil, ErrConnectionExists
	}
	if c.connecting {
		c.mu.Unlock()
		return nil, ErrConnectionInProgress
	}
	c.connecting = true
	c.status = ConnectionStatus{State: ConnectionConnecting, Account: AccountStatus{State: AccountUnknown}}
	c.mu.Unlock()
	attempt := &ConnectAttempt{done: make(chan ConnectResult, 1)}
	go func() {
		connection, status, err := c.connect(ctx)
		c.mu.Lock()
		c.connecting = false
		if err == nil {
			c.live = connection
			c.status = status
			connection.startMonitor()
		} else {
			c.status = status
		}
		c.mu.Unlock()
		attempt.done <- ConnectResult{Connection: connection, Status: status, Err: err}
		close(attempt.done)
	}()
	return attempt, nil
}

// Wait joins the asynchronous attempt, respecting the caller's context.
func (a *ConnectAttempt) Wait(ctx context.Context) ConnectResult {
	if a == nil {
		return ConnectResult{Err: ErrInvalidConnectorConfig}
	}
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case result := <-a.done:
		return result
	case <-ctx.Done():
		return ConnectResult{Status: ConnectionStatus{State: ConnectionConnecting}, Err: ctx.Err()}
	}
}

// Status returns a detached operational projection.
func (c *Connector) Status() ConnectionStatus {
	if c == nil {
		return ConnectionStatus{State: ConnectionUnavailable, Account: AccountStatus{State: AccountUnavailable}}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.live != nil {
		return c.live.Status()
	}
	return c.status
}

// Probe performs no process or repository mutation. It reports whether the
// trusted executable identity is present; version is resolved during Connect.
func (c *Connector) Probe(_ context.Context) (ConnectionStatus, error) {
	if c == nil {
		return ConnectionStatus{State: ConnectionUnavailable, Message: "provider_unavailable", Account: AccountStatus{State: AccountUnavailable}}, ErrInvalidConnectorConfig
	}
	if c.config.Executable.CanonicalPath == "" {
		return ConnectionStatus{State: ConnectionMissing, Message: "executable_missing", Account: AccountStatus{State: AccountUnavailable}}, ErrMissingExecutable
	}
	return c.Status(), nil
}

func (c *Connector) connect(ctx context.Context) (*Connection, ConnectionStatus, error) {
	if c.config.Executable.CanonicalPath == "" {
		return nil, ConnectionStatus{State: ConnectionMissing, Message: "executable_missing", Account: AccountStatus{State: AccountUnavailable}}, ErrMissingExecutable
	}
	client, err := NewClient(ctx, c.config.Runner, c.config.Executable, c.config.Client)
	if err != nil {
		return nil, ConnectionStatus{State: ConnectionUnavailable, Message: "provider_unavailable", Account: AccountStatus{State: AccountUnavailable}}, err
	}
	connection := &Connection{client: client, owner: c, status: ConnectionStatus{State: ConnectionConnecting, Account: AccountStatus{State: AccountUnknown}}}
	if err := connection.registerHandlers(); err != nil {
		_ = client.Close()
		return nil, ConnectionStatus{State: ConnectionUnavailable, Message: "provider_unavailable", Account: AccountStatus{State: AccountUnavailable}}, err
	}
	var initialize protocol.InitializeResponse
	if err := client.Call(ctx, "initialize", protocol.InitializeParams{ClientInfo: protocol.ClientInfo{Name: c.config.ClientName, Title: c.config.ClientTitle, Version: c.config.ClientVersion}}, &initialize); err != nil {
		_ = client.Close()
		return nil, ConnectionStatus{State: ConnectionUnavailable, Message: "initialize_failed", Account: AccountStatus{State: AccountUnavailable}}, err
	}
	if initialize.UserAgent == "" || initialize.PlatformFamily == "" || initialize.PlatformOS == "" || initialize.CodexHome == "" {
		_ = client.Close()
		return nil, ConnectionStatus{State: ConnectionUnavailable, Message: "initialize_invalid", Account: AccountStatus{State: AccountUnavailable}}, ErrInvalidInitialize
	}
	version, err := c.config.Compatibility.Check(initialize.UserAgent)
	if err != nil {
		_ = client.Close()
		return nil, ConnectionStatus{State: ConnectionIncompatible, Message: "version_incompatible", Account: AccountStatus{State: AccountUnavailable}}, err
	}
	if err := client.Notify("initialized", nil); err != nil {
		_ = client.Close()
		return nil, ConnectionStatus{State: ConnectionUnavailable, Message: "initialized_failed", Version: version, Account: AccountStatus{State: AccountUnavailable}}, err
	}
	var accountResponse protocol.GetAccountResponse
	if err := client.Call(ctx, "account/read", protocol.GetAccountParams{}, &accountResponse); err != nil {
		_ = client.Close()
		return nil, ConnectionStatus{State: ConnectionUnavailable, Message: "account_unavailable", Version: version, Account: AccountStatus{State: AccountUnavailable}}, err
	}
	account, err := MapAccountResponse(accountResponse)
	if err != nil {
		_ = client.Close()
		return nil, ConnectionStatus{State: ConnectionUnavailable, Message: "account_invalid", Version: version, Account: AccountStatus{State: AccountUnavailable}}, err
	}
	connection.setAccount(account)
	connection.statusMu.Lock()
	connection.status = ConnectionStatus{
		State:   ConnectionConnected,
		Version: version,
		Capabilities: provider.ProviderCapabilities{
			AccountLogin: true,
			RateLimits:   true,
		},
		Account: account,
	}
	connection.statusMu.Unlock()
	return connection, connection.Status(), nil
}

// Connection is one initialized app-server session.
type Connection struct {
	client *Client
	owner  *Connector

	accountMu sync.RWMutex
	account   AccountStatus
	loginID   string

	statusMu    sync.RWMutex
	status      ConnectionStatus
	closeOnce   sync.Once
	monitorOnce sync.Once
	monitorDone chan struct{}
}

func (c *Connection) registerHandlers() error {
	if err := c.client.RegisterNotificationHandler("account/updated", func(_ context.Context, n protocol.Notification) error { return c.handleAccountUpdated(n) }); err != nil {
		return err
	}
	return c.client.RegisterNotificationHandler("account/login/completed", func(_ context.Context, n protocol.Notification) error { return c.handleLoginCompleted(n) })
}

// Status returns a detached status projection including the latest account.
func (c *Connection) Status() ConnectionStatus {
	if c == nil {
		return ConnectionStatus{State: ConnectionUnavailable, Account: AccountStatus{State: AccountUnavailable}}
	}
	c.statusMu.RLock()
	status := c.status
	c.statusMu.RUnlock()
	status.Account = c.Account()
	return status
}

func (c *Connection) startMonitor() {
	c.monitorOnce.Do(func() {
		c.monitorDone = make(chan struct{})
		go c.monitor()
	})
}

func (c *Connection) monitor() {
	<-c.client.done
	c.statusMu.Lock()
	c.status.State = ConnectionDisconnected
	c.status.Message = "disconnected"
	status := c.status
	c.statusMu.Unlock()
	if c.owner != nil {
		c.owner.mu.Lock()
		if c.owner.live == c {
			c.owner.live = nil
			c.owner.status = status
		}
		c.owner.mu.Unlock()
	}
	close(c.monitorDone)
}

// Close terminates the one live app-server process and is idempotent.
func (c *Connection) Close() error {
	if c == nil || c.client == nil {
		return nil
	}
	var err error
	c.closeOnce.Do(func() {
		err = c.client.Close()
		if c.monitorDone != nil {
			<-c.monitorDone
		}
		if c.owner != nil {
			c.owner.mu.Lock()
			if c.owner.live == c {
				c.owner.live = nil
				c.owner.status = c.Status()
			}
			c.owner.mu.Unlock()
		}
	})
	return err
}
