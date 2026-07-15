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

var _ app.ApplyOperationStore = (*Store)(nil)
var _ app.ApplyOperationStoreTx = (*transaction)(nil)

// LoadApplyOperationByKey returns the immutable prepared operation for one
// session-scoped idempotency key.
func (s *Store) LoadApplyOperationByKey(ctx context.Context, sessionID domain.ReviewSessionID, key string) (app.ApplyOperation, error) {
	if err := s.ensureOpen(); err != nil {
		return app.ApplyOperation{}, err
	}
	if sessionID == "" || key == "" {
		return app.ApplyOperation{}, app.ErrInvalidApplyPreflight
	}
	return s.loadApplyOperation(ctx, "SELECT operation_json FROM apply_operations WHERE session_id = ? AND idempotency_key = ?", sessionID, key)
}

// LoadApplyOperationForProposal returns the immutable operation for one
// proposal version, regardless of the request's idempotency key.
func (s *Store) LoadApplyOperationForProposal(ctx context.Context, proposalID domain.ProposalID, version review.ProposalVersionNumber) (app.ApplyOperation, error) {
	if err := s.ensureOpen(); err != nil {
		return app.ApplyOperation{}, err
	}
	if proposalID == "" || version == 0 {
		return app.ApplyOperation{}, app.ErrInvalidApplyPreflight
	}
	return s.loadApplyOperation(ctx, "SELECT operation_json FROM apply_operations WHERE proposal_id = ? AND proposal_version = ?", proposalID, version)
}

func (s *Store) loadApplyOperation(ctx context.Context, query string, args ...any) (app.ApplyOperation, error) {
	var data []byte
	if err := s.db.QueryRowContext(ctx, query, args...).Scan(&data); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return app.ApplyOperation{}, app.ErrApplyOperationNotFound
		}
		return app.ApplyOperation{}, err
	}
	return decodeApplyOperation(data)
}

// PrepareApplyOperation records one prepared operation and makes both
// idempotency dimensions durable under the session writer fence.
func (t *transaction) PrepareApplyOperation(ctx context.Context, operation app.ApplyOperation) error {
	if operation.Validate() != nil || operation.SessionID != t.sessionID || operation.Phase != app.ApplyOperationPrepared {
		return app.ErrInvalidApplyPreflight
	}
	if err := t.checkProposalOwnership(ctx, operation.ProposalID, operation.WorkspaceID, operation.ThreadID); err != nil {
		return err
	}
	data, err := json.Marshal(operation)
	if err != nil {
		return err
	}
	if existing, found, err := t.loadApplyOperation(ctx, "SELECT operation_json FROM apply_operations WHERE id = ?", operation.ID); err != nil {
		return err
	} else if found {
		return compareApplyOperation(existing, data)
	}
	if existing, found, err := t.loadApplyOperation(ctx, "SELECT operation_json FROM apply_operations WHERE session_id = ? AND idempotency_key = ?", operation.SessionID, operation.IdempotencyKey); err != nil {
		return err
	} else if found {
		return compareApplyOperation(existing, data)
	}
	if existing, found, err := t.loadApplyOperation(ctx, "SELECT operation_json FROM apply_operations WHERE proposal_id = ? AND proposal_version = ?", operation.ProposalID, operation.ProposalVersion); err != nil {
		return err
	} else if found {
		return compareApplyOperation(existing, data)
	}
	_, err = t.tx.ExecContext(ctx, `INSERT INTO apply_operations(
		id, session_id, proposal_id, workspace_id, thread_id, proposal_version,
		idempotency_key, phase, operation_json, created_at, prepared_at)
		VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, operation.ID, operation.SessionID, operation.ProposalID,
		operation.WorkspaceID, operation.ThreadID, operation.ProposalVersion, operation.IdempotencyKey,
		string(operation.Phase), data, formatTime(operation.CreatedAt), formatTime(operation.PreparedAt))
	if err == nil {
		return nil
	}
	if existing, found, loadErr := t.loadApplyOperation(ctx, "SELECT operation_json FROM apply_operations WHERE session_id = ? AND idempotency_key = ?", operation.SessionID, operation.IdempotencyKey); loadErr == nil && found {
		return compareApplyOperation(existing, data)
	}
	return err
}

func (t *transaction) loadApplyOperation(ctx context.Context, query string, args ...any) (app.ApplyOperation, bool, error) {
	var data []byte
	if err := t.tx.QueryRowContext(ctx, query, args...).Scan(&data); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return app.ApplyOperation{}, false, nil
		}
		return app.ApplyOperation{}, false, err
	}
	operation, err := decodeApplyOperation(data)
	if err != nil {
		return app.ApplyOperation{}, false, err
	}
	return operation, true, nil
}

func decodeApplyOperation(data []byte) (app.ApplyOperation, error) {
	var operation app.ApplyOperation
	if json.Unmarshal(data, &operation) != nil || operation.Validate() != nil {
		return app.ApplyOperation{}, app.ErrReviewStoreCorrupt
	}
	return operation, nil
}

func compareApplyOperation(existing app.ApplyOperation, encoded []byte) error {
	data, err := json.Marshal(existing)
	if err != nil {
		return err
	}
	if bytes.Equal(data, encoded) {
		return nil
	}
	return app.ErrApplyOperationConflict
}
