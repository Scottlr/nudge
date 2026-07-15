package sqlite

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/Scottlr/nudge/internal/app"
	"github.com/Scottlr/nudge/internal/domain"
	"github.com/Scottlr/nudge/internal/domain/review"
)

var _ app.ResultSnapshotStore = (*Store)(nil)
var _ app.ResultSnapshotStoreTx = (*transaction)(nil)

// AdoptResultSnapshot durably adopts one complete independent result
// manifest/delta pair. A non-ready snapshot remains useful evidence, but an
// incomplete traversal is never persisted as a restartable result.
func (t *transaction) AdoptResultSnapshot(ctx context.Context, snapshot app.ResultSnapshot) error {
	if snapshot.Validate() != nil || snapshot.SessionID != t.sessionID || !snapshot.Manifest.Complete || !snapshot.Delta.Complete {
		return app.ErrInvalidResultSnapshot
	}
	if err := t.checkProposalOwnership(ctx, snapshot.ProposalID, snapshot.WorkspaceID, snapshot.ThreadID); err != nil {
		return err
	}
	var attemptProposal, attemptWorkspace, attemptThread string
	var attemptJSON []byte
	if err := t.tx.QueryRowContext(ctx, `SELECT proposal_id, workspace_id, thread_id, attempt_json FROM proposal_attempts WHERE id = ?`, snapshot.AttemptID).Scan(&attemptProposal, &attemptWorkspace, &attemptThread, &attemptJSON); err != nil {
		return mapResultSnapshotNotFound(err)
	}
	if attemptProposal != string(snapshot.ProposalID) || attemptWorkspace != string(snapshot.WorkspaceID) || attemptThread != string(snapshot.ThreadID) {
		return app.ErrInvalidResultSnapshot
	}
	var attempt review.ProposalAttempt
	if json.Unmarshal(attemptJSON, &attempt) != nil || attempt.Validate() != nil {
		return app.ErrReviewStoreCorrupt
	}
	data, err := json.Marshal(snapshot)
	if err != nil {
		return err
	}
	var existing []byte
	err = t.tx.QueryRowContext(ctx, "SELECT snapshot_json FROM proposal_result_snapshots WHERE attempt_id = ?", snapshot.AttemptID).Scan(&existing)
	switch {
	case err == nil:
		var value app.ResultSnapshot
		if json.Unmarshal(existing, &value) != nil || value.Validate() != nil {
			return app.ErrReviewStoreCorrupt
		}
		if value.ID != snapshot.ID || !bytes.Equal(existing, data) {
			return app.ErrResultSnapshotConflict
		}
		return nil
	case !errors.Is(err, sql.ErrNoRows):
		return err
	}
	if attempt.Outcome != review.ProposalAttemptDeriving || attempt.Result != nil || attempt.VersionNumber != nil {
		return app.ErrResultSnapshotConflict
	}
	_, err = t.tx.ExecContext(ctx, `INSERT INTO proposal_result_snapshots(
		id, session_id, proposal_id, workspace_id, attempt_id, thread_id,
		manifest_hash, snapshot_json, created_at)
		VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?)`, snapshot.ID, snapshot.SessionID,
		snapshot.ProposalID, snapshot.WorkspaceID, snapshot.AttemptID, snapshot.ThreadID,
		snapshot.Manifest.Hash, data, formatTime(snapshot.CreatedAt))
	if err != nil {
		return err
	}
	return nil
}

func (s *Store) LoadResultSnapshot(ctx context.Context, snapshotID domain.ReviewSnapshotID) (app.ResultSnapshot, error) {
	if err := s.ensureOpen(); err != nil {
		return app.ResultSnapshot{}, err
	}
	var data []byte
	if err := s.db.QueryRowContext(ctx, "SELECT snapshot_json FROM proposal_result_snapshots WHERE id = ?", snapshotID).Scan(&data); err != nil {
		return app.ResultSnapshot{}, mapResultSnapshotNotFound(err)
	}
	return decodeResultSnapshot(data)
}

func (s *Store) LoadResultSnapshotForAttempt(ctx context.Context, attemptID domain.OperationID) (app.ResultSnapshot, error) {
	if err := s.ensureOpen(); err != nil {
		return app.ResultSnapshot{}, err
	}
	var data []byte
	if err := s.db.QueryRowContext(ctx, "SELECT snapshot_json FROM proposal_result_snapshots WHERE attempt_id = ?", attemptID).Scan(&data); err != nil {
		return app.ResultSnapshot{}, mapResultSnapshotNotFound(err)
	}
	return decodeResultSnapshot(data)
}

func decodeResultSnapshot(data []byte) (app.ResultSnapshot, error) {
	var snapshot app.ResultSnapshot
	if json.Unmarshal(data, &snapshot) != nil || snapshot.Validate() != nil {
		return app.ResultSnapshot{}, app.ErrReviewStoreCorrupt
	}
	return snapshot, nil
}

func mapResultSnapshotNotFound(err error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return app.ErrResultSnapshotNotFound
	}
	return err
}
