package codex

import (
	"context"
	"sync"

	"github.com/Scottlr/nudge/internal/provider"
)

const (
	defaultProviderEventBuffer       = 128
	defaultProviderEventResident     = 2 << 20
	defaultProviderEventCoalesceSize = 256 << 10
)

// EventStreamConfig bounds the adapter-to-application event pipe. The pipe
// owns its queue and never makes the protocol dispatcher wait for a slow UI.
type EventStreamConfig struct {
	Capacity      int
	ResidentBytes int
	CoalesceBytes int
}

func (c EventStreamConfig) withDefaults() EventStreamConfig {
	if c.Capacity <= 0 {
		c.Capacity = defaultProviderEventBuffer
	}
	if c.ResidentBytes <= 0 {
		c.ResidentBytes = defaultProviderEventResident
	}
	if c.CoalesceBytes <= 0 {
		c.CoalesceBytes = defaultProviderEventCoalesceSize
	}
	return c
}

type providerEventStream struct {
	config EventStreamConfig

	mu       sync.Mutex
	queue    []provider.ProviderEvent
	resident int
	closed   bool
	wake     chan struct{}
	stop     chan struct{}
	done     chan struct{}
	out      chan provider.ProviderEvent
}

func newProviderEventStream(config EventStreamConfig) *providerEventStream {
	config = config.withDefaults()
	stream := &providerEventStream{
		config: config,
		queue:  make([]provider.ProviderEvent, 0, config.Capacity),
		wake:   make(chan struct{}, 1),
		stop:   make(chan struct{}),
		done:   make(chan struct{}),
		out:    make(chan provider.ProviderEvent, config.Capacity),
	}
	go stream.run()
	return stream
}

func (s *providerEventStream) Events() <-chan provider.ProviderEvent {
	if s == nil {
		return nil
	}
	return s.out
}

func (s *providerEventStream) Deliver(_ context.Context, event provider.ProviderEvent) provider.EventDelivery {
	if s == nil {
		return provider.EventClosed
	}
	if err := event.Validate(provider.DefaultValidationLimits()); err != nil {
		return provider.EventBackpressure
	}
	size := providerEventSize(event)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return provider.EventClosed
	}
	if size > s.config.ResidentBytes {
		return provider.EventBackpressure
	}
	if event.Coalescible && len(s.queue) > 0 {
		last := &s.queue[len(s.queue)-1]
		if compatibleProviderDeltas(*last, event) {
			previousText := last.Text
			previousSequence := last.Sequence
			previousSize := providerEventSize(*last)
			merged := len([]byte(last.Text)) + len([]byte(event.Text))
			if merged <= s.config.CoalesceBytes {
				last.Text += event.Text
				last.Sequence = event.Sequence
				mergedSize := providerEventSize(*last)
				if s.resident-previousSize+mergedSize <= s.config.ResidentBytes {
					s.resident += mergedSize - previousSize
					s.signal()
					return provider.EventAccepted
				}
				// Restore the previous event if the merged resident budget would
				// be exceeded. Its exact bytes remain available to the consumer.
				last.Text = previousText
				last.Sequence = previousSequence
			}
		}
	}
	if len(s.queue) >= s.config.Capacity || s.resident > s.config.ResidentBytes-size {
		return provider.EventBackpressure
	}
	s.queue = append(s.queue, event)
	s.resident += size
	s.signal()
	return provider.EventAccepted
}

func (s *providerEventStream) Close() {
	if s == nil {
		return
	}
	s.mu.Lock()
	if !s.closed {
		s.closed = true
		close(s.stop)
		s.signal()
	}
	s.mu.Unlock()
	<-s.done
}

func (s *providerEventStream) run() {
	defer close(s.done)
	defer close(s.out)
	for {
		s.mu.Lock()
		if len(s.queue) == 0 {
			closed := s.closed
			s.mu.Unlock()
			if closed {
				return
			}
			<-s.wake
			continue
		}
		event := s.queue[0]
		s.queue = s.queue[1:]
		s.resident -= providerEventSize(event)
		s.mu.Unlock()

		select {
		case s.out <- event:
		case <-s.stop:
			// Closure is explicit shutdown; accepted events that were not
			// handed to the consumer remain bounded in the stopped pipe.
			return
		}
	}
}

func (s *providerEventStream) signal() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

func compatibleProviderDeltas(previous, next provider.ProviderEvent) bool {
	return previous.Coalescible && next.Coalescible && previous.Kind == provider.EventMessageDelta && previous.CoalescingKey == next.CoalescingKey && previous.ThreadID == next.ThreadID && previous.TurnID == next.TurnID && previous.ConversationID == next.ConversationID
}

func providerEventSize(event provider.ProviderEvent) int {
	return len([]byte(event.Text)) + len([]byte(event.Error)) + len([]byte(event.Status)) + 256
}
