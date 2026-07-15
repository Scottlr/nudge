// Package tree owns the repository pane's bounded frontend projection. It
// emits inert query and selection intents; it never executes Git or changes
// canonical application state.
package tree

import (
	"strings"

	"github.com/Scottlr/nudge/internal/app"
	"github.com/Scottlr/nudge/internal/domain/repository"
	"github.com/Scottlr/nudge/internal/theme"
	"github.com/Scottlr/nudge/internal/tui/viewport"
)

const (
	defaultPageLimit   = 200
	defaultSearchLimit = 50
	maxRetainedPages   = 16
	maxSearchResults   = 200
	defaultOverscan    = 2
	defaultHeight      = 20
	maxTreeDepth       = 256
)

type pageState struct {
	Request    PageRequest
	Rows       []TreeRow
	NextCursor string
}

// Model is the repository pane's disposable state.
type Model struct {
	filter            FilterMode
	snapshotRevision  uint64
	expanded          map[repository.RepoPathKey]bool
	selected          repository.RepoPathKey
	pages             map[PageIdentity]pageState
	pageOrder         []PageIdentity
	pending           map[PageIdentity]uint64
	nextToken         uint64
	threadBadges      map[repository.RepoPathKey]ThreadBadge
	pageLimit         int
	maxPages          int
	overscan          int
	budget            viewport.RenderBudget
	theme             theme.Theme
	width             int
	height            int
	top               int
	lastError         string
	focused           bool
	searchSnapshot    repository.SnapshotRef
	searching         bool
	searchQuery       string
	searchMatches     []app.TreeSearchMatch
	searchCursor      string
	searchPending     uint64
	hierarchySelected repository.RepoPathKey
}

// NewModel creates a Changed-filter repository pane with bounded page
// residency and the shared TUI render budget.
func NewModel() *Model {
	return &Model{
		filter:       FilterChanged,
		expanded:     make(map[repository.RepoPathKey]bool),
		pages:        make(map[PageIdentity]pageState),
		pending:      make(map[PageIdentity]uint64),
		threadBadges: make(map[repository.RepoPathKey]ThreadBadge),
		pageLimit:    defaultPageLimit,
		maxPages:     maxRetainedPages,
		overscan:     defaultOverscan,
		budget:       viewport.DefaultRenderBudget(),
		theme:        theme.BuiltinTerminalDefault(),
	}
}

// Filter returns the active Changed/All projection.
func (m *Model) Filter() FilterMode {
	if m == nil {
		return FilterChanged
	}
	return m.filter
}

// SnapshotRevision returns the immutable revision currently represented by
// retained pages.
func (m *Model) SnapshotRevision() uint64 {
	if m == nil {
		return 0
	}
	return m.snapshotRevision
}

// SetSize updates the pane's terminal-cell viewport dimensions.
func (m *Model) SetSize(width, height int) {
	if m == nil {
		return
	}
	m.width = maxInt(width, 0)
	m.height = maxInt(height, 0)
	m.reposition()
}

// SetTheme supplies an already-resolved semantic theme.
func (m *Model) SetTheme(value theme.Theme) {
	if m != nil && value.Validate() == nil {
		m.theme = value
	}
}

// Focused reports whether the repository pane owns keyboard focus.
func (m *Model) Focused() bool {
	return m != nil && m.focused
}

// SetBudget supplies a smaller valid per-frame budget when the root scheduler
// has a stricter run-scoped allowance.
func (m *Model) SetBudget(value viewport.RenderBudget) {
	if m != nil && value.Validate() == nil {
		m.budget = value
	}
}

// InitialPageRequest returns the root page request once, coalescing repeats.
func (m *Model) InitialPageRequest() []Intent {
	if m == nil {
		return nil
	}
	return m.request(m.identity("", ""))
}

// PageCount is a bounded residency observation useful to the root projection.
func (m *Model) PageCount() int {
	if m == nil {
		return 0
	}
	return len(m.pages)
}

func (m *Model) identity(parent, cursor string) PageIdentity {
	return PageIdentity{SnapshotRevision: m.snapshotRevision, Parent: repository.RepoPathKey(parent), Filter: m.filter, Cursor: cursor, Limit: m.pageLimit}
}

func (m *Model) request(identity PageIdentity) []Intent {
	if _, ok := m.pending[identity]; ok {
		return nil
	}
	if identity.Parent != "" && !m.expanded[identity.Parent] {
		return nil
	}
	query, ok := queryForIdentity(identity)
	if !ok {
		return nil
	}
	m.nextToken++
	request := PageRequest{Identity: identity, Token: m.nextToken, Query: query}
	m.pending[identity] = request.Token
	return []Intent{{PageRequest: &request}}
}

func queryForIdentity(identity PageIdentity) (app.TreeQuery, bool) {
	query := app.TreeQuery{Filter: identity.Filter.treeFilter(), Cursor: identity.Cursor, Limit: identity.Limit}
	if identity.Parent == "" {
		return query, true
	}
	parent, err := identity.Parent.Path()
	if err != nil {
		return app.TreeQuery{}, false
	}
	query.ParentPath = &parent
	return query, true
}

func (m *Model) clearPages() {
	m.pages = make(map[PageIdentity]pageState)
	m.pageOrder = nil
	m.pending = make(map[PageIdentity]uint64)
}

func (m *Model) removeDescendants(path repository.RepoPathKey) {
	prefix := string(path) + "/"
	for identity := range m.pages {
		if strings.HasPrefix(string(identity.Parent), prefix) || identity.Parent == path {
			delete(m.pages, identity)
		}
	}
	for identity := range m.pending {
		if strings.HasPrefix(string(identity.Parent), prefix) || identity.Parent == path {
			delete(m.pending, identity)
		}
	}
	m.pageOrder = m.pageOrder[:0]
	for identity := range m.pages {
		m.pageOrder = append(m.pageOrder, identity)
	}
}

func (m *Model) touchPage(identity PageIdentity) {
	filtered := m.pageOrder[:0]
	for _, existing := range m.pageOrder {
		if existing != identity {
			filtered = append(filtered, existing)
		}
	}
	m.pageOrder = append(filtered, identity)
	for len(m.pageOrder) > m.maxPages {
		oldest := m.pageOrder[0]
		m.pageOrder = m.pageOrder[1:]
		delete(m.pages, oldest)
	}
}

func (m *Model) reposition() {
	if m.searching {
		m.top = viewport.Window(len(m.searchMatches), m.searchIndex(), m.top, m.renderHeight(), m.overscan).Top
		return
	}
	rows := m.flattenRows()
	m.top = viewport.Window(len(rows), m.selectedIndex(rows), m.top, m.renderHeight(), m.overscan).Top
}

func (m *Model) renderHeight() int {
	if m.height <= 0 {
		return defaultHeight
	}
	return m.height
}

func (m *Model) selectedIndex(rows []TreeRow) int {
	for index, row := range rows {
		if !row.Loading && row.Path.Key() == m.selected {
			return index
		}
	}
	return 0
}

func (m *Model) pageRows(parent repository.RepoPathKey) []TreeRow {
	var rows []TreeRow
	cursor := ""
	for {
		identity := m.identity(string(parent), cursor)
		page, ok := m.pages[identity]
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

func (m *Model) flattenRows() []TreeRow {
	var rows []TreeRow
	var walk func(repository.RepoPathKey, int)
	walk = func(parent repository.RepoPathKey, depth int) {
		if depth > maxTreeDepth {
			return
		}
		for _, source := range m.pageRows(parent) {
			row := cloneRow(source)
			row.Depth = depth
			rows = append(rows, row)
			if row.Kind != repository.FileKindDirectory || !m.expanded[row.Path.Key()] {
				continue
			}
			children := m.pageRows(row.Path.Key())
			if len(children) == 0 {
				if m.hasPendingParent(row.Path.Key()) {
					rows = append(rows, TreeRow{Path: repository.RepoPath(row.Path.Bytes()), Name: repository.RepoPath("loading"), Depth: depth + 1, Loading: true})
				}
				continue
			}
			walk(row.Path.Key(), depth+1)
		}
	}
	walk("", 0)
	return rows
}

func (m *Model) hasPendingParent(parent repository.RepoPathKey) bool {
	for identity := range m.pending {
		if identity.Parent == parent {
			return true
		}
	}
	return false
}

func (m *Model) moveSelection(delta int) {
	if delta == 0 {
		return
	}
	rows := m.flattenRows()
	if len(rows) == 0 {
		return
	}
	current := m.selectedIndex(rows)
	current = clampInt(current+delta, 0, len(rows)-1)
	step := 1
	if delta < 0 {
		step = -1
	}
	for rows[current].Loading && current+step >= 0 && current+step < len(rows) {
		current += step
	}
	if !rows[current].Loading {
		m.selected = rows[current].Path.Key()
	}
	m.reposition()
}

func (m *Model) selectedIntent() []Intent {
	if m.selected == "" {
		return nil
	}
	selection := SelectPathIntent{Path: m.selected}
	return []Intent{{SelectPath: &selection}}
}

func (m *Model) findRow(path repository.RepoPathKey) bool {
	for _, row := range m.flattenRows() {
		if !row.Loading && row.Path.Key() == path {
			return true
		}
	}
	return false
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
