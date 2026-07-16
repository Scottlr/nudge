package paths

import (
	"context"
	"path/filepath"
	"testing"
)

func TestProtectedPermissionServicePreservesDesiredRootIdentity(t *testing.T) {
	locations := testPermissionLocations(t)
	service, err := NewProtectedPermissionService(locations)
	if err != nil {
		t.Fatal(err)
	}
	target, err := service.LoadProtectedPermissionTarget(context.Background(), "state-root")
	if err != nil {
		t.Fatal(err)
	}
	proof, err := service.Inspect(context.Background(), target)
	if err != nil {
		t.Fatal(err)
	}
	if err := proof.Validate(); err != nil {
		t.Fatal(err)
	}
	if proof.BeforePermissionHash != target.DesiredPermissionHash {
		t.Fatalf("permission hash = %q, want desired %q", proof.BeforePermissionHash, target.DesiredPermissionHash)
	}
	after, err := service.Repair(context.Background(), target, proof)
	if err != nil {
		t.Fatal(err)
	}
	if after.NativeIdentityHash != target.NativeIdentityHash || after.AfterPermissionHash != target.DesiredPermissionHash {
		t.Fatalf("after proof = %#v", after)
	}
	if targets, err := service.ListProtectedPermissionTargets(context.Background()); err != nil {
		t.Fatal(err)
	} else if len(targets) != 0 {
		t.Fatalf("desired roots reported as repairable: %#v", targets)
	}
}

func testPermissionLocations(t *testing.T) Locations {
	t.Helper()
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	locations := Locations{
		ConfigRoot: filepath.Join(base, "config"), StateRoot: filepath.Join(base, "state"),
		CacheRoot: filepath.Join(base, "cache"), WorkspaceRoot: filepath.Join(base, "cache", "workspaces"),
		LogRoot: filepath.Join(base, "log"), ThemesRoot: filepath.Join(base, "config", "themes"),
		ConfigFile: filepath.Join(base, "config", "config.toml"),
	}
	if err := locations.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, root := range []string{locations.ConfigRoot, locations.StateRoot, locations.CacheRoot, locations.WorkspaceRoot, locations.LogRoot} {
		if err := EnsurePrivateDir(root); err != nil {
			t.Fatal(err)
		}
	}
	return locations
}
