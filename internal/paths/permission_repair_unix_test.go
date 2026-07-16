//go:build !windows && (linux || darwin)

package paths

import (
	"context"
	"errors"
	"os"
	"testing"
)

func TestProtectedPermissionServiceRepairsUnixModeOnSameObject(t *testing.T) {
	locations := testPermissionLocations(t)
	if err := os.Chmod(locations.ConfigRoot, 0o750); err != nil {
		t.Fatal(err)
	}
	service, err := NewProtectedPermissionService(locations)
	if err != nil {
		t.Fatal(err)
	}
	target, err := service.LoadProtectedPermissionTarget(context.Background(), "config-root")
	if err != nil {
		t.Fatal(err)
	}
	if target.CurrentPermissionHash == target.DesiredPermissionHash {
		t.Fatal("mode drift was not observed")
	}
	proof, err := service.Inspect(context.Background(), target)
	if err != nil {
		t.Fatal(err)
	}
	after, err := service.Repair(context.Background(), target, proof)
	if err != nil {
		t.Fatal(err)
	}
	if after.NativeIdentityHash != target.NativeIdentityHash || after.AfterPermissionHash != target.DesiredPermissionHash {
		t.Fatalf("after proof = %#v", after)
	}
	info, err := os.Stat(locations.ConfigRoot)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o700 {
		t.Fatalf("mode = %o, want 700", info.Mode().Perm())
	}
}

func TestProtectedPermissionServiceRejectsUnixAlias(t *testing.T) {
	base := t.TempDir()
	actual := base + "-actual"
	if err := os.Mkdir(actual, 0o700); err != nil {
		t.Fatal(err)
	}
	alias := base + "-alias"
	if err := os.Symlink(actual, alias); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	locations := testPermissionLocations(t)
	locations.ConfigRoot = alias
	locations.ThemesRoot = alias + "/themes"
	locations.ConfigFile = alias + "/config.toml"
	service, err := NewProtectedPermissionService(locations)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.LoadProtectedPermissionTarget(context.Background(), "config-root"); !errors.Is(err, ErrProtectedPermissionAlias) {
		t.Fatalf("LoadProtectedPermissionTarget() error = %v, want alias rejection", err)
	}
}
