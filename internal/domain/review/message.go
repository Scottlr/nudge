package review

import (
	"time"

	"github.com/Scottlr/nudge/internal/domain"
)

// Message is one provider-independent normalized transcript item. Content is
// retained as UTF-8 text; rendering and terminal safety belong to projections.
type Message struct {
	ID           domain.MessageID
	ThreadID     domain.ReviewThreadID
	Role         MessageRole
	Content      string
	ProviderID   string
	Status       MessageStatus
	Ordinal      uint64
	CreatedAt    time.Time
	UpdatedAt    time.Time
	CompletedAt  *time.Time
	FailurePhase FailurePhase
	ErrorCode    ErrorCode
}

// NewMessage validates and returns one message value.
func NewMessage(message Message) (Message, error) {
	if err := message.Validate(); err != nil {
		return Message{}, err
	}
	return message, nil
}

// NewPendingMessage constructs a message at the start of its lifecycle.
func NewPendingMessage(id domain.MessageID, threadID domain.ReviewThreadID, role MessageRole, ordinal uint64, now time.Time) (Message, error) {
	return NewMessage(Message{
		ID:        id,
		ThreadID:  threadID,
		Role:      role,
		Status:    MessagePending,
		Ordinal:   ordinal,
		CreatedAt: now,
		UpdatedAt: now,
	})
}

// Validate checks message identity, ordering, content encoding, and lifecycle
// timestamp invariants.
func (m Message) Validate() error {
	if !validDomainID(m.ID) || !validDomainID(m.ThreadID) || m.Role.Validate() != nil || m.Status.Validate() != nil || m.Ordinal == 0 || m.CreatedAt.IsZero() || m.UpdatedAt.IsZero() || m.UpdatedAt.Before(m.CreatedAt) || !validContent(m.Content) || !validOptionalMetadata(m.ProviderID) || !validOptionalMetadata(string(m.FailurePhase)) || !validOptionalMetadata(string(m.ErrorCode)) {
		return ErrInvalidMessage
	}
	terminal := m.Status == MessageCompleted || m.Status == MessageFailed || m.Status == MessageCancelled
	if terminal != (m.CompletedAt != nil) {
		return ErrInvalidMessage
	}
	if m.CompletedAt != nil && (m.CompletedAt.IsZero() || m.CompletedAt.Before(m.CreatedAt) || m.CompletedAt.Before(m.UpdatedAt)) {
		return ErrInvalidMessage
	}
	return nil
}

// BeginStreaming moves a pending message into streaming state.
func (m *Message) BeginStreaming(now time.Time) error {
	if m == nil || m.Validate() != nil || m.Status != MessagePending || !validTransitionTime(m.UpdatedAt, now) {
		return ErrInvalidStatusTransition
	}
	m.Status = MessageStreaming
	m.UpdatedAt = now
	return nil
}

// AppendContent accepts one normalized delta while a message is streaming.
func (m *Message) AppendContent(delta string, now time.Time) error {
	if m == nil || m.Validate() != nil || m.Status != MessageStreaming || !validTransitionTime(m.UpdatedAt, now) || !validContent(delta) {
		return ErrInvalidStatusTransition
	}
	m.Content += delta
	m.UpdatedAt = now
	return nil
}

// Complete freezes the message as a terminal completed item.
func (m *Message) Complete(now time.Time) error {
	if m == nil || m.Validate() != nil || (m.Status != MessagePending && m.Status != MessageStreaming) || !validTransitionTime(m.UpdatedAt, now) {
		return ErrInvalidStatusTransition
	}
	m.Status = MessageCompleted
	m.UpdatedAt = now
	m.CompletedAt = timePtr(now)
	return nil
}

// Fail freezes the message with independent phase and error-code metadata.
func (m *Message) Fail(phase FailurePhase, code ErrorCode, now time.Time) error {
	if m == nil || m.Validate() != nil || (m.Status != MessagePending && m.Status != MessageStreaming) || !validMetadata(string(phase)) || !validMetadata(string(code)) || !validTransitionTime(m.UpdatedAt, now) {
		return ErrInvalidStatusTransition
	}
	m.Status = MessageFailed
	m.FailurePhase = phase
	m.ErrorCode = code
	m.UpdatedAt = now
	m.CompletedAt = timePtr(now)
	return nil
}

// Cancel freezes a pending or streaming message as cancelled.
func (m *Message) Cancel(now time.Time) error {
	if m == nil || m.Validate() != nil || (m.Status != MessagePending && m.Status != MessageStreaming) || !validTransitionTime(m.UpdatedAt, now) {
		return ErrInvalidStatusTransition
	}
	m.Status = MessageCancelled
	m.UpdatedAt = now
	m.CompletedAt = timePtr(now)
	return nil
}

func validTransitionTime(previous, next time.Time) bool {
	return !next.IsZero() && !next.Before(previous)
}

func timePtr(value time.Time) *time.Time {
	copy := value
	return &copy
}
