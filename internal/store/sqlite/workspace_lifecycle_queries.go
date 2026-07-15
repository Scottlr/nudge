package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"

	"github.com/Scottlr/nudge/internal/app"
	"github.com/Scottlr/nudge/internal/domain"
	"github.com/Scottlr/nudge/internal/domain/review"
)

var _ app.ProposalWorkspaceLifecycleStore = (*Store)(nil)
var _ app.ProposalWorkspaceLifecycleStoreTx = (*transaction)(nil)

func (t *transaction) CreateProposalWorkspaceLifecycle(ctx context.Context, lifecycle app.ProposalWorkspaceLifecycle) error {
	if lifecycle.Validate() != nil {
		return app.ErrReviewStoreInput
	}
	if err := t.verifyLifecycleWorkspace(ctx, lifecycle); err != nil {
		return err
	}
	data, err := json.Marshal(lifecycle)
	if err != nil {
		return err
	}
	_, err = t.tx.ExecContext(ctx, `INSERT INTO proposal_workspace_lifecycle(
		operation_id, workspace_id, owner, nonce, purpose, phase,
		capacity_reservation_marker, lifecycle_json, created_at, updated_at)
		VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, lifecycle.OperationID, lifecycle.WorkspaceID,
		lifecycle.Owner, lifecycle.Nonce, string(lifecycle.Purpose), string(lifecycle.Phase),
		lifecycle.CapacityReservationMarker, data, formatTime(lifecycle.CreatedAt), formatTime(lifecycle.UpdatedAt))
	return err
}

func (t *transaction) UpdateProposalWorkspaceLifecycle(ctx context.Context, lifecycle app.ProposalWorkspaceLifecycle) error {
	if lifecycle.Validate() != nil {
		return app.ErrReviewStoreInput
	}
	if err := t.verifyLifecycleWorkspace(ctx, lifecycle); err != nil {
		return err
	}
	var existingData []byte
	if err := t.tx.QueryRowContext(ctx, "SELECT lifecycle_json FROM proposal_workspace_lifecycle WHERE operation_id = ?", lifecycle.OperationID).Scan(&existingData); err != nil {
		return mapNotFound(err)
	}
	var existing app.ProposalWorkspaceLifecycle
	if err := json.Unmarshal(existingData, &existing); err != nil || existing.WorkspaceID != lifecycle.WorkspaceID || existing.Owner != lifecycle.Owner || existing.Nonce != lifecycle.Nonce || existing.Purpose != lifecycle.Purpose || existing.CapacityReservationMarker != lifecycle.CapacityReservationMarker || !existing.Phase.CanTransitionTo(lifecycle.Phase) {
		return app.ErrProposalWorkspaceLifecycleConflict
	}
	data, err := json.Marshal(lifecycle)
	if err != nil {
		return err
	}
	result, err := t.tx.ExecContext(ctx, `UPDATE proposal_workspace_lifecycle SET
		phase = ?, capacity_reservation_marker = ?, lifecycle_json = ?, updated_at = ?
		WHERE operation_id = ? AND workspace_id = ?`, string(lifecycle.Phase), lifecycle.CapacityReservationMarker,
		data, formatTime(lifecycle.UpdatedAt), lifecycle.OperationID, lifecycle.WorkspaceID)
	if err != nil {
		return err
	}
	count, _ := result.RowsAffected()
	if count != 1 {
		return app.ErrReviewStoreNotFound
	}
	return nil
}

func (t *transaction) UpdateProposalWorkspace(ctx context.Context, value review.ProposalWorkspace) error {
	if value.Validate() != nil || value.SessionID != t.sessionID {
		return app.ErrReviewStoreInput
	}
	var existingData []byte
	if err := t.tx.QueryRowContext(ctx, "SELECT workspace_json FROM proposal_workspaces WHERE id = ?", value.ID).Scan(&existingData); err != nil {
		return mapNotFound(err)
	}
	var existing review.ProposalWorkspace
	if err := json.Unmarshal(existingData, &existing); err != nil || existing.Validate() != nil || existing.RepositoryID != value.RepositoryID || existing.WorktreeID != value.WorktreeID || existing.SessionID != value.SessionID || existing.SourceThreadID != value.SourceThreadID || !existing.State.CanTransitionTo(value.State) {
		return app.ErrProposalWorkspaceLifecycleConflict
	}
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	result, err := t.tx.ExecContext(ctx, `UPDATE proposal_workspaces SET state = ?, workspace_json = ?, updated_at = ? WHERE id = ? AND session_id = ?`, string(value.State), data, formatTime(value.UpdatedAt), value.ID, value.SessionID)
	if err != nil {
		return err
	}
	count, _ := result.RowsAffected()
	if count != 1 {
		return app.ErrReviewStoreNotFound
	}
	return nil
}

func (t *transaction) verifyLifecycleWorkspace(ctx context.Context, lifecycle app.ProposalWorkspaceLifecycle) error {
	var repositoryID, worktreeID, sessionID, threadID string
	if err := t.tx.QueryRowContext(ctx, `SELECT repository_id, worktree_id, session_id, source_thread_id FROM proposal_workspaces WHERE id = ?`, lifecycle.WorkspaceID).Scan(&repositoryID, &worktreeID, &sessionID, &threadID); err != nil {
		return mapNotFound(err)
	}
	if repositoryID != string(lifecycle.RepositoryID) || worktreeID != string(lifecycle.WorktreeID) || sessionID != string(lifecycle.SessionID) || threadID != string(lifecycle.ThreadID) || sessionID != string(t.sessionID) {
		return app.ErrReviewStoreInput
	}
	return nil
}

func (s *Store) LoadProposalWorkspaceLifecycle(ctx context.Context, workspaceID domain.WorkspaceID, operationID domain.OperationID) (app.ProposalWorkspaceLifecycle, error) {
	if err := s.ensureOpen(); err != nil {
		return app.ProposalWorkspaceLifecycle{}, err
	}
	var data []byte
	if err := s.db.QueryRowContext(ctx, "SELECT lifecycle_json FROM proposal_workspace_lifecycle WHERE workspace_id = ? AND operation_id = ?", workspaceID, operationID).Scan(&data); err != nil {
		if err == sql.ErrNoRows {
			return app.ProposalWorkspaceLifecycle{}, app.ErrReviewStoreNotFound
		}
		return app.ProposalWorkspaceLifecycle{}, err
	}
	return decodeProposalWorkspaceLifecycle(data)
}

func (s *Store) LoadLatestProposalWorkspaceLifecycle(ctx context.Context, workspaceID domain.WorkspaceID) (app.ProposalWorkspaceLifecycle, error) {
	if err := s.ensureOpen(); err != nil {
		return app.ProposalWorkspaceLifecycle{}, err
	}
	var data []byte
	if err := s.db.QueryRowContext(ctx, "SELECT lifecycle_json FROM proposal_workspace_lifecycle WHERE workspace_id = ? ORDER BY updated_at DESC, operation_id DESC LIMIT 1", workspaceID).Scan(&data); err != nil {
		if err == sql.ErrNoRows {
			return app.ProposalWorkspaceLifecycle{}, app.ErrReviewStoreNotFound
		}
		return app.ProposalWorkspaceLifecycle{}, err
	}
	return decodeProposalWorkspaceLifecycle(data)
}

func decodeProposalWorkspaceLifecycle(data []byte) (app.ProposalWorkspaceLifecycle, error) {
	var lifecycle app.ProposalWorkspaceLifecycle
	if err := json.Unmarshal(data, &lifecycle); err != nil || lifecycle.Validate() != nil {
		return app.ProposalWorkspaceLifecycle{}, app.ErrReviewStoreCorrupt
	}
	return lifecycle, nil
}
