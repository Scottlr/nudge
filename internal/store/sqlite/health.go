package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/Scottlr/nudge/internal/app"
	"github.com/Scottlr/nudge/internal/paths"
)

// ErrReadOnlyWALPending means the database has a journal sidecar that cannot
// be incorporated into a strictly no-mutation inspection.
var ErrReadOnlyWALPending = errors.New("SQLite WAL sidecar pending")

// ReadOnlyDatabaseState is the bounded state returned by a doctor database
// inspection. Inspection never creates, migrates, locks, or repairs a file.
type ReadOnlyDatabaseState string

const (
	ReadOnlyDatabaseMissing     ReadOnlyDatabaseState = "missing"
	ReadOnlyDatabaseCurrent     ReadOnlyDatabaseState = "current"
	ReadOnlyDatabaseOutdated    ReadOnlyDatabaseState = "outdated"
	ReadOnlyDatabaseCorrupt     ReadOnlyDatabaseState = "corrupt"
	ReadOnlyDatabaseUnavailable ReadOnlyDatabaseState = "unavailable"
)

// ReadOnlyDatabaseHealth is safe adapter evidence for application health
// aggregation. It contains no path, SQL, or raw driver error.
type ReadOnlyDatabaseHealth struct {
	State           ReadOnlyDatabaseState
	AppliedVersion  uint64
	ExpectedVersion uint64
	QueryOnly       bool
	IntegrityOK     bool
}

// InspectReadOnly opens an existing protected database through a read-only
// SQLite URI and verifies query-only, foreign-key, integrity, and migration
// evidence without invoking the writable store path.
func InspectReadOnly(ctx context.Context, path string) (ReadOnlyDatabaseHealth, error) {
	if ctx == nil || path == "" || !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return ReadOnlyDatabaseHealth{State: ReadOnlyDatabaseUnavailable}, ErrInvalidConfig
	}
	root := filepath.Dir(path)
	file, err := paths.OpenExistingProtectedFile(root, filepath.Base(path))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return ReadOnlyDatabaseHealth{State: ReadOnlyDatabaseMissing}, nil
		}
		return ReadOnlyDatabaseHealth{State: ReadOnlyDatabaseUnavailable}, err
	}
	if err := file.Close(); err != nil {
		return ReadOnlyDatabaseHealth{State: ReadOnlyDatabaseUnavailable}, err
	}
	for _, sidecar := range []string{path + "-wal", path + "-shm"} {
		if _, err := os.Stat(sidecar); err == nil {
			return ReadOnlyDatabaseHealth{State: ReadOnlyDatabaseUnavailable}, ErrReadOnlyWALPending
		} else if !errors.Is(err, os.ErrNotExist) {
			return ReadOnlyDatabaseHealth{State: ReadOnlyDatabaseUnavailable}, err
		}
	}

	catalog, err := migrationCatalog()
	if err != nil || len(catalog) == 0 {
		return ReadOnlyDatabaseHealth{State: ReadOnlyDatabaseCorrupt}, ErrMigrationCorrupt
	}
	dsn := fmt.Sprintf("file:%s?mode=ro&immutable=1&_pragma=foreign_keys(1)&_pragma=query_only(1)&_pragma=busy_timeout(%d)", filepath.ToSlash(path), (2 * time.Second).Milliseconds())
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return ReadOnlyDatabaseHealth{State: ReadOnlyDatabaseUnavailable}, err
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	if err := db.PingContext(ctx); err != nil {
		return ReadOnlyDatabaseHealth{State: ReadOnlyDatabaseUnavailable}, err
	}

	var queryOnly, foreignKeys int
	if err := db.QueryRowContext(ctx, "PRAGMA query_only").Scan(&queryOnly); err != nil {
		return ReadOnlyDatabaseHealth{State: ReadOnlyDatabaseUnavailable}, err
	}
	if err := db.QueryRowContext(ctx, "PRAGMA foreign_keys").Scan(&foreignKeys); err != nil {
		return ReadOnlyDatabaseHealth{State: ReadOnlyDatabaseUnavailable}, err
	}
	if queryOnly != 1 || foreignKeys != 1 {
		return ReadOnlyDatabaseHealth{State: ReadOnlyDatabaseUnavailable, QueryOnly: queryOnly == 1}, ErrInvalidConfig
	}

	var integrity string
	if err := db.QueryRowContext(ctx, "PRAGMA quick_check(1)").Scan(&integrity); err != nil {
		return ReadOnlyDatabaseHealth{State: ReadOnlyDatabaseUnavailable, QueryOnly: true}, err
	}
	if integrity != "ok" {
		return ReadOnlyDatabaseHealth{State: ReadOnlyDatabaseCorrupt, QueryOnly: true, IntegrityOK: false}, ErrMigrationCorrupt
	}

	rows, err := db.QueryContext(ctx, "SELECT version, owner, name, checksum FROM schema_migrations ORDER BY version ASC")
	if err != nil {
		return ReadOnlyDatabaseHealth{State: ReadOnlyDatabaseOutdated, QueryOnly: true, IntegrityOK: true, ExpectedVersion: uint64(len(catalog))}, nil
	}
	applied := make(map[uint64]migration, len(catalog))
	for rows.Next() {
		var item migration
		if err := rows.Scan(&item.Version, &item.Owner, &item.Name, &item.Checksum); err != nil {
			_ = rows.Close()
			return ReadOnlyDatabaseHealth{State: ReadOnlyDatabaseCorrupt, QueryOnly: true, IntegrityOK: true}, err
		}
		applied[item.Version] = item
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return ReadOnlyDatabaseHealth{State: ReadOnlyDatabaseUnavailable, QueryOnly: true, IntegrityOK: true}, err
	}
	if err := rows.Close(); err != nil {
		return ReadOnlyDatabaseUnavailableHealth(), err
	}

	known := make(map[uint64]migration, len(catalog))
	for _, item := range catalog {
		known[item.Version] = item
	}
	for version, item := range applied {
		definition, ok := known[version]
		if !ok || definition.Owner != item.Owner || definition.Name != item.Name || definition.Checksum != item.Checksum {
			return ReadOnlyDatabaseHealth{State: ReadOnlyDatabaseCorrupt, AppliedVersion: version, ExpectedVersion: uint64(len(catalog)), QueryOnly: true, IntegrityOK: true}, ErrMigrationChecksum
		}
	}
	for _, item := range catalog {
		if _, ok := applied[item.Version]; !ok {
			return ReadOnlyDatabaseHealth{State: ReadOnlyDatabaseOutdated, AppliedVersion: highestMigration(applied), ExpectedVersion: uint64(len(catalog)), QueryOnly: true, IntegrityOK: true}, nil
		}
	}
	return ReadOnlyDatabaseHealth{State: ReadOnlyDatabaseCurrent, AppliedVersion: highestMigration(applied), ExpectedVersion: uint64(len(catalog)), QueryOnly: true, IntegrityOK: true}, nil
}

func highestMigration(applied map[uint64]migration) uint64 {
	var highest uint64
	for version := range applied {
		if version > highest {
			highest = version
		}
	}
	return highest
}

func ReadOnlyDatabaseUnavailableHealth() ReadOnlyDatabaseHealth {
	return ReadOnlyDatabaseHealth{State: ReadOnlyDatabaseUnavailable}
}

// InspectOwnedStorageReadOnly reads only the global T067 totals through an
// immutable SQLite connection. Owner filesystem evidence is deliberately not
// inferred here, so the caller must keep reconciliation incomplete/uncertain.
func InspectOwnedStorageReadOnly(ctx context.Context, path string) (app.StorageLedgerSnapshot, error) {
	if ctx == nil || path == "" || !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return app.StorageLedgerSnapshot{}, ErrInvalidConfig
	}
	health, err := InspectReadOnly(ctx, path)
	if err != nil {
		return app.StorageLedgerSnapshot{}, err
	}
	if health.State != ReadOnlyDatabaseCurrent {
		if health.State == ReadOnlyDatabaseMissing {
			return app.StorageLedgerSnapshot{}, os.ErrNotExist
		}
		return app.StorageLedgerSnapshot{}, ErrInvalidConfig
	}
	dsn := fmt.Sprintf("file:%s?mode=ro&immutable=1&_pragma=foreign_keys(1)&_pragma=query_only(1)&_pragma=busy_timeout(%d)", filepath.ToSlash(path), (2 * time.Second).Milliseconds())
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return app.StorageLedgerSnapshot{}, err
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	if err := db.PingContext(ctx); err != nil {
		return app.StorageLedgerSnapshot{}, err
	}
	var logical, observed, charged, reserved, uncertain, revision int64
	err = db.QueryRowContext(ctx, `SELECT logical_bytes, observed_bytes, charged_bytes, reserved_bytes, uncertain_count, ledger_revision
		FROM storage_totals WHERE scope_kind = 'global' AND scope_id = ''`).Scan(&logical, &observed, &charged, &reserved, &uncertain, &revision)
	if errors.Is(err, sql.ErrNoRows) {
		return app.StorageLedgerSnapshot{Complete: false}, nil
	}
	if err != nil || logical < 0 || observed < 0 || charged < 0 || reserved < 0 || uncertain < 0 || revision < 0 {
		return app.StorageLedgerSnapshot{}, ErrMigrationCorrupt
	}
	var active int64
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM capacity_reservations WHERE state = 'active'").Scan(&active); err != nil || active < 0 {
		return app.StorageLedgerSnapshot{}, ErrMigrationCorrupt
	}
	global := app.StorageTotals{LogicalBytes: app.ByteSize(logical), ObservedBytes: app.ByteSize(observed), ChargedBytes: app.ByteSize(charged), ReservedBytes: app.ByteSize(reserved), UncertainCount: app.Count(uncertain), Revision: uint64(revision)}
	return app.StorageLedgerSnapshot{Revision: uint64(revision), Global: global, Pressure: storagePressure(app.StorageTotals{}, global), ActiveReservations: app.Count(active), Complete: false}, nil
}
