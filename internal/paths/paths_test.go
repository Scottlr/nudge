package paths

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func TestResolveOverrideLocations(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	environ := map[string]string{
		"NUDGE_CONFIG_HOME": filepath.Join(root, "config"),
		"NUDGE_STATE_HOME":  filepath.Join(root, "state"),
		"NUDGE_CACHE_HOME":  filepath.Join(root, "cache"),
		"NUDGE_LOG_HOME":    filepath.Join(root, "logs"),
	}
	locations, err := Resolve(environ)
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if locations.ConfigRoot != environ["NUDGE_CONFIG_HOME"] || locations.StateRoot != environ["NUDGE_STATE_HOME"] || locations.CacheRoot != environ["NUDGE_CACHE_HOME"] || locations.LogRoot != environ["NUDGE_LOG_HOME"] {
		t.Fatalf("resolved roots = %+v", locations)
	}
	if locations.WorkspaceRoot != filepath.Join(locations.CacheRoot, "workspaces") || locations.ThemesRoot != filepath.Join(locations.ConfigRoot, "themes") || locations.ConfigFile != filepath.Join(locations.ConfigRoot, "config.toml") {
		t.Fatalf("derived roots = %+v", locations)
	}
}

func TestResolveRejectsRelativeOverride(t *testing.T) {
	t.Parallel()

	if _, err := Resolve(map[string]string{"NUDGE_CONFIG_HOME": "relative"}); !errors.Is(err, ErrInvalidLocation) {
		t.Fatalf("relative override error = %v", err)
	}
}

func TestProtectedCreationIsPrivateAndNoFollow(t *testing.T) {
	root := filepath.Join(t.TempDir(), "nudge")
	if err := EnsurePrivateDir(root); err != nil {
		t.Fatalf("EnsurePrivateDir() error = %v", err)
	}
	file, err := OpenProtectedFile(root, "source.toml", os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		t.Fatalf("OpenProtectedFile(create) error = %v", err)
	}
	if _, err := io.WriteString(file, "version = 1\n"); err != nil {
		t.Fatalf("write protected file: %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("close protected file: %v", err)
	}
	if _, err := OpenProtectedFile(root, "source.toml", os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600); err == nil {
		t.Fatal("second create unexpectedly succeeded")
	}
	if _, err := OpenProtectedFile(root, filepath.Join("..", "outside"), os.O_RDONLY, 0); !errors.Is(err, ErrProtectedPath) {
		t.Fatalf("traversal error = %v", err)
	}

	alias := filepath.Join(root, "alias")
	if err := os.Symlink(root, alias); err != nil {
		t.Skipf("symlink creation unavailable: %v", err)
	}
	if _, err := OpenProtectedFile(root, filepath.Join("alias", "new"), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600); !errors.Is(err, ErrProtectedAlias) {
		t.Fatalf("alias error = %v", err)
	}
}
