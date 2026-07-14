package app

import (
	"context"
	"errors"
	"unicode"
	"unicode/utf8"

	"github.com/Scottlr/nudge/internal/domain/repository"
)

var (
	// ErrInvalidTreeQuery reports an invalid or contradictory tree-page request.
	ErrInvalidTreeQuery = errors.New("invalid tree query")
	// ErrInvalidTreePage reports a page that cannot be used as a projection.
	ErrInvalidTreePage = errors.New("invalid tree page")
)

// TreeFilter selects the repository tree projection.
type TreeFilter string

const (
	// TreeFilterAll includes tracked snapshot entries and local untracked entries.
	TreeFilterAll TreeFilter = "all"
	// TreeFilterChanged includes changed entries and their synthetic ancestors.
	TreeFilterChanged TreeFilter = "changed"
)

// TreeQuery requests one bounded, immediate-child repository page.
type TreeQuery struct {
	ParentPath *repository.RepoPath
	Filter     TreeFilter
	Cursor     string
	Limit      int
}

// Normalize applies the product page defaults and returns defensive path data.
func (q TreeQuery) Normalize(policy ResourcePolicy) (TreeQuery, error) {
	if policy == (ResourcePolicy{}) {
		policy = DefaultResourcePolicy()
	}
	if policy.TreePage.Default == 0 || policy.TreePage.Hard == 0 || policy.TreePage.Default > policy.TreePage.Hard {
		return TreeQuery{}, ErrInvalidTreeQuery
	}
	if q.Filter == "" {
		q.Filter = TreeFilterAll
	}
	if q.Filter != TreeFilterAll && q.Filter != TreeFilterChanged {
		return TreeQuery{}, ErrInvalidTreeQuery
	}
	if q.ParentPath != nil {
		if q.ParentPath.Validate() != nil {
			return TreeQuery{}, ErrInvalidTreeQuery
		}
		parent := repository.RepoPath(q.ParentPath.Bytes())
		q.ParentPath = &parent
	}
	if q.Cursor != "" && !safeTreeCursor(q.Cursor) {
		return TreeQuery{}, ErrInvalidTreeQuery
	}
	if q.Limit == 0 {
		q.Limit = int(policy.TreePage.Default)
	}
	if q.Limit < 1 || q.Limit > int(policy.TreePage.Hard) {
		return TreeQuery{}, ErrInvalidTreeQuery
	}
	return q, nil
}

// TreePage is one immutable page of immediate tree children.
type TreePage struct {
	Entries    []repository.TreeEntry
	NextCursor string
	Snapshot   repository.SnapshotRef
}

// Validate checks page identity, entry metadata, and cursor shape.
func (p TreePage) Validate() error {
	if p.Snapshot.Validate() != nil || (p.NextCursor != "" && !safeTreeCursor(p.NextCursor)) {
		return ErrInvalidTreePage
	}
	previous := repository.RepoPathKey("")
	for index, entry := range p.Entries {
		if entry.Validate() != nil {
			return ErrInvalidTreePage
		}
		if index > 0 && string(entry.Path) <= string(previous) {
			return ErrInvalidTreePage
		}
		previous = entry.Path.Key()
	}
	return nil
}

// Clone returns a page safe for cache and projection ownership transfer.
func (p TreePage) Clone() TreePage {
	result := p
	result.Entries = make([]repository.TreeEntry, len(p.Entries))
	for index, entry := range p.Entries {
		result.Entries[index] = cloneTreeEntry(entry)
	}
	return result
}

func cloneTreeEntry(entry repository.TreeEntry) repository.TreeEntry {
	entry.Path = repository.RepoPath(entry.Path.Bytes())
	entry.Name = repository.RepoPath(entry.Name.Bytes())
	entry.Parent = repository.RepoPath(entry.Parent.Bytes())
	if entry.ObjectID != nil {
		objectID := *entry.ObjectID
		entry.ObjectID = &objectID
	}
	if entry.ChangedSummary != nil {
		change := *entry.ChangedSummary
		change.OldPath = cloneRepoPath(change.OldPath)
		change.NewPath = cloneRepoPath(change.NewPath)
		if change.OldObjectID != nil {
			oldID := *change.OldObjectID
			change.OldObjectID = &oldID
		}
		if change.NewObjectID != nil {
			newID := *change.NewObjectID
			change.NewObjectID = &newID
		}
		if change.Conflict != nil {
			conflict := *change.Conflict
			conflict.Stage1 = cloneIndexStage(conflict.Stage1)
			conflict.Stage2 = cloneIndexStage(conflict.Stage2)
			conflict.Stage3 = cloneIndexStage(conflict.Stage3)
			change.Conflict = &conflict
		}
		entry.ChangedSummary = &change
	}
	return entry
}

func cloneIndexStage(stage *repository.IndexStage) *repository.IndexStage {
	if stage == nil {
		return nil
	}
	copyStage := *stage
	return &copyStage
}

// TreeReader is the application-owned boundary for bounded repository tree
// queries. Implementations must not load file bytes.
type TreeReader interface {
	ListTree(context.Context, repository.ResolvedTarget, TreeQuery) (TreePage, error)
}

func safeTreeCursor(value string) bool {
	if !utf8.ValidString(value) {
		return false
	}
	for _, char := range value {
		if unicode.IsControl(char) {
			return false
		}
	}
	return true
}
