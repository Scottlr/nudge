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

func TestMapProposalPermissionsRequiresExactlyTheResultRoot(t *testing.T) {
	root := filepath.Clean(filepath.Join(t.TempDir(), "result"))
	policy := provider.TurnPermissionPolicy{
		Filesystem:         provider.FilesystemProposalResult,
		ReadableRoots:      []provider.PermissionRoot{{Path: root}},
		WritableRoots:      []provider.PermissionRoot{{Path: root}},
		RuntimeRoots:       []provider.PermissionRoot{{Path: filepath.Join(root, "runtime")}},
		ProposalResultRoot: provider.PermissionRoot{Path: root},
		Containment: provider.ContainmentEvidence{
			CanonicalRead: true, CanonicalWrite: true, NoSymlinkEscape: true,
			NoJunctionEscape: true, NoMountEscape: true, NoHardLinkAlias: true,
			HandlesQuiescent: true,
		},
		Network:          provider.NetworkDisabled,
		RuntimeApprovals: provider.RuntimeApprovalsExplicit,
	}
	sandbox, err := MapTurnPermissions(provider.TurnPropose, policy, root)
	if err != nil {
		t.Fatal(err)
	}
	if sandbox.Type != "workspaceWrite" || sandbox.NetworkAccess == nil || *sandbox.NetworkAccess || len(sandbox.WritableRoots) != 1 || sandbox.WritableRoots[0] != root {
		t.Fatalf("sandbox = %+v, want one result root and no network", sandbox)
	}
	policy.WritableRoots = []provider.PermissionRoot{{Path: filepath.Join(root, "nested")}}
	if _, err := MapTurnPermissions(provider.TurnPropose, policy, root); !errors.Is(err, ErrPermissionUnsupported) {
		t.Fatalf("nested writable root error = %v, want unsupported", err)
	}
}
