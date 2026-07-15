package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"reflect"
	"time"

	"github.com/Scottlr/nudge/internal/app"
	"github.com/Scottlr/nudge/internal/domain"
	"github.com/Scottlr/nudge/internal/domain/repository"
	"github.com/Scottlr/nudge/internal/domain/review"
)

var _ app.PostApplyReconciliationJournal = (*Store)(nil)
var _ app.ProposalValiditySource = (*Store)(nil)
var _ app.ProposalValidityApprovalGate = (*Store)(nil)
var _ app.PostApplyReconciliationStoreTx = (*transaction)(nil)

func (s *Store) Load(ctx context.Context, operationID domain.OperationID) (app.PostApplyReconciliationRecord, error) {
	if err := s.ensureOpen(); err != nil {
		return app.PostApplyReconciliationRecord{}, err
	}
	if operationID == "" {
		return app.PostApplyReconciliationRecord{}, app.ErrPostApplyReconciliationInvalid
	}
	var data []byte
	if err := s.db.QueryRowContext(ctx, "SELECT record_json FROM post_apply_reconciliations WHERE apply_operation_id = ?", operationID).Scan(&data); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return app.PostApplyReconciliationRecord{}, app.ErrReviewStoreNotFound
		}
		return app.PostApplyReconciliationRecord{}, err
	}
	var record app.PostApplyReconciliationRecord
	if err := json.Unmarshal(data, &record); err != nil || record.Validate() != nil {
		return app.PostApplyReconciliationRecord{}, app.ErrReviewStoreCorrupt
	}
	return record, nil
}

func (s *Store) Start(ctx context.Context, guard app.SessionWriteGuard, record app.PostApplyReconciliationRecord) (app.SessionWriteGuard, error) {
	return s.withPostApplyTx(ctx, guard, func(tx app.PostApplyReconciliationStoreTx) error {
		return tx.CreatePostApplyReconciliation(ctx, record)
	})
}

func (s *Store) RecordGeneration(ctx context.Context, guard app.SessionWriteGuard, record app.PostApplyReconciliationRecord) (app.SessionWriteGuard, error) {
	return s.withPostApplyTx(ctx, guard, func(tx app.PostApplyReconciliationStoreTx) error {
		return tx.UpdatePostApplyReconciliation(ctx, record)
	})
}

func (s *Store) StageValidity(ctx context.Context, guard app.SessionWriteGuard, record app.PostApplyReconciliationRecord, results []app.ProposalValidityResult) (app.SessionWriteGuard, error) {
	return s.withPostApplyTx(ctx, guard, func(tx app.PostApplyReconciliationStoreTx) error {
		for _, result := range results {
			if err := tx.StagePostApplyValidity(ctx, result); err != nil {
				return err
			}
		}
		return tx.UpdatePostApplyReconciliation(ctx, record)
	})
}

func (s *Store) CompleteValidity(ctx context.Context, guard app.SessionWriteGuard, record app.PostApplyReconciliationRecord, completedAt time.Time) (app.SessionWriteGuard, error) {
	return s.withPostApplyTx(ctx, guard, func(tx app.PostApplyReconciliationStoreTx) error {
		return tx.CompletePostApplyValidity(ctx, record, completedAt)
	})
}

func (s *Store) Complete(ctx context.Context, guard app.SessionWriteGuard, record app.PostApplyReconciliationRecord, completedAt time.Time) (app.SessionWriteGuard, error) {
	return s.withPostApplyTx(ctx, guard, func(tx app.PostApplyReconciliationStoreTx) error {
		return tx.CompletePostApplyReconciliation(ctx, record, completedAt)
	})
}

func (s *Store) Repair(ctx context.Context, guard app.SessionWriteGuard, record app.PostApplyReconciliationRecord, reason string, at time.Time) (app.SessionWriteGuard, error) {
	return s.withPostApplyTx(ctx, guard, func(tx app.PostApplyReconciliationStoreTx) error {
		return tx.RepairPostApplyReconciliation(ctx, record, reason, at)
	})
}

func (s *Store) withPostApplyTx(ctx context.Context, guard app.SessionWriteGuard, fn func(app.PostApplyReconciliationStoreTx) error) (app.SessionWriteGuard, error) {
	if s == nil || fn == nil {
		return guard, app.ErrPostApplyReconciliationUnavailable
	}
	return s.WithSessionTx(ctx, guard, func(tx app.ReviewStoreTx) error {
		journal, ok := tx.(app.PostApplyReconciliationStoreTx)
		if !ok {
			return app.ErrPostApplyReconciliationUnavailable
		}
		return fn(journal)
	})
}

func (t *transaction) CreatePostApplyReconciliation(ctx context.Context, record app.PostApplyReconciliationRecord) error {
	if record.Validate() != nil || record.SessionID != t.sessionID {
		return app.ErrPostApplyReconciliationInvalid
	}
	var proposalSession, proposalWorkspace string
	if err := t.tx.QueryRowContext(ctx, `SELECT w.session_id, p.workspace_id
		FROM proposals p JOIN proposal_workspaces w ON w.id = p.workspace_id WHERE p.id = ?`, record.ProposalID).Scan(&proposalSession, &proposalWorkspace); err != nil {
		return mapNotFound(err)
	}
	if proposalSession != string(record.SessionID) || proposalWorkspace != string(record.WorkspaceID) {
		return app.ErrReviewStoreInput
	}
	var operationSession sql.NullString
	if err := t.tx.QueryRowContext(ctx, "SELECT session_id FROM apply_operations WHERE id = ?", record.ApplyOperationID).Scan(&operationSession); err == nil {
		if operationSession.String != string(record.SessionID) {
			return app.ErrReviewStoreInput
		}
	} else if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	data, err := json.Marshal(record)
	if err != nil {
		return err
	}
	targetJSON, destinationJSON, err := marshalPostApplyEvidence(record)
	if err != nil {
		return err
	}
	var existing []byte
	if err := t.tx.QueryRowContext(ctx, "SELECT record_json FROM post_apply_reconciliations WHERE apply_operation_id = ?", record.ApplyOperationID).Scan(&existing); err == nil {
		var value app.PostApplyReconciliationRecord
		if json.Unmarshal(existing, &value) != nil || !reflect.DeepEqual(value, record) {
			return app.ErrPostApplyReconciliationConflict
		}
		return nil
	} else if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	_, err = t.tx.ExecContext(ctx, `INSERT INTO post_apply_reconciliations(
		apply_operation_id, session_id, workspace_id, proposal_id, previous_generation,
		new_generation, capture_id, manifest_hash, provenance, phase, validity_epoch,
		validity_cursor, processed_proposals, processed_preconditions, evidence_bytes,
		repair_reason, target_json, destination_json, record_json, started_at, updated_at)
		VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		record.ApplyOperationID, record.SessionID, record.WorkspaceID, record.ProposalID, record.PreviousGeneration,
		record.NewGeneration, string(record.CaptureID), record.ManifestHash, string(record.Provenance), string(record.Phase), record.ValidityEpoch,
		record.ValidityCursor, record.ProcessedProposals, record.ProcessedPreconditions, record.EvidenceBytes,
		record.RepairReason, targetJSON, destinationJSON, data, formatTime(record.StartedAt), formatTime(record.StartedAt))
	return err
}

func (t *transaction) UpdatePostApplyReconciliation(ctx context.Context, record app.PostApplyReconciliationRecord) error {
	if record.Validate() != nil || record.SessionID != t.sessionID {
		return app.ErrPostApplyReconciliationInvalid
	}
	data, err := json.Marshal(record)
	if err != nil {
		return err
	}
	targetJSON, destinationJSON, err := marshalPostApplyEvidence(record)
	if err != nil {
		return err
	}
	result, err := t.tx.ExecContext(ctx, `UPDATE post_apply_reconciliations SET
		new_generation = ?, capture_id = ?, manifest_hash = ?, provenance = ?, phase = ?, validity_epoch = ?,
		validity_cursor = ?, processed_proposals = ?, processed_preconditions = ?, evidence_bytes = ?, repair_reason = ?,
		target_json = ?, destination_json = ?, record_json = ?, updated_at = ?, completed_at = ?
		WHERE apply_operation_id = ? AND session_id = ?`,
		record.NewGeneration, string(record.CaptureID), record.ManifestHash, string(record.Provenance), string(record.Phase), record.ValidityEpoch,
		record.ValidityCursor, record.ProcessedProposals, record.ProcessedPreconditions, record.EvidenceBytes, record.RepairReason,
		targetJSON, destinationJSON, data, formatTime(record.StartedAt), nullableTime(record.CompletedAt), record.ApplyOperationID, record.SessionID)
	if err != nil {
		return err
	}
	count, _ := result.RowsAffected()
	if count != 1 {
		return app.ErrReviewStoreNotFound
	}
	if record.Phase == app.PostApplyPhaseValidityPending && record.NewGeneration != 0 {
		if _, err := t.tx.ExecContext(ctx, `INSERT INTO proposal_validity_epochs(session_id, worktree_id, target_kind, generation, apply_operation_id, epoch, state)
			VALUES(?, ?, ?, ?, ?, ?, 'pending')
			ON CONFLICT(session_id, worktree_id, target_kind) DO UPDATE SET generation = excluded.generation, apply_operation_id = excluded.apply_operation_id, epoch = excluded.epoch, state = 'pending'`,
			record.SessionID, record.Destination.WorktreeID, string(record.Destination.TargetKind), record.NewGeneration, record.ApplyOperationID, record.ValidityEpoch); err != nil {
			return err
		}
	}
	return nil
}

func (t *transaction) StagePostApplyValidity(ctx context.Context, result app.ProposalValidityResult) error {
	if result.Validate() != nil {
		return app.ErrPostApplyReconciliationInvalid
	}
	data, err := json.Marshal(result)
	if err != nil {
		return err
	}
	var sessionID string
	if err := t.tx.QueryRowContext(ctx, `SELECT w.session_id FROM proposals p JOIN proposal_workspaces w ON w.id = p.workspace_id WHERE p.id = ?`, result.ProposalID).Scan(&sessionID); err != nil {
		return mapNotFound(err)
	}
	if sessionID != string(t.sessionID) {
		return app.ErrReviewStoreInput
	}
	_, err = t.tx.ExecContext(ctx, `INSERT INTO proposal_validity_results(
		apply_operation_id, generation, proposal_id, version, source_status, outcome, reason, conflict_path, evidence_bytes, result_json)
		VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(apply_operation_id, proposal_id, version) DO UPDATE SET
		generation = excluded.generation, source_status = excluded.source_status, outcome = excluded.outcome,
		reason = excluded.reason, conflict_path = excluded.conflict_path, evidence_bytes = excluded.evidence_bytes, result_json = excluded.result_json`,
		result.ApplyOperationID, result.Generation, result.ProposalID, result.Version, string(result.ExpectedStatus), string(result.Outcome), string(result.Reason), nullableRepoPath(result.ConflictPath), result.EvidenceBytes, data)
	return err
}

func (t *transaction) CompletePostApplyValidity(ctx context.Context, record app.PostApplyReconciliationRecord, completedAt time.Time) error {
	if record.Validate() != nil || record.SessionID != t.sessionID || record.Phase != app.PostApplyPhaseBaselinePending || completedAt.IsZero() {
		return app.ErrPostApplyReconciliationInvalid
	}
	rows, err := t.tx.QueryContext(ctx, `SELECT result_json FROM proposal_validity_results WHERE apply_operation_id = ? AND generation = ? ORDER BY proposal_id ASC, version ASC`, record.ApplyOperationID, record.NewGeneration)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var data []byte
		if err := rows.Scan(&data); err != nil {
			return err
		}
		var result app.ProposalValidityResult
		if err := json.Unmarshal(data, &result); err != nil || result.Validate() != nil {
			return app.ErrReviewStoreCorrupt
		}
		if result.Outcome != app.ProposalValidityStale {
			continue
		}
		var currentStatus string
		if err := t.tx.QueryRowContext(ctx, "SELECT status FROM proposal_versions WHERE proposal_id = ? AND version = ?", result.ProposalID, result.Version).Scan(&currentStatus); err != nil {
			return mapNotFound(err)
		}
		if review.ProposalStatus(currentStatus) != result.ExpectedStatus {
			if review.ProposalStatus(currentStatus) == review.ProposalVersionStale || review.ProposalStatus(currentStatus) == review.ProposalVersionApplied || review.ProposalStatus(currentStatus) == review.ProposalVersionFailed {
				continue
			}
			return app.ErrPostApplyReconciliationConflict
		}
		if err := t.TransitionProposal(ctx, review.ProposalTransition{ProposalID: result.ProposalID, Version: result.Version, Status: review.ProposalVersionStale, FailurePhase: review.ProposalFailureDestination, Reason: string(result.Reason), ChangedAt: completedAt}); err != nil {
			return err
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if _, err := t.tx.ExecContext(ctx, `UPDATE proposal_validity_epochs SET state = 'complete'
		WHERE session_id = ? AND worktree_id = ? AND target_kind = ? AND generation = ? AND apply_operation_id = ? AND epoch = ?`,
		record.SessionID, record.Destination.WorktreeID, string(record.Destination.TargetKind), record.NewGeneration, record.ApplyOperationID, record.ValidityEpoch); err != nil {
		return err
	}
	return t.UpdatePostApplyReconciliation(ctx, record)
}

func (t *transaction) CompletePostApplyReconciliation(ctx context.Context, record app.PostApplyReconciliationRecord, completedAt time.Time) error {
	if record.Validate() != nil || record.SessionID != t.sessionID || record.Phase != app.PostApplyPhaseBaselinePending || completedAt.IsZero() {
		return app.ErrPostApplyReconciliationInvalid
	}
	record.Phase = app.PostApplyPhaseCompleted
	record.CompletedAt = &completedAt
	return t.UpdatePostApplyReconciliation(ctx, record)
}

func (t *transaction) RepairPostApplyReconciliation(ctx context.Context, record app.PostApplyReconciliationRecord, reason string, at time.Time) error {
	if record.Validate() != nil || record.SessionID != t.sessionID || reason == "" || at.IsZero() {
		return app.ErrPostApplyReconciliationInvalid
	}
	record.Phase = app.PostApplyPhaseRepairRequired
	record.RepairReason = reason
	return t.UpdatePostApplyReconciliation(ctx, record)
}

func (s *Store) PageProposalValidity(ctx context.Context, request app.ProposalValidityPageRequest) (app.ProposalValidityPage, error) {
	if err := s.ensureOpen(); err != nil {
		return app.ProposalValidityPage{}, err
	}
	if err := request.Validate(); err != nil {
		return app.ProposalValidityPage{}, err
	}
	cursorID, cursorVersion, err := decodeProposalValidityCursor(request.Cursor)
	if err != nil {
		return app.ProposalValidityPage{}, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT v.proposal_id, v.version, v.patch_json
		FROM proposal_versions v JOIN proposals p ON p.id = v.proposal_id
		JOIN proposal_workspaces w ON w.id = p.workspace_id
		WHERE w.session_id = ? AND w.worktree_id = ? AND (v.proposal_id > ? OR (v.proposal_id = ? AND v.version > ?))
		ORDER BY v.proposal_id ASC, v.version ASC LIMIT ?`, request.SessionID, request.WorktreeID, cursorID, cursorID, cursorVersion, int(request.Limit)+1)
	if err != nil {
		return app.ProposalValidityPage{}, err
	}
	defer rows.Close()
	type row struct {
		proposalID string
		version    int64
		data       []byte
	}
	var raw []row
	for rows.Next() {
		var value row
		if err := rows.Scan(&value.proposalID, &value.version, &value.data); err != nil {
			return app.ProposalValidityPage{}, err
		}
		raw = append(raw, value)
	}
	if err := rows.Err(); err != nil {
		return app.ProposalValidityPage{}, err
	}
	hasMore := len(raw) > int(request.Limit)
	if hasMore {
		raw = raw[:request.Limit]
	}
	page := app.ProposalValidityPage{Done: !hasMore}
	for _, value := range raw {
		var patch review.ProposedPatch
		if err := json.Unmarshal(value.data, &patch); err != nil || patch.Validate() != nil || patch.ProposalID != domain.ProposalID(value.proposalID) || patch.Version != review.ProposalVersionNumber(value.version) {
			return app.ProposalValidityPage{}, app.ErrReviewStoreCorrupt
		}
		page.EncodedBytes += app.ByteSize(len(value.data))
		if patch.Status != review.ProposalVersionDeriving && patch.Status != review.ProposalVersionReady && patch.Status != review.ProposalVersionApplying || patch.Destination.WorktreeID != request.WorktreeID || patch.Destination.TargetKind != request.TargetKind {
			continue
		}
		page.Items = append(page.Items, app.ProposalValidityCandidate{ProposalID: patch.ProposalID, Version: patch.Version, Status: patch.Status, Destination: patch.Destination, Preconditions: patch.Preconditions})
	}
	if hasMore {
		last := raw[len(raw)-1]
		page.NextCursor, err = encodeProposalValidityCursor(last.proposalID, last.version)
		if err != nil {
			return app.ProposalValidityPage{}, err
		}
	}
	return page, nil
}

func (s *Store) CheckProposalApprovalValidity(ctx context.Context, proposalID domain.ProposalID, version review.ProposalVersionNumber, destination review.DestinationConstraints) error {
	if err := s.ensureOpen(); err != nil {
		return err
	}
	if proposalID == "" || version == 0 || destination.Validate() != nil {
		return app.ErrPostApplyReconciliationInvalid
	}
	var sessionID string
	if err := s.db.QueryRowContext(ctx, `SELECT w.session_id FROM proposal_versions v JOIN proposals p ON p.id = v.proposal_id JOIN proposal_workspaces w ON w.id = p.workspace_id WHERE v.proposal_id = ? AND v.version = ?`, proposalID, version).Scan(&sessionID); err != nil {
		return mapNotFound(err)
	}
	var state string
	if err := s.db.QueryRowContext(ctx, `SELECT state FROM proposal_validity_epochs WHERE session_id = ? AND worktree_id = ? AND target_kind = ?`, sessionID, destination.WorktreeID, string(destination.TargetKind)).Scan(&state); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		return err
	}
	if state != "complete" {
		return app.ErrProposalValidityPending
	}
	return nil
}

func marshalPostApplyEvidence(record app.PostApplyReconciliationRecord) ([]byte, []byte, error) {
	var targetJSON, destinationJSON []byte
	if record.NewGeneration != 0 {
		var err error
		targetJSON, err = json.Marshal(record.Target)
		if err != nil {
			return nil, nil, err
		}
		destinationJSON, err = json.Marshal(record.Destination)
		if err != nil {
			return nil, nil, err
		}
	}
	return targetJSON, destinationJSON, nil
}

type proposalValidityCursor struct {
	ProposalID string `json:"proposal_id"`
	Version    int64  `json:"version"`
}

func encodeProposalValidityCursor(proposalID string, version int64) (string, error) {
	data, err := json.Marshal(proposalValidityCursor{ProposalID: proposalID, Version: version})
	return string(data), err
}

func decodeProposalValidityCursor(value string) (string, int64, error) {
	if value == "" {
		return "", 0, nil
	}
	var cursor proposalValidityCursor
	if json.Unmarshal([]byte(value), &cursor) != nil || cursor.ProposalID == "" || cursor.Version <= 0 {
		return "", 0, app.ErrPostApplyReconciliationInvalid
	}
	return cursor.ProposalID, cursor.Version, nil
}

func nullableRepoPath(path *repository.RepoPath) any {
	if path == nil {
		return nil
	}
	return path.Bytes()
}
