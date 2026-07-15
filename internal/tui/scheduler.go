package tui

import (
	"errors"
	"sync"
	"time"

	tea "charm.land/bubbletea/v2"
)

const defaultAnimationInterval = 200 * time.Millisecond

var errInvalidScheduler = errors.New("invalid render scheduler")

// RenderTickMsg is the only message emitted by the root render scheduler.
// Epoch makes ticks that were already queued before Stop or reduced-motion
// changes harmless.
type RenderTickMsg struct {
	Epoch uint64
}

// TickPlan describes one optional Bubble Tea timer command. A nil command
// means an invalidation was coalesced or no visible animation is active.
type TickPlan struct {
	Command tea.Cmd
	Epoch   uint64
}

// RenderScheduler coalesces root invalidations and owns at most one scheduled
// Bubble Tea tick. Bubble Tea owns the timer command; this type owns admission,
// epoch validation, and the visible-work/reduced-motion policy.
type RenderScheduler struct {
	mu              sync.Mutex
	interval        time.Duration
	visibleAnimated int
	reducedMotion   bool
	pending         bool
	scheduled       bool
	epoch           uint64
}

// NewRenderScheduler creates a scheduler with a bounded positive interval.
func NewRenderScheduler(interval time.Duration) (*RenderScheduler, error) {
	if interval <= 0 {
		return nil, errInvalidScheduler
	}
	return &RenderScheduler{interval: interval, epoch: 1}, nil
}

// DefaultRenderScheduler creates the v1 5Hz scheduler interval.
func DefaultRenderScheduler() *RenderScheduler {
	scheduler, _ := NewRenderScheduler(defaultAnimationInterval)
	return scheduler
}

// StartVisibleWork admits animated visible work and returns the one command
// that should be handed to Bubble Tea, if a tick is needed.
func (s *RenderScheduler) StartVisibleWork() TickPlan {
	return s.SetVisibleAnimatedWork(1)
}

// StopVisibleWork prevents future ticks and invalidates already-queued ticks.
func (s *RenderScheduler) StopVisibleWork() {
	_ = s.SetVisibleAnimatedWork(0)
}

// SetVisibleAnimatedWork updates the count of visible animated work and
// returns the one command needed to start a root-owned tick chain.
func (s *RenderScheduler) SetVisibleAnimatedWork(count int) TickPlan {
	if s == nil {
		return TickPlan{}
	}
	if count < 0 {
		count = 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	wasVisible := s.visibleAnimated > 0
	s.visibleAnimated = count
	isVisible := count > 0
	if !isVisible {
		s.pending = false
		s.scheduled = false
		s.epoch++
		return TickPlan{}
	}
	if !wasVisible {
		s.pending = true
	}
	return s.scheduleLocked()
}

// SetReducedMotion changes the run-scoped animation policy. Turning it off
// makes currently visible work eligible for one new tick.
func (s *RenderScheduler) SetReducedMotion(reduced bool) TickPlan {
	if s == nil {
		return TickPlan{}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.reducedMotion == reduced {
		return TickPlan{}
	}
	s.reducedMotion = reduced
	s.epoch++
	s.scheduled = false
	if reduced {
		return TickPlan{}
	}
	s.pending = s.visibleAnimated > 0
	return s.scheduleLocked()
}

// Invalidate requests a redraw. Multiple requests collapse into the existing
// command, while non-animated work waits for the normal Bubble Tea update.
func (s *RenderScheduler) Invalidate() TickPlan {
	if s == nil {
		return TickPlan{}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pending = true
	return s.scheduleLocked()
}

// AcceptTick validates a scheduler tick and returns the next command when
// visible animation remains active. Stale or disabled ticks are discarded.
func (s *RenderScheduler) AcceptTick(message RenderTickMsg) (bool, TickPlan) {
	if s == nil {
		return false, TickPlan{}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.scheduled || message.Epoch != s.epoch {
		return false, TickPlan{}
	}
	s.scheduled = false
	if s.visibleAnimated <= 0 || s.reducedMotion {
		return false, TickPlan{}
	}
	s.pending = true
	return true, s.scheduleLocked()
}

func (s *RenderScheduler) scheduleLocked() TickPlan {
	if s.visibleAnimated <= 0 || s.reducedMotion || !s.pending || s.scheduled {
		return TickPlan{}
	}
	s.pending = false
	s.scheduled = true
	epoch := s.epoch
	return TickPlan{
		Epoch: epoch,
		Command: tea.Tick(s.interval, func(time.Time) tea.Msg {
			return RenderTickMsg{Epoch: epoch}
		}),
	}
}

// VisibleAnimatedWork returns the current visible animated-work count.
func (s *RenderScheduler) VisibleAnimatedWork() int {
	if s == nil {
		return 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.visibleAnimated
}
