package sqlite

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Scottlr/nudge/internal/app"
	"github.com/Scottlr/nudge/internal/domain"
	"github.com/Scottlr/nudge/internal/domain/repository"
	"github.com/Scottlr/nudge/internal/domain/review"
)

func (s *Store) UpsertRepository(ctx context.Context, repo repository.Repository, worktree repository.WorktreeRef) error {
	if err := s.ensureOpen(); err != nil {
		return err
	}
	if err := repo.Validate(); err != nil {
		return app.ErrReviewStoreInput
	}
	if err := worktree.Validate(); err != nil || worktree.RepositoryID != repo.ID {
		return app.ErrReviewStoreInput
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err := upsertRepository(ctx, tx, repo); err != nil {
		return err
	}
	if err := upsertWorktree(ctx, tx, worktree); err != nil {
		return err
	}
	return tx.Commit()
}

func upsertRepository(ctx context.Context, tx *sql.Tx, repo repository.Repository) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO repositories(
		id, common_git_dir, common_git_dir_identity, binding_version, object_format,
		display_name, default_branch, first_verified_at, last_verified_at,
		binding_status, created_at, updated_at
	) VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, 'active', ?, ?)
	ON CONFLICT(id) DO UPDATE SET
		common_git_dir=excluded.common_git_dir,
		common_git_dir_identity=excluded.common_git_dir_identity,
		binding_version=excluded.binding_version,
		object_format=excluded.object_format,
		display_name=excluded.display_name,
		default_branch=excluded.default_branch,
		last_verified_at=excluded.last_verified_at,
		binding_status='active',
		updated_at=excluded.updated_at`,
		repo.ID,
		repo.CommonGitDir,
		string(repo.Binding.CommonGitDirIdentity),
		repo.Binding.Version,
		repo.Binding.ObjectFormat,
		repo.DisplayName,
		repo.DefaultBranch,
		formatTime(repo.CreatedAt),
		formatTime(repo.UpdatedAt),
		formatTime(repo.CreatedAt),
		formatTime(repo.UpdatedAt),
	)
	return err
}

func upsertWorktree(ctx context.Context, tx *sql.Tx, worktree repository.WorktreeRef) error {
	var currentObject any
	if worktree.CurrentObjectID != "" {
		currentObject = string(worktree.CurrentObjectID)
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO worktrees(
		id, repository_id, root_path, git_dir, root_identity, git_dir_identity,
		binding_version, object_format, current_object_id, branch_name, detached,
		launch_focus, first_verified_at, last_verified_at, binding_status, created_at, updated_at
	) VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'active', ?, ?)
	ON CONFLICT(id) DO UPDATE SET
		repository_id=excluded.repository_id,
		root_path=excluded.root_path,
		git_dir=excluded.git_dir,
		root_identity=excluded.root_identity,
		git_dir_identity=excluded.git_dir_identity,
		binding_version=excluded.binding_version,
		object_format=excluded.object_format,
		current_object_id=excluded.current_object_id,
		branch_name=excluded.branch_name,
		detached=excluded.detached,
		launch_focus=excluded.launch_focus,
		last_verified_at=excluded.last_verified_at,
		binding_status='active',
		updated_at=excluded.updated_at`,
		worktree.ID,
		worktree.RepositoryID,
		worktree.RootPath,
		worktree.GitDir,
		string(worktree.Binding.RootIdentity),
		string(worktree.Binding.GitDirIdentity),
		worktree.Binding.Version,
		worktree.Binding.ObjectFormat,
		currentObject,
		worktree.BranchName,
		boolInt(worktree.Detached),
		worktree.LaunchFocus,
		formatTime(time.Now().UTC()),
		formatTime(time.Now().UTC()),
		formatTime(time.Now().UTC()),
		formatTime(time.Now().UTC()),
	)
	return err
}

func (s *Store) CreateSession(ctx context.Context, session review.ReviewSession, leaseID domain.SessionLeaseID) (app.SessionWriteGuard, error) {
	if err := s.ensureOpen(); err != nil {
		return app.SessionWriteGuard{}, err
	}
	if leaseID == "" {
		return app.SessionWriteGuard{}, app.ErrReviewStoreInput
	}
	if err := session.Validate(); err != nil {
		return app.SessionWriteGuard{}, app.ErrReviewStoreInput
	}
	key, err := review.SessionKeyFor(session)
	if err != nil {
		return app.SessionWriteGuard{}, app.ErrReviewStoreInput
	}
	sessionJSON, err := json.Marshal(session)
	if err != nil {
		return app.SessionWriteGuard{}, err
	}
	keyJSON, err := json.Marshal(key)
	if err != nil {
		return app.SessionWriteGuard{}, err
	}
	keyHash := sha256.Sum256(keyJSON)
	worktreeID := nullableID(string(key.WorktreeID))
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return app.SessionWriteGuard{}, err
	}
	defer func() { _ = tx.Rollback() }()
	if key.WorktreeID != "" {
		var repositoryID domain.RepositoryID
		if err := tx.QueryRowContext(ctx, "SELECT repository_id FROM worktrees WHERE id = ?", key.WorktreeID).Scan(&repositoryID); err != nil {
			return app.SessionWriteGuard{}, mapNotFound(err)
		}
		if repositoryID != session.RepositoryID {
			return app.SessionWriteGuard{}, app.ErrReviewStoreInput
		}
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO review_sessions(
		id, repository_id, worktree_id, target_kind, session_key_json, session_key_hash,
		target_json, current_generation, revision, writer_epoch, writer_lease_id,
		created_at, updated_at
	) VALUES(?, ?, ?, ?, ?, ?, ?, ?, 1, 1, ?, ?, ?)`,
		session.ID,
		session.RepositoryID,
		worktreeID,
		string(session.TargetSpec.Kind),
		string(keyJSON),
		hex.EncodeToString(keyHash[:]),
		sessionJSON,
		session.Target.Generation,
		leaseID,
		formatTime(session.CreatedAt),
		formatTime(session.UpdatedAt),
	)
	if err != nil {
		return app.SessionWriteGuard{}, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO target_generations(
		session_id, generation, capture_generation_json, capture_manifest_json,
		target_json, fingerprint, manifest_hash, accepted_at
	) VALUES(?, ?, ?, ?, ?, ?, '', ?)`,
		session.ID,
		session.Target.Generation,
		[]byte("null"),
		[]byte("null"),
		sessionJSON,
		session.Target.Fingerprint,
		formatTime(session.CreatedAt),
	); err != nil {
		return app.SessionWriteGuard{}, err
	}
	if err := tx.Commit(); err != nil {
		return app.SessionWriteGuard{}, err
	}
	return app.SessionWriteGuard{SessionID: session.ID, LeaseID: leaseID, WriterEpoch: 1, ExpectedRevision: 1}, nil
}

func (s *Store) ClaimSessionWriter(ctx context.Context, sessionID domain.ReviewSessionID, leaseID domain.SessionLeaseID) (app.SessionWriteGuard, error) {
	if err := s.ensureOpen(); err != nil {
		return app.SessionWriteGuard{}, err
	}
	if sessionID == "" || leaseID == "" {
		return app.SessionWriteGuard{}, app.ErrReviewStoreInput
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return app.SessionWriteGuard{}, err
	}
	defer func() { _ = tx.Rollback() }()
	var closed sql.NullString
	var epoch, revision int64
	if err := tx.QueryRowContext(ctx, "SELECT closed_at, writer_epoch, revision FROM review_sessions WHERE id = ?", sessionID).Scan(&closed, &epoch, &revision); err != nil {
		return app.SessionWriteGuard{}, mapNotFound(err)
	}
	if closed.Valid {
		return app.SessionWriteGuard{}, app.ErrSessionRevisionConflict
	}
	epoch++
	revision++
	if _, err := tx.ExecContext(ctx, "UPDATE review_sessions SET writer_epoch = ?, writer_lease_id = ?, revision = ? WHERE id = ? AND closed_at IS NULL", epoch, leaseID, revision, sessionID); err != nil {
		return app.SessionWriteGuard{}, err
	}
	if err := tx.Commit(); err != nil {
		return app.SessionWriteGuard{}, err
	}
	return app.SessionWriteGuard{SessionID: sessionID, LeaseID: leaseID, WriterEpoch: uint64(epoch), ExpectedRevision: uint64(revision)}, nil
}

func (s *Store) FindCompatibleSession(ctx context.Context, key review.SessionKey) (*review.ReviewSession, error) {
	if err := s.ensureOpen(); err != nil {
		return nil, err
	}
	if err := key.Validate(); err != nil {
		return nil, app.ErrReviewStoreInput
	}
	keyJSON, err := json.Marshal(key)
	if err != nil {
		return nil, err
	}
	hash := sha256.Sum256(keyJSON)
	var data []byte
	err = s.db.QueryRowContext(ctx, `SELECT target_json FROM review_sessions
		WHERE repository_id = ? AND target_kind = ? AND session_key_hash = ? AND closed_at IS NULL
		ORDER BY updated_at DESC, id ASC LIMIT 1`, key.RepositoryID, string(key.TargetKind), hex.EncodeToString(hash[:])).Scan(&data)
	if err != nil {
		return nil, mapNotFound(err)
	}
	var session review.ReviewSession
	if err := json.Unmarshal(data, &session); err != nil {
		return nil, fmt.Errorf("%w: session JSON: %v", app.ErrReviewStoreCorrupt, err)
	}
	if err := session.Validate(); err != nil {
		return nil, fmt.Errorf("%w: session validation: %v", app.ErrReviewStoreCorrupt, err)
	}
	return &session, nil
}

func (s *Store) WithSessionTx(ctx context.Context, guard app.SessionWriteGuard, fn func(app.ReviewStoreTx) error) (app.SessionWriteGuard, error) {
	if err := s.ensureOpen(); err != nil {
		return guard, err
	}
	if err := guard.Validate(); err != nil || fn == nil {
		return guard, app.ErrReviewStoreInput
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return guard, err
	}
	defer func() { _ = tx.Rollback() }()
	if err := verifyGuard(ctx, tx, guard); err != nil {
		return guard, err
	}
	storeTx := &transaction{tx: tx, sessionID: guard.SessionID, maxMessageBytes: s.config.MaxMessageBytes}
	if err := fn(storeTx); err != nil {
		return guard, err
	}
	result, err := tx.ExecContext(ctx, `UPDATE review_sessions SET revision = revision + 1
		WHERE id = ? AND writer_lease_id = ? AND writer_epoch = ? AND revision = ?`, guard.SessionID, guard.LeaseID, guard.WriterEpoch, guard.ExpectedRevision)
	if err != nil {
		return guard, err
	}
	if count, _ := result.RowsAffected(); count != 1 {
		return guard, app.ErrSessionLeaseLost
	}
	if err := tx.Commit(); err != nil {
		return guard, err
	}
	guard.ExpectedRevision++
	return guard, nil
}

func verifyGuard(ctx context.Context, tx *sql.Tx, guard app.SessionWriteGuard) error {
	var lease domain.SessionLeaseID
	var epoch, revision int64
	if err := tx.QueryRowContext(ctx, "SELECT writer_lease_id, writer_epoch, revision FROM review_sessions WHERE id = ?", guard.SessionID).Scan(&lease, &epoch, &revision); err != nil {
		return mapNotFound(err)
	}
	if lease != guard.LeaseID || uint64(epoch) != guard.WriterEpoch {
		return app.ErrSessionLeaseLost
	}
	if uint64(revision) != guard.ExpectedRevision {
		return app.ErrSessionRevisionConflict
	}
	return nil
}

type transaction struct {
	tx              *sql.Tx
	sessionID       domain.ReviewSessionID
	maxMessageBytes uint64
}

func (t *transaction) SaveThread(ctx context.Context, thread review.ReviewThread) error {
	if err := thread.Validate(); err != nil || thread.SessionID != t.sessionID {
		return app.ErrReviewStoreInput
	}
	anchorJSON, err := json.Marshal(thread.Anchor)
	if err != nil {
		return err
	}
	var existingSession string
	var currentVersion int64
	var createdAt string
	err = t.tx.QueryRowContext(ctx, "SELECT session_id, current_anchor_version, created_at FROM review_threads WHERE id = ?", thread.ID).Scan(&existingSession, &currentVersion, &createdAt)
	if errors.Is(err, sql.ErrNoRows) {
		if _, err := t.tx.ExecContext(ctx, `INSERT INTO review_threads(
			id, session_id, title, resolution, conversation, proposal, read_state,
			provider_conversation_id, latest_proposal_id, failure_phase, error_code,
			current_anchor_version, created_at, updated_at
		) VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 1, ?, ?)`,
			thread.ID,
			thread.SessionID,
			thread.Title,
			string(thread.Resolution),
			string(thread.Conversation),
			string(thread.Proposal),
			string(thread.Read),
			nullableDomainID(thread.ProviderConversationID),
			nullableDomainID(thread.LatestProposalID),
			string(thread.FailurePhase),
			string(thread.ErrorCode),
			formatTime(thread.CreatedAt),
			formatTime(thread.UpdatedAt),
		); err != nil {
			return err
		}
		_, err = t.tx.ExecContext(ctx, "INSERT INTO anchor_versions(thread_id, version, anchor_json, created_at) VALUES(?, 1, ?, ?)", thread.ID, anchorJSON, formatTime(thread.Anchor.CreatedAt))
		return err
	}
	if err != nil {
		return err
	}
	if existingSession != string(t.sessionID) || createdAt != formatTime(thread.CreatedAt) {
		return app.ErrReviewStoreInput
	}
	var existingAnchor []byte
	if err := t.tx.QueryRowContext(ctx, "SELECT anchor_json FROM anchor_versions WHERE thread_id = ? AND version = ?", thread.ID, currentVersion).Scan(&existingAnchor); err != nil {
		return fmt.Errorf("%w: anchor history: %v", app.ErrReviewStoreCorrupt, err)
	}
	if !bytes.Equal(existingAnchor, anchorJSON) {
		currentVersion++
		if _, err := t.tx.ExecContext(ctx, "INSERT INTO anchor_versions(thread_id, version, anchor_json, created_at) VALUES(?, ?, ?, ?)", thread.ID, currentVersion, anchorJSON, formatTime(thread.Anchor.CreatedAt)); err != nil {
			return err
		}
	}
	_, err = t.tx.ExecContext(ctx, `UPDATE review_threads SET
		title = ?, resolution = ?, conversation = ?, proposal = ?, read_state = ?,
		provider_conversation_id = ?, latest_proposal_id = ?, failure_phase = ?, error_code = ?,
		current_anchor_version = ?, updated_at = ? WHERE id = ? AND session_id = ?`,
		thread.Title,
		string(thread.Resolution),
		string(thread.Conversation),
		string(thread.Proposal),
		string(thread.Read),
		nullableDomainID(thread.ProviderConversationID),
		nullableDomainID(thread.LatestProposalID),
		string(thread.FailurePhase),
		string(thread.ErrorCode),
		currentVersion,
		formatTime(thread.UpdatedAt),
		thread.ID,
		thread.SessionID,
	)
	return err
}

func (t *transaction) SaveMessage(ctx context.Context, message review.Message) error {
	if err := message.Validate(); err != nil || message.ThreadID == "" || uint64(len(message.Content)) > t.maxMessageBytes {
		return app.ErrReviewStoreInput
	}
	var owner string
	if err := t.tx.QueryRowContext(ctx, "SELECT session_id FROM review_threads WHERE id = ?", message.ThreadID).Scan(&owner); err != nil {
		return mapNotFound(err)
	}
	if owner != string(t.sessionID) {
		return app.ErrReviewStoreInput
	}
	var existingThread string
	err := t.tx.QueryRowContext(ctx, "SELECT thread_id FROM messages WHERE id = ?", message.ID).Scan(&existingThread)
	if err == nil && existingThread != string(message.ThreadID) {
		return app.ErrReviewStoreInput
	}
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	digest := sha256.Sum256([]byte(message.Content))
	if !validSHA256(hex.EncodeToString(digest[:])) {
		return app.ErrReviewStoreInput
	}
	if errors.Is(err, sql.ErrNoRows) {
		_, err = t.tx.ExecContext(ctx, `INSERT INTO messages(
			id, thread_id, role, content, provider_id, status, ordinal, body_length, body_sha256,
			failure_phase, error_code, created_at, updated_at, completed_at
		) VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			message.ID,
			message.ThreadID,
			string(message.Role),
			[]byte(message.Content),
			message.ProviderID,
			string(message.Status),
			message.Ordinal,
			len(message.Content),
			hex.EncodeToString(digest[:]),
			string(message.FailurePhase),
			string(message.ErrorCode),
			formatTime(message.CreatedAt),
			formatTime(message.UpdatedAt),
			nullableTime(message.CompletedAt),
		)
		return err
	}
	_, err = t.tx.ExecContext(ctx, `UPDATE messages SET
		role = ?, content = ?, provider_id = ?, status = ?, ordinal = ?, body_length = ?, body_sha256 = ?,
		failure_phase = ?, error_code = ?, updated_at = ?, completed_at = ? WHERE id = ? AND thread_id = ?`,
		string(message.Role),
		[]byte(message.Content),
		message.ProviderID,
		string(message.Status),
		message.Ordinal,
		len(message.Content),
		hex.EncodeToString(digest[:]),
		string(message.FailurePhase),
		string(message.ErrorCode),
		formatTime(message.UpdatedAt),
		nullableTime(message.CompletedAt),
		message.ID,
		message.ThreadID,
	)
	return err
}

func (t *transaction) SaveCaptureGeneration(ctx context.Context, generation app.CaptureGeneration, manifest app.CaptureManifest) error {
	if err := generation.Validate(); err != nil {
		return app.ErrReviewStoreInput
	}
	if err := manifest.Validate(); err != nil || manifest.CaptureID != generation.CaptureID || manifest.RepositoryID != generation.RepositoryID || manifest.WorktreeID != generation.WorktreeID {
		return app.ErrReviewStoreInput
	}
	return t.saveCaptureGeneration(ctx, generation, manifest, nil, "")
}

func (t *transaction) SaveAcceptedTargetGeneration(ctx context.Context, accepted app.AcceptedTargetGeneration) error {
	if err := accepted.Validate(); err != nil {
		return err
	}
	policyJSON, err := json.Marshal(accepted.PolicyEvaluation)
	if err != nil {
		return err
	}
	return t.saveCaptureGeneration(ctx, accepted.Generation, accepted.Manifest, policyJSON, accepted.RetentionReference)
}

func (t *transaction) saveCaptureGeneration(ctx context.Context, generation app.CaptureGeneration, manifest app.CaptureManifest, policyJSON []byte, retentionReference string) error {
	var repositoryID string
	var worktreeID sql.NullString
	if err := t.tx.QueryRowContext(ctx, "SELECT repository_id, worktree_id FROM review_sessions WHERE id = ?", t.sessionID).Scan(&repositoryID, &worktreeID); err != nil {
		return mapNotFound(err)
	}
	if repositoryID != string(generation.RepositoryID) || (worktreeID.Valid && worktreeID.String != string(generation.WorktreeID)) {
		return app.ErrReviewStoreInput
	}
	generationJSON, err := json.Marshal(generation)
	if err != nil {
		return err
	}
	manifestJSON, err := json.Marshal(manifest)
	if err != nil {
		return err
	}
	if policyJSON == nil {
		policyJSON = []byte("null")
	}
	result, err := t.tx.ExecContext(ctx, `INSERT INTO target_generations(
		session_id, generation, capture_id, capture_generation_json, capture_manifest_json,
		fingerprint, manifest_hash, policy_evaluation_json, retention_reference, accepted_at
	) VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	ON CONFLICT(session_id, generation) DO UPDATE SET
		capture_id = excluded.capture_id,
		capture_generation_json = excluded.capture_generation_json,
		capture_manifest_json = excluded.capture_manifest_json,
		fingerprint = excluded.fingerprint,
		manifest_hash = excluded.manifest_hash,
		policy_evaluation_json = excluded.policy_evaluation_json,
		retention_reference = excluded.retention_reference,
		accepted_at = excluded.accepted_at
	WHERE target_generations.capture_id IS NULL OR target_generations.capture_id = excluded.capture_id`,
		t.sessionID,
		generation.Generation,
		string(generation.CaptureID),
		generationJSON,
		manifestJSON,
		generation.Fingerprint,
		generation.ManifestHash,
		policyJSON,
		retentionReference,
		formatTime(generation.CreatedAt),
	)
	if err != nil {
		return err
	}
	if count, _ := result.RowsAffected(); count != 1 {
		return app.ErrSessionRevisionConflict
	}
	return nil
}

func (t *transaction) CreateReconciliation(ctx context.Context, operation app.ReconciliationOperation) error {
	if !validReconciliationState(operation.State) || operation.ID == "" || operation.SessionID != t.sessionID || operation.FromGeneration == 0 || operation.ToGeneration == 0 || operation.StartedAt.IsZero() || operation.CompletedAt != nil {
		return app.ErrReviewStoreInput
	}
	_, err := t.tx.ExecContext(ctx, `INSERT INTO reconciliation_operations(id, session_id, from_generation, to_generation, state, started_at, active)
		VALUES(?, ?, ?, ?, ?, ?, 0)`, operation.ID, operation.SessionID, operation.FromGeneration, operation.ToGeneration, string(operation.State), formatTime(operation.StartedAt))
	return err
}

func (t *transaction) StageReconciliationResult(ctx context.Context, result app.ReconciliationAnchorResult) error {
	if result.OperationID == "" || result.ThreadID == "" || result.Anchor.Validate() != nil || result.State.Validate() != nil || result.Anchor.State != result.State || result.Reason == "" {
		return app.ErrReviewStoreInput
	}
	var operationSession string
	if err := t.tx.QueryRowContext(ctx, "SELECT session_id FROM reconciliation_operations WHERE id = ?", result.OperationID).Scan(&operationSession); err != nil {
		return mapNotFound(err)
	}
	if operationSession != string(t.sessionID) {
		return app.ErrReviewStoreInput
	}
	var threadSession string
	if err := t.tx.QueryRowContext(ctx, "SELECT session_id FROM review_threads WHERE id = ?", result.ThreadID).Scan(&threadSession); err != nil {
		return mapNotFound(err)
	}
	if threadSession != string(t.sessionID) {
		return app.ErrReviewStoreInput
	}
	data, err := json.Marshal(result)
	if err != nil {
		return err
	}
	_, err = t.tx.ExecContext(ctx, `INSERT INTO reconciliation_anchor_results(operation_id, thread_id, result_json, state, reason)
		VALUES(?, ?, ?, ?, ?)`, result.OperationID, result.ThreadID, data, string(result.State), result.Reason)
	return err
}

func (t *transaction) CompleteReconciliation(ctx context.Context, operationID domain.OperationID, completedAt time.Time) error {
	if operationID == "" || completedAt.IsZero() {
		return app.ErrReviewStoreInput
	}
	result, err := t.tx.ExecContext(ctx, `UPDATE reconciliation_operations SET state = ?, completed_at = ?
		WHERE id = ? AND session_id = ? AND state IN (?, ?)`, app.ReconciliationCompleted, formatTime(completedAt), operationID, t.sessionID, app.ReconciliationStaged, app.ReconciliationRunning)
	if err != nil {
		return err
	}
	count, _ := result.RowsAffected()
	if count != 1 {
		return app.ErrReviewStoreNotFound
	}
	return nil
}

func (t *transaction) ActivateReconciliation(ctx context.Context, operationID domain.OperationID) error {
	if operationID == "" {
		return app.ErrReviewStoreInput
	}
	var toGeneration int64
	var state string
	if err := t.tx.QueryRowContext(ctx, "SELECT to_generation, state FROM reconciliation_operations WHERE id = ? AND session_id = ?", operationID, t.sessionID).Scan(&toGeneration, &state); err != nil {
		return mapNotFound(err)
	}
	if state != string(app.ReconciliationCompleted) {
		return app.ErrSessionRevisionConflict
	}
	var generationExists int
	if err := t.tx.QueryRowContext(ctx, "SELECT 1 FROM target_generations WHERE session_id = ? AND generation = ?", t.sessionID, toGeneration).Scan(&generationExists); err != nil {
		return mapNotFound(err)
	}
	if _, err := t.tx.ExecContext(ctx, "UPDATE reconciliation_operations SET active = 0 WHERE session_id = ?", t.sessionID); err != nil {
		return err
	}
	if _, err := t.tx.ExecContext(ctx, "UPDATE reconciliation_operations SET active = 1 WHERE id = ? AND session_id = ?", operationID, t.sessionID); err != nil {
		return err
	}
	if _, err := t.tx.ExecContext(ctx, "UPDATE review_sessions SET active_reconciliation_operation_id = ?, current_generation = ? WHERE id = ?", operationID, toGeneration, t.sessionID); err != nil {
		return err
	}
	rows, err := t.tx.QueryContext(ctx, "SELECT result_json FROM reconciliation_anchor_results WHERE operation_id = ? ORDER BY thread_id ASC", operationID)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var data []byte
		if err := rows.Scan(&data); err != nil {
			return err
		}
		var result app.ReconciliationAnchorResult
		if err := json.Unmarshal(data, &result); err != nil || result.Anchor.Validate() != nil {
			return app.ErrReviewStoreCorrupt
		}
		var currentVersion int64
		if err := t.tx.QueryRowContext(ctx, "SELECT current_anchor_version FROM review_threads WHERE id = ? AND session_id = ?", result.ThreadID, t.sessionID).Scan(&currentVersion); err != nil {
			return mapNotFound(err)
		}
		currentVersion++
		anchorJSON, err := json.Marshal(result.Anchor)
		if err != nil {
			return err
		}
		if _, err := t.tx.ExecContext(ctx, "INSERT INTO anchor_versions(thread_id, version, anchor_json, created_at) VALUES(?, ?, ?, ?)", result.ThreadID, currentVersion, anchorJSON, formatTime(result.Anchor.CreatedAt)); err != nil {
			return err
		}
		if _, err := t.tx.ExecContext(ctx, "UPDATE review_threads SET current_anchor_version = ? WHERE id = ? AND session_id = ?", currentVersion, result.ThreadID, t.sessionID); err != nil {
			return err
		}
	}
	return rows.Err()
}

func validReconciliationState(state app.ReconciliationOperationState) bool {
	switch state {
	case app.ReconciliationStaged, app.ReconciliationRunning, app.ReconciliationCompleted, app.ReconciliationFailed:
		return true
	default:
		return false
	}
}

func (s *Store) ListThreadSummaries(ctx context.Context, sessionID domain.ReviewSessionID, page app.ThreadPage) (app.ThreadPageResult, error) {
	if err := s.ensureOpen(); err != nil {
		return app.ThreadPageResult{}, err
	}
	page.SessionID = sessionID
	if err := page.Validate(); err != nil {
		return app.ThreadPageResult{}, err
	}
	revision, err := s.sessionRevision(ctx, sessionID)
	if err != nil {
		return app.ThreadPageResult{}, err
	}
	if page.Cursor != nil && page.Cursor.Revision != revision {
		return app.ThreadPageResult{}, app.ErrSessionRevisionConflict
	}
	query := `SELECT t.id, t.title, t.resolution, t.conversation, t.proposal, t.read_state,
		t.failure_phase, t.error_code, t.provider_conversation_id, t.latest_proposal_id, t.updated_at, a.anchor_json
		FROM review_threads t JOIN anchor_versions a
		ON a.thread_id = t.id AND a.version = t.current_anchor_version
		WHERE t.session_id = ?`
	args := []any{sessionID}
	if page.Cursor != nil {
		query += " AND (t.updated_at > ? OR (t.updated_at = ? AND t.id > ?))"
		cursorTime := formatTime(page.Cursor.UpdatedAt)
		args = append(args, cursorTime, cursorTime, page.Cursor.ID)
	}
	query += " ORDER BY t.updated_at ASC, t.id ASC LIMIT ?"
	args = append(args, int64(page.Limit)+1)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return app.ThreadPageResult{}, err
	}
	defer rows.Close()
	result := app.ThreadPageResult{Revision: revision}
	for rows.Next() {
		var id, title, resolution, conversation, proposal, readState, failurePhase, errorCode, updatedAt string
		var providerID, proposalID sql.NullString
		var anchorJSON []byte
		if err := rows.Scan(&id, &title, &resolution, &conversation, &proposal, &readState, &failurePhase, &errorCode, &providerID, &proposalID, &updatedAt, &anchorJSON); err != nil {
			return app.ThreadPageResult{}, err
		}
		if len(result.Items) >= int(page.Limit) {
			result.HasMore = true
			break
		}
		var anchor review.CodeAnchor
		if err := json.Unmarshal(anchorJSON, &anchor); err != nil || anchor.Validate() != nil {
			return app.ThreadPageResult{}, app.ErrReviewStoreCorrupt
		}
		when, err := parseTime(updatedAt)
		if err != nil {
			return app.ThreadPageResult{}, app.ErrReviewStoreCorrupt
		}
		item := app.ThreadSummary{
			ID:           domain.ReviewThreadID(id),
			SessionID:    sessionID,
			Title:        title,
			Resolution:   review.ResolutionState(resolution),
			Conversation: review.ConversationState(conversation),
			Proposal:     review.ProposalState(proposal),
			Anchor:       anchor.State,
			Read:         review.ReadState(readState),
			FailurePhase: review.FailurePhase(failurePhase),
			ErrorCode:    review.ErrorCode(errorCode),
			AnchorPath:   repository.RepoPath(anchor.Path.Bytes()),
			Unread:       readState == string(review.Unread),
			UpdatedAt:    when,
		}
		if item.Resolution.Validate() != nil || item.Conversation.Validate() != nil || item.Proposal.Validate() != nil || item.Read.Validate() != nil {
			return app.ThreadPageResult{}, app.ErrReviewStoreCorrupt
		}
		if providerID.Valid {
			value := domain.ProviderConversationID(providerID.String)
			item.ProviderConversationID = &value
		}
		if proposalID.Valid {
			value := domain.ProposalID(proposalID.String)
			item.LatestProposalID = &value
		}
		size, err := pageBytes(item)
		if err != nil {
			return app.ThreadPageResult{}, err
		}
		currentBytes, _ := pageBytes(result.Items)
		if currentBytes+size > app.MaxPageEncodedBytes {
			if len(result.Items) == 0 {
				return app.ThreadPageResult{}, ErrPageItemTooLarge
			}
			result.HasMore = true
			break
		}
		result.Items = append(result.Items, item)
	}
	if err := rows.Err(); err != nil {
		return app.ThreadPageResult{}, err
	}
	if result.HasMore && len(result.Items) > 0 {
		last := result.Items[len(result.Items)-1]
		result.Next = &app.ThreadCursor{SessionID: sessionID, Revision: revision, UpdatedAt: last.UpdatedAt, ID: last.ID, FilterKey: page.FilterKey}
	}
	return result, nil
}

func (s *Store) LoadThread(ctx context.Context, threadID domain.ReviewThreadID) (review.ReviewThread, error) {
	if err := s.ensureOpen(); err != nil {
		return review.ReviewThread{}, err
	}
	var sessionID, title, resolution, conversation, proposal, readState, failurePhase, errorCode, updatedAt, createdAt string
	var providerID, proposalID sql.NullString
	var anchorJSON []byte
	if err := s.db.QueryRowContext(ctx, `SELECT t.session_id, t.title, t.resolution, t.conversation, t.proposal,
		t.read_state, t.failure_phase, t.error_code, t.provider_conversation_id, t.latest_proposal_id, t.created_at, t.updated_at, a.anchor_json
		FROM review_threads t JOIN anchor_versions a
		ON a.thread_id = t.id AND a.version = t.current_anchor_version WHERE t.id = ?`, threadID).Scan(
		&sessionID, &title, &resolution, &conversation, &proposal, &readState, &failurePhase, &errorCode, &providerID, &proposalID, &createdAt, &updatedAt, &anchorJSON); err != nil {
		return review.ReviewThread{}, mapNotFound(err)
	}
	var anchor review.CodeAnchor
	if err := json.Unmarshal(anchorJSON, &anchor); err != nil || anchor.Validate() != nil {
		return review.ReviewThread{}, app.ErrReviewStoreCorrupt
	}
	created, err := parseTime(createdAt)
	if err != nil {
		return review.ReviewThread{}, app.ErrReviewStoreCorrupt
	}
	updated, err := parseTime(updatedAt)
	if err != nil {
		return review.ReviewThread{}, app.ErrReviewStoreCorrupt
	}
	thread := review.ReviewThread{
		ID:           threadID,
		SessionID:    domain.ReviewSessionID(sessionID),
		Anchor:       anchor,
		Title:        title,
		Resolution:   review.ResolutionState(resolution),
		Conversation: review.ConversationState(conversation),
		Proposal:     review.ProposalState(proposal),
		Read:         review.ReadState(readState),
		FailurePhase: review.FailurePhase(failurePhase),
		ErrorCode:    review.ErrorCode(errorCode),
		CreatedAt:    created,
		UpdatedAt:    updated,
	}
	if providerID.Valid {
		value := domain.ProviderConversationID(providerID.String)
		thread.ProviderConversationID = &value
	}
	if proposalID.Valid {
		value := domain.ProposalID(proposalID.String)
		thread.LatestProposalID = &value
	}
	if err := thread.Validate(); err != nil {
		return review.ReviewThread{}, app.ErrReviewStoreCorrupt
	}
	return thread, nil
}

func (s *Store) ListMessages(ctx context.Context, threadID domain.ReviewThreadID, page app.MessagePage) (app.MessagePageResult, error) {
	if err := s.ensureOpen(); err != nil {
		return app.MessagePageResult{}, err
	}
	page.ThreadID = threadID
	if err := page.Validate(); err != nil {
		return app.MessagePageResult{}, err
	}
	var sessionID domain.ReviewSessionID
	var revision int64
	if err := s.db.QueryRowContext(ctx, "SELECT session_id FROM review_threads WHERE id = ?", threadID).Scan(&sessionID); err != nil {
		return app.MessagePageResult{}, mapNotFound(err)
	}
	if err := s.db.QueryRowContext(ctx, "SELECT revision FROM review_sessions WHERE id = ?", sessionID).Scan(&revision); err != nil {
		return app.MessagePageResult{}, mapNotFound(err)
	}
	if page.Cursor != nil && page.Cursor.Revision != uint64(revision) {
		return app.MessagePageResult{}, app.ErrSessionRevisionConflict
	}
	query := `SELECT id, role, provider_id, status, ordinal, body_length, body_sha256,
		failure_phase, error_code, created_at, updated_at, completed_at, substr(content, 1, 256)
		FROM messages WHERE thread_id = ?`
	args := []any{threadID}
	if page.Cursor != nil {
		query += " AND (updated_at > ? OR (updated_at = ? AND id > ?))"
		cursorTime := formatTime(page.Cursor.UpdatedAt)
		args = append(args, cursorTime, cursorTime, page.Cursor.ID)
	}
	query += " ORDER BY updated_at ASC, id ASC LIMIT ?"
	args = append(args, int64(page.Limit)+1)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return app.MessagePageResult{}, err
	}
	defer rows.Close()
	result := app.MessagePageResult{Revision: uint64(revision)}
	for rows.Next() {
		var id, role, status, failurePhase, errorCode, createdAt, updatedAt string
		var providerID string
		var ordinal, bodyLength int64
		var bodySHA string
		var completedAt, preview sql.NullString
		if err := rows.Scan(&id, &role, &providerID, &status, &ordinal, &bodyLength, &bodySHA, &failurePhase, &errorCode, &createdAt, &updatedAt, &completedAt, &preview); err != nil {
			return app.MessagePageResult{}, err
		}
		if len(result.Items) >= int(page.Limit) {
			result.HasMore = true
			break
		}
		created, err := parseTime(createdAt)
		if err != nil {
			return app.MessagePageResult{}, app.ErrReviewStoreCorrupt
		}
		updated, err := parseTime(updatedAt)
		if err != nil {
			return app.MessagePageResult{}, app.ErrReviewStoreCorrupt
		}
		item := app.MessageSummary{ID: domain.MessageID(id), ThreadID: threadID, Role: review.MessageRole(role), ProviderID: providerID, Status: review.MessageStatus(status), Ordinal: uint64(ordinal), ByteLength: uint64(bodyLength), SHA256: bodySHA, FailurePhase: review.FailurePhase(failurePhase), ErrorCode: review.ErrorCode(errorCode), CreatedAt: created, UpdatedAt: updated}
		if bodyLength < 0 || ordinal <= 0 || !validSHA256(bodySHA) || item.Role.Validate() != nil || item.Status.Validate() != nil {
			return app.MessagePageResult{}, app.ErrReviewStoreCorrupt
		}
		if preview.Valid {
			item.Preview = preview.String
		}
		if completedAt.Valid {
			value, err := parseTime(completedAt.String)
			if err != nil {
				return app.MessagePageResult{}, app.ErrReviewStoreCorrupt
			}
			item.CompletedAt = &value
		}
		size, err := pageBytes(item)
		if err != nil {
			return app.MessagePageResult{}, err
		}
		currentBytes, _ := pageBytes(result.Items)
		if currentBytes+size > app.MaxPageEncodedBytes {
			if len(result.Items) == 0 {
				return app.MessagePageResult{}, ErrPageItemTooLarge
			}
			result.HasMore = true
			break
		}
		result.Items = append(result.Items, item)
	}
	if err := rows.Err(); err != nil {
		return app.MessagePageResult{}, err
	}
	if result.HasMore && len(result.Items) > 0 {
		last := result.Items[len(result.Items)-1]
		result.Next = &app.MessageCursor{ThreadID: threadID, Revision: uint64(revision), UpdatedAt: last.UpdatedAt, ID: last.ID}
	}
	return result, nil
}

func (s *Store) ReadMessageBody(ctx context.Context, bodyRange app.BodyRange) (app.MessageBodyChunk, error) {
	if err := s.ensureOpen(); err != nil {
		return app.MessageBodyChunk{}, err
	}
	if err := bodyRange.Validate(); err != nil || !validSHA256(bodyRange.ExpectedSHA256) {
		return app.MessageBodyChunk{}, app.ErrReviewStoreInput
	}
	var length int64
	var digest string
	var data []byte
	start := int64(bodyRange.Offset) + 1
	if err := s.db.QueryRowContext(ctx, "SELECT body_length, body_sha256, substr(content, ?, ?) FROM messages WHERE id = ?", start, int64(bodyRange.Length), bodyRange.MessageID).Scan(&length, &digest, &data); err != nil {
		return app.MessageBodyChunk{}, mapNotFound(err)
	}
	if uint64(length) != bodyRange.ExpectedLength || !strings.EqualFold(digest, bodyRange.ExpectedSHA256) || uint64(len(data)) != bodyRange.Length {
		return app.MessageBodyChunk{}, app.ErrReviewStoreCorrupt
	}
	return app.MessageBodyChunk{MessageID: bodyRange.MessageID, Offset: bodyRange.Offset, Bytes: append([]byte(nil), data...), TotalLength: uint64(length), SHA256: digest, Complete: bodyRange.Offset+bodyRange.Length == uint64(length)}, nil
}

func (s *Store) sessionRevision(ctx context.Context, sessionID domain.ReviewSessionID) (uint64, error) {
	var revision int64
	if err := s.db.QueryRowContext(ctx, "SELECT revision FROM review_sessions WHERE id = ?", sessionID).Scan(&revision); err != nil {
		return 0, mapNotFound(err)
	}
	if revision <= 0 {
		return 0, app.ErrReviewStoreCorrupt
	}
	return uint64(revision), nil
}

func mapNotFound(err error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return app.ErrReviewStoreNotFound
	}
	return err
}

func formatTime(value time.Time) string {
	return value.UTC().Format(time.RFC3339Nano)
}

func parseTime(value string) (time.Time, error) {
	return time.Parse(time.RFC3339Nano, value)
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func nullableID(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func nullableTime(value *time.Time) any {
	if value == nil {
		return nil
	}
	return formatTime(*value)
}

func nullableDomainID[T ~string](value *T) any {
	if value == nil {
		return nil
	}
	return string(*value)
}

func validSHA256(value string) bool {
	if len(value) != sha256.Size*2 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func pageBytes(value any) (uint64, error) {
	encoded, err := json.Marshal(value)
	return uint64(len(encoded)), err
}
