// Package threads owns the bounded review-thread list projection. It keeps
// stable IDs and query identities while leaving thread workflow truth in app.
package threads

import (
	"github.com/Scottlr/nudge/internal/app"
	"github.com/Scottlr/nudge/internal/domain"
)

const defaultPageLimit uint32 = 100

// PageRequest identifies one revision-bound thread metadata query.
type PageRequest struct {
	SessionID domain.ReviewSessionID
	Revision  uint64
	Cursor    *app.ThreadCursor
	Limit     uint32
	Token     uint64
}

// Query converts the frontend request into the application-owned keyset
// query without retaining mutable cursor aliases.
func (r PageRequest) Query() app.ThreadPage {
	result := app.ThreadPage{SessionID: r.SessionID, Limit: r.Limit, Cursor: cloneThreadCursor(r.Cursor)}
	return result
}

// PageResult carries a complete application-owned thread page.
type PageResult struct {
	Request PageRequest
	Page    app.ThreadPageResult
}

// ActivateIntent selects one stable review thread at the application root.
type ActivateIntent struct {
	SessionID domain.ReviewSessionID
	ThreadID  domain.ReviewThreadID
}

// Intent is the child-to-root boundary for paging and canonical activation.
type Intent struct {
	PageRequest *PageRequest
	Activate    *ActivateIntent
}

// SetSnapshotMsg replaces the immutable thread summary projection.
type SetSnapshotMsg struct {
	Snapshot app.AppSnapshot
}

// PageResultMsg supplies one application-owned thread page.
type PageResultMsg struct {
	Result PageResult
}

// PageErrorMsg retires one failed thread page request.
type PageErrorMsg struct {
	Request PageRequest
	Err     error
}

// MoveSelectionMsg moves the bounded selected thread index.
type MoveSelectionMsg struct {
	Delta int
}

// ActivateSelectionMsg emits the selected thread identity.
type ActivateSelectionMsg struct{}

// LoadNextPageMsg requests the next revision-bound thread page.
type LoadNextPageMsg struct{}

// SetFocusMsg controls selected-row emphasis without changing identity.
type SetFocusMsg struct {
	Focused bool
}

func cloneThreadCursor(value *app.ThreadCursor) *app.ThreadCursor {
	if value == nil {
		return nil
	}
	copyValue := *value
	return &copyValue
}

func sameThreadCursor(left, right *app.ThreadCursor) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return left.SessionID == right.SessionID && left.Revision == right.Revision && left.UpdatedAt.Equal(right.UpdatedAt) && left.ID == right.ID && left.FilterKey == right.FilterKey
}
