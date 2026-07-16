package sqlite

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/Scottlr/nudge/internal/app"
	"github.com/Scottlr/nudge/internal/domain"
	"github.com/Scottlr/nudge/internal/domain/review"
)

const sessionLeaseRepairMarkerPrefix = "repair-stale-"

// SaveSessionLeaseIdentity records the exact native lock identity selected by
// the platform adapter. It advances the ordinary session revision under the
// same writer fence; an absent identity remains deliberately non-repairable.
func (s *Store) SaveSessionLeaseIdentity(ctx context.Context, guard app.SessionWriteGuard, identity string, distinct bool) (app.SessionWriteGuard, error) {
	if err := s.ensureOpen(); err != nil {
		return app.SessionWriteGuard{}, err
	}
	if guard.Validate() != nil || !app.ValidateSessionLeaseLockIdentity(identity) {
		return app.SessionWriteGuard{}, app.ErrReviewStoreInput
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return app.SessionWriteGuard{}, err
	}
	defer func() { _ = tx.Rollback() }()
	if err := verifyGuard(ctx, tx, guard); err != nil {
		return app.SessionWriteGuard{}, err
	}
	result, err := tx.ExecContext(ctx, `UPDATE review_sessions
		SET writer_lock_identity = ?, writer_lock_distinct = ?, revision = revision + 1
		WHERE id = ? AND writer_lease_id = ? AND writer_epoch = ? AND revision = ? AND closed_at IS NULL`,
		identity, boolInt(distinct), string(guard.SessionID), string(guard.LeaseID), guard.WriterEpoch, guard.ExpectedRevision)
	if err != nil {
		return app.SessionWriteGuard{}, err
	}
	if affected, err := result.RowsAffected(); err != nil || affected != 1 {
		return app.SessionWriteGuard{}, app.ErrSessionLeaseLost
	}
	if err := tx.Commit(); err != nil {
		return app.SessionWriteGuard{}, err
	}
	guard.ExpectedRevision++
	return guard, nil
}

// ListSessionLeaseRepairCandidates returns only active rows carrying the
// persisted lock identity required for an authoritative native-lock proof.
func (s *Store) ListSessionLeaseRepairCandidates(ctx context.Context) ([]app.SessionLeaseRepairCandidate, error) {
	if err := s.ensureOpen(); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id, repository_id, worktree_id,
		session_key_json, writer_lock_identity, writer_lock_distinct, writer_epoch,
		writer_lease_id, revision
		FROM review_sessions
		WHERE closed_at IS NULL AND writer_lock_identity <> ''
		AND writer_lease_id NOT LIKE ?
		ORDER BY id ASC LIMIT 128`, sessionLeaseRepairMarkerPrefix+"%")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]app.SessionLeaseRepairCandidate, 0)
	for rows.Next() {
		item, err := scanSessionLeaseRepairCandidate(rows)
		if err != nil {
			return nil, err
		}
		item.State = app.SessionLeaseStateRecorded
		if err := item.Validate(); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return items, nil
}

// LoadSessionLeaseRepairCandidate rereads one row for plan revalidation.
func (s *Store) LoadSessionLeaseRepairCandidate(ctx context.Context, sessionID domain.ReviewSessionID) (app.SessionLeaseRepairCandidate, error) {
	if err := s.ensureOpen(); err != nil {
		return app.SessionLeaseRepairCandidate{}, err
	}
	if sessionID == "" {
		return app.SessionLeaseRepairCandidate{}, app.ErrReviewStoreInput
	}
	row := s.db.QueryRowContext(ctx, `SELECT id, repository_id, worktree_id,
		session_key_json, writer_lock_identity, writer_lock_distinct, writer_epoch,
		writer_lease_id, revision
		FROM review_sessions WHERE id = ?`, string(sessionID))
	item, err := scanSessionLeaseRepairCandidate(row)
	if errors.Is(err, sql.ErrNoRows) {
		return app.SessionLeaseRepairCandidate{}, app.ErrReviewStoreNotFound
	}
	if err != nil {
		return app.SessionLeaseRepairCandidate{}, err
	}
	if strings.HasPrefix(string(item.LeaseID), sessionLeaseRepairMarkerPrefix) {
		item.State = app.SessionLeaseStateRepaired
	} else {
		item.State = app.SessionLeaseStateRecorded
	}
	if err := item.Validate(); err != nil {
		return app.SessionLeaseRepairCandidate{}, app.ErrReviewStoreCorrupt
	}
	return item, nil
}

// RepairStaleSessionLease performs the one fenced durable transition. The
// caller must already hold the exact native session lock.
func (s *Store) RepairStaleSessionLease(ctx context.Context, request app.SessionLeaseRepairRequest) (app.SessionLeaseRepairResult, error) {
	if err := s.ensureOpen(); err != nil {
		return app.SessionLeaseRepairResult{}, err
	}
	if request.Validate() != nil {
		return app.SessionLeaseRepairResult{}, app.ErrSessionLeaseRepairProof
	}
	keyJSON, err := json.Marshal(request.Candidate.Key)
	if err != nil {
		return app.SessionLeaseRepairResult{}, err
	}
	keyHash := sha256.Sum256(keyJSON)
	worktree := nullableID(string(request.Candidate.WorktreeID))
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return app.SessionLeaseRepairResult{}, err
	}
	defer func() { _ = tx.Rollback() }()
	var repositoryID, storedKeyHash, lockIdentity, writerLease string
	var storedWorktree sql.NullString
	var distinct, epoch, revision int64
	var closed sql.NullString
	if err := tx.QueryRowContext(ctx, `SELECT repository_id, worktree_id, session_key_hash,
		writer_lock_identity, writer_lock_distinct, writer_epoch, writer_lease_id,
		revision, closed_at FROM review_sessions WHERE id = ?`, string(request.Candidate.SessionID)).Scan(
		&repositoryID, &storedWorktree, &storedKeyHash, &lockIdentity, &distinct,
		&epoch, &writerLease, &revision, &closed); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return app.SessionLeaseRepairResult{}, app.ErrReviewStoreNotFound
		}
		return app.SessionLeaseRepairResult{}, err
	}
	if repositoryID != string(request.Candidate.RepositoryID) || nullableString(storedWorktree) != string(request.Candidate.WorktreeID) || storedKeyHash != hex.EncodeToString(keyHash[:]) || lockIdentity != request.Candidate.LockIdentity || distinct != int64(boolInt(request.Candidate.Distinct)) {
		return app.SessionLeaseRepairResult{}, app.ErrRepairPreconditions
	}
	marker := string(request.RepairLeaseID)
	if writerLease == marker && epoch == int64(request.Candidate.WriterEpoch)+1 && revision == int64(request.Candidate.SessionRevision)+1 && !closed.Valid {
		if err := tx.Commit(); err != nil {
			return app.SessionLeaseRepairResult{}, err
		}
		return app.SessionLeaseRepairResult{AlreadyRepaired: true, WriterEpoch: uint64(epoch), SessionRevision: uint64(revision)}, nil
	}
	if closed.Valid || writerLease != string(request.Candidate.LeaseID) || epoch != int64(request.Candidate.WriterEpoch) || revision != int64(request.Candidate.SessionRevision) || request.Candidate.LeaseRevision != request.Candidate.SessionRevision {
		return app.SessionLeaseRepairResult{}, app.ErrRepairPreconditions
	}
	result, err := tx.ExecContext(ctx, `UPDATE review_sessions
		SET writer_epoch = ?, writer_lease_id = ?, revision = ?
		WHERE id = ? AND repository_id = ? AND worktree_id IS ?
		AND session_key_hash = ? AND writer_lock_identity = ?
		AND writer_lock_distinct = ? AND writer_lease_id = ?
		AND writer_epoch = ? AND revision = ? AND closed_at IS NULL`,
		request.Candidate.WriterEpoch+1, marker, request.Candidate.SessionRevision+1,
		string(request.Candidate.SessionID), string(request.Candidate.RepositoryID), worktree,
		storedKeyHash, request.Candidate.LockIdentity, boolInt(request.Candidate.Distinct),
		string(request.Candidate.LeaseID), request.Candidate.WriterEpoch, request.Candidate.SessionRevision)
	if err != nil {
		return app.SessionLeaseRepairResult{}, err
	}
	if affected, err := result.RowsAffected(); err != nil || affected != 1 {
		return app.SessionLeaseRepairResult{}, app.ErrRepairPreconditions
	}
	if err := tx.Commit(); err != nil {
		return app.SessionLeaseRepairResult{}, err
	}
	return app.SessionLeaseRepairResult{WriterEpoch: request.Candidate.WriterEpoch + 1, SessionRevision: request.Candidate.SessionRevision + 1}, nil
}

func scanSessionLeaseRepairCandidate(row interface{ Scan(...any) error }) (app.SessionLeaseRepairCandidate, error) {
	var id, repositoryID, lockIdentity, writerLease, keyJSON string
	var worktree sql.NullString
	var distinct, epoch, revision int64
	if err := row.Scan(&id, &repositoryID, &worktree, &keyJSON, &lockIdentity, &distinct, &epoch, &writerLease, &revision); err != nil {
		return app.SessionLeaseRepairCandidate{}, err
	}
	var key review.SessionKey
	if err := json.Unmarshal([]byte(keyJSON), &key); err != nil {
		return app.SessionLeaseRepairCandidate{}, app.ErrReviewStoreCorrupt
	}
	return app.SessionLeaseRepairCandidate{
		SessionID:       domain.ReviewSessionID(id),
		RepositoryID:    domain.RepositoryID(repositoryID),
		WorktreeID:      domain.WorktreeID(nullableString(worktree)),
		Key:             key,
		LockIdentity:    lockIdentity,
		Distinct:        distinct != 0,
		LeaseID:         domain.SessionLeaseID(writerLease),
		FencingToken:    writerLease,
		WriterEpoch:     uint64(epoch),
		LeaseRevision:   uint64(revision),
		SessionRevision: uint64(revision),
	}, nil
}

func nullableString(value sql.NullString) string {
	if !value.Valid {
		return ""
	}
	return value.String
}

// InspectReadOnlySessionLeaseCandidates is the query-only T049 evidence
// source. It never takes a native lock and returns no candidate for an
// unavailable, missing, outdated, or corrupt database.
func InspectReadOnlySessionLeaseCandidates(ctx context.Context, path string) ([]app.SessionLeaseRepairCandidate, error) {
	health, err := InspectReadOnly(ctx, path)
	if err != nil || health.State != ReadOnlyDatabaseCurrent {
		return nil, err
	}
	dsn := fmt.Sprintf("file:%s?mode=ro&immutable=1&_pragma=foreign_keys(1)&_pragma=query_only(1)&_pragma=busy_timeout(%d)", filepath.ToSlash(path), (2 * 1000))
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	rows, err := db.QueryContext(ctx, `SELECT id, repository_id, worktree_id,
		session_key_json, writer_lock_identity, writer_lock_distinct, writer_epoch,
		writer_lease_id, revision
		FROM review_sessions
		WHERE closed_at IS NULL AND writer_lock_identity <> ''
		AND writer_lease_id NOT LIKE ? ORDER BY id ASC LIMIT 128`, sessionLeaseRepairMarkerPrefix+"%")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]app.SessionLeaseRepairCandidate, 0)
	for rows.Next() {
		item, err := scanSessionLeaseRepairCandidate(rows)
		if err != nil {
			return nil, err
		}
		item.State = app.SessionLeaseStateRecorded
		if err := item.Validate(); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

var _ app.SessionLeaseIdentityStore = (*Store)(nil)
var _ app.SessionLeaseRepairStore = (*Store)(nil)
