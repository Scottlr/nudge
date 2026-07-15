package app

import (
	"context"
	"encoding/hex"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/Scottlr/nudge/internal/domain"
	"github.com/Scottlr/nudge/internal/provider"
)

var (
	ErrRuntimeApprovalUnavailable = errors.New("runtime approval service unavailable")
	ErrRuntimeApprovalPolicy      = errors.New("runtime approval denied by policy")
	ErrRuntimeApprovalDuplicate   = errors.New("runtime approval already pending or resolved")
)

const defaultRuntimeApprovalLimit = 64

// RuntimeApproval is the ephemeral UI projection for one pending provider
// request. Exact command arguments and network targets are never persisted.
type RuntimeApproval struct {
	ID               provider.ProviderRequestID
	ThreadID         domain.ReviewThreadID
	OperationID      domain.OperationID
	TurnID           domain.ProviderTurnID
	ProviderTurnRef  provider.ProviderTurnRef
	CorrelationID    provider.CorrelationID
	Kind             provider.RuntimeApprovalKind
	ExactCommandArgs string
	NetworkTarget    string
	ToolName         string
	RequestedScope   string
	RequestedScopeID provider.RuntimeApprovalScope
	ExpiresAt        time.Time
}

// RuntimeApprovalRecord is the durable whitelist/decision metadata. It has no
// raw command, prompt, URL, or exact path field.
type RuntimeApprovalRecord struct {
	ID               provider.ProviderRequestID
	TurnID           domain.ProviderTurnID
	Kind             provider.RuntimeApprovalKind
	ScopeClass       string
	ExecutableName   string
	ArgumentHash     string
	NetworkHostClass string
	Decision         provider.ApprovalDecision
	RequestedAt      time.Time
	ResolvedAt       time.Time
}

// Validate rejects durable values that could accidentally retain a secret or
// an exact provider scope.
func (r RuntimeApprovalRecord) Validate() error {
	if r.ID == "" || r.ID.Validate() != nil || r.TurnID == "" || r.Kind == "" || strings.TrimSpace(r.ScopeClass) == "" || r.RequestedAt.IsZero() || r.ResolvedAt.IsZero() || r.ResolvedAt.Before(r.RequestedAt) || (r.Decision != provider.ApprovalAllowOnce && r.Decision != provider.ApprovalDeny) {
		return ErrReviewStoreInput
	}
	if r.ArgumentHash != "" {
		decoded, err := hex.DecodeString(r.ArgumentHash)
		if err != nil || len(decoded) != 32 {
			return ErrReviewStoreInput
		}
	}
	if len([]byte(r.ExecutableName)) > 256 || len([]byte(r.ScopeClass)) > 128 || len([]byte(r.NetworkHostClass)) > 128 {
		return ErrReviewStoreInput
	}
	return nil
}

// RuntimeApprovalRecorder is the optional durable metadata sink. Implementors
// must store only RuntimeApprovalRecord, never the ephemeral projection.
type RuntimeApprovalRecorder interface {
	SaveRuntimeApprovalRecord(context.Context, RuntimeApprovalRecord) error
}

// RuntimeApprovalOutcome is the bounded result consumed by a frontend or
// provider-event coordinator.
type RuntimeApprovalOutcome struct {
	Approval     *RuntimeApproval
	Decision     provider.ApprovalDecision
	PolicyError  error
	Disconnected bool
}

type pendingRuntimeApproval struct {
	providerApproval provider.RuntimeApproval
	view             RuntimeApproval
}

// RuntimeApprovalService owns pending runtime approvals for one review
// session. It is single-owner in normal composition; the mutex also prevents
// an expired response racing a disconnect during teardown.
type RuntimeApprovalService struct {
	provider  ReviewProvider
	clock     Clock
	recorder  RuntimeApprovalRecorder
	authorize func(RuntimeApproval) error
	limit     int

	mu      sync.Mutex
	pending map[provider.ProviderRequestID]*pendingRuntimeApproval
}

// RuntimeApprovalServiceConfig composes provider response and optional
// durable metadata behavior.
type RuntimeApprovalServiceConfig struct {
	Provider ReviewProvider
	Clock    Clock
	Recorder RuntimeApprovalRecorder
	Limit    int
	// Authorize is an optional turn-specific containment check. It receives
	// only the ephemeral approval projection and cannot broaden the provider
	// policy or persist sensitive scope text.
	Authorize func(RuntimeApproval) error
}

// NewRuntimeApprovalService constructs a bounded approval coordinator.
func NewRuntimeApprovalService(config RuntimeApprovalServiceConfig) (*RuntimeApprovalService, error) {
	if config.Provider == nil {
		return nil, ErrRuntimeApprovalUnavailable
	}
	if config.Clock == nil {
		config.Clock = SystemClock{}
	}
	if config.Limit <= 0 {
		config.Limit = defaultRuntimeApprovalLimit
	}
	return &RuntimeApprovalService{provider: config.Provider, clock: config.Clock, recorder: config.Recorder, authorize: config.Authorize, limit: config.Limit, pending: make(map[provider.ProviderRequestID]*pendingRuntimeApproval)}, nil
}

// HandleProviderEvent admits one normalized provider approval or recovery
// event. Disconnect clears pending overlays but does not alter thread state.
func (s *RuntimeApprovalService) HandleProviderEvent(event provider.ProviderEvent) (RuntimeApprovalOutcome, error) {
	if s == nil || event.Validate(provider.DefaultValidationLimits()) != nil {
		return RuntimeApprovalOutcome{}, ErrRuntimeApprovalUnavailable
	}
	switch event.Kind {
	case provider.EventRuntimeApprovalRequested:
		if event.Approval == nil {
			return RuntimeApprovalOutcome{}, ErrRuntimeApprovalUnavailable
		}
		s.mu.Lock()
		if len(s.pending) >= s.limit {
			s.mu.Unlock()
			return RuntimeApprovalOutcome{PolicyError: ErrRuntimeApprovalPolicy}, nil
		}
		if _, exists := s.pending[event.RequestID]; exists {
			s.mu.Unlock()
			return RuntimeApprovalOutcome{PolicyError: ErrRuntimeApprovalDuplicate}, nil
		}
		s.mu.Unlock()
		view := runtimeApprovalView(event)
		if s.authorize != nil {
			if err := s.authorize(view); err != nil {
				return RuntimeApprovalOutcome{PolicyError: err}, nil
			}
		}
		copyApproval := *event.Approval
		s.mu.Lock()
		defer s.mu.Unlock()
		if len(s.pending) >= s.limit {
			return RuntimeApprovalOutcome{PolicyError: ErrRuntimeApprovalPolicy}, nil
		}
		if _, exists := s.pending[event.RequestID]; exists {
			return RuntimeApprovalOutcome{PolicyError: ErrRuntimeApprovalDuplicate}, nil
		}
		s.pending[event.RequestID] = &pendingRuntimeApproval{providerApproval: copyApproval, view: view}
		return RuntimeApprovalOutcome{Approval: cloneRuntimeApproval(&view)}, nil
	case provider.EventRuntimeApprovalResolved:
		s.mu.Lock()
		pending := s.pending[event.RequestID]
		delete(s.pending, event.RequestID)
		s.mu.Unlock()
		if pending == nil {
			return RuntimeApprovalOutcome{PolicyError: provider.ErrApprovalStale}, nil
		}
		return RuntimeApprovalOutcome{Approval: cloneRuntimeApproval(&pending.view), Decision: event.Decision}, nil
	case provider.EventDisconnected:
		s.mu.Lock()
		s.pending = make(map[provider.ProviderRequestID]*pendingRuntimeApproval)
		s.mu.Unlock()
		return RuntimeApprovalOutcome{Disconnected: true}, nil
	default:
		return RuntimeApprovalOutcome{}, nil
	}
}

// Current returns the oldest pending approval for deterministic overlay
// selection. The exact display projection remains in memory only.
func (s *RuntimeApprovalService) Current() (*RuntimeApproval, bool) {
	if s == nil {
		return nil, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, pending := range s.pending {
		copyValue := pending.view
		return &copyValue, true
	}
	return nil, false
}

// Respond resolves exactly one pending request. Only command execution with
// no network target is approvable; all file/root, tool, and network requests
// are converted to deny before reaching the provider.
func (s *RuntimeApprovalService) Respond(ctx context.Context, response provider.RuntimeApprovalResponse) error {
	if s == nil || s.provider == nil {
		return ErrRuntimeApprovalUnavailable
	}
	now := s.clock.Now().UTC()
	s.mu.Lock()
	pending := s.pending[response.RequestID]
	s.mu.Unlock()
	if pending == nil {
		return provider.ErrApprovalStale
	}
	decision := response.Decision
	policyErr := error(nil)
	if decision == provider.ApprovalAllowOnce && !runtimeApprovalCanApprove(pending.view) {
		decision = provider.ApprovalDeny
		policyErr = ErrRuntimeApprovalPolicy
	}
	response.Decision = decision
	if err := pending.providerApproval.ResponseValidation(response, now); err != nil {
		if errors.Is(err, provider.ErrApprovalExpired) {
			response.Decision = provider.ApprovalDeny
			_ = s.provider.RespondToRuntimeApproval(ctx, response)
			s.mu.Lock()
			delete(s.pending, response.RequestID)
			s.mu.Unlock()
		}
		return err
	}
	if err := s.provider.RespondToRuntimeApproval(ctx, response); err != nil {
		return err
	}
	if err := pending.providerApproval.Respond(response, now); err != nil {
		return err
	}
	s.mu.Lock()
	delete(s.pending, response.RequestID)
	s.mu.Unlock()
	if s.recorder != nil {
		record := runtimeApprovalRecord(pending.view, decision, now)
		if err := record.Validate(); err != nil {
			return err
		}
		if err := s.recorder.SaveRuntimeApprovalRecord(ctx, record); err != nil {
			return err
		}
	}
	if policyErr != nil {
		return policyErr
	}
	return nil
}

func runtimeApprovalView(event provider.ProviderEvent) RuntimeApproval {
	details := event.Approval.Details
	return RuntimeApproval{ID: event.RequestID, ThreadID: event.ThreadID, OperationID: event.OperationID, TurnID: event.TurnID, ProviderTurnRef: event.TurnRef, CorrelationID: event.CorrelationID, Kind: event.Scope.Kind, ExactCommandArgs: details.ExactCommandArgs, NetworkTarget: details.NetworkTarget, ToolName: details.ToolName, RequestedScope: details.RequestedScope, RequestedScopeID: event.Scope, ExpiresAt: event.ExpiresAt}
}

func runtimeApprovalCanApprove(approval RuntimeApproval) bool {
	return approval.Kind == provider.RuntimeApprovalCommand && approval.NetworkTarget == ""
}

// CanApproveRuntimeApproval reports whether an approval is eligible for the
// one-shot allow action. It never grants permission by itself.
func CanApproveRuntimeApproval(approval RuntimeApproval) bool {
	return runtimeApprovalCanApprove(approval)
}

func runtimeApprovalRecord(approval RuntimeApproval, decision provider.ApprovalDecision, now time.Time) RuntimeApprovalRecord {
	executable := approval.RequestedScopeID.Executable
	if index := strings.LastIndexAny(executable, `/\\`); index >= 0 {
		executable = executable[index+1:]
	}
	requestedAt := approval.ExpiresAt.Add(-2 * time.Minute)
	if requestedAt.IsZero() || requestedAt.After(now) {
		requestedAt = now
	}
	return RuntimeApprovalRecord{ID: approval.ID, TurnID: approval.TurnID, Kind: approval.Kind, ScopeClass: approval.RequestedScope, ExecutableName: executable, ArgumentHash: approval.RequestedScopeID.ArgumentsDigest, NetworkHostClass: networkHostClass(approval.NetworkTarget), Decision: decision, RequestedAt: requestedAt, ResolvedAt: now}
}

func networkHostClass(target string) string {
	if target == "" {
		return ""
	}
	return "requested_host"
}

func cloneRuntimeApproval(value *RuntimeApproval) *RuntimeApproval {
	if value == nil {
		return nil
	}
	copyValue := *value
	return &copyValue
}
