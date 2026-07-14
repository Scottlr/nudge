package review

import (
	"fmt"
	"time"

	"github.com/Scottlr/nudge/internal/domain"
)

// ReviewThread is one anchored concern within exactly one review session.
// Its status axes remain independent so resolution never implies provider,
// proposal, anchor, or notification state.
type ReviewThread struct {
	ID                     domain.ReviewThreadID
	SessionID              domain.ReviewSessionID
	Anchor                 CodeAnchor
	Title                  string
	Resolution             ResolutionState
	Conversation           ConversationState
	Proposal               ProposalState
	Read                   ReadState
	ProviderConversationID *domain.ProviderConversationID
	LatestProposalID       *domain.ProposalID
	Messages               []domain.MessageID
	FailurePhase           FailurePhase
	ErrorCode              ErrorCode
	CreatedAt              time.Time
	UpdatedAt              time.Time
}

// NewReviewThread validates and returns one thread value.
func NewReviewThread(thread ReviewThread) (ReviewThread, error) {
	if err := thread.Validate(); err != nil {
		return ReviewThread{}, err
	}
	thread.Messages = append([]domain.MessageID(nil), thread.Messages...)
	return thread, nil
}

// NewOpenReviewThread constructs a thread before any provider conversation has
// succeeded. The caller can add the initial message through the application
// command boundary without changing this domain invariant.
func NewOpenReviewThread(id domain.ReviewThreadID, sessionID domain.ReviewSessionID, anchor CodeAnchor, now time.Time) (ReviewThread, error) {
	thread := ReviewThread{
		ID:           id,
		SessionID:    sessionID,
		Anchor:       anchor,
		Resolution:   ResolutionOpen,
		Conversation: ConversationIdle,
		Proposal:     ProposalNone,
		Read:         Unread,
		CreatedAt:    now,
		UpdatedAt:    now,
	}
	return NewReviewThread(thread)
}

// Validate checks thread identity, owned anchor, independent status axes, and
// monotonic timestamps.
func (t ReviewThread) Validate() error {
	if !validDomainID(t.ID) || !validDomainID(t.SessionID) || t.CreatedAt.IsZero() || t.UpdatedAt.IsZero() || t.UpdatedAt.Before(t.CreatedAt) {
		return ErrInvalidReviewThread
	}
	if err := t.Anchor.Validate(); err != nil {
		return fmt.Errorf("%w: anchor: %v", ErrInvalidReviewThread, err)
	}
	if t.Resolution.Validate() != nil || t.Conversation.Validate() != nil || t.Proposal.Validate() != nil || t.Anchor.State.Validate() != nil || t.Read.Validate() != nil {
		return ErrInvalidReviewThread
	}
	if t.Title != "" && !validContent(t.Title) {
		return ErrInvalidReviewThread
	}
	if t.ProviderConversationID != nil && !validDomainID(*t.ProviderConversationID) {
		return ErrInvalidReviewThread
	}
	if t.LatestProposalID != nil && !validDomainID(*t.LatestProposalID) {
		return ErrInvalidReviewThread
	}
	seen := make(map[domain.MessageID]struct{}, len(t.Messages))
	for _, id := range t.Messages {
		if !validDomainID(id) {
			return ErrInvalidReviewThread
		}
		if _, exists := seen[id]; exists {
			return ErrInvalidReviewThread
		}
		seen[id] = struct{}{}
	}
	if !validOptionalMetadata(string(t.FailurePhase)) || !validOptionalMetadata(string(t.ErrorCode)) {
		return ErrInvalidReviewThread
	}
	return nil
}

// Status returns the composed projection of the independent status axes.
func (t ReviewThread) Status() ThreadStatus {
	return ThreadStatus{
		Resolution:   t.Resolution,
		Conversation: t.Conversation,
		Proposal:     t.Proposal,
		Anchor:       t.Anchor.State,
		Read:         t.Read,
		FailurePhase: t.FailurePhase,
		ErrorCode:    t.ErrorCode,
	}
}

// ComposedStatus returns the same independent-axis projection using the
// terminology from the product design.
func (t ReviewThread) ComposedStatus() ComposedStatus {
	return t.Status()
}

// Resolve marks only the resolution axis resolved.
func (t *ReviewThread) Resolve(now time.Time) error {
	return t.setResolution(ResolutionResolved, now)
}

// Reopen marks only the resolution axis open.
func (t *ReviewThread) Reopen(now time.Time) error {
	return t.setResolution(ResolutionOpen, now)
}

func (t *ReviewThread) setResolution(state ResolutionState, now time.Time) error {
	if t == nil || t.Validate() != nil || state.Validate() != nil || now.IsZero() || now.Before(t.UpdatedAt) {
		return ErrInvalidStatusTransition
	}
	t.Resolution = state
	t.UpdatedAt = now
	return nil
}

// MarkRead changes only notification state.
func (t *ReviewThread) MarkRead(now time.Time) error {
	return t.setRead(Read, now)
}

// MarkUnread changes only notification state.
func (t *ReviewThread) MarkUnread(now time.Time) error {
	return t.setRead(Unread, now)
}

func (t *ReviewThread) setRead(state ReadState, now time.Time) error {
	if t == nil || t.Validate() != nil || state.Validate() != nil || now.IsZero() || now.Before(t.UpdatedAt) {
		return ErrInvalidStatusTransition
	}
	t.Read = state
	t.UpdatedAt = now
	return nil
}

// SetConversation updates the conversation axis and its optional failure
// metadata without changing resolution, proposal, anchor, or read state.
func (t *ReviewThread) SetConversation(state ConversationState, phase FailurePhase, code ErrorCode, now time.Time) error {
	if t == nil || t.Validate() != nil || state.Validate() != nil || !validOptionalMetadata(string(phase)) || !validOptionalMetadata(string(code)) || now.IsZero() || now.Before(t.UpdatedAt) {
		return ErrInvalidStatusTransition
	}
	t.Conversation = state
	t.FailurePhase = phase
	t.ErrorCode = code
	t.UpdatedAt = now
	return nil
}

// SetProposal updates proposal status and its latest proposal identity only.
func (t *ReviewThread) SetProposal(state ProposalState, proposalID *domain.ProposalID, now time.Time) error {
	if t == nil || t.Validate() != nil || state.Validate() != nil || (proposalID != nil && !validDomainID(*proposalID)) || now.IsZero() || now.Before(t.UpdatedAt) {
		return ErrInvalidStatusTransition
	}
	t.Proposal = state
	if proposalID == nil {
		t.LatestProposalID = nil
	} else {
		id := *proposalID
		t.LatestProposalID = &id
	}
	t.UpdatedAt = now
	return nil
}

// SetAnchorState changes only the anchor reconciliation axis.
func (t *ReviewThread) SetAnchorState(state AnchorState, now time.Time) error {
	if t == nil || t.Validate() != nil || state.Validate() != nil || now.IsZero() || now.Before(t.UpdatedAt) {
		return ErrInvalidStatusTransition
	}
	t.Anchor.State = state
	t.UpdatedAt = now
	return nil
}

// AttachProviderConversation links the local provider-conversation record
// after the thread itself already exists.
func (t *ReviewThread) AttachProviderConversation(id domain.ProviderConversationID, now time.Time) error {
	if t == nil || t.Validate() != nil || !validDomainID(id) || now.IsZero() || now.Before(t.UpdatedAt) {
		return ErrInvalidStatusTransition
	}
	copy := id
	t.ProviderConversationID = &copy
	t.UpdatedAt = now
	return nil
}

// AppendMessageID adds one normalized message identity in durable order.
func (t *ReviewThread) AppendMessageID(id domain.MessageID, now time.Time) error {
	if t == nil || t.Validate() != nil || !validDomainID(id) || now.IsZero() || now.Before(t.UpdatedAt) {
		return ErrInvalidStatusTransition
	}
	for _, existing := range t.Messages {
		if existing == id {
			return ErrInvalidStatusTransition
		}
	}
	t.Messages = append(t.Messages, id)
	t.UpdatedAt = now
	return nil
}
