// Package sqlite implements the application-owned ReviewStore with a local,
// CGo-free SQLite database.
package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Scottlr/nudge/internal/app"
	"github.com/Scottlr/nudge/internal/paths"
	_ "modernc.org/sqlite"
)

var (
	// ErrInvalidConfig reports an unsafe or incomplete store configuration.
	ErrInvalidConfig = errors.New("invalid SQLite store configuration")
	// ErrMigrationChecksum reports an applied migration whose bytes changed.
	ErrMigrationChecksum = errors.New("SQLite migration checksum mismatch")
	// ErrMigrationUnknown reports a schema version this binary cannot interpret.
	ErrMigrationUnknown = errors.New("unknown SQLite migration")
	// ErrMigrationCorrupt reports malformed migration metadata.
	ErrMigrationCorrupt = errors.New("corrupt SQLite migration metadata")
	// ErrForeignKeysDisabled reports a connection that failed the required FK policy.
	ErrForeignKeysDisabled = errors.New("SQLite foreign keys are disabled")
	// ErrPageItemTooLarge reports an item that cannot fit as a complete page item.
	ErrPageItemTooLarge = errors.New("review page item exceeds encoded budget")
)

const (
	defaultBusyTimeout     = 5 * time.Second
	defaultMaxOpenConns    = 4
	defaultMaxMessageBytes = 8 << 20
)

// Config controls the bounded SQLite connection and body policy.
type Config struct {
	Path               string
	BusyTimeout        time.Duration
	MaxOpenConnections int
	MaxMessageBytes    uint64
}

// Store is a process-local SQLite adapter. The write mutex serializes the
// application's effective writer; SQLite and its busy policy still handle
// cross-process contention.
type Store struct {
	db        *sql.DB
	config    Config
	writeMu   sync.Mutex
	closed    atomic.Bool
	closeOnce sync.Once
	closeErr  error
}

// Open opens, migrates, and verifies a protected SQLite database at path.
func Open(ctx context.Context, path string) (*Store, error) {
	return OpenWithConfig(ctx, Config{Path: path})
}

// OpenWithConfig opens a protected SQLite database and applies embedded
// forward-only migrations before returning a writable store.
func OpenWithConfig(ctx context.Context, config Config) (*Store, error) {
	if ctx == nil {
		return nil, ErrInvalidConfig
	}
	if config.BusyTimeout == 0 {
		config.BusyTimeout = defaultBusyTimeout
	}
	if config.MaxOpenConnections == 0 {
		config.MaxOpenConnections = defaultMaxOpenConns
	}
	if config.MaxMessageBytes == 0 {
		config.MaxMessageBytes = defaultMaxMessageBytes
	}
	if err := validateConfig(config); err != nil {
		return nil, err
	}
	if err := prepareDatabaseFile(config.Path); err != nil {
		return nil, err
	}

	dsn := fmt.Sprintf("file:%s?_pragma=foreign_keys(1)&_pragma=journal_mode(WAL)&_pragma=busy_timeout(%d)", filepath.ToSlash(config.Path), config.BusyTimeout.Milliseconds())
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(config.MaxOpenConnections)
	db.SetMaxIdleConns(config.MaxOpenConnections)
	db.SetConnMaxIdleTime(5 * time.Minute)
	store := &Store{db: db, config: config}
	if err := store.configure(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}
	store.writeMu.Lock()
	err = store.migrate(ctx)
	store.writeMu.Unlock()
	if err != nil {
		_ = db.Close()
		return nil, err
	}
	return store, nil
}

func validateConfig(config Config) error {
	if config.Path == "" || !filepath.IsAbs(config.Path) || filepath.Clean(config.Path) != config.Path || filepath.Base(config.Path) == "." || config.BusyTimeout <= 0 || config.MaxOpenConnections <= 0 || config.MaxMessageBytes == 0 {
		return ErrInvalidConfig
	}
	return nil
}

func prepareDatabaseFile(path string) error {
	root := filepath.Dir(path)
	if err := paths.EnsurePrivateDir(root); err != nil {
		return err
	}
	file, err := paths.OpenProtectedFile(root, filepath.Base(path), os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
	if err == nil {
		return file.Close()
	}
	if !errors.Is(err, os.ErrExist) {
		return err
	}
	file, err = paths.OpenExistingProtectedFile(root, filepath.Base(path))
	if err != nil {
		return err
	}
	return file.Close()
}

func (s *Store) configure(ctx context.Context) error {
	if s == nil || s.db == nil {
		return app.ErrReviewStoreClosed
	}
	if err := s.db.PingContext(ctx); err != nil {
		return err
	}
	if _, err := s.db.ExecContext(ctx, "PRAGMA foreign_keys = ON"); err != nil {
		return err
	}
	if _, err := s.db.ExecContext(ctx, "PRAGMA journal_mode = WAL"); err != nil {
		return err
	}
	var enabled int
	if err := s.db.QueryRowContext(ctx, "PRAGMA foreign_keys").Scan(&enabled); err != nil {
		return err
	}
	if enabled != 1 {
		return ErrForeignKeysDisabled
	}
	return nil
}

func (s *Store) ensureOpen() error {
	if s == nil || s.db == nil || s.closed.Load() {
		return app.ErrReviewStoreClosed
	}
	return nil
}

// Close closes the database and is safe to call repeatedly.
func (s *Store) Close() error {
	if s == nil || s.db == nil {
		return app.ErrReviewStoreClosed
	}
	s.closeOnce.Do(func() {
		s.closed.Store(true)
		s.closeErr = s.db.Close()
	})
	return s.closeErr
}
