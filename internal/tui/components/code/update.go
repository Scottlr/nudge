package code

import (
	"github.com/Scottlr/nudge/internal/app"
)

// Update applies a frontend message and returns inert root intents.
func (m *Model) Update(message any) []Intent {
	if m == nil {
		return nil
	}
	switch value := message.(type) {
	case LargeContentOpenMsg:
		if m.large == nil {
			m.large = newLargeContentProjection()
		}
		return m.large.acceptOpen(value.Result, value.Token, m.large.operation)
	case LargeContentWindowMsg:
		if m.large != nil {
			m.large.acceptWindow(value.Result, value.Token)
		}
	case LargeContentCloseMsg:
		if m.large != nil && m.large.open != nil && value.Token == m.large.pending {
			if value.Err == nil {
				m.large.reset()
			} else {
				m.lastError = "immutable content close failed"
			}
		}
	case LargeContentErrorMsg:
		if m.large != nil && m.large.pending == value.Token {
			m.large.pending = 0
			m.lastError = "immutable content request failed"
		}
	case SnapshotContentMsg:
		if value.Content.Validate() != nil {
			m.content = app.DisplayedContent{}
			m.clearProjection()
			m.lastError = "invalid displayed content"
			return nil
		}
		m.content = value.Content
		m.clearProjection()
		return m.InitialPageRequest()
	case PageResultMsg:
		m.acceptPage(value.Result)
	case PageErrorMsg:
		if m.pending[value.Request.Cursor] == value.Request.Token && value.Request.ContentID == m.content.ID {
			delete(m.pending, value.Request.Cursor)
			m.lastError = "displayed-content page request failed"
		}
	case SetHighlightMsg:
		m.addHighlight(value)
	case SetThreadMarkersMsg:
		m.setThreadMarkers(value)
	case SearchResultMsg:
		m.acceptSearch(value.Result)
	case SearchErrorMsg:
		if value.Request.Token == m.searchPending && value.Request.ContentID == m.content.ID {
			m.searchPending = 0
			m.lastError = "displayed-content search failed"
		}
	case MoveVerticalMsg:
		return m.moveVertical(value.Delta)
	case MoveHorizontalMsg:
		m.left = maxInt(m.left+value.Delta, 0)
	case SelectRowMsg:
		row, ok := m.row(value.RowID)
		if !ok {
			m.lastSelection = SelectionRejected{Reason: SelectionNoRow}
			return nil
		}
		side, reason := selectableSide(row, value.Side, m.side)
		if reason != "" {
			m.lastSelection = SelectionRejected{Reason: reason}
			return nil
		}
		m.selected, m.side = value.RowID, side
		m.reposition()
		return m.selectIntent()
	case BeginSelectionMsg:
		row, ok := m.row(m.selected)
		if !ok {
			m.lastSelection = SelectionRejected{Reason: SelectionNoRow}
			return nil
		}
		side, reason := selectableSide(row, m.side, m.side)
		if reason != "" {
			m.lastSelection = SelectionRejected{Reason: reason}
			return nil
		}
		m.side = side
		start := row.Evidence.ID
		m.selectionFrom = &start
		m.selection = nil
		m.lastSelection = SelectionRejected{}
	case ToggleSelectionMsg:
		if m.selectionFrom == nil {
			return m.Update(BeginSelectionMsg{})
		}
		return m.Update(ExtendSelectionMsg{RowID: m.selected})
	case ExtendSelectionMsg:
		return m.updateSelectionFrom(value.RowID)
	case ClearSelectionMsg:
		m.selectionFrom = nil
		m.selection = nil
		m.lastSelection = SelectionRejected{}
	case ToggleContextMsg:
		if value.GroupID == "" || !m.hasContextGroup(value.GroupID) {
			return nil
		}
		m.collapsed[value.GroupID] = !m.collapsed[value.GroupID]
		m.reposition()
	case JumpHunkMsg:
		return m.jumpHunk(value.Direction)
	case JumpToHunkMsg:
		return m.jumpToHunk(value.HunkID)
	case JumpFileEdgeMsg:
		return m.firstOrLast(value.End)
	case SetSearchQueryMsg:
		return m.setSearch(value.Query)
	case LoadNextPageMsg:
		page, ok := m.pages[value.Cursor]
		if !ok || page.NextCursor == "" {
			return nil
		}
		return m.requestPage(page.NextCursor)
	case SetFocusMsg:
		m.focused = value.Focused
	}
	return nil
}

func (m *Model) hasContextGroup(group string) bool {
	for _, row := range m.pageRows() {
		if row.Evidence.ContextGroup == group {
			return true
		}
	}
	return false
}
