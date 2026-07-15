package codex

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"

	"github.com/Scottlr/nudge/internal/provider/codex/protocol"
)

var (
	ErrProviderEventOverflow = errors.New("provider_event_overflow")
	ErrMalformedFrame        = errors.New("malformed app-server frame")
)

type frameQueue struct {
	mu          sync.Mutex
	frames      chan queuedFrame
	resident    int
	maxResident int
}

type queuedFrame struct {
	frame protocol.Frame
	size  int
}

func newFrameQueue(capacity, maxResident int) *frameQueue {
	return &frameQueue{
		frames:      make(chan queuedFrame, capacity),
		maxResident: maxResident,
	}
}

func (q *frameQueue) push(frame protocol.Frame, size int) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	if len(q.frames) == cap(q.frames) || q.resident > q.maxResident-size {
		return ErrProviderEventOverflow
	}
	select {
	case q.frames <- queuedFrame{frame: frame, size: size}:
		q.resident += size
		return nil
	default:
		return ErrProviderEventOverflow
	}
}

func (q *frameQueue) next(ctx context.Context) (protocol.Frame, bool) {
	select {
	case queued := <-q.frames:
		q.mu.Lock()
		q.resident -= queued.size
		q.mu.Unlock()
		return queued.frame, true
	case <-ctx.Done():
		return protocol.Frame{}, false
	}
}

func (c *Client) Stdout(chunk []byte) error {
	frames, err := c.framer.Feed(chunk)
	if err != nil {
		if errors.Is(err, ErrFrameTooLarge) || errors.Is(err, ErrEmptyFrame) || errors.Is(err, ErrPartialFrame) {
			c.fail(err)
			return err
		}
		wrapped := fmt.Errorf("%w: %v", ErrMalformedFrame, err)
		c.fail(wrapped)
		return wrapped
	}
	for _, raw := range frames {
		frame, parseErr := protocol.ParseFrame(raw)
		if parseErr != nil {
			wrapped := fmt.Errorf("%w: %v", ErrMalformedFrame, parseErr)
			c.fail(wrapped)
			return wrapped
		}
		if frame.Kind == protocol.FrameResponse {
			c.routeResponse(frame)
			continue
		}
		if err := c.queue.push(frame, len(raw)); err != nil {
			c.fail(err)
			return err
		}
	}
	return nil
}

func (c *Client) routeResponse(frame protocol.Frame) {
	c.pendingMu.Lock()
	call, ok := c.pending[frame.ID]
	if ok {
		delete(c.pending, frame.ID)
	}
	c.pendingMu.Unlock()
	if !ok {
		// A response for a canceled or already completed request is harmless;
		// keeping no tombstone avoids unbounded late-response state.
		return
	}
	if frame.Error != nil {
		call.done <- callResult{err: &RemoteError{Code: frame.Error.Code, Message: frame.Error.Message}}
		return
	}
	call.done <- callResult{result: frame.Result}
}

func (c *Client) dispatchLoop() {
	defer c.wg.Done()
	for {
		frame, ok := c.queue.next(c.ctx)
		if !ok {
			return
		}
		switch frame.Kind {
		case protocol.FrameNotification:
			c.dispatchNotification(frame)
		case protocol.FrameServerRequest:
			if !c.dispatchServerRequest(frame) {
				return
			}
		}
	}
}

func (c *Client) dispatchNotification(frame protocol.Frame) {
	c.handlerMu.RLock()
	handler := c.notificationHandlers[frame.Method]
	unknown := c.config.UnknownNotification
	c.handlerMu.RUnlock()
	if handler == nil {
		if unknown != nil {
			unknown(frame.Method)
		}
		return
	}
	if err := handler(c.ctx, protocol.Notification{Method: frame.Method, Params: frame.Params}); err != nil {
		c.fail(fmt.Errorf("notification handler failed: %w", err))
	}
}

func (c *Client) dispatchServerRequest(frame protocol.Frame) bool {
	c.handlerMu.RLock()
	handler := c.serverHandlers[frame.Method]
	c.handlerMu.RUnlock()
	var result json.RawMessage
	var rpcErr *protocol.RPCError
	if handler == nil {
		rpcErr = &protocol.RPCError{Code: -32601, Message: "method not found"}
	} else {
		var err error
		result, err = handler(c.ctx, protocol.ServerRequest{ID: frame.ID, Method: frame.Method, Params: frame.Params})
		if err != nil {
			if errors.Is(err, ErrServerRequestDeferred) {
				return true
			}
			rpcErr = &protocol.RPCError{Code: -32000, Message: "server request handler failed"}
		} else if len(result) == 0 || !json.Valid(result) {
			rpcErr = &protocol.RPCError{Code: -32000, Message: "invalid server request result"}
			result = nil
		}
	}
	payload, err := protocol.MarshalResponse(frame.ID, result, rpcErr)
	if err != nil {
		c.fail(err)
		return false
	}
	if err := c.writePayload(payload); err != nil {
		c.fail(err)
		return false
	}
	return true
}
