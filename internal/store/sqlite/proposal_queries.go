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

var _ app.ProposalWorkspaceStore = (*Store)(nil)
var _ app.ProposalWorkspaceStoreTx = (*transaction)(nil)

// UpdateProposalIntent rebinds one existing lineage to a newly accepted
// source generation for an explicit proposal refresh. The proposal identity,
// summary, and expected paths remain stable; only the serialized confirmed
// provenance supplied by the application may advance.
func (t *transaction) UpdateProposalIntent(ctx context.Context, intent review.ProposalIntent) error {
	if intent.Validate() != nil || intent.ID == "" || intent.ThreadID == "" || intent.ConfirmedAgainst.SessionID != t.sessionID {
		return app.ErrReviewStoreInput
	}
	var storedThread, storedSession string
	if err := t.tx.QueryRowContext(ctx, `SELECT p.thread_id, w.session_id
		FROM proposals p JOIN proposal_workspaces w ON w.id = p.workspace_id WHERE p.id = ?`, intent.ID).Scan(&storedThread, &storedSession); err != nil {
		return mapNotFound(err)
	}
	if storedThread != string(intent.ThreadID) || storedSession != string(t.sessionID) {
		return app.ErrReviewStoreInput
	}
	data, err := json.Marshal(intent)
	if err != nil {
		return err
	}
	result, err := t.tx.ExecContext(ctx, `UPDATE proposal_intents SET thread_id = ?, intent_json = ?, confirmed_at = ? WHERE proposal_id = ? AND thread_id = ?`, intent.ThreadID, data, formatTime(intent.ConfirmedAt), intent.ID, intent.ThreadID)
	if err != nil {
		return err
	}
	count, _ := result.RowsAffected()
	if count != 1 {
		return app.ErrReviewStoreNotFound
	}
	return nil
}

func (t *transaction) CreateWorkspace(ctx context.Context, workspace review.ProposalWorkspace, intent review.ProposalIntent, proposal review.Proposal) error {
	if workspace.Validate() != nil || intent.Validate() != nil || proposal.Validate() != nil || intent.ID != proposal.ID || workspace.ID != proposal.WorkspaceID || workspace.SourceThreadID != intent.ThreadID || workspace.SourceThreadID != proposal.ThreadID {
		return app.ErrReviewStoreInput
	}
	if workspace.SessionID != t.sessionID || intent.ConfirmedAgainst.SessionID != workspace.SessionID {
		return app.ErrReviewStoreInput
	}
	var threadSession string
	if err := t.tx.QueryRowContext(ctx, "SELECT session_id FROM review_threads WHERE id = ?", workspace.SourceThreadID).Scan(&threadSession); err != nil {
		return mapNotFound(err)
	}
	if threadSession != string(t.sessionID) {
		return app.ErrReviewStoreInput
	}
	var repositoryID string
	if err := t.tx.QueryRowContext(ctx, "SELECT repository_id FROM worktrees WHERE id = ?", workspace.WorktreeID).Scan(&repositoryID); err != nil {
		return mapNotFound(err)
	}
	if repositoryID != string(workspace.RepositoryID) {
		return app.ErrReviewStoreInput
	}
	workspaceJSON, err := json.Marshal(workspace)
	if err != nil {
		return err
	}
	intentJSON, err := json.Marshal(intent)
	if err != nil {
		return err
	}
	if _, err := t.tx.ExecContext(ctx, `INSERT INTO proposal_workspaces(
		id, repository_id, worktree_id, session_id, source_thread_id, state,
		workspace_json, created_at, updated_at)
		VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?)`, workspace.ID, workspace.RepositoryID, workspace.WorktreeID,
		workspace.SessionID, workspace.SourceThreadID, string(workspace.State), workspaceJSON,
		formatTime(workspace.CreatedAt), formatTime(workspace.UpdatedAt)); err != nil {
		return err
	}
	if _, err := t.tx.ExecContext(ctx, `INSERT INTO proposals(
		id, workspace_id, thread_id, status, current_version, created_at, updated_at)
		VALUES(?, ?, ?, ?, ?, ?, ?)`, proposal.ID, proposal.WorkspaceID, proposal.ThreadID,
		string(proposal.Status), nullableProposalVersion(proposal.CurrentVersion), formatTime(proposal.CreatedAt), formatTime(proposal.UpdatedAt)); err != nil {
		return err
	}
	if _, err := t.tx.ExecContext(ctx, `INSERT INTO proposal_intents(proposal_id, thread_id, intent_json, confirmed_at)
		VALUES(?, ?, ?, ?)`, intent.ID, intent.ThreadID, intentJSON, formatTime(intent.ConfirmedAt)); err != nil {
		return err
	}
	return nil
}

func (t *transaction) RecordProposalAttempt(ctx context.Context, attempt review.ProposalAttempt) error {
	if attempt.Validate() != nil || attempt.Outcome == review.ProposalAttemptNoChanges {
		return app.ErrReviewStoreInput
	}
	if err := t.checkProposalOwnership(ctx, attempt.ProposalID, attempt.WorkspaceID, attempt.ThreadID); err != nil {
		return err
	}
	return t.saveProposalAttempt(ctx, attempt, false)
}

func (t *transaction) RecordNoChanges(ctx context.Context, attempt review.ProposalAttempt) error {
	if attempt.Validate() != nil || attempt.Outcome != review.ProposalAttemptNoChanges {
		return app.ErrReviewStoreInput
	}
	if err := t.checkProposalOwnership(ctx, attempt.ProposalID, attempt.WorkspaceID, attempt.ThreadID); err != nil {
		return err
	}
	var existingJSON []byte
	if err := t.tx.QueryRowContext(ctx, "SELECT attempt_json FROM proposal_attempts WHERE id = ?", attempt.ID).Scan(&existingJSON); err != nil {
		return mapNotFound(err)
	}
	var existing review.ProposalAttempt
	if err := json.Unmarshal(existingJSON, &existing); err != nil || !sameProposalAttemptIdentity(existing, attempt) || !existing.Outcome.CanTransitionTo(review.ProposalAttemptNoChanges) {
		return app.ErrReviewStoreInput
	}
	return t.saveProposalAttempt(ctx, attempt, true)
}

// TransitionProposalResultDisposition persists one phase of the explicit
// failed-result discard workflow. It updates only the attempt disposition and
// keeps all provider/source/result identities immutable.
func (t *transaction) TransitionProposalResultDisposition(ctx context.Context, attempt review.ProposalAttempt) error {
	if attempt.Validate() != nil || attempt.Outcome != review.ProposalAttemptFailed || attempt.ResultDisposition != review.ProposalResultDiscarding && attempt.ResultDisposition != review.ProposalResultDiscarded {
		return app.ErrReviewStoreInput
	}
	if err := t.checkProposalOwnership(ctx, attempt.ProposalID, attempt.WorkspaceID, attempt.ThreadID); err != nil {
		return err
	}
	var existingJSON []byte
	if err := t.tx.QueryRowContext(ctx, "SELECT attempt_json FROM proposal_attempts WHERE id = ? AND proposal_id = ?", attempt.ID, attempt.ProposalID).Scan(&existingJSON); err != nil {
		return mapNotFound(err)
	}
	var existing review.ProposalAttempt
	if err := json.Unmarshal(existingJSON, &existing); err != nil || !sameProposalAttemptIdentity(existing, attempt) || !existing.ResultDisposition.CanTransitionTo(attempt.ResultDisposition) {
		return review.ErrProposalConflict
	}
	if attempt.ResultDispositionChangedAt == nil {
		return app.ErrReviewStoreInput
	}
	data, err := json.Marshal(attempt)
	if err != nil {
		return err
	}
	result, err := t.tx.ExecContext(ctx, `UPDATE proposal_attempts SET result_disposition = ?, attempt_json = ? WHERE id = ? AND proposal_id = ?`, string(attempt.ResultDisposition), data, attempt.ID, attempt.ProposalID)
	if err != nil {
		return err
	}
	count, _ := result.RowsAffected()
	if count != 1 {
		return app.ErrReviewStoreNotFound
	}
	return nil
}

func (t *transaction) saveProposalAttempt(ctx context.Context, attempt review.ProposalAttempt, existingOnly bool) error {
	data, err := json.Marshal(attempt)
	if err != nil {
		return err
	}
	finishedAt := nullableTime(attempt.FinishedAt)
	if existingOnly {
		result, err := t.tx.ExecContext(ctx, `UPDATE proposal_attempts SET outcome = ?, result_disposition = ?, attempt_json = ?, started_at = ?, finished_at = ? WHERE id = ? AND proposal_id = ?`,
			string(attempt.Outcome), string(attempt.ResultDisposition), data, formatTime(attempt.StartedAt), finishedAt, attempt.ID, attempt.ProposalID)
		if err != nil {
			return err
		}
		count, _ := result.RowsAffected()
		if count != 1 {
			return app.ErrReviewStoreNotFound
		}
		return nil
	}
	var existingProposal string
	err = t.tx.QueryRowContext(ctx, "SELECT proposal_id FROM proposal_attempts WHERE id = ?", attempt.ID).Scan(&existingProposal)
	switch {
	case err == nil:
		if existingProposal != string(attempt.ProposalID) {
			return review.ErrProposalConflict
		}
		var existingJSON []byte
		if err := t.tx.QueryRowContext(ctx, "SELECT attempt_json FROM proposal_attempts WHERE id = ?", attempt.ID).Scan(&existingJSON); err != nil {
			return err
		}
		var existing review.ProposalAttempt
		if err := json.Unmarshal(existingJSON, &existing); err != nil || !sameProposalAttemptIdentity(existing, attempt) || !existing.Outcome.CanTransitionTo(attempt.Outcome) {
			return review.ErrProposalConflict
		}
		result, err := t.tx.ExecContext(ctx, `UPDATE proposal_attempts SET outcome = ?, result_disposition = ?, attempt_json = ?, started_at = ?, finished_at = ? WHERE id = ? AND proposal_id = ?`,
			string(attempt.Outcome), string(attempt.ResultDisposition), data, formatTime(attempt.StartedAt), finishedAt, attempt.ID, attempt.ProposalID)
		if err != nil {
			return err
		}
		count, _ := result.RowsAffected()
		if count != 1 {
			return app.ErrReviewStoreNotFound
		}
		return nil
	case !errors.Is(err, sql.ErrNoRows):
		return err
	}
	if attempt.Outcome != review.ProposalAttemptDeriving {
		return app.ErrReviewStoreInput
	}
	_, err = t.tx.ExecContext(ctx, `INSERT INTO proposal_attempts(
		id, proposal_id, workspace_id, thread_id, outcome, result_disposition,
		attempt_json, started_at, finished_at)
		VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?)`, attempt.ID, attempt.ProposalID, attempt.WorkspaceID,
		attempt.ThreadID, string(attempt.Outcome), string(attempt.ResultDisposition), data,
		formatTime(attempt.StartedAt), finishedAt)
	return err
}

func (t *transaction) PublishProposal(ctx context.Context, patch review.ProposedPatch) error {
	if patch.Validate() != nil || patch.Status != review.ProposalVersionReady {
		return app.ErrReviewStoreInput
	}
	if err := t.checkProposalOwnership(ctx, patch.ProposalID, patch.WorkspaceID, patch.ThreadID); err != nil {
		return err
	}
	var attemptJSON []byte
	if err := t.tx.QueryRowContext(ctx, "SELECT attempt_json FROM proposal_attempts WHERE id = ? AND proposal_id = ?", patch.AttemptID, patch.ProposalID).Scan(&attemptJSON); err != nil {
		return mapNotFound(err)
	}
	var attempt review.ProposalAttempt
	if err := json.Unmarshal(attemptJSON, &attempt); err != nil || attempt.Outcome != review.ProposalAttemptDeriving || attempt.VersionNumber != nil || !sameGeneration(attempt.SourceGeneration, patch.SourceGeneration) {
		return review.ErrProposalConflict
	}
	if patch.Artifact != (review.ProposedPatchArtifactReference{}) {
		if err := t.validateProposalPatchArtifact(ctx, patch); err != nil {
			return err
		}
	}
	attempt.VersionNumber = proposalVersionPointer(patch.Version)
	attempt.Outcome = review.ProposalAttemptVersionPublished
	attempt.ResultDisposition = review.ProposalResultPresent
	attempt.Baseline = snapshotPointer(patch.Baseline)
	attempt.Result = snapshotPointer(patch.Result)
	now := patch.CreatedAt
	attempt.FinishedAt = &now
	if attempt.Validate() != nil {
		return app.ErrReviewStoreInput
	}
	attemptData, err := json.Marshal(attempt)
	if err != nil {
		return err
	}
	var currentStatus string
	if err := t.tx.QueryRowContext(ctx, "SELECT status FROM proposals WHERE id = ?", patch.ProposalID).Scan(&currentStatus); err != nil {
		return mapNotFound(err)
	}
	if currentStatus == string(review.ProposalVersionApplying) {
		return review.ErrProposalConflict
	}
	var existing int
	if err := t.tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM proposal_versions WHERE proposal_id = ? AND version = ?", patch.ProposalID, patch.Version).Scan(&existing); err != nil {
		return err
	}
	if existing != 0 {
		return review.ErrProposalConflict
	}
	patchJSON, err := json.Marshal(patch)
	if err != nil {
		return err
	}
	fileJSON := make([][]byte, len(patch.Files))
	for index, file := range patch.Files {
		fileJSON[index], err = json.Marshal(file)
		if err != nil {
			return err
		}
	}
	preconditionJSON := make([][]byte, len(patch.Preconditions))
	for index, precondition := range patch.Preconditions {
		preconditionJSON[index], err = json.Marshal(precondition)
		if err != nil {
			return err
		}
	}
	readyRows, err := t.tx.QueryContext(ctx, "SELECT version, patch_json FROM proposal_versions WHERE proposal_id = ? AND status = ?", patch.ProposalID, string(review.ProposalVersionReady))
	if err != nil {
		return err
	}
	for readyRows.Next() {
		var version int64
		var readyJSON []byte
		if err := readyRows.Scan(&version, &readyJSON); err != nil {
			_ = readyRows.Close()
			return err
		}
		var readyPatch review.ProposedPatch
		if err := json.Unmarshal(readyJSON, &readyPatch); err != nil || readyPatch.Validate() != nil {
			_ = readyRows.Close()
			return app.ErrReviewStoreCorrupt
		}
		if err := app.MarkProposalStale(&readyPatch, app.StaleReasonProposalSuperseded, patch.CreatedAt); err != nil {
			_ = readyRows.Close()
			return err
		}
		updatedReadyJSON, err := json.Marshal(readyPatch)
		if err != nil {
			_ = readyRows.Close()
			return err
		}
		if _, err := t.tx.ExecContext(ctx, "UPDATE proposal_versions SET status = ?, patch_json = ? WHERE proposal_id = ? AND version = ?", string(review.ProposalVersionStale), updatedReadyJSON, patch.ProposalID, version); err != nil {
			_ = readyRows.Close()
			return err
		}
	}
	if err := readyRows.Err(); err != nil {
		_ = readyRows.Close()
		return err
	}
	if err := readyRows.Close(); err != nil {
		return err
	}
	patchBytes := patch.PatchBytes
	artifactID, artifactSpoolID, artifactManifestHash, artifactIndexHash := any(nil), any(nil), any(nil), any(nil)
	if patch.Artifact != (review.ProposedPatchArtifactReference{}) {
		patchBytes = []byte{}
		artifactID = patch.Artifact.ArtifactID
		artifactSpoolID = patch.Artifact.SpoolID
		artifactManifestHash = patch.Artifact.ManifestHash
		artifactIndexHash = patch.Artifact.IndexHash
	}
	if _, err := t.tx.ExecContext(ctx, `INSERT INTO proposal_versions(
		proposal_id, version, attempt_id, status, patch_sha256, patch_bytes, patch_json, created_at,
		artifact_id, artifact_spool_id, artifact_manifest_hash, artifact_index_hash)
		VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, patch.ProposalID, patch.Version, patch.AttemptID, string(patch.Status), patch.PatchSHA256, patchBytes, patchJSON, formatTime(patch.CreatedAt), artifactID, artifactSpoolID, artifactManifestHash, artifactIndexHash); err != nil {
		return err
	}
	for index, file := range patch.Files {
		if _, err := t.tx.ExecContext(ctx, `INSERT INTO proposal_files(proposal_id, version, ordinal, path, file_json) VALUES(?, ?, ?, ?, ?)`, patch.ProposalID, patch.Version, index+1, file.Path.Bytes(), fileJSON[index]); err != nil {
			return err
		}
	}
	for index, precondition := range patch.Preconditions {
		if _, err := t.tx.ExecContext(ctx, `INSERT INTO proposal_preconditions(proposal_id, version, ordinal, path, precondition_json) VALUES(?, ?, ?, ?, ?)`, patch.ProposalID, patch.Version, index+1, precondition.Path.Bytes(), preconditionJSON[index]); err != nil {
			return err
		}
	}
	if _, err := t.tx.ExecContext(ctx, `UPDATE proposal_attempts SET outcome = ?, result_disposition = ?, attempt_json = ?, finished_at = ? WHERE id = ? AND proposal_id = ?`, string(attempt.Outcome), string(attempt.ResultDisposition), attemptData, nullableTime(attempt.FinishedAt), attempt.ID, patch.ProposalID); err != nil {
		return err
	}
	_, err = t.tx.ExecContext(ctx, `UPDATE proposals SET status = ?, current_version = ?, updated_at = ? WHERE id = ?`, string(patch.Status), int64(patch.Version), formatTime(patch.CreatedAt), patch.ProposalID)
	return err
}

func (t *transaction) TransitionProposal(ctx context.Context, transition review.ProposalTransition) error {
	if transition.Validate() != nil {
		return app.ErrReviewStoreInput
	}
	var patchJSON []byte
	var currentStatus string
	if err := t.tx.QueryRowContext(ctx, "SELECT v.patch_json, v.status FROM proposal_versions v WHERE v.proposal_id = ? AND v.version = ?", transition.ProposalID, transition.Version).Scan(&patchJSON, &currentStatus); err != nil {
		return mapNotFound(err)
	}
	var patch review.ProposedPatch
	if err := json.Unmarshal(patchJSON, &patch); err != nil || patch.Validate() != nil || string(patch.Status) != currentStatus {
		return app.ErrReviewStoreCorrupt
	}
	if err := t.checkProposalOwnership(ctx, patch.ProposalID, patch.WorkspaceID, patch.ThreadID); err != nil {
		return err
	}
	if !review.ProposalStatus(currentStatus).CanTransitionTo(transition.Status) {
		return review.ErrInvalidProposalTransition
	}
	if transition.Status == review.ProposalVersionReady {
		var ready int
		if err := t.tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM proposal_versions WHERE proposal_id = ? AND status = ? AND version <> ?", transition.ProposalID, string(review.ProposalVersionReady), transition.Version).Scan(&ready); err != nil {
			return err
		}
		if ready != 0 {
			return review.ErrProposalConflict
		}
	}
	patch.Status = transition.Status
	patch.StatusReason = transition.Reason
	changedAt := transition.ChangedAt
	patch.StatusChangedAt = &changedAt
	updatedJSON, err := json.Marshal(patch)
	if err != nil {
		return err
	}
	if _, err := t.tx.ExecContext(ctx, `UPDATE proposal_versions SET status = ?, patch_json = ? WHERE proposal_id = ? AND version = ?`, string(transition.Status), updatedJSON, transition.ProposalID, transition.Version); err != nil {
		return err
	}
	_, err = t.tx.ExecContext(ctx, `UPDATE proposals SET status = ?, current_version = ?, applying_operation_id = COALESCE(?, applying_operation_id), updated_at = ? WHERE id = ?`, string(transition.Status), int64(transition.Version), nullableOperationID(transition.ApplyOperationID), formatTime(transition.ChangedAt), transition.ProposalID)
	return err
}

func (t *transaction) checkProposalOwnership(ctx context.Context, proposalID domain.ProposalID, workspaceID domain.WorkspaceID, threadID domain.ReviewThreadID) error {
	var sessionID, storedWorkspace, storedThread string
	if err := t.tx.QueryRowContext(ctx, `SELECT w.session_id, p.workspace_id, p.thread_id
		FROM proposals p JOIN proposal_workspaces w ON w.id = p.workspace_id WHERE p.id = ?`, proposalID).Scan(&sessionID, &storedWorkspace, &storedThread); err != nil {
		return mapNotFound(err)
	}
	if sessionID != string(t.sessionID) || storedWorkspace != string(workspaceID) || storedThread != string(threadID) {
		return app.ErrReviewStoreInput
	}
	return nil
}

func (s *Store) LoadProposalAggregate(ctx context.Context, proposalID domain.ProposalID) (review.ProposalAggregate, error) {
	if err := s.ensureOpen(); err != nil {
		return review.ProposalAggregate{}, err
	}
	var workspaceJSON, intentJSON []byte
	var workspace review.ProposalWorkspace
	var intent review.ProposalIntent
	var proposal review.Proposal
	var proposalStatus string
	var currentVersion sql.NullInt64
	var proposalCreated, proposalUpdated string
	var workspaceID, intentID, intentThread string
	var applyingOperationID sql.NullString
	if err := s.db.QueryRowContext(ctx, `SELECT w.workspace_json, i.intent_json, p.id, p.workspace_id,
		p.thread_id, p.status, p.current_version, p.applying_operation_id, p.created_at, p.updated_at,
		i.proposal_id, i.thread_id
		FROM proposals p JOIN proposal_workspaces w ON w.id = p.workspace_id
		JOIN proposal_intents i ON i.proposal_id = p.id WHERE p.id = ?`, proposalID).Scan(
		&workspaceJSON, &intentJSON, &proposal.ID, &workspaceID, &proposal.ThreadID, &proposalStatus,
		&currentVersion, &applyingOperationID, &proposalCreated, &proposalUpdated, &intentID, &intentThread); err != nil {
		return review.ProposalAggregate{}, mapNotFound(err)
	}
	if err := json.Unmarshal(workspaceJSON, &workspace); err != nil {
		return review.ProposalAggregate{}, app.ErrReviewStoreCorrupt
	}
	if err := json.Unmarshal(intentJSON, &intent); err != nil {
		return review.ProposalAggregate{}, app.ErrReviewStoreCorrupt
	}
	proposal.WorkspaceID = domain.WorkspaceID(workspaceID)
	proposal.Status = review.ProposalStatus(proposalStatus)
	proposal.CreatedAt, _ = parseTime(proposalCreated)
	proposal.UpdatedAt, _ = parseTime(proposalUpdated)
	if currentVersion.Valid {
		value := review.ProposalVersionNumber(currentVersion.Int64)
		proposal.CurrentVersion = &value
	}
	if applyingOperationID.Valid {
		value := domain.OperationID(applyingOperationID.String)
		proposal.ApplyingOperationID = &value
	}
	if proposal.ID != proposalID || intentID != string(proposalID) || intentThread != string(proposal.ThreadID) {
		return review.ProposalAggregate{}, app.ErrReviewStoreCorrupt
	}
	aggregate := review.ProposalAggregate{Workspace: workspace, Intent: intent, Proposal: proposal}
	rows, err := s.db.QueryContext(ctx, `SELECT attempt_json FROM proposal_attempts WHERE proposal_id = ? ORDER BY started_at ASC, id ASC`, proposalID)
	if err != nil {
		return review.ProposalAggregate{}, err
	}
	for rows.Next() {
		var data []byte
		if err := rows.Scan(&data); err != nil {
			_ = rows.Close()
			return review.ProposalAggregate{}, err
		}
		var attempt review.ProposalAttempt
		if err := json.Unmarshal(data, &attempt); err != nil {
			_ = rows.Close()
			return review.ProposalAggregate{}, app.ErrReviewStoreCorrupt
		}
		aggregate.Attempts = append(aggregate.Attempts, attempt)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return review.ProposalAggregate{}, err
	}
	_ = rows.Close()
	versionRows, err := s.db.QueryContext(ctx, `SELECT patch_json, patch_bytes, patch_sha256, artifact_id, artifact_spool_id, artifact_manifest_hash, artifact_index_hash FROM proposal_versions WHERE proposal_id = ? ORDER BY version ASC`, proposalID)
	if err != nil {
		return review.ProposalAggregate{}, err
	}
	for versionRows.Next() {
		var data, patchBytes []byte
		var patchSHA string
		var artifactID, artifactSpoolID, artifactManifestHash, artifactIndexHash sql.NullString
		if err := versionRows.Scan(&data, &patchBytes, &patchSHA, &artifactID, &artifactSpoolID, &artifactManifestHash, &artifactIndexHash); err != nil {
			_ = versionRows.Close()
			return review.ProposalAggregate{}, err
		}
		var patch review.ProposedPatch
		if err := json.Unmarshal(data, &patch); err != nil || patch.Validate() != nil || patch.PatchSHA256 != patchSHA {
			_ = versionRows.Close()
			return review.ProposalAggregate{}, app.ErrReviewStoreCorrupt
		}
		if patch.Artifact == (review.ProposedPatchArtifactReference{}) {
			if len(patchBytes) == 0 || !bytes.Equal(patch.PatchBytes, patchBytes) || artifactID.Valid || artifactSpoolID.Valid || artifactManifestHash.Valid || artifactIndexHash.Valid {
				_ = versionRows.Close()
				return review.ProposalAggregate{}, app.ErrReviewStoreCorrupt
			}
		} else if len(patchBytes) != 0 || !artifactID.Valid || !artifactSpoolID.Valid || !artifactManifestHash.Valid || !artifactIndexHash.Valid || patch.Artifact.ArtifactID != artifactID.String || patch.Artifact.SpoolID != artifactSpoolID.String || patch.Artifact.ManifestHash != artifactManifestHash.String || patch.Artifact.IndexHash != artifactIndexHash.String {
			_ = versionRows.Close()
			return review.ProposalAggregate{}, app.ErrReviewStoreCorrupt
		}
		aggregate.Versions = append(aggregate.Versions, patch)
	}
	if err := versionRows.Err(); err != nil {
		_ = versionRows.Close()
		return review.ProposalAggregate{}, err
	}
	_ = versionRows.Close()
	if aggregate.Validate() != nil {
		return review.ProposalAggregate{}, app.ErrReviewStoreCorrupt
	}
	return aggregate, nil
}

func nullableProposalVersion(version *review.ProposalVersionNumber) any {
	if version == nil {
		return nil
	}
	return int64(*version)
}

func nullableOperationID(operationID domain.OperationID) any {
	if operationID == "" {
		return nil
	}
	return string(operationID)
}

func proposalVersionPointer(version review.ProposalVersionNumber) *review.ProposalVersionNumber {
	return &version
}

func snapshotPointer(snapshot review.SnapshotIdentity) *review.SnapshotIdentity {
	return &snapshot
}

func sameProposalAttemptIdentity(left, right review.ProposalAttempt) bool {
	return left.ID == right.ID && left.ProposalID == right.ProposalID && left.WorkspaceID == right.WorkspaceID && left.ThreadID == right.ThreadID && left.StartedAt.Equal(right.StartedAt) && sameGeneration(left.SourceGeneration, right.SourceGeneration)
}

func sameGeneration(left, right review.GenerationProvenance) bool {
	if left.SessionID != right.SessionID || left.Generation != right.Generation || left.Base != right.Base || left.Head != right.Head {
		return false
	}
	if left.CaptureID == nil || right.CaptureID == nil {
		return left.CaptureID == nil && right.CaptureID == nil
	}
	return *left.CaptureID == *right.CaptureID
}
