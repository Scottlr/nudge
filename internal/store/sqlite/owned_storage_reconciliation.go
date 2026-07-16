package sqlite

import (
	"context"
	"database/sql"
	"encoding/hex"
	"errors"
	"math"
	"strings"

	"github.com/Scottlr/nudge/internal/app"
	"github.com/Scottlr/nudge/internal/domain"
)

var _ app.OwnedStorageReconcileLedger = (*Store)(nil)
var _ app.OwnedStorageReconciliationStore = (*Store)(nil)

const (
	storageArtifactCursor    = "artifact:"
	storageReservationCursor = "reservation:"
)

// ReconciliationPage returns one keyset-bounded, filesystem-free view of the
// ledger. Artifact rows are followed by active reservations so the cursor is
// stable even when creation timestamps collide.
func (s *Store) ReconciliationPage(ctx context.Context, query app.OwnedStorageLedgerPageQuery) (app.OwnedStorageLedgerPage, error) {
	if err := s.ensureOpen(); err != nil {
		return app.OwnedStorageLedgerPage{}, err
	}
	if query.Limit == 0 {
		query.Limit = app.DefaultOwnedStorageReconcileItems
	}
	if err := query.Validate(); err != nil {
		return app.OwnedStorageLedgerPage{}, err
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return app.OwnedStorageLedgerPage{}, err
	}
	defer func() { _ = tx.Rollback() }()
	global, repository, err := loadLedgerTotals(tx, query.RepositoryID)
	if err != nil {
		return app.OwnedStorageLedgerPage{}, err
	}
	page := app.OwnedStorageLedgerPage{Revision: global.Revision, Global: global}
	if repository != nil {
		page.Repository = *repository
	}
	page.ActiveReservations, err = countReconciliationReservations(ctx, tx, query.RepositoryID)
	if err != nil {
		return app.OwnedStorageLedgerPage{}, err
	}
	page.Pressure = storagePressure(page.Repository, page.Global)
	kind, value, err := storageReconciliationCursor(query.Cursor)
	if err != nil {
		return app.OwnedStorageLedgerPage{}, err
	}
	switch kind {
	case storageArtifactCursor:
		page.Artifacts, page.Complete, err = listReconciliationArtifacts(ctx, tx, query.RepositoryID, value, query.Limit)
		if err != nil {
			return app.OwnedStorageLedgerPage{}, err
		}
		if !page.Complete {
			page.NextCursor = storageArtifactCursor + page.Artifacts[len(page.Artifacts)-1].ArtifactID
			page.Complete = false
		} else {
			remaining := query.Limit - uint32(len(page.Artifacts))
			if remaining == 0 {
				page.NextCursor = storageReservationCursor
				page.Complete = false
				break
			}
			page.Reservations, page.Complete, err = listReconciliationReservations(ctx, tx, query.RepositoryID, "", remaining)
			if err != nil {
				return app.OwnedStorageLedgerPage{}, err
			}
			if !page.Complete {
				page.NextCursor = storageReservationCursor + page.Reservations[len(page.Reservations)-1].ReservationID
			}
		}
	case storageReservationCursor:
		page.Reservations, page.Complete, err = listReconciliationReservations(ctx, tx, query.RepositoryID, value, query.Limit)
		if err != nil {
			return app.OwnedStorageLedgerPage{}, err
		}
		if !page.Complete {
			page.NextCursor = storageReservationCursor + page.Reservations[len(page.Reservations)-1].ReservationID
		}
	default:
		return app.OwnedStorageLedgerPage{}, app.ErrInvalidOwnedStorageReconcile
	}
	if err := tx.Commit(); err != nil {
		return app.OwnedStorageLedgerPage{}, err
	}
	return page, nil
}

func countReconciliationReservations(ctx context.Context, tx *sql.Tx, repositoryID *domain.RepositoryID) (app.Count, error) {
	query := `SELECT COUNT(*) FROM capacity_reservations WHERE state = 'active'`
	args := []any{}
	if repositoryID != nil {
		query += " AND repository_id = ?"
		args = append(args, string(*repositoryID))
	}
	var count int64
	if err := tx.QueryRowContext(ctx, query, args...).Scan(&count); err != nil || count < 0 {
		if err != nil {
			return 0, err
		}
		return 0, app.ErrReviewStoreCorrupt
	}
	return app.Count(count), nil
}

func storageReconciliationCursor(cursor string) (string, string, error) {
	if cursor == "" {
		return storageArtifactCursor, "", nil
	}
	if strings.HasPrefix(cursor, storageArtifactCursor) {
		return storageArtifactCursor, strings.TrimPrefix(cursor, storageArtifactCursor), nil
	}
	if strings.HasPrefix(cursor, storageReservationCursor) {
		return storageReservationCursor, strings.TrimPrefix(cursor, storageReservationCursor), nil
	}
	return "", "", app.ErrInvalidOwnedStorageReconcile
}

func listReconciliationArtifacts(ctx context.Context, tx *sql.Tx, repositoryID *domain.RepositoryID, after string, limit uint32) ([]app.OwnedArtifact, bool, error) {
	query := `SELECT artifact_id, owner_kind, owner_id, operation_id, reservation_id, repository_id,
		class, lifecycle, logical_bytes, observed_bytes, charged_bytes, volume_id, manifest_hash,
		accounting_version, policy_version, complete, created_at
		FROM owned_artifact_ledger WHERE artifact_id > ?`
	args := []any{after}
	if repositoryID != nil {
		query += " AND repository_id = ?"
		args = append(args, string(*repositoryID))
	}
	query += " ORDER BY artifact_id ASC LIMIT ?"
	args = append(args, int64(limit)+1)
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	result := make([]app.OwnedArtifact, 0, limit)
	complete := true
	for rows.Next() {
		if uint32(len(result)) == limit {
			complete = false
			break
		}
		artifact, err := scanReconciliationArtifact(rows)
		if err != nil {
			return nil, false, err
		}
		result = append(result, artifact)
	}
	return result, complete && rows.Err() == nil, rows.Err()
}

func scanReconciliationArtifact(rows *sql.Rows) (app.OwnedArtifact, error) {
	var artifact app.OwnedArtifact
	var ownerKind, operationID, reservationID, class, lifecycle, volumeID, manifestHash, createdAt string
	var repositoryValue sql.NullString
	var logical, observed, charged, accountingVersion, policyVersion int64
	var completeValue int
	if err := rows.Scan(&artifact.ArtifactID, &ownerKind, &artifact.OwnerID, &operationID, &reservationID, &repositoryValue, &class, &lifecycle, &logical, &observed, &charged, &volumeID, &manifestHash, &accountingVersion, &policyVersion, &completeValue, &createdAt); err != nil {
		return app.OwnedArtifact{}, err
	}
	if logical < 0 || observed < 0 || charged < 0 || accountingVersion <= 0 || policyVersion <= 0 || completeValue < 0 || completeValue > 1 {
		return app.OwnedArtifact{}, app.ErrReviewStoreCorrupt
	}
	when, err := parseTime(createdAt)
	if err != nil {
		return app.OwnedArtifact{}, app.ErrReviewStoreCorrupt
	}
	artifact.OwnerKind = app.OwnerKind(ownerKind)
	artifact.OperationID = domain.OperationID(operationID)
	artifact.ReservationID = reservationID
	artifact.Class = app.StorageArtifactClass(class)
	artifact.Lifecycle = app.OwnedArtifactLifecycle(lifecycle)
	artifact.LogicalBytes = app.ByteSize(logical)
	artifact.ObservedBytes = app.ByteSize(observed)
	artifact.ChargedBytes = app.ByteSize(charged)
	artifact.VolumeID = volumeID
	artifact.ManifestHash = manifestHash
	artifact.AccountingVersion = uint32(accountingVersion)
	artifact.PolicyVersion = app.ResourcePolicyVersion(policyVersion)
	artifact.Complete = completeValue == 1
	artifact.CreatedAt = when
	if repositoryValue.Valid {
		value := domain.RepositoryID(repositoryValue.String)
		artifact.RepositoryID = &value
	}
	if artifact.Validate() != nil {
		return app.OwnedArtifact{}, app.ErrReviewStoreCorrupt
	}
	return artifact, nil
}

func listReconciliationReservations(ctx context.Context, tx *sql.Tx, repositoryID *domain.RepositoryID, after string, limit uint32) ([]app.CapacityReservationSummary, bool, error) {
	query := `SELECT reservation_id, owner_kind, owner_id, operation_id, repository_id, state,
		retained_bytes, created_at FROM capacity_reservations WHERE state = 'active' AND reservation_id > ?`
	args := []any{after}
	if repositoryID != nil {
		query += " AND repository_id = ?"
		args = append(args, string(*repositoryID))
	}
	query += " ORDER BY reservation_id ASC LIMIT ?"
	args = append(args, int64(limit)+1)
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	result := make([]app.CapacityReservationSummary, 0, limit)
	complete := true
	for rows.Next() {
		if uint32(len(result)) == limit {
			complete = false
			break
		}
		var reservationID, ownerKind, ownerID, operationID, state, createdAt string
		var repositoryValue sql.NullString
		var retained int64
		if err := rows.Scan(&reservationID, &ownerKind, &ownerID, &operationID, &repositoryValue, &state, &retained, &createdAt); err != nil {
			return nil, false, err
		}
		when, err := parseTime(createdAt)
		if err != nil || retained < 0 || app.CapacityReservationState(state).Validate() != nil {
			return nil, false, app.ErrReviewStoreCorrupt
		}
		item := app.CapacityReservationSummary{ReservationID: reservationID, OwnerKind: app.OwnerKind(ownerKind), OwnerID: ownerID, OperationID: domain.OperationID(operationID), State: app.CapacityReservationState(state), RetainedBytes: app.ByteSize(retained), CreatedAt: when}
		if repositoryValue.Valid {
			value := domain.RepositoryID(repositoryValue.String)
			item.RepositoryID = &value
		}
		result = append(result, item)
	}
	return result, complete && rows.Err() == nil, rows.Err()
}

// SaveOwnedStorageReconciliation persists one epoch batch with revision and
// cursor fencing. Replaying the exact batch is idempotent; a changed batch or
// an unexpected cursor is rejected.
func (s *Store) SaveOwnedStorageReconciliation(ctx context.Context, epoch app.OwnedStorageReconciliationEpoch, discrepancies []app.OwnedStorageDiscrepancy) error {
	if err := s.ensureOpen(); err != nil {
		return err
	}
	if err := epoch.Validate(); err != nil {
		return err
	}
	for _, discrepancy := range discrepancies {
		if err := discrepancy.Validate(); err != nil {
			return err
		}
	}
	if epoch.ProcessedItems > math.MaxInt64 || epoch.DiscrepancyCount > math.MaxInt64 || epoch.EvidenceBytes > math.MaxInt64 || epoch.UncertaintyCount > math.MaxInt64 {
		return app.ErrInvalidOwnedStorageReconcile
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	var existingCursor, existingNext, existingBatch string
	var existingRevision, existingPolicy, existingProcessed, existingDiscrepancies, existingEvidence, existingUncertainty int64
	var existingComplete int
	loadErr := tx.QueryRowContext(ctx, `SELECT cursor, next_cursor, batch_key, ledger_revision, policy_version,
		processed_items, discrepancy_count, evidence_bytes, uncertainty_count, complete
		FROM storage_reconciliation_epochs WHERE epoch_id = ?`, epoch.Epoch).Scan(&existingCursor, &existingNext, &existingBatch, &existingRevision, &existingPolicy, &existingProcessed, &existingDiscrepancies, &existingEvidence, &existingUncertainty, &existingComplete)
	if loadErr != nil && !errors.Is(loadErr, sql.ErrNoRows) {
		return loadErr
	}
	if loadErr == nil {
		if existingBatch == epoch.BatchKey && existingCursor == epoch.Cursor && existingNext == epoch.NextCursor {
			return tx.Commit()
		}
		if existingRevision != int64(epoch.LedgerRevision) || existingPolicy != int64(epoch.PolicyVersion) || existingNext != epoch.Cursor {
			return app.ErrOwnedStorageReconcileConflict
		}
	}
	processed := epoch.ProcessedItems
	discrepancyCount := epoch.DiscrepancyCount
	evidenceBytes := epoch.EvidenceBytes
	uncertainCount := epoch.UncertaintyCount
	if loadErr == nil {
		if existingProcessed < 0 || existingDiscrepancies < 0 || existingEvidence < 0 || existingUncertainty < 0 {
			return app.ErrReviewStoreCorrupt
		}
		processed += app.Count(existingProcessed)
		discrepancyCount += app.Count(existingDiscrepancies)
		evidenceBytes += app.ByteSize(existingEvidence)
		uncertainCount += app.Count(existingUncertainty)
	}
	values := []any{epoch.Epoch, nullableRepository(epoch.RepositoryID), int64(epoch.LedgerRevision), int64(epoch.PolicyVersion), epoch.Cursor, epoch.NextCursor, epoch.BatchKey, int64(processed), int64(discrepancyCount), int64(evidenceBytes), int64(uncertainCount), boolInt(epoch.Complete), formatTime(epoch.UpdatedAt.UTC())}
	if loadErr != nil {
		if _, err := tx.ExecContext(ctx, `INSERT INTO storage_reconciliation_epochs(
			epoch_id, repository_id, ledger_revision, policy_version, cursor, next_cursor, batch_key,
			processed_items, discrepancy_count, evidence_bytes, uncertainty_count, complete, updated_at)
			VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, values...); err != nil {
			return err
		}
	} else if _, err := tx.ExecContext(ctx, `UPDATE storage_reconciliation_epochs SET
			cursor = ?, next_cursor = ?, batch_key = ?, processed_items = ?, discrepancy_count = ?,
			evidence_bytes = ?, uncertainty_count = ?, complete = ?, updated_at = ? WHERE epoch_id = ?`,
		epoch.Cursor, epoch.NextCursor, epoch.BatchKey, int64(processed), int64(discrepancyCount), int64(evidenceBytes), int64(uncertainCount), boolInt(epoch.Complete), formatTime(epoch.UpdatedAt.UTC()), epoch.Epoch); err != nil {
		return err
	}
	for ordinal, discrepancy := range discrepancies {
		if _, err := tx.ExecContext(ctx, `INSERT INTO storage_reconciliation_discrepancies(
			epoch_id, batch_key, ordinal, kind, owner_kind, owner_id, artifact_id, reservation_id,
			repository_id, volume_id, marker_nonce, expected_manifest_hash, observed_manifest_hash,
			expected_bytes, observed_bytes, evidence_code, plan_eligible, handler_kind, handler_version, preconditions_hash)
			VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, epoch.Epoch, epoch.BatchKey, ordinal,
			string(discrepancy.Kind), string(discrepancy.OwnerKind), discrepancy.OwnerID, discrepancy.ArtifactID, discrepancy.ReservationID,
			nullableRepository(discrepancy.RepositoryID), discrepancy.VolumeID, discrepancy.MarkerNonce, discrepancy.ExpectedManifestHash,
			discrepancy.ObservedManifestHash, int64(discrepancy.ExpectedBytes), int64(discrepancy.ObservedBytes), discrepancy.EvidenceCode, boolInt(discrepancy.PlanEligible),
			string(discrepancy.HandlerKind), discrepancy.HandlerVersion, discrepancy.PreconditionsHash); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// LoadOwnedStorageReconciliation returns the latest bounded epoch and its
// redacted discrepancy rows for query-only health and repair-plan binding.
func (s *Store) LoadOwnedStorageReconciliation(ctx context.Context, epochID string) (app.OwnedStorageReconciliationEpoch, []app.OwnedStorageDiscrepancy, error) {
	if err := s.ensureOpen(); err != nil {
		return app.OwnedStorageReconciliationEpoch{}, nil, err
	}
	if !validStorageReconciliationHash(epochID) {
		return app.OwnedStorageReconciliationEpoch{}, nil, app.ErrInvalidOwnedStorageReconcile
	}
	var epoch app.OwnedStorageReconciliationEpoch
	var repositoryValue sql.NullString
	var cursor, next, batch, updated string
	var revision, policy, processed, discrepancies, evidence, uncertain int64
	var complete int
	if err := s.db.QueryRowContext(ctx, `SELECT repository_id, ledger_revision, policy_version, cursor, next_cursor,
		batch_key, processed_items, discrepancy_count, evidence_bytes, uncertainty_count, complete, updated_at
		FROM storage_reconciliation_epochs WHERE epoch_id = ?`, epochID).Scan(&repositoryValue, &revision, &policy, &cursor, &next, &batch, &processed, &discrepancies, &evidence, &uncertain, &complete, &updated); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return app.OwnedStorageReconciliationEpoch{}, nil, app.ErrStorageLedgerNotFound
		}
		return app.OwnedStorageReconciliationEpoch{}, nil, err
	}
	if revision < 0 || policy <= 0 || processed < 0 || discrepancies < 0 || evidence < 0 || uncertain < 0 || complete < 0 || complete > 1 {
		return app.OwnedStorageReconciliationEpoch{}, nil, app.ErrReviewStoreCorrupt
	}
	epoch.Epoch, epoch.LedgerRevision, epoch.PolicyVersion = epochID, uint64(revision), app.ResourcePolicyVersion(policy)
	epoch.Cursor, epoch.NextCursor, epoch.BatchKey = cursor, next, batch
	epoch.ProcessedItems, epoch.DiscrepancyCount, epoch.EvidenceBytes, epoch.UncertaintyCount = app.Count(processed), app.Count(discrepancies), app.ByteSize(evidence), app.Count(uncertain)
	epoch.Complete = complete == 1
	epoch.UpdatedAt, _ = parseTime(updated)
	if repositoryValue.Valid {
		value := domain.RepositoryID(repositoryValue.String)
		epoch.RepositoryID = &value
	}
	if err := epoch.Validate(); err != nil {
		return app.OwnedStorageReconciliationEpoch{}, nil, app.ErrReviewStoreCorrupt
	}
	rows, err := s.db.QueryContext(ctx, `SELECT kind, owner_kind, owner_id, artifact_id, reservation_id,
		repository_id, volume_id, marker_nonce, expected_manifest_hash, observed_manifest_hash,
		expected_bytes, observed_bytes, evidence_code, plan_eligible, handler_kind, handler_version, preconditions_hash
		FROM storage_reconciliation_discrepancies WHERE epoch_id = ? ORDER BY batch_key ASC, ordinal ASC`, epochID)
	if err != nil {
		return app.OwnedStorageReconciliationEpoch{}, nil, err
	}
	defer rows.Close()
	result := make([]app.OwnedStorageDiscrepancy, 0, discrepancies)
	for rows.Next() {
		var discrepancy app.OwnedStorageDiscrepancy
		var kind, ownerKind, volume, marker, expectedHash, observedHash, evidenceCode, handlerKind, handlerVersion, preconditions string
		var artifactID, reservationID, ownerID string
		var repositoryValue sql.NullString
		var expectedBytes, observedBytes int64
		var planEligible int
		if err := rows.Scan(&kind, &ownerKind, &ownerID, &artifactID, &reservationID, &repositoryValue, &volume, &marker, &expectedHash, &observedHash, &expectedBytes, &observedBytes, &evidenceCode, &planEligible, &handlerKind, &handlerVersion, &preconditions); err != nil {
			return app.OwnedStorageReconciliationEpoch{}, nil, err
		}
		if expectedBytes < 0 || observedBytes < 0 || planEligible < 0 || planEligible > 1 {
			return app.OwnedStorageReconciliationEpoch{}, nil, app.ErrReviewStoreCorrupt
		}
		discrepancy.Kind, discrepancy.OwnerKind, discrepancy.OwnerID = app.OwnedStorageDiscrepancyKind(kind), app.OwnerKind(ownerKind), ownerID
		discrepancy.ArtifactID, discrepancy.ReservationID, discrepancy.VolumeID = artifactID, reservationID, volume
		discrepancy.MarkerNonce, discrepancy.ExpectedManifestHash, discrepancy.ObservedManifestHash = marker, expectedHash, observedHash
		discrepancy.ExpectedBytes, discrepancy.ObservedBytes = app.ByteSize(expectedBytes), app.ByteSize(observedBytes)
		discrepancy.EvidenceCode, discrepancy.PlanEligible, discrepancy.HandlerKind, discrepancy.HandlerVersion, discrepancy.PreconditionsHash = evidenceCode, planEligible == 1, app.RepairHandlerKind(handlerKind), handlerVersion, preconditions
		if repositoryValue.Valid {
			value := domain.RepositoryID(repositoryValue.String)
			discrepancy.RepositoryID = &value
		}
		if err := discrepancy.Validate(); err != nil {
			return app.OwnedStorageReconciliationEpoch{}, nil, app.ErrReviewStoreCorrupt
		}
		result = append(result, discrepancy)
	}
	if err := rows.Err(); err != nil {
		return app.OwnedStorageReconciliationEpoch{}, nil, err
	}
	return epoch, result, nil
}

func nullableRepository(repositoryID *domain.RepositoryID) any {
	if repositoryID == nil {
		return nil
	}
	return string(*repositoryID)
}

func validStorageReconciliationHash(value string) bool {
	if len(value) != 64 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}
