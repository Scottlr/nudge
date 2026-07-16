package app

import (
	"context"
	"errors"
	"time"

	"github.com/Scottlr/nudge/internal/domain"
)

var ErrWorkspaceRetentionCursorNotFound = errors.New("workspace retention cursor not found")

// WorkspaceRetentionPage is a stable keyset query over workspace IDs.
type WorkspaceRetentionPage struct {
	Limit   uint32
	AfterID domain.WorkspaceID
}

// Validate enforces the T070 candidate bound.
func (p *WorkspaceRetentionPage) Validate() error {
	if p == nil {
		return ErrInvalidWorkspaceRetentionPolicy
	}
	if p.Limit == 0 {
		p.Limit = DefaultWorkspaceRetentionCandidatePage
	}
	if p.Limit > MaxWorkspaceRetentionCandidatePage {
		return ErrInvalidWorkspaceRetentionPolicy
	}
	return nil
}

// WorkspaceRetentionPageResult contains one bounded candidate page and a
// stable continuation cursor.
type WorkspaceRetentionPageResult struct {
	Candidates []WorkspaceRetentionCandidate
	NextID     domain.WorkspaceID
	HasMore    bool
}

// WorkspaceRetentionCursor is the durable continuation for the bounded
// reaper. An empty AfterID means the pass is complete or not yet started.
type WorkspaceRetentionCursor struct {
	AfterID   domain.WorkspaceID
	UpdatedAt time.Time
}

func (c WorkspaceRetentionCursor) Validate() error {
	if c.UpdatedAt.IsZero() {
		return ErrInvalidWorkspaceRetentionPolicy
	}
	return nil
}

// WorkspaceRetentionCursorStore persists the reaper continuation separately
// from workspace history so a crash cannot rewind an unbounded scan.
type WorkspaceRetentionCursorStore interface {
	LoadWorkspaceRetentionCursor(context.Context) (WorkspaceRetentionCursor, error)
	SaveWorkspaceRetentionCursor(context.Context, WorkspaceRetentionCursor) error
}
