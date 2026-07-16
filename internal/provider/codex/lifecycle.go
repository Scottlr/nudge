package codex

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Scottlr/nudge/internal/domain"
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
	EventStream   EventStreamConfig
	// HealthOnly denies provider-initiated runtime requests and records them
	// as an incompatible health observation. It is never used for review turns.
	HealthOnly bool
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

	mu                    sync.Mutex
	connecting            bool
	live                  *Connection
	status                ConnectionStatus
	healthRequestObserved atomic.Bool
}

// NewConnector constructs a connection manager without starting Codex.
func NewConnector(config ConnectorConfig) (*Connector, error) {
	config, err := config.withDefaults()
	if err != nil {
		return nil, err
	}
	connector := &Connector{
		config: config,
		status: ConnectionStatus{State: ConnectionDisconnected, Account: AccountStatus{State: AccountUnknown}},
	}
	if config.HealthOnly {
		connector.config.Client.UnknownServerRequest = func(string) {
			connector.healthRequestObserved.Store(true)
		}
	}
	return connector, nil
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
	connection := &Connection{
		client:               client,
		owner:                c,
		turns:                make(map[provider.ProviderTurnRef]provider.ProviderConversationRef),
		conversationBindings: make(map[provider.ProviderConversationRef]conversationEventBinding),
		turnBindings:         make(map[provider.ProviderTurnRef]turnEventBinding),
		events:               newProviderEventStream(c.config.EventStream),
		status:               ConnectionStatus{State: ConnectionConnecting, Account: AccountStatus{State: AccountUnknown}},
	}
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
			AccountLogin:       true,
			RateLimits:         true,
			ResumeConversation: true,
			Streaming:          true,
			Steering:           true,
			ReadOnlyFilesystem: true,
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

	statusMu             sync.RWMutex
	status               ConnectionStatus
	turnMu               sync.Mutex
	turns                map[provider.ProviderTurnRef]provider.ProviderConversationRef
	eventMu              sync.RWMutex
	conversationBindings map[provider.ProviderConversationRef]conversationEventBinding
	turnBindings         map[provider.ProviderTurnRef]turnEventBinding
	pendingApprovals     map[provider.ProviderRequestID]*pendingRuntimeApproval
	eventSequence        atomic.Uint64
	events               *providerEventStream
	closeOnce            sync.Once
	monitorOnce          sync.Once
	monitorDone          chan struct{}
}

const maxPendingRuntimeApprovals = 64

type pendingRuntimeApproval struct {
	remote   protocol.RequestID
	method   string
	approval provider.RuntimeApproval
}

func (c *Connection) registerHandlers() error {
	if err := c.client.RegisterNotificationHandler("account/updated", func(_ context.Context, n protocol.Notification) error { return c.handleAccountUpdated(n) }); err != nil {
		return err
	}
	if err := c.client.RegisterNotificationHandler("account/login/completed", func(_ context.Context, n protocol.Notification) error { return c.handleLoginCompleted(n) }); err != nil {
		return err
	}
	if err := c.client.RegisterNotificationHandler("account/rateLimits/updated", func(_ context.Context, n protocol.Notification) error { return c.handleRateLimitsUpdated(n) }); err != nil {
		return err
	}
	for _, method := range []string{serverRequestCommandApproval, serverRequestFileApproval, serverRequestPermissionsApproval, serverRequestToolCall, serverRequestToolInput, serverRequestLegacyExec, serverRequestLegacyPatch} {
		method := method
		if err := c.client.RegisterServerRequestHandler(method, func(_ context.Context, request protocol.ServerRequest) (json.RawMessage, error) {
			if c.owner != nil && c.owner.config.HealthOnly {
				c.owner.healthRequestObserved.Store(true)
				return runtimeApprovalDenyResult(request.Method), nil
			}
			return c.handleRuntimeApprovalRequest(request)
		}); err != nil {
			return err
		}
	}
	for _, method := range []string{"thread/started", "turn/started", "turn/completed", "item/started", "item/completed", "item/agentMessage/delta", "error", "thread/status/changed"} {
		method := method
		if err := c.client.RegisterNotificationHandler(method, func(_ context.Context, n protocol.Notification) error { return c.handleProviderNotification(n) }); err != nil {
			return err
		}
	}
	return nil
}

func (c *Connection) handleRuntimeApprovalRequest(request protocol.ServerRequest) (json.RawMessage, error) {
	if c == nil || c.client == nil {
		return nil, ErrClientClosed
	}
	c.eventMu.RLock()
	context := c.notificationContext(protocol.Notification{Method: request.Method, Params: request.Params})
	c.eventMu.RUnlock()
	mapped, err := MapRuntimeApprovalRequest(request, context, time.Now().UTC())
	if err != nil {
		return runtimeApprovalDenyResult(request.Method), nil
	}
	c.eventMu.Lock()
	if len(c.pendingApprovals) >= maxPendingRuntimeApprovals {
		c.eventMu.Unlock()
		return runtimeApprovalDenyResult(request.Method), nil
	}
	if _, exists := c.pendingApprovals[mapped.Approval.Request.RequestID]; exists {
		c.eventMu.Unlock()
		return runtimeApprovalDenyResult(request.Method), nil
	}
	if c.pendingApprovals == nil {
		c.pendingApprovals = make(map[provider.ProviderRequestID]*pendingRuntimeApproval)
	}
	c.pendingApprovals[mapped.Approval.Request.RequestID] = &pendingRuntimeApproval{remote: request.ID, method: mapped.Method, approval: mapped.Approval}
	c.eventMu.Unlock()
	event := provider.ProviderEvent{
		Kind:            provider.EventRuntimeApprovalRequested,
		ThreadID:        mapped.Approval.Request.ThreadID,
		OperationID:     mapped.Approval.Request.OperationID,
		CorrelationID:   mapped.Approval.Request.CorrelationID,
		ConversationID:  context.ConversationID,
		ConversationRef: context.ConversationRef,
		TurnID:          "",
		TurnRef:         mapped.Approval.Request.TurnRef,
		RequestID:       mapped.Approval.Request.RequestID,
		ExpiresAt:       mapped.Approval.Request.ExpiresAt,
		Scope:           mapped.Approval.Request.Scope,
		Approval:        &mapped.Approval,
	}
	// The local turn identity is supplied by the binding, not the opaque ref.
	if binding, ok := c.turnBinding(mapped.Approval.Request.TurnRef); ok {
		event.TurnID = binding.TurnID
	}
	if err := c.publishProviderEvent(event); err != nil {
		c.eventMu.Lock()
		delete(c.pendingApprovals, mapped.Approval.Request.RequestID)
		c.eventMu.Unlock()
		return runtimeApprovalDenyResult(request.Method), nil
	}
	return nil, ErrServerRequestDeferred
}

func (c *Connection) turnBinding(ref provider.ProviderTurnRef) (turnEventBinding, bool) {
	c.eventMu.RLock()
	defer c.eventMu.RUnlock()
	binding, ok := c.turnBindings[ref]
	return binding, ok
}

func runtimeApprovalDenyResult(method string) json.RawMessage {
	switch method {
	case serverRequestPermissionsApproval:
		return json.RawMessage(`{"permissions":{"fileSystem":null,"network":null},"scope":"turn","strictAutoReview":true}`)
	case serverRequestToolCall:
		return json.RawMessage(`{"contentItems":[],"success":false}`)
	case serverRequestToolInput:
		return json.RawMessage(`{"answers":{}}`)
	case serverRequestLegacyExec:
		return json.RawMessage(`{"decision":"denied"}`)
	default:
		return json.RawMessage(`{"decision":"decline"}`)
	}
}

// RespondToRuntimeApproval resolves one pending provider request. Only a
// contained command can be allowed once; file/root, tool, and network scopes
// are denied regardless of the UI response.
func (c *Connection) RespondToRuntimeApproval(ctx context.Context, response provider.RuntimeApprovalResponse) error {
	if c == nil || c.client == nil {
		return ErrClientClosed
	}
	now := time.Now().UTC()
	c.eventMu.Lock()
	pending := c.pendingApprovals[response.RequestID]
	c.eventMu.Unlock()
	if pending == nil {
		return provider.ErrApprovalStale
	}
	decision := response.Decision
	if decision == provider.ApprovalAllowOnce && (pending.approval.Request.Scope.Kind != provider.RuntimeApprovalCommand || pending.approval.Details.NetworkTarget != "") {
		decision = provider.ApprovalDeny
	}
	response.Decision = decision
	if err := pending.approval.ResponseValidation(response, now); err != nil {
		if errors.Is(err, provider.ErrApprovalExpired) {
			_ = c.client.RespondServerRequest(ctx, pending.remote, runtimeApprovalDenyResult(pending.method), nil)
			c.eventMu.Lock()
			delete(c.pendingApprovals, response.RequestID)
			c.eventMu.Unlock()
		}
		return err
	}
	result := runtimeApprovalDenyResult(pending.method)
	if decision == provider.ApprovalAllowOnce {
		result = runtimeApprovalAllowResult(pending.method)
	}
	if err := c.client.RespondServerRequest(ctx, pending.remote, result, nil); err != nil {
		return err
	}
	if err := pending.approval.Respond(response, now); err != nil {
		return err
	}
	c.eventMu.Lock()
	delete(c.pendingApprovals, response.RequestID)
	c.eventMu.Unlock()
	return c.publishProviderEvent(provider.ProviderEvent{
		Kind:            provider.EventRuntimeApprovalResolved,
		ThreadID:        pending.approval.Request.ThreadID,
		OperationID:     pending.approval.Request.OperationID,
		CorrelationID:   pending.approval.Request.CorrelationID,
		ConversationID:  contextConversationID(c, pending.approval.Request.TurnRef),
		ConversationRef: contextConversationRef(c, pending.approval.Request.TurnRef),
		TurnID:          contextTurnID(c, pending.approval.Request.TurnRef),
		TurnRef:         pending.approval.Request.TurnRef,
		RequestID:       response.RequestID,
		Scope:           response.Scope,
		Decision:        decision,
	})
}

func runtimeApprovalAllowResult(method string) json.RawMessage {
	switch method {
	case serverRequestLegacyExec:
		return json.RawMessage(`{"decision":"approved"}`)
	case serverRequestToolCall:
		return json.RawMessage(`{"contentItems":[],"success":false}`)
	default:
		return json.RawMessage(`{"decision":"accept"}`)
	}
}

func contextTurnID(c *Connection, ref provider.ProviderTurnRef) domain.ProviderTurnID {
	if binding, ok := c.turnBinding(ref); ok {
		return binding.TurnID
	}
	return ""
}

func contextConversationID(c *Connection, ref provider.ProviderTurnRef) domain.ProviderConversationID {
	if binding, ok := c.turnBinding(ref); ok {
		return binding.ConversationID
	}
	return ""
}

func contextConversationRef(c *Connection, ref provider.ProviderTurnRef) provider.ProviderConversationRef {
	if binding, ok := c.turnBinding(ref); ok {
		return binding.ConversationRef
	}
	return ""
}

// Events returns the bounded normalized event stream for this connection.
func (c *Connection) Events() <-chan provider.ProviderEvent {
	if c == nil {
		return nil
	}
	c.eventMu.Lock()
	if c.events == nil {
		c.events = newProviderEventStream(EventStreamConfig{})
	}
	stream := c.events
	c.eventMu.Unlock()
	return stream.Events()
}

type conversationEventBinding struct {
	ThreadID       domain.ReviewThreadID
	OperationID    domain.OperationID
	CorrelationID  provider.CorrelationID
	ConversationID domain.ProviderConversationID
}

type turnEventBinding struct {
	ConversationRef provider.ProviderConversationRef
	ThreadID        domain.ReviewThreadID
	OperationID     domain.OperationID
	CorrelationID   provider.CorrelationID
	ConversationID  domain.ProviderConversationID
	TurnID          domain.ProviderTurnID
}

// BindConversation associates the remote thread with the durable local
// conversation after the application has completed its attach transaction.
func (c *Connection) BindConversation(ref provider.ProviderConversationRef, id domain.ProviderConversationID, threadID domain.ReviewThreadID, operationID domain.OperationID, correlationID provider.CorrelationID) error {
	if c == nil || ref.Validate() != nil || id == "" || threadID == "" || operationID == "" || correlationID.Validate() != nil {
		return ErrInvalidConversationResponse
	}
	c.eventMu.Lock()
	if c.conversationBindings == nil {
		c.conversationBindings = make(map[provider.ProviderConversationRef]conversationEventBinding)
	}
	c.conversationBindings[ref] = conversationEventBinding{ThreadID: threadID, OperationID: operationID, CorrelationID: correlationID, ConversationID: id}
	c.eventMu.Unlock()
	return nil
}

// BindTurn associates a remote turn with the durable local turn after its
// prepared/start journal has been committed.
func (c *Connection) BindTurn(ref provider.ProviderTurnRef, id domain.ProviderTurnID, conversationID domain.ProviderConversationID, threadID domain.ReviewThreadID, operationID domain.OperationID, correlationID provider.CorrelationID) error {
	if c == nil || ref.Validate() != nil || id == "" || conversationID == "" || threadID == "" || operationID == "" || correlationID.Validate() != nil {
		return ErrInvalidTurnResponse
	}
	c.eventMu.Lock()
	if c.turnBindings == nil {
		c.turnBindings = make(map[provider.ProviderTurnRef]turnEventBinding)
	}
	conversationRef := provider.ProviderConversationRef("")
	c.turnMu.Lock()
	if refConversation, ok := c.turns[ref]; ok {
		conversationRef = refConversation
	}
	c.turnMu.Unlock()
	c.turnBindings[ref] = turnEventBinding{ConversationRef: conversationRef, ThreadID: threadID, OperationID: operationID, CorrelationID: correlationID, ConversationID: conversationID, TurnID: id}
	c.eventMu.Unlock()
	return nil
}

func (c *Connection) handleProviderNotification(notification protocol.Notification) error {
	c.eventMu.RLock()
	context := c.notificationContext(notification)
	c.eventMu.RUnlock()
	events, err := MapNotification(notification, context)
	if err != nil {
		return err
	}
	for _, event := range events {
		if err := c.publishProviderEvent(event); err != nil {
			if errors.Is(err, ErrProviderEventOverflow) {
				return err
			}
			// A notification can arrive between the remote response and the
			// local attach fence. It remains safely ignored until its local
			// binding exists; no remote ID is promoted to a local identity.
			if errors.Is(err, provider.ErrInvalidEvent) {
				continue
			}
			return err
		}
	}
	return nil
}

func (c *Connection) publishProviderEvent(event provider.ProviderEvent) error {
	if event.Sequence == 0 {
		event.Sequence = c.eventSequence.Add(1)
	}
	if err := event.Validate(provider.DefaultValidationLimits()); err != nil {
		return err
	}
	delivery := c.EventsSink().Deliver(c.client.ctx, event)
	if delivery == provider.EventAccepted {
		return nil
	}
	if delivery == provider.EventBackpressure {
		c.client.fail(ErrProviderEventOverflow)
		return ErrProviderEventOverflow
	}
	return ErrClientClosed
}

// EventsSink exposes the adapter-owned bounded sink to the connection's
// handlers and focused adapter tests.
func (c *Connection) EventsSink() *providerEventStream {
	if c == nil {
		return nil
	}
	c.eventMu.Lock()
	if c.events == nil {
		c.events = newProviderEventStream(EventStreamConfig{})
	}
	stream := c.events
	c.eventMu.Unlock()
	return stream
}

func (c *Connection) notificationContext(notification protocol.Notification) NotificationContext {
	context := NotificationContext{}
	var envelope struct {
		ThreadID string `json:"threadId"`
		TurnID   string `json:"turnId"`
		Thread   struct {
			ID string `json:"id"`
		} `json:"thread"`
		Turn struct {
			ID string `json:"id"`
		} `json:"turn"`
	}
	_ = json.Unmarshal(notification.Params, &envelope)
	remoteTurn := envelope.TurnID
	if remoteTurn == "" {
		remoteTurn = envelope.Turn.ID
	}
	if remoteTurn != "" {
		binding, ok := c.turnBindings[provider.ProviderTurnRef(remoteTurn)]
		if ok {
			return NotificationContext{ThreadID: binding.ThreadID, OperationID: binding.OperationID, CorrelationID: binding.CorrelationID, ConversationID: binding.ConversationID, ConversationRef: binding.ConversationRef, TurnID: binding.TurnID, TurnRef: provider.ProviderTurnRef(remoteTurn)}
		}
	}
	remoteThread := envelope.ThreadID
	if remoteThread == "" {
		remoteThread = envelope.Thread.ID
	}
	if remoteThread != "" {
		binding, ok := c.conversationBindings[provider.ProviderConversationRef(remoteThread)]
		if ok {
			return NotificationContext{ThreadID: binding.ThreadID, OperationID: binding.OperationID, CorrelationID: binding.CorrelationID, ConversationID: binding.ConversationID, ConversationRef: provider.ProviderConversationRef(remoteThread)}
		}
	}
	return context
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
	c.eventMu.Lock()
	c.pendingApprovals = make(map[provider.ProviderRequestID]*pendingRuntimeApproval)
	c.eventMu.Unlock()
	c.statusMu.Lock()
	c.status.State = ConnectionDisconnected
	c.status.Message = "disconnected"
	status := c.status
	c.statusMu.Unlock()
	if c.events != nil {
		_ = c.publishProviderEvent(provider.ProviderEvent{Kind: provider.EventDisconnected})
		c.events.Close()
	}
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
