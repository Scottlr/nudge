package sqlite

import (
	"context"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"sort"
	"strings"
	"time"
)

// migrationFiles is the immutable migration catalog shipped with the binary.
//
//go:embed migrations/*.sql
var migrationFiles embed.FS

type migration struct {
	Version  uint64
	Owner    string
	Name     string
	SQL      string
	Checksum string
}

// MigrationStatus reports the applied schema identity.
type MigrationStatus struct {
	Version  uint64
	Checksum string
}

func migrationCatalog() ([]migration, error) {
	entries, err := fs.Glob(migrationFiles, "migrations/*.sql")
	if err != nil {
		return nil, err
	}
	sort.Strings(entries)
	catalog := make([]migration, 0, len(entries))
	for index, name := range entries {
		data, err := migrationFiles.ReadFile(name)
		if err != nil {
			return nil, err
		}
		digest := sha256.Sum256(data)
		owner := "core_review"
		if strings.Contains(name, "owned_storage") {
			owner = "owned_storage"
		} else if strings.Contains(name, "repository_preferences") {
			owner = "persist_nudge_state"
		} else if strings.Contains(name, "reconciliation") {
			owner = "git_proposals"
		} else if strings.Contains(name, "provider_lifecycle") {
			owner = "integrate_nudge_codex"
		} else if strings.Contains(name, "review_snapshots") {
			owner = "git_proposals"
		} else if strings.Contains(name, "discussion_turn_provenance") {
			owner = "integrate_nudge_codex"
		} else if strings.Contains(name, "streamed_message_bodies") {
			owner = "integrate_nudge_codex"
		} else if strings.Contains(name, "runtime_approval_records") {
			owner = "integrate_nudge_codex"
		} else if strings.Contains(name, "proposal_workspaces") {
			owner = "git_proposals"
		} else if strings.Contains(name, "workspace_creation_evidence") {
			owner = "git_proposals"
		} else if strings.Contains(name, "workspace_lifecycle") {
			owner = "git_proposals"
		} else if strings.Contains(name, "result_snapshots") {
			owner = "git_proposals"
		} else if strings.Contains(name, "proposal_patch_artifacts") {
			owner = "git_proposals"
		} else if strings.Contains(name, "proposal_patch_artifact_refs") {
			owner = "git_proposals"
		} else if strings.Contains(name, "apply_operations") {
			owner = "git_proposals"
		} else if strings.Contains(name, "proposal_apply_reference") {
			owner = "git_proposals"
		}
		catalog = append(catalog, migration{
			Version:  uint64(index + 1),
			Owner:    owner,
			Name:     name,
			SQL:      string(data),
			Checksum: hex.EncodeToString(digest[:]),
		})
	}
	return catalog, nil
}

func (s *Store) migrate(ctx context.Context) error {
	if err := s.ensureOpen(); err != nil {
		return err
	}
	catalog, err := migrationCatalog()
	if err != nil {
		return err
	}
	if len(catalog) == 0 {
		return ErrMigrationCorrupt
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		version INTEGER PRIMARY KEY,
		owner TEXT NOT NULL,
		name TEXT NOT NULL,
		checksum TEXT NOT NULL,
		applied_at TEXT NOT NULL
	)`); err != nil {
		return err
	}
	rows, err := tx.QueryContext(ctx, "SELECT version, owner, name, checksum FROM schema_migrations ORDER BY version ASC")
	if err != nil {
		return err
	}
	applied := make(map[uint64]migration, len(catalog))
	for rows.Next() {
		var item migration
		if err := rows.Scan(&item.Version, &item.Owner, &item.Name, &item.Checksum); err != nil {
			_ = rows.Close()
			return err
		}
		applied[item.Version] = item
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	known := make(map[uint64]migration, len(catalog))
	for _, item := range catalog {
		known[item.Version] = item
	}
	for version, item := range applied {
		definition, ok := known[version]
		if !ok {
			return fmt.Errorf("%w: version %d", ErrMigrationUnknown, version)
		}
		if item.Checksum != definition.Checksum || item.Owner != definition.Owner || item.Name != definition.Name {
			return fmt.Errorf("%w: version %d", ErrMigrationChecksum, version)
		}
	}
	for _, item := range catalog {
		if _, exists := applied[item.Version]; exists {
			continue
		}
		if _, err := tx.ExecContext(ctx, item.SQL); err != nil {
			return fmt.Errorf("migration %d: %w", item.Version, err)
		}
		if _, err := tx.ExecContext(ctx, "INSERT INTO schema_migrations(version, owner, name, checksum, applied_at) VALUES(?, ?, ?, ?, ?)", item.Version, item.Owner, item.Name, item.Checksum, time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
			return fmt.Errorf("record migration %d: %w", item.Version, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	return nil
}

// MigrationStatus returns the highest applied embedded migration identity.
func (s *Store) MigrationStatus(ctx context.Context) (MigrationStatus, error) {
	if err := s.ensureOpen(); err != nil {
		return MigrationStatus{}, err
	}
	var status MigrationStatus
	var version int64
	if err := s.db.QueryRowContext(ctx, "SELECT version, checksum FROM schema_migrations ORDER BY version DESC LIMIT 1").Scan(&version, &status.Checksum); err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return MigrationStatus{}, err
		}
		return MigrationStatus{}, err
	}
	if version <= 0 {
		return MigrationStatus{}, ErrMigrationCorrupt
	}
	status.Version = uint64(version)
	return status, nil
}
