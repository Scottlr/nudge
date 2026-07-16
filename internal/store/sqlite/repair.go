package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"

	"github.com/Scottlr/nudge/internal/app"
	"github.com/Scottlr/nudge/internal/domain"
)

// SaveRepairPlan persists one immutable plan. Re-advertising the same
// identity is idempotent only when every plan field is unchanged.
func (s *Store) SaveRepairPlan(ctx context.Context, plan app.RepairPlan) error {
	if err := s.ensureOpen(); err != nil {
		return err
	}
	if err := plan.Validate(); err != nil {
		return err
	}
	refs, err := json.Marshal(plan.OwnedResourceRefs)
	if err != nil {
		return err
	}
	existing, loadErr := s.LoadRepairPlan(ctx, plan.ID)
	if loadErr == nil {
		if sameRepairPlan(existing, plan) {
			return nil
		}
		return app.ErrRepairPlanConflict
	}
	if !errors.Is(loadErr, app.ErrRepairPlanNotFound) {
		return loadErr
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	_, err = s.db.ExecContext(ctx, `INSERT INTO repair_plans(
		id, health_code, health_revision, policy_version, summary, effect,
		owned_resource_refs_json, preconditions_hash, confirmation_text,
		handler_kind, handler_version, created_at, expires_at)
		VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		string(plan.ID), string(plan.HealthCode), plan.HealthRevision, plan.PolicyVersion,
		plan.Summary, plan.Effect, string(refs), plan.PreconditionsHash,
		plan.ConfirmationText, string(plan.HandlerKind), plan.HandlerVersion,
		formatTime(plan.CreatedAt), formatTime(plan.ExpiresAt))
	if err != nil {
		if stringsContainsConstraint(err) {
			return app.ErrRepairPlanConflict
		}
		return err
	}
	return nil
}

// LoadRepairPlan returns the exact redacted plan identity.
func (s *Store) LoadRepairPlan(ctx context.Context, id app.RepairPlanID) (app.RepairPlan, error) {
	if err := s.ensureOpen(); err != nil {
		return app.RepairPlan{}, err
	}
	if id == "" {
		return app.RepairPlan{}, app.ErrInvalidRepairPlan
	}
	var plan app.RepairPlan
	var planID, healthCode, refs, handlerKind, createdAt, expiresAt string
	if err := s.db.QueryRowContext(ctx, `SELECT id, health_code, health_revision,
		policy_version, summary, effect, owned_resource_refs_json,
		preconditions_hash, confirmation_text, handler_kind, handler_version,
		created_at, expires_at FROM repair_plans WHERE id = ?`, string(id)).Scan(
		&planID, &healthCode, &plan.HealthRevision, &plan.PolicyVersion, &plan.Summary,
		&plan.Effect, &refs, &plan.PreconditionsHash, &plan.ConfirmationText,
		&handlerKind, &plan.HandlerVersion, &createdAt, &expiresAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return app.RepairPlan{}, app.ErrRepairPlanNotFound
		}
		return app.RepairPlan{}, err
	}
	if err := json.Unmarshal([]byte(refs), &plan.OwnedResourceRefs); err != nil {
		return app.RepairPlan{}, app.ErrInvalidRepairPlan
	}
	plan.ID = app.RepairPlanID(planID)
	plan.HealthCode = app.HealthCode(healthCode)
	plan.HandlerKind = app.RepairHandlerKind(handlerKind)
	plan.CreatedAt, _ = parseTime(createdAt)
	plan.ExpiresAt, _ = parseTime(expiresAt)
	if err := plan.Validate(); err != nil {
		return app.RepairPlan{}, app.ErrRepairPlanNotFound
	}
	return plan, nil
}

// SaveRepairOperation persists one lifecycle phase and never changes its
// immutable plan or idempotency identity.
func (s *Store) SaveRepairOperation(ctx context.Context, operation app.RepairOperation) error {
	if err := s.ensureOpen(); err != nil {
		return err
	}
	if err := operation.Validate(); err != nil {
		return err
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	_, err := s.db.ExecContext(ctx, `INSERT INTO repair_operations(
		id, plan_id, handler_kind, handler_version, health_revision,
		idempotency_key, phase, outcome, error_code, preconditions_hash,
		lock_proof, journal_id, effect_id, postcondition_hash, created_at, updated_at)
		VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
		phase=excluded.phase, outcome=excluded.outcome, error_code=excluded.error_code,
		preconditions_hash=excluded.preconditions_hash, lock_proof=excluded.lock_proof,
		journal_id=excluded.journal_id, effect_id=excluded.effect_id,
		postcondition_hash=excluded.postcondition_hash, updated_at=excluded.updated_at
		WHERE repair_operations.plan_id = excluded.plan_id
		AND repair_operations.handler_kind = excluded.handler_kind
		AND repair_operations.handler_version = excluded.handler_version
		AND repair_operations.health_revision = excluded.health_revision
		AND repair_operations.idempotency_key = excluded.idempotency_key`,
		string(operation.ID), string(operation.PlanID), string(operation.HandlerKind), operation.HandlerVersion,
		operation.HealthRevision, operation.IdempotencyKey, string(operation.Phase), string(operation.Outcome),
		operation.ErrorCode, operation.PreconditionsHash, operation.LockProof, operation.JournalID,
		operation.EffectID, operation.PostconditionHash, formatTime(operation.CreatedAt), formatTime(operation.UpdatedAt))
	if err != nil {
		if stringsContainsConstraint(err) {
			return app.ErrRepairPlanConflict
		}
		return err
	}
	return nil
}

// LoadRepairOperation loads one operation by its stable operation identity.
func (s *Store) LoadRepairOperation(ctx context.Context, id domain.OperationID) (app.RepairOperation, error) {
	if err := s.ensureOpen(); err != nil {
		return app.RepairOperation{}, err
	}
	if id == "" {
		return app.RepairOperation{}, app.ErrInvalidRepairOperation
	}
	return s.loadRepairOperation(ctx, "id", string(id))
}

// LoadRepairOperationByIdempotency returns the one lifecycle bound to a
// caller-provided retry identity.
func (s *Store) LoadRepairOperationByIdempotency(ctx context.Context, key string) (app.RepairOperation, error) {
	if err := s.ensureOpen(); err != nil {
		return app.RepairOperation{}, err
	}
	if key == "" {
		return app.RepairOperation{}, app.ErrInvalidRepairRequest
	}
	return s.loadRepairOperation(ctx, "idempotency_key", key)
}

func (s *Store) loadRepairOperation(ctx context.Context, column, value string) (app.RepairOperation, error) {
	query := `SELECT id, plan_id, handler_kind, handler_version, health_revision,
		idempotency_key, phase, outcome, error_code, preconditions_hash, lock_proof,
		journal_id, effect_id, postcondition_hash, created_at, updated_at
		FROM repair_operations WHERE ` + column + ` = ?`
	var operation app.RepairOperation
	var id, planID, handlerKind, phase, outcome, createdAt, updatedAt string
	if err := s.db.QueryRowContext(ctx, query, value).Scan(
		&id, &planID, &handlerKind, &operation.HandlerVersion, &operation.HealthRevision,
		&operation.IdempotencyKey, &phase, &outcome, &operation.ErrorCode,
		&operation.PreconditionsHash, &operation.LockProof, &operation.JournalID,
		&operation.EffectID, &operation.PostconditionHash, &createdAt, &updatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return app.RepairOperation{}, app.ErrRepairOperationNotFound
		}
		return app.RepairOperation{}, err
	}
	operation.Version = app.RepairFrameworkVersion
	operation.ID = domain.OperationID(id)
	operation.PlanID = app.RepairPlanID(planID)
	operation.HandlerKind = app.RepairHandlerKind(handlerKind)
	operation.Phase = app.RepairPhase(phase)
	operation.Outcome = app.RepairOutcome(outcome)
	operation.CreatedAt, _ = parseTime(createdAt)
	operation.UpdatedAt, _ = parseTime(updatedAt)
	if err := operation.Validate(); err != nil {
		return app.RepairOperation{}, app.ErrRepairOperationNotFound
	}
	return operation, nil
}

// AppendRepairAudit appends one redacted phase record.
func (s *Store) AppendRepairAudit(ctx context.Context, audit app.RepairAuditEntry) error {
	if err := s.ensureOpen(); err != nil {
		return err
	}
	if err := audit.Validate(); err != nil {
		return err
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	var count int
	if err := s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM repair_audit WHERE operation_id = ?", string(audit.OperationID)).Scan(&count); err != nil {
		return err
	}
	if count >= app.MaxRepairAuditEntries {
		return app.ErrInvalidRepairAudit
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO repair_audit(operation_id, plan_id, phase, code, at) VALUES(?, ?, ?, ?, ?)`, string(audit.OperationID), string(audit.PlanID), string(audit.Phase), audit.Code, formatTime(audit.At))
	return err
}

func sameRepairPlan(left, right app.RepairPlan) bool {
	leftJSON, leftErr := json.Marshal(left)
	rightJSON, rightErr := json.Marshal(right)
	return leftErr == nil && rightErr == nil && string(leftJSON) == string(rightJSON)
}

func stringsContainsConstraint(err error) bool {
	return err != nil && (strings.Contains(err.Error(), "UNIQUE constraint failed") || strings.Contains(err.Error(), "constraint failed"))
}
