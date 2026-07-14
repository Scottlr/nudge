package tree

import (
	"github.com/Scottlr/nudge/internal/app"
	"github.com/Scottlr/nudge/internal/domain/repository"
)

// FilterMode is the repository pane's two bounded hierarchy projections.
type FilterMode string

const (
	FilterChanged FilterMode = "changed"
	FilterAll     FilterMode = "all"
)

func (f FilterMode) treeFilter() app.TreeFilter {
	if f == FilterAll {
		return app.TreeFilterAll
	}
	return app.TreeFilterChanged
}

// PageIdentity binds one immediate-child page to the complete snapshot and
// query shape that produced it.
type PageIdentity struct {
	SnapshotRevision uint64
	Parent           repository.RepoPathKey
	Filter           FilterMode
	Cursor           string
	Limit            int
}

// PageRequest is an inert query intent for the root/application boundary.
type PageRequest struct {
	Identity PageIdentity
	Token    uint64
	Query    app.TreeQuery
}

// PageResult is a page response that must echo the exact request identity and
// token before the pane can adopt it.
type PageResult struct {
	Request PageRequest
	Page    app.TreePage
}

// SearchRequest is an inert repository-wide path-search intent. The query is
// bound to one immutable snapshot and never derives its corpus from loaded
// hierarchy rows.
type SearchRequest struct {
	Query app.SearchTreeQuery
	Token uint64
}

// SearchResult carries one bounded immutable search page.
type SearchResult struct {
	Request SearchRequest
	Page    app.SearchTreePage
}

// SelectPathIntent identifies a selected raw repository path without using
// display text as identity.
type SelectPathIntent struct {
	Path repository.RepoPathKey
}

// Intent is emitted by the component for root-owned query or selection work.
type Intent struct {
	PageRequest *PageRequest
	Search      *SearchRequest
	SelectPath  *SelectPathIntent
}

// ThreadBadge is a bounded projection supplied by the later thread owner.
type ThreadBadge struct {
	Count  int
	Status string
}

// TreeRow is inert display metadata derived from one immutable tree entry.
type TreeRow struct {
	Path         repository.RepoPath
	Name         repository.RepoPath
	Kind         repository.FileKind
	Depth        int
	LazyChild    bool
	Change       repository.ChangeKind
	Staged       bool
	Unstaged     bool
	Conflict     bool
	ThreadCount  int
	ThreadStatus string
	Loading      bool
}

func rowFromEntry(entry repository.TreeEntry, badge ThreadBadge) TreeRow {
	row := TreeRow{
		Path:         repository.RepoPath(entry.Path.Bytes()),
		Name:         repository.RepoPath(entry.Name.Bytes()),
		Kind:         entry.Kind,
		LazyChild:    entry.LazyChild,
		ThreadCount:  maxInt(badge.Count, 0),
		ThreadStatus: badge.Status,
	}
	if entry.ChangedSummary != nil {
		row.Change = entry.ChangedSummary.Kind
		row.Staged = entry.ChangedSummary.Staged
		row.Unstaged = entry.ChangedSummary.Unstaged
		row.Conflict = entry.ChangedSummary.Conflict != nil
	}
	return row
}

func cloneRow(row TreeRow) TreeRow {
	row.Path = repository.RepoPath(row.Path.Bytes())
	row.Name = repository.RepoPath(row.Name.Bytes())
	return row
}

func maxInt(left, right int) int {
	if left > right {
		return left
	}
	return right
}
