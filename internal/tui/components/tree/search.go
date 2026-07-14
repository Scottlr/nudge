package tree

import (
	"github.com/Scottlr/nudge/internal/app"
	"github.com/Scottlr/nudge/internal/domain/repository"
)

// SetSearchSnapshotMsg binds future searches to one immutable repository
// snapshot. A target refresh retires an active search rather than mixing rows.
type SetSearchSnapshotMsg struct {
	Snapshot repository.SnapshotRef
}

// SetSearchQueryMsg starts a new repository-wide search or clears search mode.
type SetSearchQueryMsg struct {
	Query string
}

// SearchResultMsg carries a complete bounded repository-search page.
type SearchResultMsg struct {
	Result SearchResult
}

// SearchErrorMsg retires one pending search without admitting partial rows.
type SearchErrorMsg struct {
	Request SearchRequest
	Err     error
}

// LoadNextSearchPageMsg requests the next bounded page for the active query.
type LoadNextSearchPageMsg struct{}

// ExitSearchMsg returns to the retained hierarchy selection.
type ExitSearchMsg struct{}

func (m *Model) setSearchSnapshot(snapshot repository.SnapshotRef) {
	if m == nil || snapshot.Validate() != nil {
		return
	}
	if !sameSearchSnapshot(m.searchSnapshot, snapshot) && m.searching {
		m.exitSearch()
	}
	m.searchSnapshot = snapshot
}

func (m *Model) setSearchQuery(query string) []Intent {
	if m == nil {
		return nil
	}
	if query == "" {
		m.exitSearch()
		return nil
	}
	if m.searchSnapshot.Validate() != nil {
		m.lastError = "tree search snapshot unavailable"
		return nil
	}
	m.hierarchySelected = m.selected
	m.searching = true
	m.searchQuery = query
	m.searchMatches = nil
	m.searchCursor = ""
	m.nextToken++
	request := SearchRequest{Query: app.SearchTreeQuery{Snapshot: m.searchSnapshot, Query: query, Limit: defaultSearchLimit}, Token: m.nextToken}
	m.searchPending = request.Token
	return []Intent{{Search: &request}}
}

func (m *Model) acceptSearch(result SearchResult) {
	if m == nil || !m.searching || result.Request.Token == 0 || result.Request.Token != m.searchPending || result.Request.Query.Query != m.searchQuery || !sameSearchSnapshot(result.Request.Query.Snapshot, m.searchSnapshot) || app.ValidateSearchTreeResult(result.Request.Query, result.Page, app.DefaultResourcePolicy()) != nil {
		return
	}
	m.searchPending = 0
	page := result.Page.Clone()
	for _, match := range page.Matches {
		if len(m.searchMatches) >= maxSearchResults {
			break
		}
		m.searchMatches = append(m.searchMatches, match)
	}
	m.searchCursor = page.NextCursor
	if len(m.searchMatches) > 0 && !m.hasSearchMatch(m.selected) {
		m.selected = m.searchMatches[0].Entry.Path.Key()
	}
	m.reposition()
}

func (m *Model) searchError(request SearchRequest) {
	if m == nil || request.Token != m.searchPending || request.Query.Query != m.searchQuery {
		return
	}
	m.searchPending = 0
	m.lastError = "tree search request failed"
}

func (m *Model) loadNextSearchPage() []Intent {
	if m == nil || !m.searching || m.searchPending != 0 || m.searchCursor == "" || len(m.searchMatches) >= maxSearchResults {
		return nil
	}
	m.nextToken++
	request := SearchRequest{Query: app.SearchTreeQuery{Snapshot: m.searchSnapshot, Query: m.searchQuery, Cursor: m.searchCursor, Limit: defaultSearchLimit}, Token: m.nextToken}
	m.searchPending = request.Token
	return []Intent{{Search: &request}}
}

func (m *Model) exitSearch() {
	if m == nil {
		return
	}
	m.searching = false
	m.searchQuery = ""
	m.searchMatches = nil
	m.searchCursor = ""
	m.searchPending = 0
	if m.hierarchySelected != "" && m.findRow(m.hierarchySelected) {
		m.selected = m.hierarchySelected
	}
	m.hierarchySelected = ""
	m.reposition()
}

func (m *Model) hasSearchMatch(path repository.RepoPathKey) bool {
	for _, match := range m.searchMatches {
		if match.Entry.Path.Key() == path {
			return true
		}
	}
	return false
}

func sameSearchSnapshot(left, right repository.SnapshotRef) bool {
	return left.Kind == right.Kind && left.ObjectID == right.ObjectID && left.WorktreeID == right.WorktreeID && left.Fingerprint == right.Fingerprint
}
