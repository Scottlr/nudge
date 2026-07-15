package app

import (
	"context"
	"errors"
	"sync"

	"github.com/Scottlr/nudge/internal/domain"
)

var (
	ErrTurnNotActive     = errors.New("provider turn is not active")
	ErrTurnAlreadyActive = errors.New("provider turn is already active")
)

// TurnAction distinguishes an ordinary queued reply from an explicit steer
// and an interrupt. The UI must choose the action; text alone never steers.
type TurnAction string

const (
	TurnActionReply  TurnAction = "reply"
	TurnActionSteer  TurnAction = "steer"
	TurnActionCancel TurnAction = "cancel"
)

// TurnRoute is the deterministic local result before a provider side effect.
type TurnRoute string

const (
	TurnRouteStarted    TurnRoute = "started"
	TurnRouteQueued     TurnRoute = "queued"
	TurnRouteSteered    TurnRoute = "steered"
	TurnRouteCancelling TurnRoute = "cancelling"
)

// TurnIntent is the bounded user text retained while a reply waits for the
// active turn to finish. It contains no provider protocol values.
type TurnIntent struct {
	ThreadID domain.ReviewThreadID
	Text     string
}

type turnRouterState struct {
	active     bool
	cancelling bool
	queued     []TurnIntent
}

// TurnActionRouter owns reply/steer/cancel distinction independently of the
// provider transport. It is single-writer state and has a bounded reply queue.
type TurnActionRouter struct {
	mu       sync.Mutex
	maxQueue int
	threads  map[domain.ReviewThreadID]*turnRouterState
}

// NewTurnActionRouter constructs the local turn-action gate.
func NewTurnActionRouter(maxQueue int) *TurnActionRouter {
	if maxQueue <= 0 {
		maxQueue = 16
	}
	return &TurnActionRouter{maxQueue: maxQueue, threads: make(map[domain.ReviewThreadID]*turnRouterState)}
}

// Begin marks a provider turn active. Duplicate active turns fail closed.
func (r *TurnActionRouter) Begin(threadID domain.ReviewThreadID) error {
	if r == nil || threadID == "" {
		return ErrTurnNotActive
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	state := r.state(threadID)
	if state.active {
		return ErrTurnAlreadyActive
	}
	state.active = true
	state.cancelling = false
	return nil
}

// Reply queues ordinary text while a turn is active. It never steers the
// active provider turn implicitly.
func (r *TurnActionRouter) Reply(intent TurnIntent) (TurnRoute, error) {
	if r == nil || intent.ThreadID == "" || intent.Text == "" {
		return "", ErrTurnNotActive
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	state := r.state(intent.ThreadID)
	if !state.active {
		state.active = true
		return TurnRouteStarted, nil
	}
	if len(state.queued) >= r.maxQueue {
		return "", ErrConsumerTooSlow
	}
	state.queued = append(state.queued, intent)
	return TurnRouteQueued, nil
}

// Steer requires an active turn and reports a distinct route for the
// transport's explicit turn/steer call.
func (r *TurnActionRouter) Steer(threadID domain.ReviewThreadID) (TurnRoute, error) {
	if r == nil || threadID == "" {
		return "", ErrTurnNotActive
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	state := r.state(threadID)
	if !state.active || state.cancelling {
		return "", ErrTurnNotActive
	}
	return TurnRouteSteered, nil
}

// Cancel marks cancellation visible before the provider interrupt is sent.
// A terminal completion after this call is interpreted by Complete as the
// cancellation race outcome, not as a new successful turn.
func (r *TurnActionRouter) Cancel(threadID domain.ReviewThreadID) (TurnRoute, error) {
	if r == nil || threadID == "" {
		return "", ErrTurnNotActive
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	state := r.state(threadID)
	if !state.active {
		return "", ErrTurnNotActive
	}
	state.cancelling = true
	return TurnRouteCancelling, nil
}

// Complete closes the active turn and returns the next ordinary reply, if
// one was queued. The caller starts that reply only after terminal provider
// truth has been processed.
func (r *TurnActionRouter) Complete(threadID domain.ReviewThreadID) (TurnIntent, bool, bool, error) {
	if r == nil || threadID == "" {
		return TurnIntent{}, false, false, ErrTurnNotActive
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	state := r.state(threadID)
	if !state.active {
		return TurnIntent{}, false, false, ErrTurnNotActive
	}
	cancelled := state.cancelling
	state.active = false
	state.cancelling = false
	if len(state.queued) == 0 {
		return TurnIntent{}, false, cancelled, nil
	}
	next := state.queued[0]
	state.queued = state.queued[1:]
	state.active = true
	return next, true, cancelled, nil
}

// Queued returns a detached count for UI status without exposing mutable
// provider actions.
func (r *TurnActionRouter) Queued(threadID domain.ReviewThreadID) int {
	if r == nil {
		return 0
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.state(threadID).queued)
}

func (r *TurnActionRouter) state(threadID domain.ReviewThreadID) *turnRouterState {
	state := r.threads[threadID]
	if state == nil {
		state = &turnRouterState{}
		r.threads[threadID] = state
	}
	return state
}

// ResumeDiscussion is the explicit reconnect seam. It preserves the local
// thread and asks the existing lifecycle service to resume the opaque remote
// conversation when the provider advertised that capability.
func ResumeDiscussion(ctx context.Context, service *ProviderConversationService, command ResumeProviderConversation) (ProviderConversationCommit, error) {
	if service == nil {
		return ProviderConversationCommit{}, ErrProviderConversationUnavailable
	}
	return service.ResumeConversation(ctx, command)
}
