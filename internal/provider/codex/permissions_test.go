package codex

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/Scottlr/nudge/internal/provider"
)

func TestMapDiscussionPermissionsReadOnly(t *testing.T) {
	policy := provider.TurnPermissionPolicy{
		Filesystem:       provider.FilesystemPromptOnly,
		Network:          provider.NetworkDisabled,
		RuntimeApprovals: provider.RuntimeApprovalsDisabled,
	}
	sandbox, err := MapDiscussionPermissions(policy, "")
	if err != nil {
		t.Fatal(err)
	}
	if sandbox.Type != "readOnly" || sandbox.NetworkAccess == nil || *sandbox.NetworkAccess || len(sandbox.WritableRoots) != 0 {
		t.Fatalf("sandbox = %+v, want read-only no-write/no-network", sandbox)
	}
}

func TestMapDiscussionPermissionsFailsClosedForUnsupportedSnapshotRoots(t *testing.T) {
	root := filepath.Clean(filepath.Join(t.TempDir(), "snapshot"))
	policy := provider.TurnPermissionPolicy{
		Filesystem:    provider.FilesystemReviewSnapshot,
		ReadableRoots: []provider.PermissionRoot{{Path: root}},
		Containment: provider.ContainmentEvidence{
			CanonicalRead: true, NoSymlinkEscape: true, NoJunctionEscape: true,
			NoMountEscape: true, NoHardLinkAlias: true, HandlesQuiescent: true,
		},
		Network:          provider.NetworkDisabled,
		RuntimeApprovals: provider.RuntimeApprovalsDisabled,
	}
	if _, err := MapDiscussionPermissions(policy, root); !errors.Is(err, ErrPermissionUnsupported) {
		t.Fatalf("snapshot mapping error = %v, want unsupported", err)
	}
}
