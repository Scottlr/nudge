package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"

	"github.com/Scottlr/nudge/internal/app"
	"github.com/Scottlr/nudge/internal/domain"
	"github.com/Scottlr/nudge/internal/workspace"
)

var _ workspace.WorkspaceStore = (*Store)(nil)
var _ workspace.WorkspaceStoreTx = (*transaction)(nil)

func (t *transaction) CreateWorkspaceCreation(ctx context.Context, evidence workspace.WorkspaceCreationEvidence) error {
	if evidence.Validate() != nil || evidence.Phase != workspace.WorkspaceCreating && evidence.Phase != workspace.WorkspacePlanned || evidence.WorkspaceID == "" {
		return app.ErrReviewStoreInput
	}
	if err := t.verifyWorkspaceSession(ctx, evidence); err != nil {
		return err
	}
	data, err := json.Marshal(evidence)
	if err != nil {
		return err
	}
	_, err = t.tx.ExecContext(ctx, `INSERT INTO proposal_workspace_creation(
		workspace_id, operation_id, nonce, phase, marker_version, isolation_version,
		marker_sha256, evidence_json, created_at, updated_at)
		VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, evidence.WorkspaceID, evidence.OperationID,
		evidence.Nonce, string(evidence.Phase), evidence.MarkerVersion, evidence.IsolationVersion,
		evidence.MarkerSHA256, data, formatTime(evidence.CreatedAt), formatTime(evidence.UpdatedAt))
	return err
}

func (t *transaction) UpdateWorkspaceCreation(ctx context.Context, evidence workspace.WorkspaceCreationEvidence) error {
	if evidence.Validate() != nil || evidence.WorkspaceID == "" {
		return app.ErrReviewStoreInput
	}
	if err := t.verifyWorkspaceSession(ctx, evidence); err != nil {
		return err
	}
	var existingData []byte
	if err := t.tx.QueryRowContext(ctx, "SELECT evidence_json FROM proposal_workspace_creation WHERE workspace_id = ?", evidence.WorkspaceID).Scan(&existingData); err != nil {
		return mapNotFound(err)
	}
	var existing workspace.WorkspaceCreationEvidence
	if err := json.Unmarshal(existingData, &existing); err != nil || existing.OperationID != evidence.OperationID || existing.Nonce != evidence.Nonce || !existing.Phase.CanTransitionTo(evidence.Phase) {
		return app.ErrSessionRevisionConflict
	}
	data, err := json.Marshal(evidence)
	if err != nil {
		return err
	}
	result, err := t.tx.ExecContext(ctx, `UPDATE proposal_workspace_creation SET
		operation_id = ?, nonce = ?, phase = ?, marker_version = ?, isolation_version = ?,
		marker_sha256 = ?, evidence_json = ?, updated_at = ? WHERE workspace_id = ?`,
		evidence.OperationID, evidence.Nonce, string(evidence.Phase), evidence.MarkerVersion,
		evidence.IsolationVersion, evidence.MarkerSHA256, data, formatTime(evidence.UpdatedAt), evidence.WorkspaceID)
	if err != nil {
		return err
	}
	count, _ := result.RowsAffected()
	if count != 1 {
		return app.ErrReviewStoreNotFound
	}
	return nil
}

func (t *transaction) verifyWorkspaceSession(ctx context.Context, evidence workspace.WorkspaceCreationEvidence) error {
	var sessionID string
	if err := t.tx.QueryRowContext(ctx, `SELECT w.session_id FROM proposal_workspaces w WHERE w.id = ?`, evidence.WorkspaceID).Scan(&sessionID); err != nil {
		return mapNotFound(err)
	}
	if sessionID != string(t.sessionID) {
		return app.ErrReviewStoreInput
	}
	return nil
}

func (s *Store) LoadWorkspaceCreation(ctx context.Context, workspaceID domain.WorkspaceID) (workspace.WorkspaceCreationEvidence, error) {
	if err := s.ensureOpen(); err != nil {
		return workspace.WorkspaceCreationEvidence{}, err
	}
	var data []byte
	if err := s.db.QueryRowContext(ctx, "SELECT evidence_json FROM proposal_workspace_creation WHERE workspace_id = ?", workspaceID).Scan(&data); err != nil {
		if err == sql.ErrNoRows {
			return workspace.WorkspaceCreationEvidence{}, app.ErrReviewStoreNotFound
		}
		return workspace.WorkspaceCreationEvidence{}, err
	}
	var evidence workspace.WorkspaceCreationEvidence
	if err := json.Unmarshal(data, &evidence); err != nil || evidence.Validate() != nil {
		return workspace.WorkspaceCreationEvidence{}, app.ErrReviewStoreCorrupt
	}
	return evidence, nil
}
