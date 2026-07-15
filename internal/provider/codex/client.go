package codex

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"

	"github.com/Scottlr/nudge/internal/process"
	"github.com/Scottlr/nudge/internal/provider/codex/protocol"
)

const (
	defaultMaxFrameBytes            = 16 * 1024 * 1024
	defaultQueueEvents              = 512
	defaultQueueResidentBytes       = 32 * 1024 * 1024
	defaultMaxPendingCalls          = 512
	defaultMaxStderrBytes     int64 = 1 * 1024 * 1024
)

var (
	ErrClientClosed        = errors.New("codex app-server client is closed")
	ErrProcessExited       = errors.New("codex app-server process exited")
	ErrPendingLimit        = errors.New("codex app-server pending request limit reached")
	ErrInvalidClientConfig = errors.New("invalid codex app-server client configuration")
)

// Config bounds all resident protocol state owned by Client. The process
// runner supplies the executable identity; T080 remains responsible for
// resolving and revalidating that identity before construction.
type Config struct {
	MaxFrameBytes      int
	QueueEvents        int
	QueueResidentBytes int
	MaxPendingCalls    int
	MaxStderrBytes     int64
	WorkingDir         string
	Environment        process.EnvironmentPolicy

	// UnknownNotification receives only the method name of an additive
	// notification that has no registered handler. Params are intentionally
	// excluded from this callback.
	UnknownNotification func(method string)
}

// DefaultConfig returns the v1 protocol bounds.
func DefaultConfig() Config {
	return Config{
		MaxFrameBytes:      defaultMaxFrameBytes,
		QueueEvents:        defaultQueueEvents,
		QueueResidentBytes: defaultQueueResidentBytes,
		MaxPendingCalls:    defaultMaxPendingCalls,
		MaxStderrBytes:     defaultMaxStderrBytes,
	}
}

func (c Config) withDefaults() (Config, error) {
	defaults := DefaultConfig()
	if c.MaxFrameBytes == 0 {
		c.MaxFrameBytes = defaults.MaxFrameBytes
	}
	if c.QueueEvents == 0 {
		c.QueueEvents = defaults.QueueEvents
	}
	if c.QueueResidentBytes == 0 {
		c.QueueResidentBytes = defaults.QueueResidentBytes
	}
	if c.MaxPendingCalls == 0 {
		c.MaxPendingCalls = defaults.MaxPendingCalls
	}
	if c.MaxStderrBytes == 0 {
		c.MaxStderrBytes = defaults.MaxStderrBytes
	}
	if c.MaxFrameBytes <= 0 || c.QueueEvents <= 0 || c.QueueResidentBytes <= 0 || c.MaxPendingCalls <= 0 || c.MaxStderrBytes <= 0 || c.QueueResidentBytes < c.MaxFrameBytes {
		return Config{}, ErrInvalidClientConfig
	}
	return c, nil
}

// NotificationHandler handles one normalized notification envelope. The
// callback runs on a bounded dispatcher goroutine and must return promptly.
type NotificationHandler func(context.Context, protocol.Notification) error

// ServerRequestHandler returns a JSON result for one provider-initiated
// request. Returning an error produces a generic protocol error without
// exposing the error text to the child process.
type ServerRequestHandler func(context.Context, protocol.ServerRequest) (json.RawMessage, error)

// RemoteError is the bounded protocol error returned by a server response.
type RemoteError struct {
	Code    int64
	Message string
}

func (e *RemoteError) Error() string {
	if e == nil {
		return "codex app-server request failed"
	}
	return fmt.Sprintf("codex app-server request failed (%d): %s", e.Code, e.Message)
}

type pendingCall struct {
	result any
	done   chan callResult
}

type callResult struct {
	result json.RawMessage
	err    error
}

// Client is a bounded, managed-duplex JSONL client for one Codex app-server
// process. It intentionally does not perform initialization or provider
// normalization; those are owned by later adapter tasks.
type Client struct {
	ctx    context.Context
	cancel context.CancelFunc
	proc   process.Process
	framer *Framer
	queue  *frameQueue
	config Config

	stateMu      sync.Mutex
	terminalErr  error
	closed       bool
	completeOnce sync.Once
	done         chan struct{}

	pendingMu sync.Mutex
	pending   map[protocol.RequestID]*pendingCall
	nextID    atomic.Uint64

	writeMu sync.Mutex

	handlerMu            sync.RWMutex
	notificationHandlers map[string]NotificationHandler
	serverHandlers       map[string]ServerRequestHandler

	wg sync.WaitGroup
}

// NewClient starts one trusted codex app-server process using the repository
// process runner. The caller owns the returned client and should Close it.
func NewClient(ctx context.Context, runner process.Runner, executable process.ExecutableIdentity, config Config) (*Client, error) {
	if runner == nil {
		return nil, ErrInvalidClientConfig
	}
	if ctx == nil {
		ctx = context.Background()
	}
	config, err := config.withDefaults()
	if err != nil {
		return nil, err
	}
	clientCtx, cancel := context.WithCancel(ctx)
	framer, err := NewFramer(config.MaxFrameBytes)
	if err != nil {
		cancel()
		return nil, err
	}
	client := &Client{
		ctx:                  clientCtx,
		cancel:               cancel,
		framer:               framer,
		queue:                newFrameQueue(config.QueueEvents, config.QueueResidentBytes),
		config:               config,
		done:                 make(chan struct{}),
		pending:              make(map[protocol.RequestID]*pendingCall),
		notificationHandlers: make(map[string]NotificationHandler),
		serverHandlers:       make(map[string]ServerRequestHandler),
	}

	proc, err := runner.Start(clientCtx, process.Spec{
		Executable:  executable,
		Args:        []string{"app-server"},
		Dir:         config.WorkingDir,
		Environment: config.Environment,
		StdoutLimit: int64(config.MaxFrameBytes),
		StderrLimit: config.MaxStderrBytes,
	}, client)
	if err != nil {
		cancel()
		return nil, err
	}
	client.proc = proc
	client.wg.Add(2)
	go client.dispatchLoop()
	go client.waitLoop()
	client.stateMu.Lock()
	alreadyComplete := client.terminalErr != nil
	client.stateMu.Unlock()
	if alreadyComplete {
		_ = proc.Cancel()
	}
	return client, nil
}

// RegisterNotificationHandler registers the consumer for one method. A
// later registration replaces the previous handler before dispatch.
func (c *Client) RegisterNotificationHandler(method string, handler NotificationHandler) error {
	if c == nil || handler == nil {
		return protocol.ErrInvalidMethod
	}
	if _, err := protocol.MarshalNotification(method, nil); err != nil {
		return err
	}
	c.handlerMu.Lock()
	c.notificationHandlers[method] = handler
	c.handlerMu.Unlock()
	return nil
}

// RegisterServerRequestHandler registers the handler for one provider-
// initiated request method.
func (c *Client) RegisterServerRequestHandler(method string, handler ServerRequestHandler) error {
	if c == nil || handler == nil {
		return protocol.ErrInvalidMethod
	}
	if _, err := protocol.MarshalNotification(method, nil); err != nil {
		return err
	}
	c.handlerMu.Lock()
	c.serverHandlers[method] = handler
	c.handlerMu.Unlock()
	return nil
}

// Call sends one request and decodes its response into result. Calls may be
// concurrent and responses are matched by the client-allocated request ID.
func (c *Client) Call(ctx context.Context, method string, params any, result any) error {
	if c == nil {
		return ErrClientClosed
	}
	if ctx == nil {
		ctx = context.Background()
	}

	id := protocol.NumericRequestID(c.nextID.Add(1))
	payload, err := protocol.MarshalRequest(id, method, params)
	if err != nil {
		return err
	}
	call := &pendingCall{result: result, done: make(chan callResult, 1)}
	c.stateMu.Lock()
	closed := c.closed
	terminalErr := c.terminalErr
	if !closed {
		c.pendingMu.Lock()
		if len(c.pending) >= c.config.MaxPendingCalls {
			c.pendingMu.Unlock()
			c.stateMu.Unlock()
			return ErrPendingLimit
		}
		c.pending[id] = call
		c.pendingMu.Unlock()
	}
	c.stateMu.Unlock()
	if closed {
		if terminalErr != nil {
			return terminalErr
		}
		return ErrClientClosed
	}

	if err := c.writePayload(payload); err != nil {
		c.removePending(id, err)
		return err
	}

	select {
	case outcome := <-call.done:
		if outcome.err != nil {
			return outcome.err
		}
		if result == nil {
			return nil
		}
		if err := json.Unmarshal(outcome.result, result); err != nil {
			return fmt.Errorf("decode codex app-server response: %w", err)
		}
		return nil
	case <-ctx.Done():
		c.removePending(id, ctx.Err())
		return ctx.Err()
	case <-c.done:
		select {
		case outcome := <-call.done:
			return c.decodeCallOutcome(outcome, result)
		default:
			return c.terminalError()
		}
	}
}

func (c *Client) decodeCallOutcome(outcome callResult, result any) error {
	if outcome.err != nil {
		return outcome.err
	}
	if result == nil {
		return nil
	}
	if err := json.Unmarshal(outcome.result, result); err != nil {
		return fmt.Errorf("decode codex app-server response: %w", err)
	}
	return nil
}

// Notify sends a client notification without allocating a pending request.
func (c *Client) Notify(method string, params any) error {
	if c == nil {
		return ErrClientClosed
	}
	payload, err := protocol.MarshalNotification(method, params)
	if err != nil {
		return err
	}
	return c.writePayload(payload)
}

// Close closes stdin, waits for the managed process, and releases all
// pending calls. It is safe to call more than once.
func (c *Client) Close() error {
	if c == nil {
		return nil
	}
	c.complete(ErrClientClosed, false)
	c.writeMu.Lock()
	closeErr := c.proc.CloseStdin()
	c.writeMu.Unlock()
	_, waitErr := c.proc.Wait()
	c.wg.Wait()
	if closeErr != nil {
		return closeErr
	}
	if waitErr != nil && !errors.Is(waitErr, context.Canceled) {
		return waitErr
	}
	return nil
}

func (c *Client) writePayload(payload []byte) error {
	line, err := EncodeLine(payload, c.config.MaxFrameBytes)
	if err != nil {
		return err
	}
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	c.stateMu.Lock()
	closed := c.closed
	terminalErr := c.terminalErr
	proc := c.proc
	c.stateMu.Unlock()
	if closed || proc == nil {
		if terminalErr != nil {
			return terminalErr
		}
		return ErrClientClosed
	}
	return proc.WriteStdin(c.ctx, line)
}

func (c *Client) waitLoop() {
	defer c.wg.Done()
	_, waitErr := c.proc.Wait()
	frameErr := c.framer.Finish()
	if waitErr == nil && frameErr != nil {
		waitErr = frameErr
	}
	if waitErr == nil {
		waitErr = ErrProcessExited
	}
	c.complete(waitErr, false)
}

func (c *Client) complete(err error, cancelProcess bool) {
	if err == nil {
		err = ErrClientClosed
	}
	c.completeOnce.Do(func() {
		c.stateMu.Lock()
		c.closed = true
		c.terminalErr = err
		proc := c.proc
		c.stateMu.Unlock()
		c.cancel()
		c.pendingMu.Lock()
		pending := c.pending
		c.pending = make(map[protocol.RequestID]*pendingCall)
		c.pendingMu.Unlock()
		for _, call := range pending {
			call.done <- callResult{err: err}
		}
		close(c.done)
		if cancelProcess && proc != nil {
			_ = proc.Cancel()
		}
	})
}

func (c *Client) terminalError() error {
	c.stateMu.Lock()
	defer c.stateMu.Unlock()
	if c.terminalErr != nil {
		return c.terminalErr
	}
	return ErrClientClosed
}

func (c *Client) removePending(id protocol.RequestID, err error) {
	c.pendingMu.Lock()
	call, ok := c.pending[id]
	if ok {
		delete(c.pending, id)
	}
	c.pendingMu.Unlock()
	if ok {
		call.done <- callResult{err: err}
	}
}

func (c *Client) fail(err error) {
	c.complete(err, true)
}

// Stderr deliberately discards child diagnostics from the protocol stream;
// the process runner retains only its bounded diagnostic tail.
func (c *Client) Stderr(_ []byte) error { return nil }
