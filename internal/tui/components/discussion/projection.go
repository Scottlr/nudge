// Package discussion owns the bounded active-thread discussion projection.
// It retains message metadata and identity-bound body ranges, never a second
// canonical transcript.
package discussion

import (
	tea "charm.land/bubbletea/v2"
	"github.com/Scottlr/nudge/internal/app"
	"github.com/Scottlr/nudge/internal/domain"
)

const defaultPageLimit uint32 = 100

// MessagePageRequest identifies one revision-bound message metadata query.
type MessagePageRequest struct {
	ThreadID domain.ReviewThreadID
	Revision uint64
	Cursor   *app.MessageCursor
	Limit    uint32
	Token    uint64
}

// Query converts the frontend request into the application-owned message
// page query without retaining mutable cursor aliases.
func (r MessagePageRequest) Query() app.MessagePage {
	return app.MessagePage{ThreadID: r.ThreadID, Limit: r.Limit, Cursor: cloneMessageCursor(r.Cursor)}
}

// MessagePageResult carries one complete bounded metadata page.
type MessagePageResult struct {
	Request MessagePageRequest
	Page    app.MessagePageResult
}

// BodyRangeRequest identifies one immutable identity-bound body range.
type BodyRangeRequest struct {
	ThreadID domain.ReviewThreadID
	Revision uint64
	Range    app.BodyRange
	Token    uint64
}

// BodyRangeResult carries one bounded body range response.
type BodyRangeResult struct {
	Request BodyRangeRequest
	Chunk   app.MessageBodyChunk
}

// ReplyIntent asks the root to persist a local pending user reply.
type ReplyIntent struct {
	ThreadID domain.ReviewThreadID
	Text     string
}

// ResolveIntent asks the root to change only the resolution axis.
type ResolveIntent struct {
	ThreadID domain.ReviewThreadID
	Resolved bool
}

// MarkReadIntent is emitted only after an explicit presented-message signal.
type MarkReadIntent struct {
	ThreadID domain.ReviewThreadID
}

// Intent is the component-to-root boundary for paging and typed actions.
type Intent struct {
	MessagePageRequest *MessagePageRequest
	BodyRangeRequest   *BodyRangeRequest
	Reply              *ReplyIntent
	Resolve            *ResolveIntent
	MarkRead           *MarkReadIntent
}

// SetThreadMsg binds the discussion to one active-thread projection.
type SetThreadMsg struct {
	Revision uint64
	Thread   *app.ThreadSummary
}

// MessagePageResultMsg supplies one application-owned metadata page.
type MessagePageResultMsg struct {
	Result MessagePageResult
}

// BodyRangeResultMsg supplies one identity-checked message range.
type BodyRangeResultMsg struct {
	Result BodyRangeResult
	Err    error
}

// MoveSelectionMsg moves the visible message selection.
type MoveSelectionMsg struct {
	Delta int
}

// PresentMessageMsg confirms that a complete body range was presented.
type PresentMessageMsg struct {
	MessageID domain.MessageID
}

// ToggleReplyMsg opens or closes the retained reply draft.
type ToggleReplyMsg struct{}

// UpdateDraftMsg delegates a typed Bubble Tea message to the wrapped T020
// textarea editor.
type UpdateDraftMsg struct {
	Message tea.Msg
}

// SetDraftMsg replaces the retained unsent reply draft.
type SetDraftMsg struct {
	Text string
}

// ResolveMsg emits an explicit resolution action for the active thread.
type ResolveMsg struct {
	Resolved bool
}

// LoadNextPageMsg requests the next message metadata page.
type LoadNextPageMsg struct{}

// SetFocusMsg controls discussion focus and draft visibility.
type SetFocusMsg struct {
	Focused bool
}

func cloneMessageCursor(value *app.MessageCursor) *app.MessageCursor {
	if value == nil {
		return nil
	}
	copyValue := *value
	return &copyValue
}

func sameMessageCursor(left, right *app.MessageCursor) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return left.ThreadID == right.ThreadID && left.Revision == right.Revision && left.UpdatedAt.Equal(right.UpdatedAt) && left.ID == right.ID
}
