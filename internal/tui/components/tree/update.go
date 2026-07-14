package tree

import (
	"github.com/Scottlr/nudge/internal/app"
	"github.com/Scottlr/nudge/internal/domain/repository"
)

// SnapshotRevisionMsg binds future page requests to a new immutable app
// snapshot and retires all pages from the previous revision.
type SnapshotRevisionMsg struct {
	Revision uint64
}

// SetFilterMsg switches the bounded hierarchy projection.
type SetFilterMsg struct {
	Filter FilterMode
}

// ToggleExpandedMsg expands or collapses one directory by raw path identity.
type ToggleExpandedMsg struct {
	Path repository.RepoPathKey
}

// PageResultMsg carries one application-owned page response.
type PageResultMsg struct {
	Result PageResult
}

// PageErrorMsg retires one pending request without admitting untrusted page
// data.
type PageErrorMsg struct {
	Request PageRequest
	Err     error
}

// SetThreadBadgesMsg supplies bounded thread projections without changing the
// tree query or canonical review state.
type SetThreadBadgesMsg struct {
	Badges map[repository.RepoPathKey]ThreadBadge
}

// MoveSelectionMsg moves the local selection by a bounded row delta.
type MoveSelectionMsg struct {
	Delta int
}

// SelectRowMsg activates one visible raw path.
type SelectRowMsg struct {
	Path repository.RepoPathKey
}

// ActivateSelectionMsg emits the currently selected path intent.
type ActivateSelectionMsg struct{}

// LoadNextPageMsg requests the next cursor in one already accepted page.
type LoadNextPageMsg struct {
	Identity PageIdentity
}

// Update applies a frontend message and returns zero or more root intents.
func (m *Model) Update(message any) []Intent {
	if m == nil {
		return nil
	}
	switch value := message.(type) {
	case SnapshotRevisionMsg:
		if value.Revision != m.snapshotRevision {
			m.snapshotRevision = value.Revision
			m.clearPages()
			return m.InitialPageRequest()
		}
	case SetFilterMsg:
		if value.Filter != FilterChanged && value.Filter != FilterAll {
			return nil
		}
		if value.Filter != m.filter {
			m.filter = value.Filter
			m.clearPages()
			return m.InitialPageRequest()
		}
	case ToggleExpandedMsg:
		if !m.findRow(value.Path) {
			return nil
		}
		m.expanded[value.Path] = !m.expanded[value.Path]
		if !m.expanded[value.Path] {
			m.removeDescendants(value.Path)
			m.reposition()
			return nil
		}
		identity := m.identity(string(value.Path), "")
		return m.request(identity)
	case PageResultMsg:
		return m.acceptPage(value.Result)
	case PageErrorMsg:
		m.acceptError(value.Request, value.Err)
	case SetThreadBadgesMsg:
		m.threadBadges = make(map[repository.RepoPathKey]ThreadBadge, len(value.Badges))
		for path, badge := range value.Badges {
			m.threadBadges[path] = badge
		}
		for identity, page := range m.pages {
			for index := range page.Rows {
				badge := m.threadBadges[page.Rows[index].Path.Key()]
				page.Rows[index].ThreadCount, page.Rows[index].ThreadStatus = maxInt(badge.Count, 0), badge.Status
			}
			m.pages[identity] = page
		}
	case MoveSelectionMsg:
		m.moveSelection(value.Delta)
	case SelectRowMsg:
		if m.findRow(value.Path) {
			m.selected = value.Path
			m.reposition()
			return m.selectedIntent()
		}
	case ActivateSelectionMsg:
		return m.selectedIntent()
	case LoadNextPageMsg:
		page, ok := m.pages[value.Identity]
		if !ok || page.NextCursor == "" {
			return nil
		}
		return m.request(m.identity(string(value.Identity.Parent), page.NextCursor))
	}
	return nil
}

func (m *Model) acceptPage(result PageResult) []Intent {
	identity := result.Request.Identity
	if result.Request.Token == 0 || m.pending[identity] != result.Request.Token || identity.SnapshotRevision != m.snapshotRevision || identity.Filter != m.filter {
		return nil
	}
	expectedQuery, ok := queryForIdentity(identity)
	if !ok || !sameTreeQuery(result.Request.Query, expectedQuery) {
		delete(m.pending, identity)
		return nil
	}
	if identity.Parent != "" && !m.expanded[identity.Parent] {
		delete(m.pending, identity)
		return nil
	}
	if result.Page.Validate() != nil {
		delete(m.pending, identity)
		m.lastError = "invalid tree page"
		return nil
	}
	delete(m.pending, identity)
	rows := make([]TreeRow, 0, len(result.Page.Entries))
	for _, entry := range result.Page.Entries {
		if entry.Parent.Key() != identity.Parent {
			m.lastError = "invalid tree page"
			return nil
		}
		rows = append(rows, rowFromEntry(entry, m.threadBadges[entry.Path.Key()]))
	}
	m.pages[identity] = pageState{Request: clonePageRequest(result.Request), Rows: rows, NextCursor: result.Page.NextCursor}
	m.touchPage(identity)
	m.reposition()
	return nil
}

func (m *Model) acceptError(request PageRequest, err error) {
	if m.pending[request.Identity] != request.Token {
		return
	}
	delete(m.pending, request.Identity)
	if err != nil {
		m.lastError = "tree page request failed"
	}
}

func sameTreeQuery(left, right app.TreeQuery) bool {
	if left.Filter != right.Filter || left.Cursor != right.Cursor || left.Limit != right.Limit {
		return false
	}
	if left.ParentPath == nil || right.ParentPath == nil {
		return left.ParentPath == nil && right.ParentPath == nil
	}
	return string(left.ParentPath.Bytes()) == string(right.ParentPath.Bytes())
}

func clonePageRequest(request PageRequest) PageRequest {
	result := request
	if request.Query.ParentPath != nil {
		parent := repository.RepoPath(request.Query.ParentPath.Bytes())
		result.Query.ParentPath = &parent
	}
	return result
}
