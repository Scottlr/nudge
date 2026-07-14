// Package code owns the bounded code-pane projection. It consumes immutable
// application row evidence and emits inert root intents.
package code

import (
	"github.com/Scottlr/nudge/internal/app"
	"github.com/Scottlr/nudge/internal/domain"
	"github.com/Scottlr/nudge/internal/highlight"
)

const defaultPageLimit = 128

// PageRequest requests one bounded logical-row page for the current content.
type PageRequest struct {
	ContentID app.DisplayedContentID
	Cursor    string
	Limit     int
	Token     uint64
}

// PageResult carries one page response and must echo the request identity.
type PageResult struct {
	Request PageRequest
	Page    app.DisplayedContentPage
}

// SearchRequest is an application-owned search intent over one immutable
// displayed-content identity.
type SearchRequest struct {
	ContentID app.DisplayedContentID
	Query     string
	Cursor    string
	Limit     int
	Token     uint64
}

// SearchResult carries stable row identities rather than display positions.
type SearchResult struct {
	Request    SearchRequest
	Matches    []app.CodeRowID
	NextCursor string
}

// SelectRowIntent identifies the selected row and explicit side.
type SelectRowIntent struct {
	ContentID app.DisplayedContentID
	RowID     app.CodeRowID
	Side      app.RowSide
}

// RangeSelection is a same-side, same-hunk selection.
type RangeSelection struct {
	ContentID app.DisplayedContentID
	Start     app.CodeRowID
	End       app.CodeRowID
	Side      app.RowSide
	HunkID    string
}

// SelectionIntent is emitted only after the range invariants pass.
type SelectionIntent struct {
	Selection RangeSelection
}

// Intent is returned to the root/application boundary. The component never
// invokes a query, highlighter, provider, or store directly.
type Intent struct {
	PageRequest *PageRequest
	Search      *SearchRequest
	SelectRow   *SelectRowIntent
	Selection   *SelectionIntent
	LargeOpen   *LargeContentOpenRequest
	LargeWindow *LargeContentWindowRequest
	LargeClose  *LargeContentCloseRequest
}

// LargeContentOpenRequest is the explicit confirmation intent for an
// immutable over-threshold content identity.
type LargeContentOpenRequest struct {
	Request app.OpenLargeContent
	Token   uint64
}

// LargeContentWindowRequest asks the application for one bounded line window.
type LargeContentWindowRequest struct {
	Request app.LargeContentWindowRequest
	Token   uint64
}

// LargeContentCloseRequest releases the application's disposable open lease.
type LargeContentCloseRequest struct {
	Request app.CloseLargeContent
	Token   uint64
}

// LargeContentOpenMsg carries the verified immutable open lease.
type LargeContentOpenMsg struct {
	Result app.LargeContentOpen
	Token  uint64
}

// LargeContentWindowMsg carries one bounded line-window projection.
type LargeContentWindowMsg struct {
	Result app.ContentWindow
	Token  uint64
}

// LargeContentCloseMsg confirms release of the disposable open lease.
type LargeContentCloseMsg struct {
	Token uint64
	Err   error
}

// LargeContentErrorMsg retires one stale or failed large-content request.
type LargeContentErrorMsg struct {
	Token uint64
	Err   error
}

// LargeContentOpenIntent constructs the confirmation-bound code-pane intent.
func LargeContentOpenIntent(identity app.ContentIdentity, revision uint64, operationID domain.OperationID, confirmed bool, token uint64) (Intent, error) {
	if identity.Validate() != nil || revision == 0 || operationID == "" || token == 0 {
		return Intent{}, app.ErrInvalidLargeContentRequest
	}
	request := LargeContentOpenRequest{Request: app.OpenLargeContent{Identity: identity, ExpectedQueryRevision: revision, OperationID: operationID, Confirmed: confirmed}, Token: token}
	return Intent{LargeOpen: &request}, nil
}

// SnapshotContentMsg replaces the immutable content envelope.
type SnapshotContentMsg struct {
	Content app.DisplayedContent
}

// PageResultMsg carries an application-owned page response.
type PageResultMsg struct {
	Result PageResult
}

// PageErrorMsg retires one failed page request.
type PageErrorMsg struct {
	Request PageRequest
	Err     error
}

// SetHighlightMsg adds T012 style spans to one already-loaded row.
type SetHighlightMsg struct {
	RowID app.CodeRowID
	Side  app.RowSide
	Spans []highlight.StyledSpan
}

// SearchResultMsg carries a bounded application search response.
type SearchResultMsg struct {
	Result SearchResult
}

// SearchErrorMsg retires one failed search request.
type SearchErrorMsg struct {
	Request SearchRequest
	Err     error
}

// MoveVerticalMsg moves among selectable logical rows.
type MoveVerticalMsg struct {
	Delta int
}

// MoveHorizontalMsg moves the bounded cell viewport.
type MoveHorizontalMsg struct {
	Delta int
}

// SelectRowMsg selects a row using an explicit side when context is shared.
type SelectRowMsg struct {
	RowID app.CodeRowID
	Side  app.RowSide
}

// BeginSelectionMsg starts a same-side range selection at the current row.
type BeginSelectionMsg struct{}

// ExtendSelectionMsg ends a same-side range selection at one row.
type ExtendSelectionMsg struct {
	RowID app.CodeRowID
}

// ClearSelectionMsg clears the transient selection range.
type ClearSelectionMsg struct{}

// ToggleContextMsg collapses or expands one bounded context group.
type ToggleContextMsg struct {
	GroupID string
}

// JumpHunkMsg moves to the next or previous loaded hunk.
type JumpHunkMsg struct {
	Direction int
}

// JumpToHunkMsg moves to the first loaded row in one hunk.
type JumpToHunkMsg struct {
	HunkID string
}

// JumpFileEdgeMsg moves to the first or last selectable row.
type JumpFileEdgeMsg struct {
	End bool
}

// SetSearchQueryMsg starts or clears an application-owned search.
type SetSearchQueryMsg struct {
	Query string
}

// LoadNextPageMsg requests the cursor after one accepted page.
type LoadNextPageMsg struct {
	Cursor string
}

// SetFocusMsg controls the pane's focus emphasis without changing selection.
type SetFocusMsg struct {
	Focused bool
}

// SelectionRejectedReason is a safe typed reason exposed to the root.
type SelectionRejectedReason string

const (
	SelectionNoRow            SelectionRejectedReason = "no_row"
	SelectionNotSelectable    SelectionRejectedReason = "not_selectable"
	SelectionSideRequired     SelectionRejectedReason = "side_required"
	SelectionSideMismatch     SelectionRejectedReason = "side_mismatch"
	SelectionDifferentContent SelectionRejectedReason = "different_content"
	SelectionDifferentHunk    SelectionRejectedReason = "different_hunk"
)

// SelectionRejected exposes the latest failed selection attempt without
// embedding unsafe source text in a UI message.
type SelectionRejected struct {
	Reason SelectionRejectedReason
}
