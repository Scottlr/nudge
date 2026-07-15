package threads

import (
	"github.com/Scottlr/nudge/internal/app"
	"github.com/Scottlr/nudge/internal/domain"
	"github.com/Scottlr/nudge/internal/domain/review"
	"github.com/Scottlr/nudge/internal/theme"
	"github.com/Scottlr/nudge/internal/tui/viewport"
)

const (
	maxRetainedItems = 200
	defaultOverscan  = 2
	defaultHeight    = 12
)

// Model is the disposable, bounded review-thread list state.
type Model struct {
	snapshotRevision uint64
	sessionID        domain.ReviewSessionID
	items            []app.ThreadSummary
	query            app.ThreadPage
	nextCursor       *app.ThreadCursor
	totalCount       uint64
	selected         domain.ReviewThreadID
	pending          *PageRequest
	nextToken        uint64
	focused          bool
	top              int
	width            int
	height           int
	overscan         int
	budget           viewport.RenderBudget
	theme            theme.Theme
	lastError        string
}

// NewModel creates an empty bounded thread list.
func NewModel() *Model {
	return &Model{
		overscan: defaultOverscan,
		budget:   viewport.DefaultRenderBudget(),
		theme:    theme.BuiltinTerminalDefault(),
	}
}

// SetSize updates the terminal-cell viewport dimensions.
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

// SetBudget supplies the shared T073 frame budget.
func (m *Model) SetBudget(value viewport.RenderBudget) {
	if m != nil && value.Validate() == nil {
		m.budget = value
	}
}

// Selected returns the stable selected thread identity.
func (m *Model) Selected() domain.ReviewThreadID {
	if m == nil {
		return ""
	}
	return m.selected
}

// SnapshotRevision returns the revision represented by retained summaries.
func (m *Model) SnapshotRevision() uint64 {
	if m == nil {
		return 0
	}
	return m.snapshotRevision
}

// VisibleAnimatedWork counts only streaming or proposal-generating summaries
// in the currently rendered bounded thread window.
func (m *Model) VisibleAnimatedWork() int {
	if m == nil || len(m.items) == 0 {
		return 0
	}
	window := viewport.Window(len(m.items), m.selectedIndex(), m.top, m.renderHeight(), m.overscan)
	count := 0
	for index := window.Start; index < window.End; index++ {
		item := m.items[index]
		if item.Conversation == review.ConversationStreaming || item.Proposal == review.ProposalGenerating {
			count++
		}
	}
	return count
}

// InitialPageRequest asks the root to fetch the first thread page when the
// current session has no retained summaries.
func (m *Model) InitialPageRequest() []Intent {
	if m == nil || m.sessionID == "" || m.snapshotRevision == 0 || len(m.items) != 0 || m.pending != nil {
		return nil
	}
	m.nextToken++
	request := PageRequest{SessionID: m.sessionID, Revision: m.snapshotRevision, Limit: defaultPageLimit, Token: m.nextToken}
	m.pending = &request
	return []Intent{{PageRequest: &request}}
}

func (m *Model) clearPageRequest() {
	m.pending = nil
	m.nextCursor = nil
	m.query = app.ThreadPage{}
	m.items = nil
	m.totalCount = 0
	m.top = 0
}

func (m *Model) replaceSnapshot(snapshot app.AppSnapshot) []Intent {
	if snapshot.Revision < m.snapshotRevision {
		return nil
	}
	sessionID := domain.ReviewSessionID("")
	if snapshot.SessionID != nil {
		sessionID = *snapshot.SessionID
	}
	if snapshot.Revision != m.snapshotRevision || sessionID != m.sessionID {
		m.clearPageRequest()
	}
	m.snapshotRevision = snapshot.Revision
	if snapshot.SessionID != nil {
		m.sessionID = *snapshot.SessionID
	} else {
		m.sessionID = ""
	}
	window := snapshot.ThreadWindow
	items := window.Items
	if len(items) == 0 {
		items = snapshot.Threads
	}
	m.items = cloneSummaries(items)
	m.query = cloneThreadPage(window.Query)
	m.nextCursor = cloneThreadCursor(window.NextCursor)
	m.totalCount = window.TotalCount
	if m.totalCount < uint64(len(m.items)) {
		m.totalCount = uint64(len(m.items))
	}
	if m.selected != "" && !m.contains(m.selected) {
		m.selected = ""
	}
	if m.selected == "" && len(m.items) > 0 {
		m.selected = m.items[0].ID
	}
	m.reposition()
	return m.InitialPageRequest()
}

func (m *Model) contains(id domain.ReviewThreadID) bool {
	for _, item := range m.items {
		if item.ID == id {
			return true
		}
	}
	return false
}

func (m *Model) selectedIndex() int {
	for index, item := range m.items {
		if item.ID == m.selected {
			return index
		}
	}
	return 0
}

func (m *Model) reposition() {
	m.top = viewport.Window(len(m.items), m.selectedIndex(), m.top, m.renderHeight(), m.overscan).Top
}

func (m *Model) renderHeight() int {
	if m.height <= 0 {
		return defaultHeight
	}
	return m.height
}

func (m *Model) moveSelection(delta int) {
	if delta == 0 || len(m.items) == 0 {
		return
	}
	index := clampInt(m.selectedIndex()+delta, 0, len(m.items)-1)
	m.selected = m.items[index].ID
	m.reposition()
}

func (m *Model) pageRequest(cursor *app.ThreadCursor) *PageRequest {
	if m.pending != nil || m.sessionID == "" || m.snapshotRevision == 0 {
		return nil
	}
	m.nextToken++
	request := PageRequest{SessionID: m.sessionID, Revision: m.snapshotRevision, Cursor: cloneThreadCursor(cursor), Limit: defaultPageLimit, Token: m.nextToken}
	m.pending = &request
	return &request
}

func (m *Model) acceptPage(result PageResult) {
	if m.pending == nil || result.Request.Token == 0 || result.Request.Token != m.pending.Token || result.Request.Revision != m.snapshotRevision || result.Request.SessionID != m.sessionID || !sameThreadCursor(result.Request.Cursor, m.pending.Cursor) || result.Page.Revision != result.Request.Revision {
		return
	}
	deleteRequest := m.pending
	m.pending = nil
	if deleteRequest == nil {
		return
	}
	for _, item := range result.Page.Items {
		if item.ID == "" || item.SessionID != m.sessionID {
			m.lastError = "invalid thread page"
			return
		}
	}
	if result.Request.Cursor == nil {
		m.items = cloneSummaries(result.Page.Items)
	} else {
		m.items = mergeSummaries(m.items, result.Page.Items)
	}
	if len(m.items) > maxRetainedItems {
		m.items = m.items[:maxRetainedItems]
	}
	m.query = result.Request.Query()
	m.nextCursor = cloneThreadCursor(result.Page.Next)
	if result.Page.HasMore && m.nextCursor == nil {
		m.lastError = "invalid thread page cursor"
		return
	}
	if m.totalCount < uint64(len(m.items)) {
		m.totalCount = uint64(len(m.items))
	}
	if m.selected == "" && len(m.items) > 0 {
		m.selected = m.items[0].ID
	}
	m.reposition()
}

func cloneSummaries(values []app.ThreadSummary) []app.ThreadSummary {
	if len(values) == 0 {
		return nil
	}
	result := make([]app.ThreadSummary, len(values))
	copy(result, values)
	return result
}

func mergeSummaries(existing, incoming []app.ThreadSummary) []app.ThreadSummary {
	result := cloneSummaries(existing)
	seen := make(map[domain.ReviewThreadID]struct{}, len(result))
	for _, item := range result {
		seen[item.ID] = struct{}{}
	}
	for _, item := range incoming {
		if _, ok := seen[item.ID]; ok {
			continue
		}
		seen[item.ID] = struct{}{}
		result = append(result, item)
	}
	return result
}

func cloneThreadPage(value app.ThreadPage) app.ThreadPage {
	value.Cursor = cloneThreadCursor(value.Cursor)
	return value
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
