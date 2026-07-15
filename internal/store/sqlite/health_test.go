package sqlite

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/Scottlr/nudge/internal/paths"
)

func TestInspectReadOnlyDoesNotCreateOrMigrate(t *testing.T) {
	root := t.TempDir()
	databasePath := filepath.Join(root, "missing", "nudge.db")
	health, err := InspectReadOnly(context.Background(), databasePath)
	if err != nil || health.State != ReadOnlyDatabaseMissing {
		t.Fatalf("missing database inspection = %#v, %v", health, err)
	}
	if _, err := os.Stat(databasePath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("inspection created database: %v", err)
	}
}

func TestInspectReadOnlyReportsCurrentDatabaseWithoutChangingFile(t *testing.T) {
	root := t.TempDir()
	protectedRoot := filepath.Join(root, "state")
	if err := paths.EnsurePrivateDir(protectedRoot); err != nil {
		t.Fatalf("EnsurePrivateDir() error = %v", err)
	}
	databasePath := filepath.Join(protectedRoot, "nudge.db")
	store, err := Open(context.Background(), databasePath)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	before, err := os.Stat(databasePath)
	if err != nil {
		t.Fatalf("Stat(before) error = %v", err)
	}
	_, beforeWAL := os.Stat(databasePath + "-wal")
	health, err := InspectReadOnly(context.Background(), databasePath)
	if err != nil || health.State != ReadOnlyDatabaseCurrent || !health.QueryOnly || !health.IntegrityOK {
		t.Fatalf("current database inspection = %#v, %v", health, err)
	}
	after, err := os.Stat(databasePath)
	if err != nil {
		t.Fatalf("Stat(after) error = %v", err)
	}
	if before.Size() != after.Size() || !before.ModTime().Equal(after.ModTime()) {
		t.Fatalf("read-only inspection changed file metadata: before=%v after=%v", before, after)
	}
	_, afterWAL := os.Stat(databasePath + "-wal")
	if (beforeWAL == nil) != (afterWAL == nil) {
		t.Fatalf("read-only inspection changed WAL sidecar state: before=%v after=%v", beforeWAL, afterWAL)
	}
}

func TestInspectReadOnlyHonorsCancellation(t *testing.T) {
	root := t.TempDir()
	protectedRoot := filepath.Join(root, "state")
	if err := paths.EnsurePrivateDir(protectedRoot); err != nil {
		t.Fatalf("EnsurePrivateDir() error = %v", err)
	}
	databasePath := filepath.Join(protectedRoot, "nudge.db")
	store, err := Open(context.Background(), databasePath)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	_ = store.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = InspectReadOnly(ctx, databasePath)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled inspection error = %v", err)
	}
}
