package sqlite

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"path/filepath"
	"time"

	"github.com/Scottlr/nudge/internal/app"
	"github.com/Scottlr/nudge/internal/domain"
	"github.com/Scottlr/nudge/internal/domain/review"
	"github.com/Scottlr/nudge/internal/workspace"
)

var _ app.CleanupJournal = (*Store)(nil)
var _ app.CleanupInventoryStore = (*Store)(nil)
var _ app.CaptureManifestReader = (*Store)(nil)
var _ app.CaptureManifestWriter = (*Store)(nil)

func (s *Store) SaveCaptureManifest(ctx context.Context, manifest app.CaptureManifest) error {
	if err := s.ensureOpen(); err != nil {
		return err
	}
	if manifest.Validate() != nil {
		return app.ErrInvalidLocalCaptureManifest
	}
	data, err := json.Marshal(manifest)
	if err != nil {
		return err
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	_, err = s.db.ExecContext(ctx, `INSERT INTO capture_ownership(
		capture_id, repository_id, worktree_id, manifest_hash, manifest_json, created_at
	) VALUES(?, ?, ?, ?, ?, ?)
	ON CONFLICT(capture_id) DO UPDATE SET
		repository_id = excluded.repository_id,
		worktree_id = excluded.worktree_id,
		manifest_hash = excluded.manifest_hash,
		manifest_json = excluded.manifest_json`,
		manifest.CaptureID, manifest.RepositoryID, manifest.WorktreeID, manifest.ManifestHash, data, formatTime(manifest.CreatedAt))
	return err
}

func (s *Store) OpenCaptureManifest(ctx context.Context, captureID domain.CaptureID) (app.CaptureManifest, error) {
	if err := s.ensureOpen(); err != nil {
		return app.CaptureManifest{}, err
	}
	if captureID == "" {
		return app.CaptureManifest{}, app.ErrInvalidLocalCaptureManifest
	}
	var data []byte
	if err := s.db.QueryRowContext(ctx, "SELECT manifest_json FROM capture_ownership WHERE capture_id = ?", captureID).Scan(&data); errors.Is(err, sql.ErrNoRows) {
		return app.CaptureManifest{}, app.ErrCaptureNotFound
	} else if err != nil {
		return app.CaptureManifest{}, err
	}
	var manifest app.CaptureManifest
	if err := json.Unmarshal(data, &manifest); err != nil || manifest.CaptureID != captureID || manifest.Validate() != nil {
		return app.CaptureManifest{}, app.ErrCaptureCorrupt
	}
	return manifest, nil
}

func (s *Store) ListCleanupSessionLockTargets(ctx context.Context, repositoryID domain.RepositoryID) ([]app.CleanupSessionLockTarget, error) {
	if err := s.ensureOpen(); err != nil {
		return nil, err
	}
	if repositoryID == "" {
		return nil, app.ErrCleanupInvalid
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id, session_key_json, writer_lease_id, writer_lock_distinct
		FROM review_sessions WHERE repository_id = ? ORDER BY id ASC`, repositoryID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	targets := make([]app.CleanupSessionLockTarget, 0)
	for rows.Next() {
		var id, leaseID string
		var keyJSON []byte
		var distinct int64
		if err := rows.Scan(&id, &keyJSON, &leaseID, &distinct); err != nil {
			return nil, err
		}
		var key review.SessionKey
		if err := json.Unmarshal(keyJSON, &key); err != nil {
			return nil, app.ErrCleanupConflict
		}
		target := app.CleanupSessionLockTarget{SessionID: domain.ReviewSessionID(id), Key: key, LeaseID: domain.SessionLeaseID(leaseID), Distinct: distinct != 0}
		if target.Validate() != nil {
			return nil, app.ErrCleanupConflict
		}
		targets = append(targets, target)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return targets, nil
}

func (s *Store) SaveCleanupPlan(ctx context.Context, plan app.CleanupPlan) error {
	if err := s.ensureOpen(); err != nil {
		return err
	}
	if plan.Validate() != nil {
		return app.ErrCleanupInvalid
	}
	data, err := json.Marshal(plan)
	if err != nil {
		return err
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	var existingHash string
	err = s.db.QueryRowContext(ctx, "SELECT manifest_hash FROM cleanup_plans WHERE plan_id = ?", plan.ID).Scan(&existingHash)
	if err == nil {
		if existingHash != plan.ManifestHash {
			return app.ErrCleanupConflict
		}
		return nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO cleanup_plans(
		plan_id, repository_id, observed_revision, manifest_hash, plan_json, created_at
	) VALUES(?, ?, ?, ?, ?, ?)`, plan.ID, plan.RepositoryID, plan.ObservedRevision, plan.ManifestHash, data, formatTime(plan.CreatedAt))
	return err
}

func (s *Store) LoadCleanupPlan(ctx context.Context, planID string) (app.CleanupPlan, error) {
	if err := s.ensureOpen(); err != nil {
		return app.CleanupPlan{}, err
	}
	var repositoryID, observedRevision, manifestHash, createdAt string
	var data []byte
	err := s.db.QueryRowContext(ctx, `SELECT repository_id, observed_revision, manifest_hash, plan_json, created_at
		FROM cleanup_plans WHERE plan_id = ?`, planID).Scan(&repositoryID, &observedRevision, &manifestHash, &data, &createdAt)
	if errors.Is(err, sql.ErrNoRows) {
		return app.CleanupPlan{}, app.ErrCleanupNotFound
	}
	if err != nil {
		return app.CleanupPlan{}, err
	}
	var plan app.CleanupPlan
	if err := json.Unmarshal(data, &plan); err != nil || plan.Validate() != nil || string(plan.RepositoryID) != repositoryID || plan.ObservedRevision != observedRevision || plan.ManifestHash != manifestHash {
		return app.CleanupPlan{}, app.ErrCleanupInvalid
	}
	when, err := parseTime(createdAt)
	if err != nil || !plan.CreatedAt.Equal(when) {
		return app.CleanupPlan{}, app.ErrCleanupInvalid
	}
	return plan, nil
}

func (s *Store) SaveCleanupOperation(ctx context.Context, operation app.CleanupOperation) error {
	if err := s.ensureOpen(); err != nil {
		return err
	}
	if operation.Validate() != nil {
		return app.ErrCleanupInvalid
	}
	completedResources, err := json.Marshal(operation.CompletedResources)
	if err != nil {
		return err
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	var existingID, existingRepo, existingManifest string
	err = tx.QueryRowContext(ctx, "SELECT id, repository_id, manifest_hash FROM cleanup_operations WHERE plan_id = ?", operation.PlanID).Scan(&existingID, &existingRepo, &existingManifest)
	switch {
	case err == nil && (existingID != string(operation.ID) || existingRepo != string(operation.RepositoryID) || existingManifest != operation.ManifestHash):
		return app.ErrCleanupConflict
	case err != nil && !errors.Is(err, sql.ErrNoRows):
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO cleanup_operations(
		id, plan_id, repository_id, manifest_hash, observed_revision, phase,
		outcome, attempt, error_code, evidence_hash, completed_resources_json, created_at, updated_at, completed_at
	) VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	ON CONFLICT(id) DO UPDATE SET
		phase = excluded.phase,
		outcome = excluded.outcome,
		attempt = excluded.attempt,
		error_code = excluded.error_code,
		evidence_hash = excluded.evidence_hash,
		completed_resources_json = excluded.completed_resources_json,
		updated_at = excluded.updated_at,
		completed_at = excluded.completed_at`,
		operation.ID, operation.PlanID, operation.RepositoryID, operation.ManifestHash,
		operation.ObservedRevision, operation.Phase, operation.Outcome, operation.Attempt,
		operation.ErrorCode, operation.EvidenceHash, completedResources, formatTime(operation.CreatedAt),
		formatTime(operation.UpdatedAt), nullableCleanupTime(operation.CompletedAt))
	if err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) LoadCleanupOperation(ctx context.Context, id domain.OperationID) (app.CleanupOperation, error) {
	if err := s.ensureOpen(); err != nil {
		return app.CleanupOperation{}, err
	}
	if id == "" {
		return app.CleanupOperation{}, app.ErrCleanupInvalid
	}
	return s.loadCleanupOperation(ctx, "id = ?", id)
}

func (s *Store) LoadCleanupOperationByPlan(ctx context.Context, planID string) (app.CleanupOperation, error) {
	if err := s.ensureOpen(); err != nil {
		return app.CleanupOperation{}, err
	}
	if planID == "" {
		return app.CleanupOperation{}, app.ErrCleanupInvalid
	}
	return s.loadCleanupOperation(ctx, "plan_id = ?", planID)
}

func (s *Store) loadCleanupOperation(ctx context.Context, predicate string, value any) (app.CleanupOperation, error) {
	var operation app.CleanupOperation
	var id, repositoryID, createdAt, updatedAt string
	var completedAt sql.NullString
	var completedResourcesData []byte
	var version, attempt int64
	err := s.db.QueryRowContext(ctx, `SELECT id, plan_id, repository_id, manifest_hash,
		observed_revision, phase, outcome, attempt, error_code, evidence_hash,
		completed_resources_json, created_at, updated_at, completed_at
		FROM cleanup_operations WHERE `+predicate+` LIMIT 1`, value).Scan(
		&id, &operation.PlanID, &repositoryID, &operation.ManifestHash, &operation.ObservedRevision,
		&operation.Phase, &operation.Outcome, &attempt, &operation.ErrorCode, &operation.EvidenceHash, &completedResourcesData,
		&createdAt, &updatedAt, &completedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return app.CleanupOperation{}, app.ErrCleanupNotFound
	}
	if err != nil {
		return app.CleanupOperation{}, err
	}
	operation.ID = domain.OperationID(id)
	operation.RepositoryID = domain.RepositoryID(repositoryID)
	if version <= 0 {
		version = int64(app.CleanupJournalVersion)
	}
	operation.Version = uint64(version)
	operation.Attempt = uint64(attempt)
	if err := json.Unmarshal(completedResourcesData, &operation.CompletedResources); err != nil {
		return app.CleanupOperation{}, app.ErrCleanupInvalid
	}
	operation.CreatedAt, err = parseTime(createdAt)
	if err != nil {
		return app.CleanupOperation{}, app.ErrCleanupInvalid
	}
	operation.UpdatedAt, err = parseTime(updatedAt)
	if err != nil {
		return app.CleanupOperation{}, app.ErrCleanupInvalid
	}
	if completedAt.Valid {
		when, parseErr := parseTime(completedAt.String)
		if parseErr != nil {
			return app.CleanupOperation{}, app.ErrCleanupInvalid
		}
		operation.CompletedAt = &when
	}
	if operation.Validate() != nil {
		return app.CleanupOperation{}, app.ErrCleanupInvalid
	}
	return operation, nil
}

func nullableCleanupTime(value *time.Time) any {
	if value == nil {
		return nil
	}
	return formatTime(value.UTC())
}

func (s *Store) LoadCleanupInventory(ctx context.Context, repositoryID domain.RepositoryID) (app.CleanupInventory, error) {
	if err := s.ensureOpen(); err != nil {
		return app.CleanupInventory{}, err
	}
	if repositoryID == "" {
		return app.CleanupInventory{}, app.ErrCleanupInvalid
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return app.CleanupInventory{}, err
	}
	defer func() { _ = tx.Rollback() }()
	var displayName, updatedAt string
	if err := tx.QueryRowContext(ctx, "SELECT display_name, updated_at FROM repositories WHERE id = ?", repositoryID).Scan(&displayName, &updatedAt); errors.Is(err, sql.ErrNoRows) {
		return app.CleanupInventory{}, app.ErrCleanupNotFound
	} else if err != nil {
		return app.CleanupInventory{}, err
	}
	counts, err := cleanupRowCounts(ctx, tx, repositoryID)
	if err != nil {
		return app.CleanupInventory{}, err
	}
	resources, blockers, err := cleanupResources(ctx, tx, repositoryID)
	if err != nil {
		return app.CleanupInventory{}, err
	}
	revision, err := cleanupRevision(repositoryID, updatedAt, counts, resources)
	if err != nil {
		return app.CleanupInventory{}, err
	}
	inventory := app.CleanupInventory{
		RepositoryID: repositoryID, RepositoryDisplay: displayName, ObservedRevision: revision,
		Rows: counts, Resources: resources,
		Exclusions: []string{"user worktree, index, refs, Git config, credentials, remote provider history", "global startup logs and other repositories"},
		Effects:    []string{"remove only positively owned Nudge resources after quiescence", "delete selected repository rows in explicit dependency order", "retain a redacted cleanup tombstone"},
		Blockers:   blockers,
	}
	if err := inventory.Validate(); err != nil {
		return app.CleanupInventory{}, err
	}
	return inventory, nil
}

func cleanupRowCounts(ctx context.Context, tx *sql.Tx, repositoryID domain.RepositoryID) (app.CleanupRowCounts, error) {
	queries := []struct {
		name  string
		query string
	}{
		{"Worktrees", "SELECT COUNT(*) FROM worktrees WHERE repository_id = ?"},
		{"Sessions", "SELECT COUNT(*) FROM review_sessions WHERE repository_id = ?"},
		{"Generations", "SELECT COUNT(*) FROM target_generations g JOIN review_sessions s ON s.id = g.session_id WHERE s.repository_id = ?"},
		{"Threads", "SELECT COUNT(*) FROM review_threads t JOIN review_sessions s ON s.id = t.session_id WHERE s.repository_id = ?"},
		{"Messages", "SELECT COUNT(*) FROM messages m JOIN review_threads t ON t.id = m.thread_id JOIN review_sessions s ON s.id = t.session_id WHERE s.repository_id = ?"},
		{"ProviderConversations", "SELECT COUNT(*) FROM provider_conversations c JOIN review_threads t ON t.id = c.thread_id JOIN review_sessions s ON s.id = t.session_id WHERE s.repository_id = ?"},
		{"ProviderTurns", "SELECT COUNT(*) FROM provider_turns p JOIN review_threads t ON t.id = p.thread_id JOIN review_sessions s ON s.id = t.session_id WHERE s.repository_id = ?"},
		{"ReviewSnapshots", "SELECT COUNT(*) FROM review_snapshots WHERE repository_id = ?"},
		{"ReviewSnapshotLeases", "SELECT COUNT(*) FROM review_snapshot_leases l JOIN review_snapshots s ON s.id = l.snapshot_id WHERE s.repository_id = ?"},
		{"ProposalWorkspaces", "SELECT COUNT(*) FROM proposal_workspaces WHERE repository_id = ?"},
		{"Proposals", "SELECT COUNT(*) FROM proposals p JOIN proposal_workspaces w ON w.id = p.workspace_id WHERE w.repository_id = ?"},
		{"ProposalAttempts", "SELECT COUNT(*) FROM proposal_attempts a JOIN proposal_workspaces w ON w.id = a.workspace_id WHERE w.repository_id = ?"},
		{"ProposalVersions", "SELECT COUNT(*) FROM proposal_versions v JOIN proposals p ON p.id = v.proposal_id JOIN proposal_workspaces w ON w.id = p.workspace_id WHERE w.repository_id = ?"},
		{"ProposalPatchArtifacts", "SELECT COUNT(*) FROM proposal_patch_artifacts a JOIN proposal_workspaces w ON w.id = a.workspace_id WHERE w.repository_id = ?"},
		{"ApplyOperations", "SELECT COUNT(*) FROM apply_operations a JOIN proposal_workspaces w ON w.id = a.workspace_id WHERE w.repository_id = ?"},
		{"OwnedArtifacts", "SELECT COUNT(*) FROM owned_artifact_ledger WHERE repository_id = ?"},
		{"CapacityReservations", "SELECT COUNT(*) FROM capacity_reservations WHERE repository_id = ?"},
	}
	counts := app.CleanupRowCounts{Repositories: 1}
	for _, item := range queries {
		var count int64
		if err := tx.QueryRowContext(ctx, item.query, repositoryID).Scan(&count); err != nil || count < 0 {
			return app.CleanupRowCounts{}, err
		}
		switch item.name {
		case "Worktrees":
			counts.Worktrees = uint64(count)
		case "Sessions":
			counts.Sessions = uint64(count)
		case "Generations":
			counts.Generations = uint64(count)
		case "Threads":
			counts.Threads = uint64(count)
		case "Messages":
			counts.Messages = uint64(count)
		case "ProviderConversations":
			counts.ProviderConversations = uint64(count)
		case "ProviderTurns":
			counts.ProviderTurns = uint64(count)
		case "ReviewSnapshots":
			counts.ReviewSnapshots = uint64(count)
		case "ReviewSnapshotLeases":
			counts.ReviewSnapshotLeases = uint64(count)
		case "ProposalWorkspaces":
			counts.ProposalWorkspaces = uint64(count)
		case "Proposals":
			counts.Proposals = uint64(count)
		case "ProposalAttempts":
			counts.ProposalAttempts = uint64(count)
		case "ProposalVersions":
			counts.ProposalVersions = uint64(count)
		case "ProposalPatchArtifacts":
			counts.ProposalPatchArtifacts = uint64(count)
		case "ApplyOperations":
			counts.ApplyOperations = uint64(count)
		case "OwnedArtifacts":
			counts.OwnedArtifacts = uint64(count)
		case "CapacityReservations":
			counts.CapacityReservations = uint64(count)
		}
	}
	return counts, nil
}

func cleanupResources(ctx context.Context, tx *sql.Tx, repositoryID domain.RepositoryID) ([]app.CleanupResource, []string, error) {
	resources := make([]app.CleanupResource, 0)
	blockers := make([]string, 0)
	rows, err := tx.QueryContext(ctx, `SELECT capture_id, manifest_json FROM capture_ownership WHERE repository_id = ? ORDER BY capture_id ASC`, repositoryID)
	if err != nil {
		return nil, nil, err
	}
	for rows.Next() {
		var captureID string
		var manifestJSON []byte
		if err := rows.Scan(&captureID, &manifestJSON); err != nil {
			_ = rows.Close()
			return nil, nil, err
		}
		var manifest app.CaptureManifest
		if err := json.Unmarshal(manifestJSON, &manifest); err != nil || manifest.Validate() != nil {
			blockers = append(blockers, "capture ownership manifest is corrupt: "+captureID)
			continue
		}
		for _, ref := range []app.CaptureArtifactRef{manifest.Patch, manifest.Blobs} {
			if ref.Limits.Validate() != nil {
				blockers = append(blockers, "capture artifact limits are not durably recorded: "+captureID)
				continue
			}
			resources = append(resources, app.CleanupResource{
				ID: captureID + ":" + ref.Identity.SpoolID, Kind: app.CleanupResourceCapture,
				OwnerID: captureID, RepositoryID: repositoryID, ManifestHash: ref.Identity.ManifestHash,
				Published: app.PublishedArtifact{Identity: ref.Identity, Target: ref.Target, Limits: ref.Limits},
			})
		}
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, nil, err
	}
	_ = rows.Close()
	rows, err = tx.QueryContext(ctx, `SELECT id, root, marker_nonce, manifest_hash FROM review_snapshots WHERE repository_id = ? ORDER BY id ASC`, repositoryID)
	if err != nil {
		return nil, nil, err
	}
	for rows.Next() {
		var id, root, nonce, manifest string
		if err := rows.Scan(&id, &root, &nonce, &manifest); err != nil {
			_ = rows.Close()
			return nil, nil, err
		}
		resources = append(resources, app.CleanupResource{ID: id, Kind: app.CleanupResourceReviewSnapshot, OwnerID: id, RepositoryID: repositoryID, CanonicalPath: root, ParentRoot: filepath.Dir(root), MarkerNonce: nonce, ManifestHash: manifest})
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, nil, err
	}
	_ = rows.Close()
	rows, err = tx.QueryContext(ctx, `SELECT w.id, c.evidence_json FROM proposal_workspaces w JOIN proposal_workspace_creation c ON c.workspace_id = w.id WHERE w.repository_id = ? ORDER BY w.id ASC`, repositoryID)
	if err != nil {
		return nil, nil, err
	}
	for rows.Next() {
		var id string
		var evidenceJSON []byte
		if err := rows.Scan(&id, &evidenceJSON); err != nil {
			_ = rows.Close()
			return nil, nil, err
		}
		var evidence workspace.WorkspaceCreationEvidence
		if err := json.Unmarshal(evidenceJSON, &evidence); err != nil || evidence.Validate() != nil {
			blockers = append(blockers, "proposal workspace ownership evidence is corrupt: "+id)
			continue
		}
		workspaceRoot := filepath.Dir(evidence.Roots.Baseline.Path)
		resources = append(resources, app.CleanupResource{ID: id, Kind: app.CleanupResourceWorkspace, OwnerID: id, RepositoryID: repositoryID, CanonicalPath: workspaceRoot, ParentRoot: evidence.Parent.CanonicalPath, MarkerNonce: evidence.Nonce, NativeIdentity: string(evidence.Parent.NativeIdentity)})
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, nil, err
	}
	_ = rows.Close()
	rows, err = tx.QueryContext(ctx, `SELECT artifact_id, owner_kind, manifest_hash FROM owned_artifact_ledger WHERE repository_id = ? ORDER BY artifact_id ASC`, repositoryID)
	if err != nil {
		return nil, nil, err
	}
	for rows.Next() {
		var id, ownerKind, manifest string
		if err := rows.Scan(&id, &ownerKind, &manifest); err != nil {
			_ = rows.Close()
			return nil, nil, err
		}
		kind := app.CleanupResourceCapture
		if ownerKind == string(app.OwnerProposal) {
			kind = app.CleanupResourceProposal
		}
		resources = append(resources, app.CleanupResource{ID: id, Kind: kind, OwnerID: id, RepositoryID: repositoryID, ManifestHash: manifest})
		blockers = append(blockers, "owned artifact has no durable cleanup path identity: "+id)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, nil, err
	}
	_ = rows.Close()
	return resources, blockers, nil
}

func cleanupRevision(repositoryID domain.RepositoryID, updatedAt string, rows app.CleanupRowCounts, resources []app.CleanupResource) (string, error) {
	data, err := json.Marshal(struct {
		RepositoryID domain.RepositoryID
		UpdatedAt    string
		Rows         app.CleanupRowCounts
		Resources    []app.CleanupResource
	}{repositoryID, updatedAt, rows, resources})
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:]), nil
}

func (s *Store) DeleteRepositoryRows(ctx context.Context, repositoryID domain.RepositoryID, plan app.CleanupPlan) error {
	if err := s.ensureOpen(); err != nil {
		return err
	}
	if repositoryID == "" || plan.Validate() != nil || plan.RepositoryID != repositoryID {
		return app.ErrCleanupInvalid
	}
	current, err := s.LoadCleanupInventory(ctx, repositoryID)
	if errors.Is(err, app.ErrCleanupNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if current.DatabaseManifestHash() != plan.DatabaseManifestHash() {
		return app.ErrCleanupStalePlan
	}
	if len(plan.Blockers) != 0 {
		return app.ErrCleanupConflict
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	var activeLeases int64
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM review_snapshot_leases l
		JOIN review_snapshots s ON s.id = l.snapshot_id
		WHERE s.repository_id = ? AND l.released_at IS NULL`, repositoryID).Scan(&activeLeases); err != nil {
		return err
	}
	if activeLeases != 0 {
		return app.ErrCleanupConflict
	}
	statements := []string{
		`DELETE FROM runtime_approval_records WHERE turn_id IN (
			SELECT p.id FROM provider_turns p JOIN review_threads t ON t.id = p.thread_id
			JOIN review_sessions s ON s.id = t.session_id WHERE s.repository_id = ?)`,
		`DELETE FROM provider_turns WHERE thread_id IN (
			SELECT t.id FROM review_threads t JOIN review_sessions s ON s.id = t.session_id WHERE s.repository_id = ?)`,
		`DELETE FROM provider_conversations WHERE thread_id IN (
			SELECT t.id FROM review_threads t JOIN review_sessions s ON s.id = t.session_id WHERE s.repository_id = ?)`,
		`DELETE FROM message_body_chunks WHERE message_id IN (
			SELECT m.id FROM messages m JOIN review_threads t ON t.id = m.thread_id
			JOIN review_sessions s ON s.id = t.session_id WHERE s.repository_id = ?)`,
		`DELETE FROM message_body_identities WHERE message_id IN (
			SELECT m.id FROM messages m JOIN review_threads t ON t.id = m.thread_id
			JOIN review_sessions s ON s.id = t.session_id WHERE s.repository_id = ?)`,
		`DELETE FROM messages WHERE thread_id IN (
			SELECT t.id FROM review_threads t JOIN review_sessions s ON s.id = t.session_id WHERE s.repository_id = ?)`,
		`DELETE FROM proposal_validity_results WHERE apply_operation_id IN (
			SELECT a.apply_operation_id FROM post_apply_reconciliations a WHERE a.session_id IN (SELECT id FROM review_sessions WHERE repository_id = ?))`,
		`DELETE FROM proposal_validity_epochs WHERE session_id IN (SELECT id FROM review_sessions WHERE repository_id = ?)`,
		`DELETE FROM post_apply_reconciliations WHERE session_id IN (SELECT id FROM review_sessions WHERE repository_id = ?)`,
		`DELETE FROM apply_operations WHERE session_id IN (SELECT id FROM review_sessions WHERE repository_id = ?)`,
		`DELETE FROM proposal_result_snapshots WHERE session_id IN (SELECT id FROM review_sessions WHERE repository_id = ?)`,
		`DELETE FROM proposal_patch_artifacts WHERE session_id IN (SELECT id FROM review_sessions WHERE repository_id = ?)`,
		`DELETE FROM proposal_files WHERE proposal_id IN (
			SELECT p.id FROM proposals p JOIN proposal_workspaces w ON w.id = p.workspace_id WHERE w.repository_id = ?)`,
		`DELETE FROM proposal_preconditions WHERE proposal_id IN (
			SELECT p.id FROM proposals p JOIN proposal_workspaces w ON w.id = p.workspace_id WHERE w.repository_id = ?)`,
		`DELETE FROM proposal_versions WHERE proposal_id IN (
			SELECT p.id FROM proposals p JOIN proposal_workspaces w ON w.id = p.workspace_id WHERE w.repository_id = ?)`,
		`DELETE FROM proposal_attempts WHERE workspace_id IN (SELECT id FROM proposal_workspaces WHERE repository_id = ?)`,
		`DELETE FROM proposal_intents WHERE proposal_id IN (
			SELECT p.id FROM proposals p JOIN proposal_workspaces w ON w.id = p.workspace_id WHERE w.repository_id = ?)`,
		`DELETE FROM proposals WHERE workspace_id IN (SELECT id FROM proposal_workspaces WHERE repository_id = ?)`,
		`DELETE FROM workspace_retirements WHERE workspace_id IN (SELECT id FROM proposal_workspaces WHERE repository_id = ?)`,
		`DELETE FROM proposal_workspace_lifecycle WHERE workspace_id IN (SELECT id FROM proposal_workspaces WHERE repository_id = ?)`,
		`DELETE FROM proposal_workspace_creation WHERE workspace_id IN (SELECT id FROM proposal_workspaces WHERE repository_id = ?)`,
		`DELETE FROM proposal_workspaces WHERE repository_id = ?`,
		`DELETE FROM review_snapshot_leases WHERE snapshot_id IN (SELECT id FROM review_snapshots WHERE repository_id = ?)`,
		`DELETE FROM review_snapshots WHERE repository_id = ?`,
		`DELETE FROM reconciliation_anchor_results WHERE operation_id IN (
			SELECT r.id FROM reconciliation_operations r JOIN review_sessions s ON s.id = r.session_id WHERE s.repository_id = ?)`,
		`DELETE FROM anchor_versions WHERE thread_id IN (
			SELECT t.id FROM review_threads t JOIN review_sessions s ON s.id = t.session_id WHERE s.repository_id = ?)`,
		`DELETE FROM reconciliation_operations WHERE session_id IN (SELECT id FROM review_sessions WHERE repository_id = ?)`,
		`DELETE FROM target_generations WHERE session_id IN (SELECT id FROM review_sessions WHERE repository_id = ?)`,
		`DELETE FROM review_threads WHERE session_id IN (SELECT id FROM review_sessions WHERE repository_id = ?)`,
		`DELETE FROM review_sessions WHERE repository_id = ?`,
		`DELETE FROM capture_ownership WHERE repository_id = ?`,
		`DELETE FROM repository_base_preferences WHERE repository_id = ?`,
		`DELETE FROM capacity_reservation_volumes WHERE reservation_id IN (SELECT reservation_id FROM capacity_reservations WHERE repository_id = ?)`,
		`DELETE FROM storage_ledger_operations WHERE reservation_id IN (SELECT reservation_id FROM capacity_reservations WHERE repository_id = ?)`,
		`DELETE FROM owned_artifact_ledger WHERE repository_id = ?`,
		`DELETE FROM capacity_reservations WHERE repository_id = ?`,
		`DELETE FROM storage_reconciliation_discrepancies WHERE epoch_id IN (SELECT epoch_id FROM storage_reconciliation_epochs WHERE repository_id = ?)`,
		`DELETE FROM storage_reconciliation_epochs WHERE repository_id = ?`,
		`DELETE FROM storage_totals WHERE scope_kind = 'repository' AND scope_id = ?`,
		`DELETE FROM worktrees WHERE repository_id = ?`,
		`DELETE FROM repositories WHERE id = ?`,
	}
	for _, statement := range statements {
		if _, err := tx.ExecContext(ctx, statement, repositoryID); err != nil {
			return err
		}
	}
	if err := refreshGlobalStorageTotals(ctx, tx); err != nil {
		return err
	}
	return tx.Commit()
}

func refreshGlobalStorageTotals(ctx context.Context, tx *sql.Tx) error {
	var logical, observed, charged, uncertain, reserved int64
	if err := tx.QueryRowContext(ctx, `SELECT
		COALESCE(SUM(logical_bytes), 0), COALESCE(SUM(observed_bytes), 0),
		COALESCE(SUM(charged_bytes), 0), COALESCE(SUM(CASE WHEN lifecycle = 'accounting_uncertain' THEN 1 ELSE 0 END), 0)
		FROM owned_artifact_ledger`).Scan(&logical, &observed, &charged, &uncertain); err != nil {
		return err
	}
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(SUM(retained_bytes), 0) FROM capacity_reservations WHERE state = 'active'`).Scan(&reserved); err != nil {
		return err
	}
	if logical < 0 || observed < 0 || charged < 0 || uncertain < 0 || reserved < 0 {
		return app.ErrCleanupConflict
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO storage_totals(
		scope_kind, scope_id, logical_bytes, observed_bytes, charged_bytes, reserved_bytes,
		uncertain_count, ledger_revision, updated_at
	) VALUES('global', '', ?, ?, ?, ?, ?, 1, ?)
	ON CONFLICT(scope_kind, scope_id) DO UPDATE SET
		logical_bytes = excluded.logical_bytes,
		observed_bytes = excluded.observed_bytes,
		charged_bytes = excluded.charged_bytes,
		reserved_bytes = excluded.reserved_bytes,
		uncertain_count = excluded.uncertain_count,
		ledger_revision = storage_totals.ledger_revision + 1,
		updated_at = excluded.updated_at`, logical, observed, charged, reserved, uncertain, formatTime(time.Now().UTC()))
	return err
}
