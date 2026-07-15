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

var _ app.ProposalPatchArtifactStore = (*Store)(nil)
var _ app.ProposalPatchArtifactStoreTx = (*transaction)(nil)

// AdoptProposalPatchArtifact records one complete patch/index pair after its
// published artifact target has been independently verified. The patch bytes
// remain in the owner-controlled artifact target; SQLite stores its identity
// and immutable review metadata as one adoption record.
func (t *transaction) AdoptProposalPatchArtifact(ctx context.Context, artifact app.ProposalPatchArtifact) error {
	if artifact.Validate() != nil || artifact.SessionID != t.sessionID {
		return app.ErrInvalidProposalPatchArtifact
	}
	if err := t.checkProposalOwnership(ctx, artifact.ProposalID, artifact.WorkspaceID, artifact.ThreadID); err != nil {
		return err
	}
	var attemptJSON []byte
	var attemptProposal, attemptWorkspace, attemptThread string
	if err := t.tx.QueryRowContext(ctx, `SELECT proposal_id, workspace_id, thread_id, attempt_json FROM proposal_attempts WHERE id = ?`, artifact.AttemptID).Scan(&attemptProposal, &attemptWorkspace, &attemptThread, &attemptJSON); err != nil {
		return mapProposalPatchArtifactNotFound(err)
	}
	if attemptProposal != string(artifact.ProposalID) || attemptWorkspace != string(artifact.WorkspaceID) || attemptThread != string(artifact.ThreadID) {
		return app.ErrInvalidProposalPatchArtifact
	}
	var attempt review.ProposalAttempt
	if json.Unmarshal(attemptJSON, &attempt) != nil || attempt.Validate() != nil {
		return app.ErrReviewStoreCorrupt
	}
	var snapshotJSON []byte
	if err := t.tx.QueryRowContext(ctx, "SELECT snapshot_json FROM proposal_result_snapshots WHERE attempt_id = ?", artifact.AttemptID).Scan(&snapshotJSON); err != nil {
		return mapProposalPatchArtifactNotFound(err)
	}
	var snapshot app.ResultSnapshot
	if json.Unmarshal(snapshotJSON, &snapshot) != nil || snapshot.Validate() != nil || snapshot.ID != artifact.ResultSnapshotID || snapshot.Baseline.ID != artifact.BaselineSnapshotID {
		return app.ErrReviewStoreCorrupt
	}
	data, err := json.Marshal(artifact)
	if err != nil {
		return err
	}
	var existing []byte
	err = t.tx.QueryRowContext(ctx, "SELECT artifact_json FROM proposal_patch_artifacts WHERE attempt_id = ?", artifact.AttemptID).Scan(&existing)
	switch {
	case err == nil:
		var value app.ProposalPatchArtifact
		if json.Unmarshal(existing, &value) != nil || value.Validate() != nil {
			return app.ErrReviewStoreCorrupt
		}
		if value.ID != artifact.ID || !bytes.Equal(existing, data) {
			return app.ErrProposalPatchArtifactConflict
		}
		return nil
	case !errors.Is(err, sql.ErrNoRows):
		return err
	}
	if attempt.Outcome != review.ProposalAttemptDeriving || attempt.VersionNumber != nil {
		return app.ErrProposalPatchArtifactConflict
	}
	_, err = t.tx.ExecContext(ctx, `INSERT INTO proposal_patch_artifacts(
		id, session_id, proposal_id, workspace_id, attempt_id, thread_id,
		patch_sha256, patch_bytes, index_hash, artifact_json, created_at)
		VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, artifact.ID, artifact.SessionID, artifact.ProposalID,
		artifact.WorkspaceID, artifact.AttemptID, artifact.ThreadID, artifact.PatchSHA256,
		int64(artifact.Published.Identity.Bytes), artifact.Index.Hash, data, formatTime(artifact.CreatedAt))
	return err
}

func (s *Store) LoadProposalPatchArtifact(ctx context.Context, artifactID string) (app.ProposalPatchArtifact, error) {
	if err := s.ensureOpen(); err != nil {
		return app.ProposalPatchArtifact{}, err
	}
	var data []byte
	if err := s.db.QueryRowContext(ctx, "SELECT artifact_json FROM proposal_patch_artifacts WHERE id = ?", artifactID).Scan(&data); err != nil {
		return app.ProposalPatchArtifact{}, mapProposalPatchArtifactNotFound(err)
	}
	return decodeProposalPatchArtifact(data)
}

func (s *Store) LoadProposalPatchArtifactForAttempt(ctx context.Context, attemptID domain.OperationID) (app.ProposalPatchArtifact, error) {
	if err := s.ensureOpen(); err != nil {
		return app.ProposalPatchArtifact{}, err
	}
	var data []byte
	if err := s.db.QueryRowContext(ctx, "SELECT artifact_json FROM proposal_patch_artifacts WHERE attempt_id = ?", attemptID).Scan(&data); err != nil {
		return app.ProposalPatchArtifact{}, mapProposalPatchArtifactNotFound(err)
	}
	return decodeProposalPatchArtifact(data)
}

func decodeProposalPatchArtifact(data []byte) (app.ProposalPatchArtifact, error) {
	var artifact app.ProposalPatchArtifact
	if json.Unmarshal(data, &artifact) != nil || artifact.Validate() != nil {
		return app.ProposalPatchArtifact{}, app.ErrReviewStoreCorrupt
	}
	return artifact, nil
}

func mapProposalPatchArtifactNotFound(err error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return app.ErrProposalPatchArtifactNotFound
	}
	return err
}
