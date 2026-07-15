package sqlite

import (
	"context"
	"database/sql"
	"errors"

	"github.com/Scottlr/nudge/internal/app"
	"github.com/Scottlr/nudge/internal/domain"
	"github.com/Scottlr/nudge/internal/domain/repository"
)

var _ app.ReviewSnapshotStore = (*Store)(nil)

// SaveReviewSnapshot binds one verified filesystem root to its accepted
// capture. The capture identity is unique so a later Ensure can reuse the
// exact durable root rather than creating a competing history row.
func (s *Store) SaveReviewSnapshot(ctx context.Context, snapshot app.ReviewSnapshot) error {
	if err := s.ensureOpen(); err != nil {
		return err
	}
	if err := snapshot.Validate(); err != nil {
		return err
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	_, err := s.db.ExecContext(ctx, `INSERT INTO review_snapshots(
		id, capture_id, repository_id, worktree_id, target_kind, head_object_id,
		base_object_id, parent_label, object_format, format_version, root, marker_nonce,
		manifest_hash, policy_version, evidence_version, state, created_at, updated_at
	) VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		snapshot.ID, nullableID(string(snapshot.CaptureID)), snapshot.RepositoryID, snapshot.WorktreeID,
		snapshot.TargetKind, snapshot.HeadObjectID, snapshot.BaseObjectID, snapshot.ParentLabel,
		snapshot.ObjectFormat, snapshot.FormatVersion,
		snapshot.Root, snapshot.MarkerNonce, snapshot.ManifestHash, snapshot.PolicyVersion,
		snapshot.EvidenceVersion, snapshot.State, formatTime(snapshot.CreatedAt), formatTime(snapshot.CreatedAt))
	if err != nil {
		if isSQLiteConstraint(err) {
			return app.ErrReviewSnapshotBusy
		}
		return err
	}
	return nil
}

// LoadReviewSnapshotByObject loads a pinned branch/commit snapshot by its
// repository, head object, resource policy, and materialization format key.
func (s *Store) LoadReviewSnapshotByObject(ctx context.Context, repositoryID domain.RepositoryID, headObjectID repository.ObjectID, policyVersion app.ResourcePolicyVersion, formatVersion uint32) (app.ReviewSnapshot, error) {
	if err := s.ensureOpen(); err != nil {
		return app.ReviewSnapshot{}, err
	}
	if repositoryID == "" || headObjectID == "" || policyVersion == 0 || formatVersion == 0 {
		return app.ReviewSnapshot{}, app.ErrInvalidReviewSnapshot
	}
	return s.loadReviewSnapshot(ctx, "repository_id = ? AND head_object_id = ? AND policy_version = ? AND format_version = ?", repositoryID, headObjectID, policyVersion, formatVersion)
}

// LoadReviewSnapshot loads and validates the durable identity without
// accepting the filesystem root as proof of readiness.
func (s *Store) LoadReviewSnapshot(ctx context.Context, id domain.ReviewSnapshotID) (app.ReviewSnapshot, error) {
	if err := s.ensureOpen(); err != nil {
		return app.ReviewSnapshot{}, err
	}
	if id == "" {
		return app.ReviewSnapshot{}, app.ErrInvalidReviewSnapshot
	}
	return s.loadReviewSnapshot(ctx, "id = ?", id)
}

// LoadReviewSnapshotByCapture loads the one snapshot associated with an
// accepted capture.
func (s *Store) LoadReviewSnapshotByCapture(ctx context.Context, captureID domain.CaptureID) (app.ReviewSnapshot, error) {
	if err := s.ensureOpen(); err != nil {
		return app.ReviewSnapshot{}, err
	}
	if captureID == "" {
		return app.ReviewSnapshot{}, app.ErrInvalidReviewSnapshot
	}
	return s.loadReviewSnapshot(ctx, "capture_id = ?", captureID)
}

func (s *Store) loadReviewSnapshot(ctx context.Context, predicate string, values ...any) (app.ReviewSnapshot, error) {
	query := `SELECT id, capture_id, repository_id, worktree_id, target_kind,
		head_object_id, base_object_id, parent_label, object_format, format_version,
		root, marker_nonce, manifest_hash, policy_version, evidence_version, state, created_at
		FROM review_snapshots WHERE ` + predicate + ` LIMIT 1`
	var snapshot app.ReviewSnapshot
	var captureID sql.NullString
	var createdAt string
	var policyVersion, evidenceVersion int64
	err := s.db.QueryRowContext(ctx, query, values...).Scan(
		&snapshot.ID, &captureID, &snapshot.RepositoryID, &snapshot.WorktreeID, &snapshot.TargetKind,
		&snapshot.HeadObjectID, &snapshot.BaseObjectID, &snapshot.ParentLabel, &snapshot.ObjectFormat, &snapshot.FormatVersion,
		&snapshot.Root, &snapshot.MarkerNonce, &snapshot.ManifestHash, &policyVersion,
		&evidenceVersion, &snapshot.State, &createdAt)
	if errors.Is(err, sql.ErrNoRows) {
		return app.ReviewSnapshot{}, app.ErrReviewSnapshotNotFound
	}
	if err != nil {
		return app.ReviewSnapshot{}, err
	}
	if captureID.Valid {
		snapshot.CaptureID = domain.CaptureID(captureID.String)
	}
	if policyVersion <= 0 || evidenceVersion <= 0 {
		return app.ReviewSnapshot{}, app.ErrReviewSnapshotCorrupt
	}
	snapshot.PolicyVersion = app.ResourcePolicyVersion(policyVersion)
	snapshot.EvidenceVersion = app.EvidenceVersion(evidenceVersion)
	when, err := parseTime(createdAt)
	if err != nil {
		return app.ReviewSnapshot{}, app.ErrReviewSnapshotCorrupt
	}
	snapshot.CreatedAt = when
	if err := snapshot.Validate(); err != nil {
		return app.ReviewSnapshot{}, app.ErrReviewSnapshotCorrupt
	}
	return snapshot, nil
}

// DeleteReviewSnapshot removes only the durable row; the workspace owner must
// prove and remove the matching filesystem root before calling it.
func (s *Store) DeleteReviewSnapshot(ctx context.Context, id domain.ReviewSnapshotID) error {
	if err := s.ensureOpen(); err != nil {
		return err
	}
	if id == "" {
		return app.ErrInvalidReviewSnapshot
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	result, err := s.db.ExecContext(ctx, "DELETE FROM review_snapshots WHERE id = ?", id)
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count != 1 {
		return app.ErrReviewSnapshotNotFound
	}
	return nil
}

// SaveReviewSnapshotLease records a live read lease after the workspace owner
// has validated the marker and native lock.
func (s *Store) SaveReviewSnapshotLease(ctx context.Context, lease app.ReviewSnapshotLease) error {
	if err := s.ensureOpen(); err != nil {
		return err
	}
	if err := lease.Validate(); err != nil {
		return err
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	_, err := s.db.ExecContext(ctx, `INSERT INTO review_snapshot_leases(
		id, snapshot_id, capture_id, root, manifest_hash, process_nonce, acquired_at
	) VALUES(?, ?, ?, ?, ?, ?, ?)`, lease.ID, lease.SnapshotID, nullableID(string(lease.CaptureID)),
		lease.Root, lease.ManifestHash, lease.ProcessNonce, formatTime(lease.AcquiredAt))
	if err != nil {
		if isSQLiteConstraint(err) {
			return app.ErrReviewSnapshotBusy
		}
		return err
	}
	return nil
}

// ReleaseReviewSnapshotLease closes one lease. A repeated release is
// idempotent so shutdown paths do not need to recover an already closed row.
func (s *Store) ReleaseReviewSnapshotLease(ctx context.Context, id domain.ReviewSnapshotLeaseID) error {
	if err := s.ensureOpen(); err != nil {
		return err
	}
	if id == "" {
		return app.ErrInvalidReviewSnapshot
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	result, err := s.db.ExecContext(ctx, `UPDATE review_snapshot_leases
		SET released_at = COALESCE(released_at, ?)
		WHERE id = ?`, formatTime(app.SystemClock{}.Now()), id)
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count != 1 {
		return app.ErrReviewSnapshotNotFound
	}
	return nil
}

// CountReviewSnapshotLeases returns active leases only.
func (s *Store) CountReviewSnapshotLeases(ctx context.Context, id domain.ReviewSnapshotID) (app.Count, error) {
	if err := s.ensureOpen(); err != nil {
		return 0, err
	}
	if id == "" {
		return 0, app.ErrInvalidReviewSnapshot
	}
	var count int64
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM review_snapshot_leases
		WHERE snapshot_id = ? AND released_at IS NULL`, id).Scan(&count); err != nil {
		return 0, err
	}
	if count < 0 {
		return 0, app.ErrReviewSnapshotCorrupt
	}
	return app.Count(count), nil
}
