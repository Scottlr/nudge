package sqlite

import (
	"context"
	"database/sql"
	"errors"

	"github.com/Scottlr/nudge/internal/app"
	"github.com/Scottlr/nudge/internal/domain"
	moderncsqlite "modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

var _ app.RepositoryPreferenceStore = (*Store)(nil)

// LoadBaseBranchPreference loads the preference for one verified Nudge
// repository binding. A missing row is distinct from a corrupt saved row.
func (s *Store) LoadBaseBranchPreference(ctx context.Context, repositoryID domain.RepositoryID) (*app.BaseBranchPreference, error) {
	if err := s.ensureOpen(); err != nil {
		return nil, err
	}
	if repositoryID == "" {
		return nil, app.ErrReviewStoreInput
	}
	var expression, updatedAt string
	var revision int64
	err := s.db.QueryRowContext(ctx, `SELECT expression, revision, updated_at
		FROM repository_base_preferences WHERE repository_id = ?`, repositoryID).Scan(&expression, &revision, &updatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, app.ErrReviewStoreNotFound
	}
	if err != nil {
		return nil, err
	}
	if revision <= 0 {
		return nil, &app.SavedBaseUnavailableError{Expression: expression, Cause: app.ErrReviewStoreCorrupt}
	}
	when, err := parseTime(updatedAt)
	if err != nil {
		return nil, &app.SavedBaseUnavailableError{Expression: expression, Cause: app.ErrReviewStoreCorrupt}
	}
	preference := &app.BaseBranchPreference{
		RepositoryID: domain.RepositoryID(repositoryID),
		Expression:   expression,
		Revision:     uint64(revision),
		UpdatedAt:    when,
	}
	if err := preference.Validate(); err != nil {
		return nil, &app.SavedBaseUnavailableError{Expression: expression, Cause: err}
	}
	return preference, nil
}

// SaveBaseBranchPreference persists one explicitly accepted raw expression.
// The preference revision is the next revision and expectedRevision is the
// caller's observed revision: zero creates the row, otherwise the existing
// row must still have the expected revision.
func (s *Store) SaveBaseBranchPreference(ctx context.Context, preference app.BaseBranchPreference, expectedRevision uint64) error {
	if err := s.ensureOpen(); err != nil {
		return err
	}
	if err := preference.Validate(); err != nil || expectedRevision == ^uint64(0) || preference.Revision != expectedRevision+1 {
		return app.ErrReviewStoreInput
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	var exists int
	if err := tx.QueryRowContext(ctx, "SELECT 1 FROM repositories WHERE id = ?", preference.RepositoryID).Scan(&exists); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return app.ErrReviewStoreNotFound
		}
		return err
	}
	if expectedRevision == 0 {
		_, err = tx.ExecContext(ctx, `INSERT INTO repository_base_preferences(repository_id, expression, revision, updated_at)
			VALUES(?, ?, ?, ?)`, preference.RepositoryID, preference.Expression, preference.Revision, formatTime(preference.UpdatedAt))
		if err != nil {
			if isSQLiteConstraint(err) {
				return app.ErrPreferenceRevisionConflict
			}
			return err
		}
	} else {
		result, updateErr := tx.ExecContext(ctx, `UPDATE repository_base_preferences
			SET expression = ?, revision = ?, updated_at = ?
			WHERE repository_id = ? AND revision = ?`, preference.Expression, preference.Revision, formatTime(preference.UpdatedAt), preference.RepositoryID, expectedRevision)
		if updateErr != nil {
			return updateErr
		}
		count, countErr := result.RowsAffected()
		if countErr != nil {
			return countErr
		}
		if count != 1 {
			return app.ErrPreferenceRevisionConflict
		}
	}
	return tx.Commit()
}

func isSQLiteConstraint(err error) bool {
	var sqliteErr *moderncsqlite.Error
	return errors.As(err, &sqliteErr) && sqliteErr.Code()&0xff == sqlite3.SQLITE_CONSTRAINT
}

// ClearBaseBranchPreference removes one repository binding's saved
// expression. Clearing an already-empty row with expected revision zero is
// idempotent; an observed non-zero revision remains CAS-protected.
func (s *Store) ClearBaseBranchPreference(ctx context.Context, repositoryID domain.RepositoryID, expectedRevision uint64) error {
	if err := s.ensureOpen(); err != nil {
		return err
	}
	if repositoryID == "" {
		return app.ErrReviewStoreInput
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	var exists int
	if err := tx.QueryRowContext(ctx, "SELECT 1 FROM repositories WHERE id = ?", repositoryID).Scan(&exists); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return app.ErrReviewStoreNotFound
		}
		return err
	}
	var current int64
	err = tx.QueryRowContext(ctx, "SELECT revision FROM repository_base_preferences WHERE repository_id = ?", repositoryID).Scan(&current)
	if errors.Is(err, sql.ErrNoRows) {
		if expectedRevision == 0 {
			return tx.Commit()
		}
		return app.ErrPreferenceRevisionConflict
	}
	if err != nil {
		return err
	}
	if current <= 0 || uint64(current) != expectedRevision {
		return app.ErrPreferenceRevisionConflict
	}
	result, err := tx.ExecContext(ctx, "DELETE FROM repository_base_preferences WHERE repository_id = ? AND revision = ?", repositoryID, expectedRevision)
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count != 1 {
		return app.ErrPreferenceRevisionConflict
	}
	return tx.Commit()
}
