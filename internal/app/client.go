package app

import (
	"context"
	"errors"
	"sync"

	"github.com/Scottlr/nudge/internal/domain"
)

var (
	// ErrClientClosed reports dispatch after runtime shutdown has begun.
	ErrClientClosed = errors.New("application client is closed")
	// ErrConsumerTooSlow reports a non-coalescible event stream that exceeded
	// its bounded queue. The consumer must resubscribe from a full snapshot.
	ErrConsumerTooSlow = errors.New("event consumer too slow")
)

const (
	defaultCommandBuffer = 32
	defaultEventBuffer   = 32
)

// ApplicationClient is the frontend boundary for commands, snapshots, events,
// and bounded point-in-time queries.
type ApplicationClient interface {
	Dispatch(ctx context.Context, command Command) (operationID domain.OperationID, err error)
	Snapshots() <-chan AppSnapshot
	Events() <-chan Event
	Query(ctx context.Context, query Query) (QueryResult, error)
	Close() error
}

// ResultClient is the application boundary used by cancellable operations to
// return typed results to the reducer actor.
type ResultClient interface {
	ApplicationClient
	SubmitResult(ctx context.Context, result Result) error
	EventError() error
}

// ClientOptions configures bounded runtime queues and reducer dependencies.
type ClientOptions struct {
	Clock         Clock
	IDs           IDSource
	TreeSearcher  TreeSearcher
	LargeContent  LargeContentQueryPort
	CommandBuffer int
	EventBuffer   int
}

// Query is a sealed read-only application query.
type Query interface {
	isQuery()
}

// SnapshotQuery asks for the latest complete snapshot at actor order.
type SnapshotQuery struct{}

func (SnapshotQuery) isQuery() {}

// QueryResult is the bounded result of an application query.
type QueryResult struct {
	Snapshot      AppSnapshot
	SearchTree    *SearchTreePage
	ContentRange  *ContentRange
	ContentWindow *ContentWindow
}

// Client owns the reducer goroutine, mailbox, and one frontend subscription.
// Closing the client cancels active state, joins the actor, and closes streams.
type Client struct {
	reducer      *Reducer
	searcher     TreeSearcher
	largeContent LargeContentQueryPort
	mailbox      chan clientRequest
	snapshots    *snapshotSubscription
	events       *eventSubscription
	actorDone    chan struct{}

	mu         sync.Mutex
	accepting  bool
	closeError error
}

type clientRequest struct {
	input    ReducerInput
	query    Query
	ctx      context.Context
	response chan clientResponse
}

type clientResponse struct {
	operationID domain.OperationID
	queryResult QueryResult
	err         error
}

// NewClient starts a single-writer application runtime.
func NewClient(options ClientOptions) (*Client, error) {
	commandBuffer := options.CommandBuffer
	if commandBuffer <= 0 {
		commandBuffer = defaultCommandBuffer
	}
	eventBuffer := options.EventBuffer
	if eventBuffer <= 0 {
		eventBuffer = defaultEventBuffer
	}
	reducer := NewReducer(ReducerConfig{Clock: options.Clock, IDs: options.IDs})
	client := &Client{
		reducer:      reducer,
		searcher:     options.TreeSearcher,
		largeContent: options.LargeContent,
		mailbox:      make(chan clientRequest, commandBuffer),
		snapshots:    newSnapshotSubscription(reducer.Snapshot()),
		events:       newEventSubscription(eventBuffer),
		actorDone:    make(chan struct{}),
		accepting:    true,
	}
	go client.run()
	return client, nil
}

// Dispatch admits one frontend command and returns its operation identity.
func (c *Client) Dispatch(ctx context.Context, command Command) (domain.OperationID, error) {
	if command == nil {
		return "", ErrInvalidReducerInput
	}
	response, err := c.submit(ctx, clientRequest{input: command})
	return response.operationID, err
}

// SubmitResult admits one asynchronous result to the reducer actor.
func (c *Client) SubmitResult(ctx context.Context, result Result) error {
	if result == nil {
		return ErrInvalidReducerInput
	}
	_, err := c.submit(ctx, clientRequest{input: result})
	return err
}

// Complete is an alias for SubmitResult used by operation adapters.
func (c *Client) Complete(ctx context.Context, result Result) error {
	return c.SubmitResult(ctx, result)
}

// Snapshots returns the capacity-one latest-wins snapshot stream. The stream
// starts with the reducer's snapshot at client creation.
func (c *Client) Snapshots() <-chan AppSnapshot {
	return c.snapshots.channel()
}

// Events returns the ordered bounded event stream.
func (c *Client) Events() <-chan Event {
	return c.events.channel()
}

// EventError returns the terminal event-stream error, including slow-consumer
// closure. It returns nil while the stream remains active or closed normally.
func (c *Client) EventError() error {
	return c.events.err()
}

// Query returns a complete snapshot after all earlier mailbox messages have
// been handled by the reducer actor.
func (c *Client) Query(ctx context.Context, query Query) (QueryResult, error) {
	if query == nil {
		return QueryResult{}, ErrInvalidReducerInput
	}
	response, err := c.submit(ctx, clientRequest{query: query, ctx: ctx})
	return response.queryResult, err
}

// Close rejects new work, commits shutdown through the reducer, joins the
// actor, and closes both streams. It is safe to call concurrently or again.
func (c *Client) Close() error {
	c.mu.Lock()
	if !c.accepting {
		done := c.actorDone
		c.mu.Unlock()
		<-done
		c.mu.Lock()
		err := c.closeError
		c.mu.Unlock()
		return err
	}
	c.accepting = false
	request := clientRequest{input: Shutdown{}, response: make(chan clientResponse, 1)}
	c.mailbox <- request
	c.mu.Unlock()

	<-request.response
	<-c.actorDone
	c.mu.Lock()
	err := c.closeError
	c.mu.Unlock()
	return err
}

func (c *Client) submit(ctx context.Context, request clientRequest) (clientResponse, error) {
	if ctx == nil {
		return clientResponse{}, errors.New("nil context")
	}
	if request.response == nil {
		request.response = make(chan clientResponse, 1)
	}
	c.mu.Lock()
	if !c.accepting {
		c.mu.Unlock()
		return clientResponse{}, ErrClientClosed
	}
	select {
	case c.mailbox <- request:
		c.mu.Unlock()
	case <-ctx.Done():
		c.mu.Unlock()
		return clientResponse{}, ctx.Err()
	case <-c.actorDone:
		c.mu.Unlock()
		return clientResponse{}, ErrClientClosed
	}

	select {
	case response := <-request.response:
		return response, response.err
	case <-ctx.Done():
		return clientResponse{}, ctx.Err()
	case <-c.actorDone:
		return clientResponse{}, ErrClientClosed
	}
}

func (c *Client) run() {
	for request := range c.mailbox {
		if request.query != nil {
			if _, ok := request.query.(SnapshotQuery); ok {
				request.response <- clientResponse{queryResult: QueryResult{Snapshot: c.reducer.Snapshot()}}
				continue
			}
			ctx := request.ctx
			if ctx == nil {
				ctx = context.Background()
			}
			switch query := request.query.(type) {
			case LargeContentRangeQuery:
				if c.largeContent == nil {
					request.response <- clientResponse{err: ErrLargeContentUnavailable}
					continue
				}
				result, err := c.largeContent.ReadRange(ctx, query.Request)
				request.response <- clientResponse{queryResult: QueryResult{ContentRange: &result}, err: err}
				continue
			case LargeContentWindowQuery:
				if c.largeContent == nil {
					request.response <- clientResponse{err: ErrLargeContentUnavailable}
					continue
				}
				result, err := c.largeContent.ReadLines(ctx, query.Request)
				request.response <- clientResponse{queryResult: QueryResult{ContentWindow: &result}, err: err}
				continue
			case SearchTreeQuery:
				if c.searcher == nil {
					request.response <- clientResponse{err: ErrTreeSearchUnavailable}
					continue
				}
				page, err := c.searcher.SearchTree(ctx, query)
				if err != nil {
					request.response <- clientResponse{err: err}
					continue
				}
				request.response <- clientResponse{queryResult: QueryResult{SearchTree: &page}}
				continue
			default:
				request.response <- clientResponse{err: ErrInvalidReducerInput}
				continue
			}
		}
		response, err := c.reducer.Handle(request.input)
		if err == nil {
			c.publish(response.Commit)
		}
		request.response <- clientResponse{operationID: response.OperationID, err: err}
		if response.Commit.Closed {
			c.mu.Lock()
			c.accepting = false
			c.closeError = err
			c.mu.Unlock()
			c.snapshots.close()
			c.events.close()
			close(c.actorDone)
			return
		}
	}
}

func (c *Client) publish(commit Commit) {
	if !commit.Changed {
		return
	}
	c.snapshots.offer(commit.Snapshot)
	for _, event := range commit.Events {
		c.events.enqueue(event)
	}
}

type snapshotSubscription struct {
	mu     sync.Mutex
	stream chan AppSnapshot
	closed bool
}

func newSnapshotSubscription(initial AppSnapshot) *snapshotSubscription {
	stream := make(chan AppSnapshot, 1)
	stream <- initial.Clone()
	return &snapshotSubscription{stream: stream}
}

func (s *snapshotSubscription) channel() <-chan AppSnapshot {
	return s.stream
}

func (s *snapshotSubscription) offer(snapshot AppSnapshot) {
	snapshot = snapshot.Clone()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	select {
	case s.stream <- snapshot:
		return
	default:
	}
	select {
	case <-s.stream:
	default:
	}
	s.stream <- snapshot
}

func (s *snapshotSubscription) close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.closed {
		s.closed = true
		close(s.stream)
	}
}

type eventSubscription struct {
	mu       sync.Mutex
	capacity int
	queue    []Event
	wake     chan struct{}
	stop     chan struct{}
	done     chan struct{}
	stream   chan Event
	closed   bool
	stopOnce sync.Once
	errValue error
}

func newEventSubscription(capacity int) *eventSubscription {
	subscription := &eventSubscription{
		capacity: capacity,
		queue:    make([]Event, 0, capacity),
		wake:     make(chan struct{}, 1),
		stop:     make(chan struct{}),
		done:     make(chan struct{}),
		stream:   make(chan Event, capacity),
	}
	go subscription.run()
	return subscription
}

func (s *eventSubscription) channel() <-chan Event {
	return s.stream
}

func (s *eventSubscription) enqueue(event Event) {
	if event == nil {
		return
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	if len(s.queue) < s.capacity {
		s.queue = append(s.queue, event)
		s.mu.Unlock()
		s.signal()
		return
	}
	metadata := event.eventMetadata()
	if metadata.Coalescible && metadata.CoalescingKey != "" {
		for i := len(s.queue) - 1; i >= 0; i-- {
			queuedMetadata := s.queue[i].eventMetadata()
			if queuedMetadata.Coalescible && queuedMetadata.CoalescingKey == metadata.CoalescingKey {
				s.queue[i] = event
				s.mu.Unlock()
				s.signal()
				return
			}
		}
		s.mu.Unlock()
		return
	}
	s.closed = true
	s.errValue = ErrConsumerTooSlow
	s.mu.Unlock()
	s.stopOnce.Do(func() { close(s.stop) })
	s.signal()
}

func (s *eventSubscription) close() {
	s.mu.Lock()
	if !s.closed {
		s.closed = true
	}
	s.mu.Unlock()
	s.stopOnce.Do(func() { close(s.stop) })
	s.signal()
	<-s.done
}

func (s *eventSubscription) err() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.errValue
}

func (s *eventSubscription) signal() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

func (s *eventSubscription) run() {
	defer close(s.stream)
	defer close(s.done)
	for {
		s.mu.Lock()
		if s.closed {
			s.mu.Unlock()
			return
		}
		if len(s.queue) > 0 {
			event := s.queue[0]
			s.queue = s.queue[1:]
			s.mu.Unlock()
			select {
			case s.stream <- event:
			case <-s.stop:
				return
			}
			continue
		}
		s.mu.Unlock()
		select {
		case <-s.wake:
		case <-s.stop:
			return
		}
	}
}

var _ ApplicationClient = (*Client)(nil)
var _ ResultClient = (*Client)(nil)
