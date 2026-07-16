package sqlite

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"math"
	"sort"
	"time"

	"github.com/Scottlr/nudge/internal/app"
	"github.com/Scottlr/nudge/internal/domain"
	moderncsqlite "modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

var _ app.OwnedStorageLedger = (*Store)(nil)

const (
	ledgerOperationReserve = "reserve"
	ledgerOperationSettle  = "settle"
	ledgerOperationRelease = "release"
	ledgerScopeGlobal      = "global"
	ledgerScopeRepository  = "repository"
	ledgerBusyRetryLimit   = 4
)

// RecordReservation imports one already-admitted T065 reservation. The
// reservation and aggregate totals are committed together, and a repeated
// idempotency key never creates a second charge.
func (s *Store) RecordReservation(ctx context.Context, record app.CapacityReservationRecord) error {
	if err := s.ensureOpen(); err != nil {
		return err
	}
	if err := record.Validate(); err != nil {
		return err
	}
	requestHash := reservationRequestHash(record)
	volumes, err := reservationVolumes(record.Plan)
	if err != nil {
		return err
	}
	if err := ensureSQLiteBytes(record.Plan.RetainedDelta); err != nil {
		return err
	}
	for _, volume := range volumes {
		if err := ensureSQLiteBytes(volume.PeakBytes, volume.RetainedBytes); err != nil {
			return err
		}
	}
	return s.withLedgerTx(ctx, func(tx *sql.Tx) error {
		if handled, err := checkLedgerOperation(tx, ledgerOperationReserve, record.IdempotencyKey, record.Reservation.Marker(), requestHash); err != nil || handled {
			return err
		}
		if err := ensureNoReservationConflict(tx, record); err != nil {
			return err
		}
		global, repository, err := loadLedgerTotals(tx, record.Plan.RepositoryID)
		if err != nil {
			return err
		}
		if err := checkGrowthAllowed(global, repository, record.Plan.RetainedDelta); err != nil {
			return err
		}
		now := record.CreatedAt.UTC()
		if _, err := tx.ExecContext(ctx, `INSERT INTO capacity_reservations(
			reservation_id, owner_kind, owner_id, operation_id, repository_id, plan_digest,
			policy_version, accounting_version, state, retained_bytes, idempotency_key,
			created_at, updated_at
		) VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			record.Reservation.Marker(), string(record.OwnerKind), record.OwnerID,
			record.Plan.OperationID, nullableRepositoryID(record.Plan.RepositoryID),
			record.Reservation.PlanDigest(), record.Plan.PolicyVersion,
			app.CurrentStorageAccountingVersion, string(app.ReservationActive),
			mustSQLBytes(record.Plan.RetainedDelta), record.IdempotencyKey,
			formatTime(now), formatTime(now)); err != nil {
			return err
		}
		for _, volume := range volumes {
			if _, err := tx.ExecContext(ctx, `INSERT INTO capacity_reservation_volumes(
				reservation_id, volume_id, peak_bytes, retained_bytes
			) VALUES(?, ?, ?, ?)`, record.Reservation.Marker(), volume.ID,
				mustSQLBytes(volume.PeakBytes), mustSQLBytes(volume.RetainedBytes)); err != nil {
				return err
			}
		}
		revision, err := nextLedgerRevision(global, repository)
		if err != nil {
			return err
		}
		global.ReservedBytes, err = addStorage(global.ReservedBytes, record.Plan.RetainedDelta)
		if err != nil {
			return err
		}
		global.Revision = revision
		if err := saveLedgerTotals(ctx, tx, global); err != nil {
			return err
		}
		if repository != nil {
			repository.ReservedBytes, err = addStorage(repository.ReservedBytes, record.Plan.RetainedDelta)
			if err != nil {
				return err
			}
			repository.Revision = revision
			if err := saveLedgerTotals(ctx, tx, *repository); err != nil {
				return err
			}
		}
		return insertLedgerOperation(ctx, tx, ledgerOperationReserve, record.IdempotencyKey, record.Reservation.Marker(), requestHash, now)
	})
}

// SettleReservation converts one active reservation into accepted T066
// artifacts and updates all affected totals in one transaction.
func (s *Store) SettleReservation(ctx context.Context, settlement app.ReservationSettlement) error {
	if err := s.ensureOpen(); err != nil {
		return err
	}
	if err := settlement.Validate(); err != nil {
		return err
	}
	for _, artifact := range settlement.Artifacts {
		if err := ensureSQLiteBytes(artifact.LogicalBytes, artifact.ObservedBytes, artifact.ChargedBytes); err != nil {
			return err
		}
	}
	requestHash := settlementRequestHash(settlement)
	return s.withLedgerTx(ctx, func(tx *sql.Tx) error {
		if handled, err := checkLedgerOperation(tx, ledgerOperationSettle, settlement.IdempotencyKey, settlement.ReservationID, requestHash); err != nil || handled {
			return err
		}
		reservation, err := loadReservation(tx, settlement.ReservationID)
		if err != nil {
			return err
		}
		if reservation.State != app.ReservationActive {
			return app.ErrStorageLedgerConflict
		}
		if settlement.ExpectedRevision != 0 && settlement.ExpectedRevision != reservation.GlobalRevision {
			return app.ErrStorageLedgerConflict
		}
		for _, artifact := range settlement.Artifacts {
			if artifact.Lifecycle != app.OwnedArtifactAccepted || artifact.OperationID != reservation.OperationID || artifact.OwnerKind != reservation.OwnerKind || artifact.OwnerID != reservation.OwnerID || artifact.PolicyVersion != reservation.PolicyVersion || artifact.AccountingVersion != app.CurrentStorageAccountingVersion || !sameRepository(artifact.RepositoryID, reservation.RepositoryID) {
				return app.ErrStorageLedgerConflict
			}
			var exists int
			if err := tx.QueryRowContext(ctx, "SELECT 1 FROM owned_artifact_ledger WHERE artifact_id = ?", artifact.ArtifactID).Scan(&exists); err == nil {
				return app.ErrStorageLedgerConflict
			} else if !errors.Is(err, sql.ErrNoRows) {
				return err
			}
		}
		global, repository, err := loadLedgerTotals(tx, reservation.RepositoryID)
		if err != nil {
			return err
		}
		if global.ReservedBytes < reservation.RetainedBytes || repository != nil && repository.ReservedBytes < reservation.RetainedBytes {
			return app.ErrStorageLedgerConflict
		}
		var logical, observed, charged app.ByteSize
		for _, artifact := range settlement.Artifacts {
			logical, err = addStorage(logical, artifact.LogicalBytes)
			if err != nil {
				return err
			}
			observed, err = addStorage(observed, artifact.ObservedBytes)
			if err != nil {
				return err
			}
			charged, err = addStorage(charged, artifact.ChargedBytes)
			if err != nil {
				return err
			}
		}
		if err := checkSettlementAllowed(global, repository, reservation.RetainedBytes, charged); err != nil {
			return err
		}
		now := time.Now().UTC()
		for _, artifact := range settlement.Artifacts {
			if _, err := tx.ExecContext(ctx, `INSERT INTO owned_artifact_ledger(
				artifact_id, owner_kind, owner_id, operation_id, reservation_id, repository_id,
				class, lifecycle, logical_bytes, observed_bytes, charged_bytes, volume_id,
				manifest_hash, accounting_version, policy_version, complete, created_at
			) VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
				artifact.ArtifactID, string(artifact.OwnerKind), artifact.OwnerID,
				artifact.OperationID, artifact.ReservationID, nullableRepositoryID(artifact.RepositoryID),
				string(artifact.Class), string(artifact.Lifecycle), mustSQLBytes(artifact.LogicalBytes),
				mustSQLBytes(artifact.ObservedBytes), mustSQLBytes(artifact.ChargedBytes), artifact.VolumeID,
				artifact.ManifestHash, artifact.AccountingVersion, artifact.PolicyVersion,
				boolInt(artifact.Complete), formatTime(artifact.CreatedAt.UTC())); err != nil {
				return err
			}
		}
		revision, err := nextLedgerRevision(global, repository)
		if err != nil {
			return err
		}
		global.ReservedBytes -= reservation.RetainedBytes
		global.LogicalBytes, err = addStorage(global.LogicalBytes, logical)
		if err != nil {
			return err
		}
		global.ObservedBytes, err = addStorage(global.ObservedBytes, observed)
		if err != nil {
			return err
		}
		global.ChargedBytes, err = addStorage(global.ChargedBytes, charged)
		if err != nil {
			return err
		}
		global.Revision = revision
		if err := saveLedgerTotals(ctx, tx, global); err != nil {
			return err
		}
		if repository != nil {
			repository.ReservedBytes -= reservation.RetainedBytes
			repository.LogicalBytes, err = addStorage(repository.LogicalBytes, logical)
			if err != nil {
				return err
			}
			repository.ObservedBytes, err = addStorage(repository.ObservedBytes, observed)
			if err != nil {
				return err
			}
			repository.ChargedBytes, err = addStorage(repository.ChargedBytes, charged)
			if err != nil {
				return err
			}
			repository.Revision = revision
			if err := saveLedgerTotals(ctx, tx, *repository); err != nil {
				return err
			}
		}
		if _, err := tx.ExecContext(ctx, "UPDATE capacity_reservations SET state = ?, updated_at = ? WHERE reservation_id = ? AND state = ?", string(app.ReservationConsumed), formatTime(now), settlement.ReservationID, string(app.ReservationActive)); err != nil {
			return err
		}
		return insertLedgerOperation(ctx, tx, ledgerOperationSettle, settlement.IdempotencyKey, settlement.ReservationID, requestHash, now)
	})
}

// ReleaseReservation closes one active reservation without touching accepted
// artifacts. Repeating the same release is safe and does not double-release.
func (s *Store) ReleaseReservation(ctx context.Context, release app.ReservationRelease) error {
	if err := s.ensureOpen(); err != nil {
		return err
	}
	if err := release.Validate(); err != nil {
		return err
	}
	requestHash := releaseRequestHash(release)
	return s.withLedgerTx(ctx, func(tx *sql.Tx) error {
		if handled, err := checkLedgerOperation(tx, ledgerOperationRelease, release.IdempotencyKey, release.ReservationID, requestHash); err != nil || handled {
			return err
		}
		reservation, err := loadReservation(tx, release.ReservationID)
		if err != nil {
			return err
		}
		if reservation.State == app.ReservationConsumed {
			return app.ErrStorageLedgerConflict
		}
		global, repository, err := loadLedgerTotals(tx, reservation.RepositoryID)
		if err != nil {
			return err
		}
		if release.ExpectedRevision != 0 && release.ExpectedRevision != reservation.GlobalRevision {
			return app.ErrStorageLedgerConflict
		}
		if reservation.State == app.ReservationReleased {
			return insertLedgerOperation(ctx, tx, ledgerOperationRelease, release.IdempotencyKey, release.ReservationID, requestHash, time.Now().UTC())
		}
		if global.ReservedBytes < reservation.RetainedBytes || repository != nil && repository.ReservedBytes < reservation.RetainedBytes {
			return app.ErrStorageLedgerConflict
		}
		revision, err := nextLedgerRevision(global, repository)
		if err != nil {
			return err
		}
		global.ReservedBytes -= reservation.RetainedBytes
		global.Revision = revision
		if err := saveLedgerTotals(ctx, tx, global); err != nil {
			return err
		}
		if repository != nil {
			repository.ReservedBytes -= reservation.RetainedBytes
			repository.Revision = revision
			if err := saveLedgerTotals(ctx, tx, *repository); err != nil {
				return err
			}
		}
		now := time.Now().UTC()
		if _, err := tx.ExecContext(ctx, "UPDATE capacity_reservations SET state = ?, updated_at = ? WHERE reservation_id = ? AND state = ?", string(app.ReservationReleased), formatTime(now), release.ReservationID, string(app.ReservationActive)); err != nil {
			return err
		}
		return insertLedgerOperation(ctx, tx, ledgerOperationRelease, release.IdempotencyKey, release.ReservationID, requestHash, now)
	})
}

// Snapshot returns one bounded, revisioned view of totals and ledger rows. It
// does not inspect paths, markers, or filesystem state.
func (s *Store) Snapshot(ctx context.Context, query app.StorageLedgerQuery) (app.StorageLedgerSnapshot, error) {
	if err := s.ensureOpen(); err != nil {
		return app.StorageLedgerSnapshot{}, err
	}
	if query.Limit == 0 {
		query.Limit = app.DefaultStorageLedgerPage
	}
	if err := query.Validate(); err != nil {
		return app.StorageLedgerSnapshot{}, err
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return app.StorageLedgerSnapshot{}, err
	}
	defer func() { _ = tx.Rollback() }()
	global, repository, err := loadLedgerTotals(tx, query.RepositoryID)
	if err != nil {
		return app.StorageLedgerSnapshot{}, err
	}
	revision := global.Revision
	snapshot := app.StorageLedgerSnapshot{Revision: revision, Global: global}
	if repository != nil {
		snapshot.Repository = *repository
	}
	snapshot.Pressure = storagePressure(snapshot.Repository, snapshot.Global)
	if query.IncludeReservations {
		reservations, complete, err := listReservations(ctx, tx, query.RepositoryID, query.Limit)
		if err != nil {
			return app.StorageLedgerSnapshot{}, err
		}
		snapshot.Reservations = reservations
		var activeCount int64
		countQuery := "SELECT COUNT(*) FROM capacity_reservations WHERE state = 'active'"
		countArgs := []any{}
		if query.RepositoryID != nil {
			countQuery += " AND repository_id = ?"
			countArgs = append(countArgs, string(*query.RepositoryID))
		}
		if err := tx.QueryRowContext(ctx, countQuery, countArgs...).Scan(&activeCount); err != nil || activeCount < 0 {
			if err != nil {
				return app.StorageLedgerSnapshot{}, err
			}
			return app.StorageLedgerSnapshot{}, app.ErrReviewStoreCorrupt
		}
		snapshot.ActiveReservations = app.Count(activeCount)
		snapshot.Complete = complete
	} else {
		snapshot.Complete = true
	}
	if query.IncludeArtifacts {
		artifacts, complete, err := listArtifacts(ctx, tx, query.RepositoryID, query.Limit)
		if err != nil {
			return app.StorageLedgerSnapshot{}, err
		}
		snapshot.Artifacts = artifacts
		snapshot.Complete = snapshot.Complete && complete
	}
	if err := tx.Commit(); err != nil {
		return app.StorageLedgerSnapshot{}, err
	}
	return snapshot, nil
}

type ledgerVolume struct {
	ID            string
	PeakBytes     app.ByteSize
	RetainedBytes app.ByteSize
}

func reservationVolumes(plan app.CapacityPlan) ([]ledgerVolume, error) {
	volumes := make([]ledgerVolume, 0, len(plan.VolumePeaks))
	var retained app.ByteSize
	for _, peak := range plan.VolumePeaks {
		charge, err := peak.Charge()
		if err != nil {
			return nil, app.ErrStorageLedgerInput
		}
		retained, err = addStorage(retained, peak.RetainedDelta)
		if err != nil {
			return nil, err
		}
		volumes = append(volumes, ledgerVolume{ID: peak.ID, PeakBytes: charge, RetainedBytes: peak.RetainedDelta})
	}
	if retained != plan.RetainedDelta {
		return nil, app.ErrStorageLedgerInput
	}
	return volumes, nil
}

type ledgerReservationRow struct {
	ReservationID  string
	OwnerKind      app.OwnerKind
	OwnerID        string
	OperationID    domain.OperationID
	RepositoryID   *domain.RepositoryID
	PolicyVersion  app.ResourcePolicyVersion
	State          app.CapacityReservationState
	RetainedBytes  app.ByteSize
	CreatedAt      time.Time
	GlobalRevision uint64
}

func loadReservation(tx *sql.Tx, reservationID string) (ledgerReservationRow, error) {
	var row ledgerReservationRow
	var repositoryID sql.NullString
	var policyVersion, retainedBytes, revision int64
	var state, createdAt string
	err := tx.QueryRow(`SELECT r.reservation_id, r.owner_kind, r.owner_id, r.operation_id,
		r.repository_id, r.policy_version, r.state, r.retained_bytes, r.created_at,
		COALESCE((SELECT ledger_revision FROM storage_totals WHERE scope_kind = 'global' AND scope_id = ''), 0)
		FROM capacity_reservations r WHERE r.reservation_id = ?`, reservationID).Scan(
		&row.ReservationID, &row.OwnerKind, &row.OwnerID, &row.OperationID, &repositoryID,
		&policyVersion, &state, &retainedBytes, &createdAt, &revision)
	if errors.Is(err, sql.ErrNoRows) {
		return ledgerReservationRow{}, app.ErrStorageLedgerNotFound
	}
	if err != nil {
		return ledgerReservationRow{}, err
	}
	if policyVersion <= 0 || retainedBytes < 0 || revision < 0 {
		return ledgerReservationRow{}, app.ErrReviewStoreCorrupt
	}
	row.PolicyVersion = app.ResourcePolicyVersion(policyVersion)
	row.State = app.CapacityReservationState(state)
	row.RetainedBytes = app.ByteSize(retainedBytes)
	row.GlobalRevision = uint64(revision)
	when, err := parseTime(createdAt)
	if err != nil || row.State.Validate() != nil {
		return ledgerReservationRow{}, app.ErrReviewStoreCorrupt
	}
	row.CreatedAt = when
	if repositoryID.Valid {
		value := domain.RepositoryID(repositoryID.String)
		row.RepositoryID = &value
	}
	return row, nil
}

func ensureNoReservationConflict(tx *sql.Tx, record app.CapacityReservationRecord) error {
	var operationKey, operationHash string
	if err := tx.QueryRow("SELECT idempotency_key, plan_digest FROM capacity_reservations WHERE reservation_id = ?", record.Reservation.Marker()).Scan(&operationKey, &operationHash); err == nil {
		if operationKey == record.IdempotencyKey && operationHash == record.Reservation.PlanDigest() {
			return app.ErrStorageLedgerConflict
		}
		return app.ErrStorageLedgerConflict
	} else if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	return nil
}

func loadLedgerTotals(tx *sql.Tx, repositoryID *domain.RepositoryID) (app.StorageTotals, *app.StorageTotals, error) {
	global, err := loadOneTotal(tx, ledgerScopeGlobal, "", nil)
	if err != nil {
		return app.StorageTotals{}, nil, err
	}
	if repositoryID == nil {
		return global, nil, nil
	}
	value := *repositoryID
	repository, err := loadOneTotal(tx, ledgerScopeRepository, string(value), &value)
	if err != nil {
		return app.StorageTotals{}, nil, err
	}
	return global, &repository, nil
}

func loadOneTotal(tx *sql.Tx, scopeKind, scopeID string, repositoryID *domain.RepositoryID) (app.StorageTotals, error) {
	var logical, observed, charged, reserved, uncertain, revision int64
	err := tx.QueryRow("SELECT logical_bytes, observed_bytes, charged_bytes, reserved_bytes, uncertain_count, ledger_revision FROM storage_totals WHERE scope_kind = ? AND scope_id = ?", scopeKind, scopeID).Scan(&logical, &observed, &charged, &reserved, &uncertain, &revision)
	if errors.Is(err, sql.ErrNoRows) {
		return app.StorageTotals{RepositoryID: repositoryID}, nil
	}
	if err != nil {
		return app.StorageTotals{}, err
	}
	if logical < 0 || observed < 0 || charged < 0 || reserved < 0 || uncertain < 0 || revision < 0 {
		return app.StorageTotals{}, app.ErrReviewStoreCorrupt
	}
	return app.StorageTotals{RepositoryID: repositoryID, LogicalBytes: app.ByteSize(logical), ObservedBytes: app.ByteSize(observed), ChargedBytes: app.ByteSize(charged), ReservedBytes: app.ByteSize(reserved), UncertainCount: app.Count(uncertain), Revision: uint64(revision)}, nil
}

func saveLedgerTotals(ctx context.Context, tx *sql.Tx, totals app.StorageTotals) error {
	if err := ensureSQLiteBytes(totals.LogicalBytes, totals.ObservedBytes, totals.ChargedBytes, totals.ReservedBytes); err != nil || uint64(totals.UncertainCount) > math.MaxInt64 || totals.Revision > math.MaxInt64 {
		return app.ErrStorageLedgerInput
	}
	scopeKind, scopeID := ledgerScopeGlobal, ""
	if totals.RepositoryID != nil {
		scopeKind, scopeID = ledgerScopeRepository, string(*totals.RepositoryID)
	}
	values := []any{scopeKind, scopeID, mustSQLBytes(totals.LogicalBytes), mustSQLBytes(totals.ObservedBytes), mustSQLBytes(totals.ChargedBytes), mustSQLBytes(totals.ReservedBytes), int64(totals.UncertainCount), int64(totals.Revision), formatTime(time.Now().UTC())}
	_, err := tx.ExecContext(ctx, `INSERT INTO storage_totals(
		scope_kind, scope_id, logical_bytes, observed_bytes, charged_bytes, reserved_bytes,
		uncertain_count, ledger_revision, updated_at
	) VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?)
	ON CONFLICT(scope_kind, scope_id) DO UPDATE SET
		logical_bytes = excluded.logical_bytes,
		observed_bytes = excluded.observed_bytes,
		charged_bytes = excluded.charged_bytes,
		reserved_bytes = excluded.reserved_bytes,
		uncertain_count = excluded.uncertain_count,
		ledger_revision = excluded.ledger_revision,
		updated_at = excluded.updated_at`, values...)
	return err
}

func nextLedgerRevision(global app.StorageTotals, repository *app.StorageTotals) (uint64, error) {
	current := global.Revision
	if repository != nil && repository.Revision > current {
		current = repository.Revision
	}
	if current == math.MaxUint64 {
		return 0, app.ErrStorageLedgerInput
	}
	return current + 1, nil
}

func checkGrowthAllowed(global app.StorageTotals, repository *app.StorageTotals, retained app.ByteSize) error {
	if retained == 0 {
		return nil
	}
	if global.UncertainCount > 0 || repository != nil && repository.UncertainCount > 0 {
		return app.ErrStoragePublicationBlocked
	}
	policy := app.DefaultResourcePolicy()
	globalUsed, err := addStorage(global.ChargedBytes, global.ReservedBytes)
	if err != nil {
		return err
	}
	globalProjected, err := addStorage(globalUsed, retained)
	if err != nil {
		return err
	}
	if globalProjected >= policy.Storage.GlobalSoftBytes {
		return app.ErrStoragePublicationBlocked
	}
	if repository != nil {
		used, err := addStorage(repository.ChargedBytes, repository.ReservedBytes)
		if err != nil {
			return err
		}
		projected, err := addStorage(used, retained)
		if err != nil {
			return err
		}
		if projected >= policy.Storage.RepositorySoftBytes {
			return app.ErrStoragePublicationBlocked
		}
	}
	return nil
}

func checkSettlementAllowed(global app.StorageTotals, repository *app.StorageTotals, reserved, added app.ByteSize) error {
	if global.UncertainCount > 0 || repository != nil && repository.UncertainCount > 0 {
		return app.ErrStoragePublicationBlocked
	}
	globalAfterReserved, err := storageSubtract(global.ReservedBytes, reserved)
	if err != nil {
		return err
	}
	globalUsed, err := addStorage(global.ChargedBytes, globalAfterReserved)
	if err != nil {
		return err
	}
	globalUsed, err = addStorage(globalUsed, added)
	if err != nil {
		return err
	}
	policy := app.DefaultResourcePolicy()
	if globalUsed >= policy.Storage.GlobalHardBytes || globalUsed >= policy.Storage.GlobalSoftBytes {
		return app.ErrStoragePublicationBlocked
	}
	if repository != nil {
		afterReserved, err := storageSubtract(repository.ReservedBytes, reserved)
		if err != nil {
			return err
		}
		used, err := addStorage(repository.ChargedBytes, afterReserved)
		if err != nil {
			return err
		}
		used, err = addStorage(used, added)
		if err != nil {
			return err
		}
		if used >= policy.Storage.RepositoryHardBytes || used >= policy.Storage.RepositorySoftBytes {
			return app.ErrStoragePublicationBlocked
		}
	}
	return nil
}

func storageSubtract(a, b app.ByteSize) (app.ByteSize, error) {
	if b > a {
		return 0, app.ErrStorageLedgerConflict
	}
	return a - b, nil
}

func storagePressure(repository, global app.StorageTotals) app.StoragePressureState {
	state := app.StoragePressureState{Reservation: app.StorageDecisionAllowed, Publication: app.StorageDecisionAllowed, Uncertain: repository.UncertainCount > 0 || global.UncertainCount > 0}
	repoUsed, repoErr := addStorage(repository.ChargedBytes, repository.ReservedBytes)
	globalUsed, globalErr := addStorage(global.ChargedBytes, global.ReservedBytes)
	policy := app.DefaultResourcePolicy()
	if repoErr != nil || globalErr != nil {
		state.Uncertain = true
	}
	if state.Uncertain {
		state.Reservation = app.StorageDecisionAccountingUncertain
		state.Publication = app.StorageDecisionAccountingUncertain
		return state
	}
	state.RepositoryPressure, _ = app.ClassifyStoragePressure(repoUsed, policy.Storage.RepositorySoftBytes, policy.Storage.RepositoryHardBytes)
	state.GlobalPressure, _ = app.ClassifyStoragePressure(globalUsed, policy.Storage.GlobalSoftBytes, policy.Storage.GlobalHardBytes)
	if state.RepositoryPressure == app.StoragePressureHard || state.GlobalPressure == app.StoragePressureHard {
		state.Reservation = app.StorageDecisionHardPressure
		state.Publication = app.StorageDecisionHardPressure
	} else if state.RepositoryPressure == app.StoragePressureSoft || state.GlobalPressure == app.StoragePressureSoft {
		state.Reservation = app.StorageDecisionSoftPressure
		state.Publication = app.StorageDecisionSoftPressure
	}
	return state
}

func listReservations(ctx context.Context, tx *sql.Tx, repositoryID *domain.RepositoryID, limit uint32) ([]app.CapacityReservationSummary, bool, error) {
	query := `SELECT reservation_id, owner_kind, owner_id, operation_id, repository_id, state, retained_bytes, created_at FROM capacity_reservations WHERE state = 'active'`
	args := []any{}
	if repositoryID != nil {
		query += " AND repository_id = ?"
		args = append(args, string(*repositoryID))
	}
	query += " ORDER BY created_at ASC, reservation_id ASC LIMIT ?"
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

func listArtifacts(ctx context.Context, tx *sql.Tx, repositoryID *domain.RepositoryID, limit uint32) ([]app.OwnedArtifact, bool, error) {
	query := `SELECT artifact_id, owner_kind, owner_id, operation_id, reservation_id, repository_id, class, lifecycle, logical_bytes, observed_bytes, charged_bytes, volume_id, manifest_hash, accounting_version, policy_version, complete, created_at FROM owned_artifact_ledger`
	args := []any{}
	if repositoryID != nil {
		query += " WHERE repository_id = ?"
		args = append(args, string(*repositoryID))
	}
	query += " ORDER BY created_at ASC, artifact_id ASC LIMIT ?"
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
		var artifact app.OwnedArtifact
		var ownerKind, operationID, reservationID, class, lifecycle, volumeID, manifestHash, createdAt string
		var repositoryValue sql.NullString
		var logical, observed, charged, accountingVersion, policyVersion int64
		var completeValue int
		if err := rows.Scan(&artifact.ArtifactID, &ownerKind, &artifact.OwnerID, &operationID, &reservationID, &repositoryValue, &class, &lifecycle, &logical, &observed, &charged, &volumeID, &manifestHash, &accountingVersion, &policyVersion, &completeValue, &createdAt); err != nil {
			return nil, false, err
		}
		if logical < 0 || observed < 0 || charged < 0 || accountingVersion <= 0 || policyVersion <= 0 || completeValue < 0 || completeValue > 1 {
			return nil, false, app.ErrReviewStoreCorrupt
		}
		when, err := parseTime(createdAt)
		if err != nil {
			return nil, false, app.ErrReviewStoreCorrupt
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
			return nil, false, app.ErrReviewStoreCorrupt
		}
		result = append(result, artifact)
	}
	return result, complete && rows.Err() == nil, rows.Err()
}

func checkLedgerOperation(tx *sql.Tx, kind, key, reservationID, requestHash string) (bool, error) {
	var existingReservation, existingHash string
	err := tx.QueryRow("SELECT reservation_id, request_hash FROM storage_ledger_operations WHERE kind = ? AND idempotency_key = ?", kind, key).Scan(&existingReservation, &existingHash)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if existingReservation != reservationID || existingHash != requestHash {
		return false, app.ErrStorageLedgerConflict
	}
	return true, nil
}

func insertLedgerOperation(ctx context.Context, tx *sql.Tx, kind, key, reservationID, requestHash string, now time.Time) error {
	_, err := tx.ExecContext(ctx, "INSERT INTO storage_ledger_operations(kind, idempotency_key, reservation_id, request_hash, created_at) VALUES(?, ?, ?, ?, ?)", kind, key, reservationID, requestHash, formatTime(now.UTC()))
	return err
}

func reservationRequestHash(record app.CapacityReservationRecord) string {
	h := sha256.New()
	writeStorageHashString(h, record.Reservation.Marker())
	writeStorageHashString(h, string(record.OwnerKind))
	writeStorageHashString(h, record.OwnerID)
	writeStorageHashString(h, string(record.Plan.OperationID))
	writeStorageHashString(h, record.Reservation.PlanDigest())
	writeStorageHashUint64(h, uint64(record.Plan.RetainedDelta))
	return hex.EncodeToString(h.Sum(nil))
}

func settlementRequestHash(settlement app.ReservationSettlement) string {
	artifacts := append([]app.OwnedArtifact(nil), settlement.Artifacts...)
	sort.Slice(artifacts, func(i, j int) bool { return artifacts[i].ArtifactID < artifacts[j].ArtifactID })
	h := sha256.New()
	writeStorageHashString(h, settlement.ReservationID)
	for _, artifact := range artifacts {
		writeStorageHashString(h, artifact.ArtifactID)
		writeStorageHashString(h, string(artifact.Class))
		writeStorageHashUint64(h, uint64(artifact.LogicalBytes))
		writeStorageHashUint64(h, uint64(artifact.ObservedBytes))
		writeStorageHashUint64(h, uint64(artifact.ChargedBytes))
		writeStorageHashString(h, artifact.ManifestHash)
	}
	return hex.EncodeToString(h.Sum(nil))
}

func releaseRequestHash(release app.ReservationRelease) string {
	h := sha256.New()
	writeStorageHashString(h, release.ReservationID)
	return hex.EncodeToString(h.Sum(nil))
}

func writeStorageHashString(h interface{ Write([]byte) (int, error) }, value string) {
	writeStorageHashUint64(h, uint64(len(value)))
	_, _ = h.Write([]byte(value))
}

func writeStorageHashUint64(h interface{ Write([]byte) (int, error) }, value uint64) {
	var encoded [8]byte
	binary.BigEndian.PutUint64(encoded[:], value)
	_, _ = h.Write(encoded[:])
}

func (s *Store) withLedgerTx(ctx context.Context, fn func(*sql.Tx) error) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	var lastErr error
	for attempt := 0; attempt < ledgerBusyRetryLimit; attempt++ {
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			if !isSQLiteBusy(err) {
				return err
			}
			lastErr = err
		} else {
			err = func() error {
				defer func() { _ = tx.Rollback() }()
				if err := fn(tx); err != nil {
					return err
				}
				return tx.Commit()
			}()
			if !isSQLiteBusy(err) {
				return err
			}
			lastErr = err
		}
		delay := time.Duration(attempt+1) * 10 * time.Millisecond
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			return ctx.Err()
		case <-timer.C:
		}
	}
	return lastErr
}

func isSQLiteBusy(err error) bool {
	var sqliteErr *moderncsqlite.Error
	return errors.As(err, &sqliteErr) && sqliteErr.Code() == sqlite3.SQLITE_BUSY
}

func addStorage(a, b app.ByteSize) (app.ByteSize, error) {
	if uint64(b) > math.MaxUint64-uint64(a) {
		return 0, app.ErrStorageLedgerInput
	}
	return a + b, nil
}

func mustSQLBytes(value app.ByteSize) int64 {
	return int64(value)
}

func ensureSQLiteBytes(values ...app.ByteSize) error {
	for _, value := range values {
		if uint64(value) > math.MaxInt64 {
			return app.ErrStorageLedgerInput
		}
	}
	return nil
}

func nullableRepositoryID(value *domain.RepositoryID) any {
	if value == nil {
		return nil
	}
	return string(*value)
}

func sameRepository(left, right *domain.RepositoryID) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}
