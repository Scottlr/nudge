package code

import (
	"strings"

	"github.com/Scottlr/nudge/internal/app"
	"github.com/Scottlr/nudge/internal/domain/repository"
	"github.com/Scottlr/nudge/internal/theme"
	"github.com/Scottlr/nudge/internal/tui/viewport"
)

const (
	maxRetainedPages = 8
	defaultOverscan  = 2
	defaultHeight    = 20
	maxSearchMatches = 4096
)

// Model is the disposable code-pane projection. It retains only bounded
// logical pages and never owns source, diff, or canonical review truth.
type Model struct {
	content       app.DisplayedContent
	pages         map[string]pageState
	pageOrder     []string
	pending       map[string]uint64
	nextToken     uint64
	selected      app.CodeRowID
	side          app.RowSide
	selectionFrom *app.CodeRowID
	selection     *RangeSelection
	collapsed     map[string]bool
	searchQuery   string
	searchMatches map[app.CodeRowID]struct{}
	searchPending uint64
	searchNext    string
	pageLimit     int
	maxPages      int
	overscan      int
	budget        viewport.RenderBudget
	theme         theme.Theme
	focused       bool
	width         int
	height        int
	top           int
	left          int
	lastError     string
	lastSelection SelectionRejected
	large         *largeContentProjection
	markers       map[markerKey]markerGroup
}

// NewModel creates an empty bounded code pane.
func NewModel() *Model {
	return &Model{
		pages:         make(map[string]pageState),
		pending:       make(map[string]uint64),
		collapsed:     make(map[string]bool),
		searchMatches: make(map[app.CodeRowID]struct{}),
		pageLimit:     defaultPageLimit,
		maxPages:      maxRetainedPages,
		overscan:      defaultOverscan,
		budget:        viewport.DefaultRenderBudget(),
		theme:         theme.BuiltinTerminalDefault(),
		side:          app.SideHead,
		large:         newLargeContentProjection(),
		markers:       make(map[markerKey]markerGroup),
	}
}

// SetSize updates the terminal-cell viewport.
func (m *Model) SetSize(width, height int) {
	if m == nil {
		return
	}
	m.width, m.height = maxInt(width, 0), maxInt(height, 0)
	m.reposition()
}

// SetTheme supplies a resolved semantic theme.
func (m *Model) SetTheme(value theme.Theme) {
	if m != nil && value.Validate() == nil {
		m.theme = value
	}
}

// SetBudget supplies the root's checked frame allowance.
func (m *Model) SetBudget(value viewport.RenderBudget) {
	if m != nil && value.Validate() == nil {
		m.budget = value
	}
}

// Content returns a defensive content envelope copy.
func (m *Model) Content() app.DisplayedContent {
	if m == nil {
		return app.DisplayedContent{}
	}
	result := m.content
	if m.content.BasePath != nil {
		path := repository.RepoPath(m.content.BasePath.Bytes())
		result.BasePath = &path
	}
	if m.content.HeadPath != nil {
		path := repository.RepoPath(m.content.HeadPath.Bytes())
		result.HeadPath = &path
	}
	return result
}

// InitialPageRequest requests the first logical page after content is ready.
func (m *Model) InitialPageRequest() []Intent {
	if m == nil || m.content.Validate() != nil || m.content.Status != app.ContentReady {
		return nil
	}
	return m.requestPage("")
}

// Selected returns the current stable row identity and explicit side.
func (m *Model) Selected() (app.CodeRowID, app.RowSide) {
	if m == nil {
		return app.CodeRowID{}, app.SideNone
	}
	return m.selected, m.side
}

// Selection returns a defensive copy of the current valid range.
func (m *Model) Selection() *RangeSelection {
	if m == nil || m.selection == nil {
		return nil
	}
	result := *m.selection
	return &result
}

// LastSelectionRejection reports the latest typed selection failure.
func (m *Model) LastSelectionRejection() SelectionRejected {
	if m == nil {
		return SelectionRejected{}
	}
	return m.lastSelection
}

func (m *Model) clearProjection() {
	m.pages = make(map[string]pageState)
	m.pageOrder = nil
	m.pending = make(map[string]uint64)
	m.selected = app.CodeRowID{}
	m.selectionFrom = nil
	m.selection = nil
	m.collapsed = make(map[string]bool)
	m.searchMatches = make(map[app.CodeRowID]struct{})
	m.searchPending = 0
	m.searchNext = ""
	m.markers = make(map[markerKey]markerGroup)
	m.top, m.left = 0, 0
	if m.large != nil {
		m.large.reset()
	}
}

func (m *Model) requestPage(cursor string) []Intent {
	if _, ok := m.pending[cursor]; ok || m.content.Validate() != nil || m.content.Status != app.ContentReady {
		return nil
	}
	m.nextToken++
	request := PageRequest{ContentID: m.content.ID, Cursor: cursor, Limit: m.pageLimit, Token: m.nextToken}
	m.pending[cursor] = request.Token
	return []Intent{{PageRequest: &request}}
}

func (m *Model) acceptPage(result PageResult) {
	request := result.Request
	if request.ContentID != m.content.ID || m.pending[request.Cursor] != request.Token || request.Token == 0 || request.Limit != m.pageLimit || result.Page.ContentID != request.ContentID || result.Page.Cursor != request.Cursor || result.Page.Validate() != nil {
		return
	}
	delete(m.pending, request.Cursor)
	rows := make([]codeRow, 0, len(result.Page.Rows))
	for _, row := range result.Page.Rows {
		rows = append(rows, newCodeRow(row))
	}
	m.pages[request.Cursor] = pageState{Request: request, Rows: rows, NextCursor: result.Page.NextCursor}
	m.touchPage(request.Cursor)
	m.reposition()
}

func (m *Model) touchPage(cursor string) {
	order := m.pageOrder[:0]
	for _, existing := range m.pageOrder {
		if existing != cursor {
			order = append(order, existing)
		}
	}
	m.pageOrder = append(order, cursor)
	for len(m.pageOrder) > m.maxPages {
		index := 0
		if m.pageOrder[index] == "" && len(m.pageOrder) > 1 {
			index = 1
		}
		oldest := m.pageOrder[index]
		m.pageOrder = append(m.pageOrder[:index], m.pageOrder[index+1:]...)
		delete(m.pages, oldest)
	}
}

func (m *Model) pageRows() []codeRow {
	var rows []codeRow
	cursor := ""
	for {
		page, ok := m.pages[cursor]
		if !ok {
			break
		}
		rows = append(rows, page.Rows...)
		if page.NextCursor == "" {
			break
		}
		cursor = page.NextCursor
	}
	return rows
}

func (m *Model) visibleRows() []codeRow {
	rows := m.pageRows()
	if len(rows) == 0 {
		return nil
	}
	result := make([]codeRow, 0, len(rows))
	emittedGroups := make(map[string]struct{})
	for _, row := range rows {
		group := row.Evidence.ContextGroup
		if group != "" && m.collapsed[group] {
			if _, ok := emittedGroups[group]; ok {
				continue
			}
			emittedGroups[group] = struct{}{}
			collapsed := row
			collapsed.Evidence.Text = "context collapsed; expand to view"
			collapsed.Evidence.BaseText = ""
			collapsed.Evidence.HeadText = ""
			collapsed.Evidence.Selectable = false
			collapsed.Evidence.ContextCollapsed = true
			result = append(result, collapsed)
			continue
		}
		result = append(result, row)
	}
	return result
}

func (m *Model) row(id app.CodeRowID) (codeRow, bool) {
	for _, row := range m.visibleRows() {
		if row.Evidence.ID == id {
			return row, true
		}
	}
	return codeRow{}, false
}

func (m *Model) selectableRows() []codeRow {
	rows := m.visibleRows()
	result := make([]codeRow, 0, len(rows))
	for _, row := range rows {
		if row.Evidence.Selectable {
			result = append(result, row)
		}
	}
	return result
}

func (m *Model) moveVertical(delta int) []Intent {
	if delta == 0 {
		return nil
	}
	rows := m.selectableRows()
	if len(rows) == 0 {
		return nil
	}
	index := 0
	for candidate, row := range rows {
		if row.Evidence.ID == m.selected {
			index = candidate
			break
		}
	}
	index = clampInt(index+delta, 0, len(rows)-1)
	row := rows[index]
	side, reason := selectableSide(row, m.side, m.side)
	if reason != "" {
		return nil
	}
	m.selected, m.side = row.Evidence.ID, side
	m.reposition()
	return m.selectIntent()
}

func (m *Model) selectIntent() []Intent {
	if m.selected.Validate() != nil {
		return nil
	}
	selection := SelectRowIntent{ContentID: m.content.ID, RowID: m.selected, Side: m.side}
	return []Intent{{SelectRow: &selection}}
}

func (m *Model) reposition() {
	rows := m.visibleRows()
	cursor := 0
	for index, row := range rows {
		if row.Evidence.ID == m.selected {
			cursor = index
			break
		}
	}
	m.top = viewport.Window(len(rows), cursor, m.top, m.renderHeight(), m.overscan).Top
}

func (m *Model) renderHeight() int {
	if m.height <= 0 {
		return defaultHeight
	}
	return m.height
}

func (m *Model) firstOrLast(end bool) []Intent {
	rows := m.selectableRows()
	if len(rows) == 0 {
		return nil
	}
	row := rows[0]
	if end {
		row = rows[len(rows)-1]
	}
	side, reason := selectableSide(row, m.side, m.side)
	if reason != "" {
		return nil
	}
	m.selected, m.side = row.Evidence.ID, side
	m.reposition()
	return m.selectIntent()
}

func (m *Model) jumpHunk(direction int) []Intent {
	if direction == 0 {
		return nil
	}
	rows := m.visibleRows()
	var hunks []string
	seen := make(map[string]struct{})
	for _, row := range rows {
		if row.Evidence.HunkID != "" {
			if _, ok := seen[row.Evidence.HunkID]; !ok {
				seen[row.Evidence.HunkID] = struct{}{}
				hunks = append(hunks, row.Evidence.HunkID)
			}
		}
	}
	if len(hunks) == 0 {
		return nil
	}
	index := -1
	for candidate, hunk := range hunks {
		row, ok := m.row(m.selected)
		if ok && row.Evidence.HunkID == hunk {
			index = candidate
			break
		}
	}
	if index < 0 {
		index = 0
	} else {
		index = clampInt(index+direction, 0, len(hunks)-1)
	}
	return m.jumpToHunk(hunks[index])
}

func (m *Model) jumpToHunk(hunk string) []Intent {
	for _, row := range m.selectableRows() {
		if row.Evidence.HunkID != hunk {
			continue
		}
		side, reason := selectableSide(row, m.side, m.side)
		if reason != "" {
			return nil
		}
		m.selected, m.side = row.Evidence.ID, side
		m.reposition()
		return m.selectIntent()
	}
	return nil
}

func (m *Model) setSearch(query string) []Intent {
	m.searchQuery = strings.TrimSpace(query)
	m.searchMatches = make(map[app.CodeRowID]struct{})
	m.searchNext = ""
	m.searchPending = 0
	if m.searchQuery == "" || m.content.Validate() != nil {
		return nil
	}
	m.nextToken++
	request := SearchRequest{ContentID: m.content.ID, Query: m.searchQuery, Limit: defaultPageLimit, Token: m.nextToken}
	m.searchPending = request.Token
	return []Intent{{Search: &request}}
}

func (m *Model) acceptSearch(result SearchResult) {
	if result.Request.ContentID != m.content.ID || result.Request.Query != m.searchQuery || result.Request.Token == 0 || result.Request.Token != m.searchPending || !validSearchCursor(result.NextCursor) {
		return
	}
	m.searchPending = 0
	m.searchNext = result.NextCursor
	for _, match := range result.Matches {
		if len(m.searchMatches) >= maxSearchMatches {
			break
		}
		if match.Matches(m.content.ID) {
			m.searchMatches[match] = struct{}{}
		}
	}
	for _, row := range m.visibleRows() {
		if _, ok := m.searchMatches[row.Evidence.ID]; ok {
			m.selected = row.Evidence.ID
			m.reposition()
			break
		}
	}
}

func validSearchCursor(cursor string) bool {
	if cursor == "" {
		return true
	}
	for _, char := range cursor {
		if char < 0x20 || char == 0x7f {
			return false
		}
	}
	return true
}

func (m *Model) addHighlight(message SetHighlightMsg) {
	if message.RowID.Validate() != nil || message.RowID.Content != m.content.ID || (message.Side != app.SideBase && message.Side != app.SideHead) {
		return
	}
	for cursor, page := range m.pages {
		for index := range page.Rows {
			if page.Rows[index].Evidence.ID != message.RowID {
				continue
			}
			if message.Side == app.SideBase {
				page.Rows[index].BaseSpans = cloneSpans(message.Spans)
			} else {
				page.Rows[index].HeadSpans = cloneSpans(message.Spans)
			}
			m.pages[cursor] = page
			return
		}
	}
}

func (m *Model) updateSelectionFrom(rowID app.CodeRowID) []Intent {
	startID := m.selectionFrom
	if startID == nil {
		m.lastSelection = SelectionRejected{Reason: SelectionNoRow}
		return nil
	}
	start, startOK := m.row(*startID)
	end, endOK := m.row(rowID)
	if !startOK || !endOK {
		m.lastSelection = SelectionRejected{Reason: SelectionNoRow}
		return nil
	}
	selection, reason := selectionForRows(m.content.ID, start, end, m.side)
	if reason != "" {
		m.lastSelection = SelectionRejected{Reason: reason}
		return nil
	}
	m.selection = &selection
	m.lastSelection = SelectionRejected{}
	intent := SelectionIntent{Selection: selection}
	return []Intent{{Selection: &intent}}
}

func clampInt(value, low, high int) int {
	if value < low {
		return low
	}
	if value > high {
		return high
	}
	return value
}

func maxInt(left, right int) int {
	if left > right {
		return left
	}
	return right
}
