package app

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/Scottlr/nudge/internal/domain"
	"github.com/Scottlr/nudge/internal/domain/review"
	"github.com/Scottlr/nudge/internal/provider"
)

var (
	ErrProviderConversationInProgress  = errors.New("provider conversation creation is already in progress")
	ErrProviderConversationAttached    = errors.New("provider conversation is already attached")
	ErrProviderConversationOrphan      = errors.New("provider conversation may have a remote orphan")
	ErrProviderTurnActive              = errors.New("provider turn is already active")
	ErrProviderTurnOrphan              = errors.New("provider turn may have a remote orphan")
	ErrProviderConversationUnavailable = errors.New("provider conversation persistence is unavailable")
)

const (
	providerNameCodex          = "codex"
	providerExpressionVersion  = "provider-expression-v1"
	maxProviderNameBytes       = 64
	maxProviderVersionBytes    = 256
	maxProviderSessionRefBytes = 4 << 10
)

// ProviderConversationState records the two-phase local/provider lifecycle.
type ProviderConversationState string

const (
	ProviderConversationCreating       ProviderConversationState = "creating"
	ProviderConversationAttachedState  ProviderConversationState = "attached"
	ProviderConversationResuming       ProviderConversationState = "resuming"
	ProviderConversationInterrupted    ProviderConversationState = "interrupted"
	ProviderConversationFailed         ProviderConversationState = "failed"
	ProviderConversationPossibleOrphan ProviderConversationState = "possible_orphan"
)

// ProviderTurnState records one local turn journal independently of provider
// transcript state.
type ProviderTurnState string

const (
	ProviderTurnPrepared       ProviderTurnState = "prepared"
	ProviderTurnStarted        ProviderTurnState = "started"
	ProviderTurnSteering       ProviderTurnState = "steering"
	ProviderTurnInterrupted    ProviderTurnState = "interrupted"
	ProviderTurnUnknown        ProviderTurnState = "unknown"
	ProviderTurnCompleted      ProviderTurnState = "completed"
	ProviderTurnFailed         ProviderTurnState = "failed"
	ProviderTurnPossibleOrphan ProviderTurnState = "possible_orphan"
)

// ProviderConversationRecord is the durable local record. Provider refs are
// opaque strings and are never substituted for the local strong identity.
type ProviderConversationRecord struct {
	ID                      domain.ProviderConversationID
	ThreadID                domain.ReviewThreadID
	ProviderName            string
	ProviderConversationRef provider.ProviderConversationRef
	ProviderSessionRef      string
	ProviderVersion         string
	OperationID             domain.OperationID
	CorrelationID           CorrelationID
	State                   ProviderConversationState
	ErrorCode               review.ErrorCode
	CreatedAt               time.Time
	UpdatedAt               time.Time
}

// Validate checks bounded metadata, local ownership, and state/ref relations.
func (r ProviderConversationRecord) Validate() error {
	if r.ID == "" || r.ThreadID == "" || r.OperationID == "" || !validProviderMetadata(r.ProviderName, maxProviderNameBytes) || !validProviderMetadata(r.ProviderVersion, maxProviderVersionBytes) || !validProviderMetadata(string(r.CorrelationID), maxProviderVersionBytes) || r.CreatedAt.IsZero() || r.UpdatedAt.IsZero() || r.UpdatedAt.Before(r.CreatedAt) {
		return ErrReviewStoreInput
	}
	if r.ProviderConversationRef != "" && r.ProviderConversationRef.Validate() != nil {
		return ErrReviewStoreInput
	}
	if r.ProviderSessionRef != "" && !validProviderMetadata(r.ProviderSessionRef, maxProviderSessionRefBytes) {
		return ErrReviewStoreInput
	}
	switch r.State {
	case ProviderConversationCreating, ProviderConversationResuming, ProviderConversationInterrupted, ProviderConversationFailed, ProviderConversationPossibleOrphan:
		if r.State == ProviderConversationCreating && r.ProviderConversationRef != "" {
			return ErrReviewStoreInput
		}
	case ProviderConversationAttachedState:
		if r.ProviderConversationRef == "" || r.ProviderConversationRef.Validate() != nil {
			return ErrReviewStoreInput
		}
	default:
		return ErrReviewStoreInput
	}
	if !validProviderMetadataOptional(string(r.ErrorCode), maxProviderNameBytes) {
		return ErrReviewStoreInput
	}
	return nil
}

// ProviderTurnRecord is the durable local turn journal.
type ProviderTurnRecord struct {
	ID                       domain.ProviderTurnID
	ThreadID                 domain.ReviewThreadID
	ConversationID           domain.ProviderConversationID
	ProviderTurnRef          provider.ProviderTurnRef
	OperationID              domain.OperationID
	CorrelationID            CorrelationID
	Mode                     provider.TurnMode
	State                    ProviderTurnState
	ProviderVersion          string
	RequestExpressionVersion string
	Provenance               DiscussionTurnProvenance
	StartedAt                time.Time
	CompletedAt              *time.Time
	ErrorCode                review.ErrorCode
}

// Validate checks the turn's local ownership, opaque-ref bounds, and terminal
// timestamp relation.
func (r ProviderTurnRecord) Validate() error {
	if r.ID == "" || r.ThreadID == "" || r.ConversationID == "" || r.OperationID == "" || !validProviderMetadata(string(r.CorrelationID), maxProviderVersionBytes) || !validProviderMetadata(r.ProviderVersion, maxProviderVersionBytes) || !validProviderMetadata(r.RequestExpressionVersion, maxProviderVersionBytes) || r.StartedAt.IsZero() {
		return ErrReviewStoreInput
	}
	if r.ProviderTurnRef != "" && r.ProviderTurnRef.Validate() != nil {
		return ErrReviewStoreInput
	}
	if r.Mode != provider.TurnDiscuss && r.Mode != provider.TurnPropose {
		return ErrReviewStoreInput
	}
	switch r.State {
	case ProviderTurnPrepared:
		if r.ProviderTurnRef != "" {
			return ErrReviewStoreInput
		}
	case ProviderTurnStarted, ProviderTurnSteering, ProviderTurnCompleted:
		if r.ProviderTurnRef == "" || r.ProviderTurnRef.Validate() != nil {
			return ErrReviewStoreInput
		}
	case ProviderTurnInterrupted:
	case ProviderTurnUnknown, ProviderTurnFailed, ProviderTurnPossibleOrphan:
	default:
		return ErrReviewStoreInput
	}
	if r.CompletedAt != nil && r.CompletedAt.Before(r.StartedAt) {
		return ErrReviewStoreInput
	}
	if !validProviderMetadataOptional(string(r.ErrorCode), maxProviderNameBytes) {
		return ErrReviewStoreInput
	}
	if !r.Provenance.IsZero() && r.Provenance.Validate() != nil {
		return ErrReviewStoreInput
	}
	return nil
}

func (r ProviderTurnRecord) active() bool {
	return r.State == ProviderTurnPrepared || r.State == ProviderTurnStarted || r.State == ProviderTurnSteering
}

func validProviderMetadata(value string, maximum int) bool {
	return value != "" && utf8.ValidString(value) && len([]byte(value)) <= maximum && strings.IndexByte(value, 0) < 0 && !strings.ContainsAny(value, "\r\n")
}

func validProviderMetadataOptional(value string, maximum int) bool {
	return value == "" || validProviderMetadata(value, maximum)
}

// ProviderConversationPort is the application-owned narrow port needed for
// conversation/turn journaling. It intentionally excludes event streams and
// runtime approvals owned by later lifecycle tasks.
type ProviderConversationPort interface {
	Probe(context.Context) (ProviderStatus, error)
	StartConversation(context.Context, provider.StartConversationRequest) (provider.ProviderConversationRef, error)
	ResumeConversation(context.Context, provider.ProviderConversationRef) error
	StartTurn(context.Context, provider.ProviderConversationRef, provider.TurnRequest) (provider.ProviderTurnRef, error)
	SteerTurn(context.Context, provider.ProviderTurnRef, string) error
	CancelTurn(context.Context, provider.ProviderTurnRef) error
}

// EnsureProviderConversation is the local intent to create or reuse one
// conversation for an existing review thread.
type EnsureProviderConversation struct {
	Guard           SessionWriteGuard
	ThreadID        domain.ReviewThreadID
	Thread          review.ReviewThread
	ProviderName    string
	ProviderVersion string
	Mode            provider.TurnMode
	WorkingDir      string
	Permissions     provider.TurnPermissionPolicy
	OperationID     domain.OperationID
	CorrelationID   CorrelationID
}

// StartProviderTurn is the local intent to journal and start one turn.
type StartProviderTurn struct {
	Guard          SessionWriteGuard
	ThreadID       domain.ReviewThreadID
	ConversationID domain.ProviderConversationID
	Mode           provider.TurnMode
	Prompt         string
	WorkingDir     string
	Permissions    provider.TurnPermissionPolicy
	Provenance     DiscussionTurnProvenance
	OperationID    domain.OperationID
	CorrelationID  CorrelationID
}

// SteerProviderTurn adds intentional guidance to an active provider turn.
type SteerProviderTurn struct {
	Guard         SessionWriteGuard
	ThreadID      domain.ReviewThreadID
	TurnID        domain.ProviderTurnID
	Input         string
	OperationID   domain.OperationID
	CorrelationID CorrelationID
}

// InterruptProviderTurn requests cancellation of one active provider turn.
type InterruptProviderTurn struct {
	Guard         SessionWriteGuard
	ThreadID      domain.ReviewThreadID
	TurnID        domain.ProviderTurnID
	OperationID   domain.OperationID
	CorrelationID CorrelationID
}

// ResumeProviderConversation reconnects an existing durable local mapping to
// the same opaque provider conversation after a process restart.
type ResumeProviderConversation struct {
	Guard          SessionWriteGuard
	ThreadID       domain.ReviewThreadID
	ConversationID domain.ProviderConversationID
	OperationID    domain.OperationID
	CorrelationID  CorrelationID
}

// ProviderConversationAttached reports the second-phase opaque-ref attach.
type ProviderConversationAttached struct {
	Revision       uint64
	ConversationID domain.ProviderConversationID
	ThreadID       domain.ReviewThreadID
	ProviderRef    provider.ProviderConversationRef
	OperationID    domain.OperationID
	CorrelationID  CorrelationID
}

// ProviderTurnStateChanged reports a local turn journal transition.
type ProviderTurnStateChanged struct {
	Revision       uint64
	TurnID         domain.ProviderTurnID
	ConversationID domain.ProviderConversationID
	ThreadID       domain.ReviewThreadID
	State          ProviderTurnState
	ErrorCode      review.ErrorCode
	OperationID    domain.OperationID
	CorrelationID  CorrelationID
}

// ProviderConversationCommit contains the fenced state and normalized events
// produced after a lifecycle operation.
type ProviderConversationCommit struct {
	Guard        SessionWriteGuard
	Conversation ProviderConversationRecord
	Turn         *ProviderTurnRecord
	Events       []Event
}

// ProviderConversationService owns the two-phase local/provider lifecycle.
type ProviderConversationService struct {
	store           ReviewStore
	provider        ProviderConversationPort
	clock           Clock
	ids             IDSource
	persistence     PersistenceMode
	providerName    string
	providerVersion string

	mu            sync.Mutex
	startMu       sync.Mutex
	conversations map[domain.ReviewThreadID]ProviderConversationRecord
	turns         map[domain.ProviderTurnID]ProviderTurnRecord
}

// ProviderConversationServiceConfig composes durable or no-persist lifecycle
// behavior. Durable mode requires the fenced ReviewStore and no-persist mode
// deliberately keeps all provider records process-local.
type ProviderConversationServiceConfig struct {
	Store           ReviewStore
	Provider        ProviderConversationPort
	Clock           Clock
	IDs             IDSource
	Persistence     PersistenceMode
	ProviderName    string
	ProviderVersion string
}

// NewProviderConversationService validates lifecycle dependencies.
func NewProviderConversationService(config ProviderConversationServiceConfig) (*ProviderConversationService, error) {
	if config.Provider == nil || (config.Persistence != PersistenceDurable && config.Persistence != PersistenceNoPersist) {
		return nil, ErrProviderConversationUnavailable
	}
	if config.Persistence == PersistenceDurable && config.Store == nil {
		return nil, ErrProviderConversationUnavailable
	}
	if config.Clock == nil {
		config.Clock = SystemClock{}
	}
	if config.IDs == nil {
		config.IDs = RandomIDSource{}
	}
	if config.ProviderName == "" {
		config.ProviderName = providerNameCodex
	}
	if config.ProviderVersion == "" {
		config.ProviderVersion = "unknown"
	}
	if !validProviderMetadata(config.ProviderName, maxProviderNameBytes) || !validProviderMetadata(config.ProviderVersion, maxProviderVersionBytes) {
		return nil, ErrProviderConversationUnavailable
	}
	return &ProviderConversationService{
		store: config.Store, provider: config.Provider, clock: config.Clock, ids: config.IDs,
		persistence: config.Persistence, providerName: config.ProviderName, providerVersion: config.ProviderVersion,
		conversations: make(map[domain.ReviewThreadID]ProviderConversationRecord),
		turns:         make(map[domain.ProviderTurnID]ProviderTurnRecord),
	}, nil
}

// EnsureConversation journals creation before calling the provider and
// attaches the returned opaque ref only after a second fenced transaction.
func (s *ProviderConversationService) EnsureConversation(ctx context.Context, command EnsureProviderConversation) (ProviderConversationCommit, error) {
	if err := s.validateCommand(ctx, command.Guard, command.ThreadID, command.OperationID, command.CorrelationID); err != nil {
		return ProviderConversationCommit{}, err
	}
	if err := s.providerReady(ctx); err != nil {
		return ProviderConversationCommit{}, err
	}
	startRequest := provider.StartConversationRequest{ThreadID: command.ThreadID, OperationID: command.OperationID, CorrelationID: provider.CorrelationID(command.CorrelationID), Mode: command.Mode, WorkingDir: command.WorkingDir, Permissions: command.Permissions}
	if err := startRequest.Validate(); err != nil {
		return ProviderConversationCommit{}, err
	}
	thread, err := s.loadThread(ctx, command.Guard, command.ThreadID, command.Thread)
	if err != nil {
		return ProviderConversationCommit{}, err
	}
	if thread.ProviderConversationID != nil {
		return s.existingConversation(ctx, command, *thread.ProviderConversationID)
	}
	if s.persistence == PersistenceDurable {
		if existing, loadErr := s.store.LoadProviderConversationForThread(ctx, command.ThreadID); loadErr == nil {
			return s.existingConversationRecord(command, *existing)
		} else if !errors.Is(loadErr, ErrReviewStoreNotFound) {
			return ProviderConversationCommit{}, loadErr
		}
	}
	s.mu.Lock()
	if existing, ok := s.conversations[command.ThreadID]; ok {
		s.mu.Unlock()
		return s.existingConversationRecord(command, existing)
	}
	localID, idErr := domain.NewProviderConversationID(s.ids.NewID())
	if idErr != nil {
		s.mu.Unlock()
		return ProviderConversationCommit{}, ErrReviewStoreInput
	}
	now := s.clock.Now().UTC()
	record := ProviderConversationRecord{ID: localID, ThreadID: command.ThreadID, ProviderName: command.ProviderName, ProviderVersion: command.ProviderVersion, OperationID: command.OperationID, CorrelationID: command.CorrelationID, State: ProviderConversationCreating, CreatedAt: now, UpdatedAt: now}
	if record.ProviderName == "" {
		record.ProviderName = s.providerName
	}
	if record.ProviderVersion == "" {
		record.ProviderVersion = s.providerVersion
	}
	if err := record.Validate(); err != nil {
		s.mu.Unlock()
		return ProviderConversationCommit{}, err
	}
	s.conversations[command.ThreadID] = record
	s.mu.Unlock()
	guard := command.Guard
	if s.persistence == PersistenceDurable {
		guard, err = s.store.WithSessionTx(ctx, guard, func(tx ReviewStoreTx) error { return tx.SaveProviderConversation(ctx, record) })
		if err != nil {
			s.removeConversation(command.ThreadID, record.ID)
			return ProviderConversationCommit{}, err
		}
	}
	ref, providerErr := s.provider.StartConversation(ctx, startRequest)
	if providerErr != nil || ref.Validate() != nil {
		record.State = ProviderConversationPossibleOrphan
		record.ErrorCode = review.ErrorCode("provider_conversation_uncertain")
		record.UpdatedAt = s.clock.Now().UTC()
		s.updateConversation(record)
		if s.persistence == PersistenceDurable {
			_, _ = s.store.WithSessionTx(ctx, guard, func(tx ReviewStoreTx) error { return tx.SaveProviderConversation(ctx, record) })
		}
		if providerErr != nil {
			return ProviderConversationCommit{Guard: guard, Conversation: record}, fmt.Errorf("%w: %v", ErrProviderConversationOrphan, providerErr)
		}
		return ProviderConversationCommit{Guard: guard, Conversation: record}, ErrProviderConversationOrphan
	}
	record.ProviderConversationRef = ref
	record.State = ProviderConversationAttachedState
	record.UpdatedAt = s.clock.Now().UTC()
	if err := record.Validate(); err != nil {
		return ProviderConversationCommit{}, err
	}
	threadNow := record.UpdatedAt
	if err := thread.AttachProviderConversation(record.ID, threadNow); err != nil {
		return ProviderConversationCommit{}, err
	}
	if s.persistence == PersistenceDurable {
		guard, err = s.store.WithSessionTx(ctx, guard, func(tx ReviewStoreTx) error {
			if err := tx.SaveProviderConversation(ctx, record); err != nil {
				return err
			}
			return tx.SaveThread(ctx, thread)
		})
		if err != nil {
			record.State = ProviderConversationPossibleOrphan
			record.ErrorCode = review.ErrorCode("provider_conversation_uncertain")
			record.UpdatedAt = s.clock.Now().UTC()
			s.updateConversation(record)
			_, _ = s.store.WithSessionTx(ctx, guard, func(tx ReviewStoreTx) error { return tx.SaveProviderConversation(ctx, record) })
			return ProviderConversationCommit{Guard: guard, Conversation: record}, fmt.Errorf("%w: %v", ErrProviderConversationOrphan, err)
		}
	}
	s.updateConversation(record)
	return ProviderConversationCommit{Guard: guard, Conversation: record, Events: []Event{ProviderConversationAttached{ConversationID: record.ID, ThreadID: record.ThreadID, ProviderRef: record.ProviderConversationRef, OperationID: command.OperationID, CorrelationID: command.CorrelationID}}}, nil
}

// ResumeConversation journals the reconnect transition before invoking the
// provider. A failed or fenced transition remains visibly non-ready and is
// never retried implicitly.
func (s *ProviderConversationService) ResumeConversation(ctx context.Context, command ResumeProviderConversation) (ProviderConversationCommit, error) {
	if err := s.validateCommand(ctx, command.Guard, command.ThreadID, command.OperationID, command.CorrelationID); err != nil {
		return ProviderConversationCommit{}, err
	}
	status, err := s.providerStatus(ctx)
	if err != nil {
		return ProviderConversationCommit{}, err
	}
	if !status.Capabilities.ResumeConversation {
		return ProviderConversationCommit{}, ErrProviderUnavailable
	}
	record, err := s.loadConversation(ctx, command.Guard, command.ThreadID, command.ConversationID)
	if err != nil {
		return ProviderConversationCommit{}, err
	}
	if record.State != ProviderConversationAttachedState || record.ProviderConversationRef == "" {
		return ProviderConversationCommit{}, ErrProviderConversationAttached
	}
	record.State = ProviderConversationResuming
	record.OperationID = command.OperationID
	record.CorrelationID = command.CorrelationID
	record.ErrorCode = ""
	record.UpdatedAt = s.clock.Now().UTC()
	if err := record.Validate(); err != nil {
		return ProviderConversationCommit{}, err
	}
	guard := command.Guard
	if s.persistence == PersistenceDurable {
		guard, err = s.store.WithSessionTx(ctx, guard, func(tx ReviewStoreTx) error { return tx.SaveProviderConversation(ctx, record) })
		if err != nil {
			return ProviderConversationCommit{}, err
		}
	}
	s.updateConversation(record)
	if err := s.provider.ResumeConversation(ctx, record.ProviderConversationRef); err != nil {
		record.State = ProviderConversationPossibleOrphan
		record.ErrorCode = review.ErrorCode("provider_resume_uncertain")
		record.UpdatedAt = s.clock.Now().UTC()
		s.updateConversation(record)
		if s.persistence == PersistenceDurable {
			_, _ = s.store.WithSessionTx(ctx, guard, func(tx ReviewStoreTx) error { return tx.SaveProviderConversation(ctx, record) })
		}
		return ProviderConversationCommit{Guard: guard, Conversation: record}, fmt.Errorf("%w: %v", ErrProviderConversationOrphan, err)
	}
	record.State = ProviderConversationAttachedState
	record.ErrorCode = ""
	record.UpdatedAt = s.clock.Now().UTC()
	if err := record.Validate(); err != nil {
		return ProviderConversationCommit{}, err
	}
	if s.persistence == PersistenceDurable {
		guard, err = s.store.WithSessionTx(ctx, guard, func(tx ReviewStoreTx) error { return tx.SaveProviderConversation(ctx, record) })
		if err != nil {
			record.State = ProviderConversationPossibleOrphan
			record.ErrorCode = review.ErrorCode("provider_resume_uncertain")
			record.UpdatedAt = s.clock.Now().UTC()
			s.updateConversation(record)
			return ProviderConversationCommit{Guard: guard, Conversation: record}, fmt.Errorf("%w: %v", ErrProviderConversationOrphan, err)
		}
	}
	s.updateConversation(record)
	return ProviderConversationCommit{Guard: guard, Conversation: record}, nil
}

// StartTurn journals prepared state before the remote side effect and attaches
// the returned opaque turn ref in a second fenced transaction.
func (s *ProviderConversationService) StartTurn(ctx context.Context, command StartProviderTurn) (ProviderConversationCommit, error) {
	if err := s.validateCommand(ctx, command.Guard, command.ThreadID, command.OperationID, command.CorrelationID); err != nil {
		return ProviderConversationCommit{}, err
	}
	if err := s.providerReady(ctx); err != nil {
		return ProviderConversationCommit{}, err
	}
	conversation, err := s.loadConversation(ctx, command.Guard, command.ThreadID, command.ConversationID)
	if err != nil {
		return ProviderConversationCommit{}, err
	}
	if conversation.State != ProviderConversationAttachedState || conversation.ProviderConversationRef == "" {
		if conversation.State == ProviderConversationPossibleOrphan {
			return ProviderConversationCommit{}, ErrProviderConversationOrphan
		}
		return ProviderConversationCommit{}, ErrProviderConversationAttached
	}
	request := provider.TurnRequest{ThreadID: command.ThreadID, OperationID: command.OperationID, Mode: command.Mode, Prompt: command.Prompt, WorkingDir: command.WorkingDir, Permissions: command.Permissions, CorrelationID: provider.CorrelationID(command.CorrelationID)}
	if err := request.Validate(); err != nil {
		return ProviderConversationCommit{}, err
	}
	s.startMu.Lock()
	defer s.startMu.Unlock()
	active, err := s.hasActiveTurn(ctx, command.ThreadID)
	if err != nil {
		return ProviderConversationCommit{}, err
	}
	if active {
		return ProviderConversationCommit{}, ErrProviderTurnActive
	}
	turnID, idErr := domain.NewProviderTurnID(s.ids.NewID())
	if idErr != nil {
		return ProviderConversationCommit{}, ErrReviewStoreInput
	}
	now := s.clock.Now().UTC()
	provenance := command.Provenance
	if command.Mode == provider.TurnDiscuss && provenance.IsZero() && command.Permissions.Filesystem == provider.FilesystemPromptOnly {
		provenance = DiscussionTurnProvenance{
			Mode:                    DiscussionModePromptOnly,
			ContextHash:             DiscussionPromptHash(command.Prompt),
			CapabilityPolicyVersion: CurrentCapabilityPolicyVersion,
			ResourcePolicyVersion:   CurrentResourcePolicyVersion,
			EvidenceVersion:         CurrentCapabilityEvidenceVersion,
			PermissionVersion:       "provider-permissions-v1",
		}
	}
	turn := ProviderTurnRecord{ID: turnID, ThreadID: command.ThreadID, ConversationID: conversation.ID, OperationID: command.OperationID, CorrelationID: command.CorrelationID, Mode: command.Mode, State: ProviderTurnPrepared, ProviderVersion: conversation.ProviderVersion, RequestExpressionVersion: providerExpressionVersion, Provenance: provenance, StartedAt: now}
	if err := turn.Validate(); err != nil {
		return ProviderConversationCommit{}, err
	}
	s.mu.Lock()
	s.turns[turn.ID] = turn
	s.mu.Unlock()
	guard := command.Guard
	if s.persistence == PersistenceDurable {
		guard, err = s.store.WithSessionTx(ctx, guard, func(tx ReviewStoreTx) error { return tx.SaveProviderTurn(ctx, turn) })
		if err != nil {
			s.removeTurn(turn.ID)
			return ProviderConversationCommit{}, err
		}
	}
	s.startMu.Unlock()
	ref, providerErr := s.provider.StartTurn(ctx, conversation.ProviderConversationRef, request)
	s.startMu.Lock()
	if providerErr != nil || ref.Validate() != nil {
		turn.State = ProviderTurnPossibleOrphan
		turn.ErrorCode = review.ErrorCode("provider_turn_uncertain")
		s.updateTurn(turn)
		if s.persistence == PersistenceDurable {
			_, _ = s.store.WithSessionTx(ctx, guard, func(tx ReviewStoreTx) error { return tx.SaveProviderTurn(ctx, turn) })
		}
		if providerErr != nil {
			return ProviderConversationCommit{Guard: guard, Conversation: conversation, Turn: &turn}, fmt.Errorf("%w: %v", ErrProviderTurnOrphan, providerErr)
		}
		return ProviderConversationCommit{Guard: guard, Conversation: conversation, Turn: &turn}, ErrProviderTurnOrphan
	}
	turn.ProviderTurnRef = ref
	turn.State = ProviderTurnStarted
	if err := turn.Validate(); err != nil {
		return ProviderConversationCommit{}, err
	}
	if s.persistence == PersistenceDurable {
		guard, err = s.store.WithSessionTx(ctx, guard, func(tx ReviewStoreTx) error { return tx.SaveProviderTurn(ctx, turn) })
		if err != nil {
			turn.State = ProviderTurnPossibleOrphan
			turn.ErrorCode = review.ErrorCode("provider_turn_uncertain")
			s.updateTurn(turn)
			_, _ = s.store.WithSessionTx(ctx, guard, func(tx ReviewStoreTx) error { return tx.SaveProviderTurn(ctx, turn) })
			return ProviderConversationCommit{Guard: guard, Conversation: conversation, Turn: &turn}, fmt.Errorf("%w: %v", ErrProviderTurnOrphan, err)
		}
	}
	s.updateTurn(turn)
	return ProviderConversationCommit{Guard: guard, Conversation: conversation, Turn: &turn, Events: []Event{ProviderTurnStateChanged{TurnID: turn.ID, ConversationID: turn.ConversationID, ThreadID: turn.ThreadID, State: turn.State, OperationID: command.OperationID, CorrelationID: command.CorrelationID}}}, nil
}

// SteerTurn persists the active-turn transition around the remote side effect.
func (s *ProviderConversationService) SteerTurn(ctx context.Context, command SteerProviderTurn) (ProviderConversationCommit, error) {
	if err := s.validateCommand(ctx, command.Guard, command.ThreadID, command.OperationID, command.CorrelationID); err != nil {
		return ProviderConversationCommit{}, err
	}
	if err := provider.ValidateSteeringInput(command.Input); err != nil {
		return ProviderConversationCommit{}, err
	}
	turn, err := s.loadTurn(ctx, command.Guard, command.ThreadID, command.TurnID)
	if err != nil || !turn.active() || turn.ProviderTurnRef == "" {
		if err != nil {
			return ProviderConversationCommit{}, err
		}
		return ProviderConversationCommit{}, ErrProviderTurnActive
	}
	turn.State = ProviderTurnSteering
	guard, err := s.saveTurn(ctx, command.Guard, turn)
	if err != nil {
		return ProviderConversationCommit{}, err
	}
	if err := s.provider.SteerTurn(ctx, turn.ProviderTurnRef, command.Input); err != nil {
		turn.State = ProviderTurnUnknown
		turn.ErrorCode = review.ErrorCode("provider_steer_uncertain")
		_, _ = s.saveTurn(ctx, guard, turn)
		return ProviderConversationCommit{}, err
	}
	turn.State = ProviderTurnStarted
	turn.ErrorCode = ""
	guard, err = s.saveTurn(ctx, guard, turn)
	if err != nil {
		return ProviderConversationCommit{}, err
	}
	return ProviderConversationCommit{Guard: guard, Turn: &turn, Events: []Event{ProviderTurnStateChanged{TurnID: turn.ID, ConversationID: turn.ConversationID, ThreadID: turn.ThreadID, State: turn.State, OperationID: command.OperationID, CorrelationID: command.CorrelationID}}}, nil
}

// InterruptTurn records an explicit local interruption after the provider
// cancellation request succeeds. An uncertain cancellation is unknown.
func (s *ProviderConversationService) InterruptTurn(ctx context.Context, command InterruptProviderTurn) (ProviderConversationCommit, error) {
	if err := s.validateCommand(ctx, command.Guard, command.ThreadID, command.OperationID, command.CorrelationID); err != nil {
		return ProviderConversationCommit{}, err
	}
	turn, err := s.loadTurn(ctx, command.Guard, command.ThreadID, command.TurnID)
	if err != nil {
		return ProviderConversationCommit{}, err
	}
	if !turn.active() || turn.ProviderTurnRef == "" {
		return ProviderConversationCommit{}, ErrProviderTurnActive
	}
	if err := s.provider.CancelTurn(ctx, turn.ProviderTurnRef); err != nil {
		turn.State = ProviderTurnUnknown
		turn.ErrorCode = review.ErrorCode("provider_interrupt_uncertain")
		_, _ = s.saveTurn(ctx, command.Guard, turn)
		return ProviderConversationCommit{}, err
	}
	now := s.clock.Now().UTC()
	turn.State = ProviderTurnInterrupted
	turn.CompletedAt = &now
	guard, err := s.saveTurn(ctx, command.Guard, turn)
	if err != nil {
		return ProviderConversationCommit{}, err
	}
	return ProviderConversationCommit{Guard: guard, Turn: &turn, Events: []Event{ProviderTurnStateChanged{TurnID: turn.ID, ConversationID: turn.ConversationID, ThreadID: turn.ThreadID, State: turn.State, OperationID: command.OperationID, CorrelationID: command.CorrelationID}}}, nil
}

// Restore marks nonterminal durable turns interrupted after process restart.
// It never claims provider completion and does not retry uncertain effects.
func (s *ProviderConversationService) Restore(ctx context.Context, guard SessionWriteGuard, threadID domain.ReviewThreadID) (SessionWriteGuard, error) {
	if s == nil || ctx == nil || threadID == "" {
		return guard, ErrReviewStoreInput
	}
	if s.persistence == PersistenceNoPersist {
		return guard, nil
	}
	turns, err := s.store.ListProviderTurns(ctx, threadID)
	if err != nil {
		return guard, err
	}
	now := s.clock.Now().UTC()
	return s.store.WithSessionTx(ctx, guard, func(tx ReviewStoreTx) error {
		for _, turn := range turns {
			if !turn.active() {
				continue
			}
			turn.State = ProviderTurnInterrupted
			turn.ErrorCode = review.ErrorCode("provider_restart")
			turn.CompletedAt = &now
			if err := tx.SaveProviderTurn(ctx, turn); err != nil {
				return err
			}
		}
		return nil
	})
}

func (s *ProviderConversationService) providerReady(ctx context.Context) error {
	_, err := s.providerStatus(ctx)
	return err
}

func (s *ProviderConversationService) providerStatus(ctx context.Context) (ProviderStatus, error) {
	status, err := s.provider.Probe(ctx)
	if err != nil {
		return ProviderStatus{}, err
	}
	if err := CheckProviderTurn(status); err != nil {
		return ProviderStatus{}, err
	}
	return status, nil
}

func (s *ProviderConversationService) validateCommand(ctx context.Context, guard SessionWriteGuard, threadID domain.ReviewThreadID, operationID domain.OperationID, correlationID CorrelationID) error {
	if s == nil || ctx == nil || threadID == "" || operationID == "" || !validProviderMetadata(string(correlationID), maxProviderVersionBytes) {
		return ErrReviewStoreInput
	}
	if s.persistence == PersistenceDurable {
		return guard.Validate()
	}
	return nil
}

func (s *ProviderConversationService) loadThread(ctx context.Context, guard SessionWriteGuard, threadID domain.ReviewThreadID, ephemeral review.ReviewThread) (review.ReviewThread, error) {
	if s.persistence == PersistenceNoPersist {
		if err := ephemeral.Validate(); err != nil || ephemeral.ID != threadID {
			return review.ReviewThread{}, ErrThreadNotOwned
		}
		return ephemeral, nil
	}
	thread, err := s.store.LoadThread(ctx, threadID)
	if err != nil {
		return review.ReviewThread{}, err
	}
	if thread.SessionID != guard.SessionID {
		return review.ReviewThread{}, ErrThreadNotOwned
	}
	return thread, nil
}

func (s *ProviderConversationService) existingConversation(ctx context.Context, command EnsureProviderConversation, id domain.ProviderConversationID) (ProviderConversationCommit, error) {
	record, err := s.loadConversation(ctx, command.Guard, command.ThreadID, id)
	if err != nil {
		return ProviderConversationCommit{}, err
	}
	return s.existingConversationRecord(command, record)
}

func (s *ProviderConversationService) existingConversationRecord(command EnsureProviderConversation, record ProviderConversationRecord) (ProviderConversationCommit, error) {
	switch record.State {
	case ProviderConversationAttachedState:
		return ProviderConversationCommit{Guard: command.Guard, Conversation: record}, nil
	case ProviderConversationCreating, ProviderConversationResuming:
		return ProviderConversationCommit{}, ErrProviderConversationInProgress
	case ProviderConversationPossibleOrphan:
		return ProviderConversationCommit{}, ErrProviderConversationOrphan
	default:
		return ProviderConversationCommit{}, ErrProviderConversationAttached
	}
}

func (s *ProviderConversationService) loadConversation(ctx context.Context, guard SessionWriteGuard, threadID domain.ReviewThreadID, id domain.ProviderConversationID) (ProviderConversationRecord, error) {
	if id == "" {
		return ProviderConversationRecord{}, ErrReviewStoreInput
	}
	if s.persistence == PersistenceNoPersist {
		s.mu.Lock()
		record, ok := s.conversations[threadID]
		s.mu.Unlock()
		if !ok || record.ID != id {
			return ProviderConversationRecord{}, ErrReviewStoreNotFound
		}
		return record, nil
	}
	record, err := s.store.LoadProviderConversation(ctx, id)
	if err != nil {
		return ProviderConversationRecord{}, err
	}
	if record.ThreadID != threadID || record.Validate() != nil {
		return ProviderConversationRecord{}, ErrReviewStoreCorrupt
	}
	if guard.SessionID != "" {
		thread, loadErr := s.store.LoadThread(ctx, threadID)
		if loadErr != nil {
			return ProviderConversationRecord{}, loadErr
		}
		if thread.SessionID != guard.SessionID {
			return ProviderConversationRecord{}, ErrThreadNotOwned
		}
	}
	return *record, nil
}

func (s *ProviderConversationService) loadTurn(ctx context.Context, guard SessionWriteGuard, threadID domain.ReviewThreadID, id domain.ProviderTurnID) (ProviderTurnRecord, error) {
	if id == "" {
		return ProviderTurnRecord{}, ErrReviewStoreInput
	}
	if s.persistence == PersistenceNoPersist {
		s.mu.Lock()
		turn, ok := s.turns[id]
		s.mu.Unlock()
		if !ok || turn.ThreadID != threadID {
			return ProviderTurnRecord{}, ErrReviewStoreNotFound
		}
		return turn, nil
	}
	turn, err := s.store.LoadProviderTurn(ctx, id)
	if err != nil {
		return ProviderTurnRecord{}, err
	}
	if turn.ThreadID != threadID || turn.Validate() != nil {
		return ProviderTurnRecord{}, ErrReviewStoreCorrupt
	}
	if guard.SessionID != "" {
		thread, loadErr := s.store.LoadThread(ctx, threadID)
		if loadErr != nil {
			return ProviderTurnRecord{}, loadErr
		}
		if thread.SessionID != guard.SessionID {
			return ProviderTurnRecord{}, ErrThreadNotOwned
		}
	}
	return *turn, nil
}

func (s *ProviderConversationService) hasActiveTurn(ctx context.Context, threadID domain.ReviewThreadID) (bool, error) {
	if s.persistence == PersistenceDurable {
		turns, err := s.store.ListProviderTurns(ctx, threadID)
		if err != nil {
			return false, err
		}
		for _, turn := range turns {
			if turn.active() {
				return true, nil
			}
		}
		return false, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, turn := range s.turns {
		if turn.ThreadID == threadID && turn.active() {
			return true, nil
		}
	}
	return false, nil
}

func (s *ProviderConversationService) saveTurn(ctx context.Context, guard SessionWriteGuard, turn ProviderTurnRecord) (SessionWriteGuard, error) {
	if err := turn.Validate(); err != nil {
		return guard, err
	}
	if s.persistence == PersistenceNoPersist {
		s.updateTurn(turn)
		return guard, nil
	}
	next, err := s.store.WithSessionTx(ctx, guard, func(tx ReviewStoreTx) error { return tx.SaveProviderTurn(ctx, turn) })
	if err == nil {
		s.updateTurn(turn)
	}
	return next, err
}

func (s *ProviderConversationService) updateConversation(record ProviderConversationRecord) {
	s.mu.Lock()
	s.conversations[record.ThreadID] = record
	s.mu.Unlock()
}

func (s *ProviderConversationService) removeConversation(threadID domain.ReviewThreadID, id domain.ProviderConversationID) {
	s.mu.Lock()
	if record, ok := s.conversations[threadID]; ok && record.ID == id {
		delete(s.conversations, threadID)
	}
	s.mu.Unlock()
}

func (s *ProviderConversationService) updateTurn(record ProviderTurnRecord) {
	s.mu.Lock()
	s.turns[record.ID] = record
	s.mu.Unlock()
}

func (s *ProviderConversationService) removeTurn(id domain.ProviderTurnID) {
	s.mu.Lock()
	delete(s.turns, id)
	s.mu.Unlock()
}
