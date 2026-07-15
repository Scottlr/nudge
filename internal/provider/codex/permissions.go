package codex

import (
	"errors"
	"path/filepath"
	"strings"

	"github.com/Scottlr/nudge/internal/provider"
	"github.com/Scottlr/nudge/internal/provider/codex/protocol"
)

var ErrPermissionUnsupported = errors.New("codex permission boundary is unsupported")

func pathWithin(root, child string) bool {
	rel, err := filepath.Rel(filepath.Clean(root), filepath.Clean(child))
	return err == nil && (rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))))
}

func pathWithinAny(roots []provider.PermissionRoot, child string) bool {
	for _, root := range roots {
		if pathWithin(root.Path, child) {
			return true
		}
	}
	return false
}

// PermissionCapabilities records adapter evidence rather than user intent.
// The pinned schema can express read-only execution and a cwd, but does not
// carry an explicit readable-root list. Exact snapshot turns therefore need
// an independent provider/platform proof before they are admitted.
type PermissionCapabilities struct {
	ReadOnlyFilesystem bool
	ExactReadRoots     bool
	NetworkDisabled    bool
}

func defaultPermissionCapabilities() PermissionCapabilities {
	return PermissionCapabilities{
		ReadOnlyFilesystem: true,
		NetworkDisabled:    true,
	}
}

func mapDiscussionPermissions(policy provider.TurnPermissionPolicy, workingDir string, capabilities PermissionCapabilities) (protocol.SandboxPolicy, error) {
	if err := policy.Validate(provider.DefaultValidationLimits()); err != nil || policy.Filesystem == provider.FilesystemProposalResult || policy.Network != provider.NetworkDisabled || len(policy.WritableRoots) != 0 || len(policy.RuntimeRoots) > 1 {
		return protocol.SandboxPolicy{}, ErrPermissionUnsupported
	}
	if !capabilities.ReadOnlyFilesystem || !capabilities.NetworkDisabled {
		return protocol.SandboxPolicy{}, ErrPermissionUnsupported
	}
	if workingDir != "" && !filepath.IsAbs(workingDir) {
		return protocol.SandboxPolicy{}, ErrPermissionUnsupported
	}
	if policy.Filesystem == provider.FilesystemReviewSnapshot {
		if !capabilities.ExactReadRoots || len(policy.ReadableRoots) != 1 || workingDir != filepath.Clean(policy.ReadableRoots[0].Path) || !policy.Containment.CanonicalRead {
			return protocol.SandboxPolicy{}, ErrPermissionUnsupported
		}
	} else if len(policy.ReadableRoots) != 0 || workingDir != "" && !pathWithinAny(policy.RuntimeRoots, workingDir) {
		return protocol.SandboxPolicy{}, ErrPermissionUnsupported
	}
	networkDisabled := false
	return protocol.SandboxPolicy{Type: "readOnly", NetworkAccess: &networkDisabled}, nil
}

// MapDiscussionPermissions validates the provider-neutral policy and maps it
// to the pinned Codex turn schema. It intentionally fails closed when exact
// snapshot containment is not part of the current adapter evidence.
func MapDiscussionPermissions(policy provider.TurnPermissionPolicy, workingDir string) (protocol.SandboxPolicy, error) {
	return MapDiscussionPermissionsWithCapabilities(policy, workingDir, defaultPermissionCapabilities())
}

// MapDiscussionPermissionsWithCapabilities is used by a qualified adapter
// that has current evidence for the exact cwd/read-root containment contract.
func MapDiscussionPermissionsWithCapabilities(policy provider.TurnPermissionPolicy, workingDir string, capabilities PermissionCapabilities) (protocol.SandboxPolicy, error) {
	return mapDiscussionPermissions(policy, workingDir, capabilities)
}

// MapTurnPermissions maps both provider-neutral turn modes. Discussion keeps
// the stricter exact-read-root gate above; proposal mode is limited to the
// declared result roots and still has network disabled.
func MapTurnPermissions(mode provider.TurnMode, policy provider.TurnPermissionPolicy, workingDir string) (protocol.SandboxPolicy, error) {
	if mode == provider.TurnDiscuss {
		return MapDiscussionPermissions(policy, workingDir)
	}
	if mode != provider.TurnPropose || policy.Validate(provider.DefaultValidationLimits()) != nil || policy.Filesystem != provider.FilesystemProposalResult || policy.Network != provider.NetworkDisabled || len(policy.ReadableRoots) != 1 || len(policy.WritableRoots) != 1 || policy.ReadableRoots[0].Path != policy.ProposalResultRoot.Path || policy.WritableRoots[0].Path != policy.ProposalResultRoot.Path || workingDir == "" || !filepath.IsAbs(workingDir) || !filepath.IsAbs(policy.ProposalResultRoot.Path) || workingDir != filepath.Clean(policy.ProposalResultRoot.Path) {
		return protocol.SandboxPolicy{}, ErrPermissionUnsupported
	}
	for _, root := range policy.RuntimeRoots {
		if !pathWithin(policy.ProposalResultRoot.Path, root.Path) {
			return protocol.SandboxPolicy{}, ErrPermissionUnsupported
		}
	}
	networkDisabled := false
	roots := make([]string, 0, len(policy.WritableRoots))
	for _, root := range policy.WritableRoots {
		roots = append(roots, root.Path)
	}
	return protocol.SandboxPolicy{Type: "workspaceWrite", NetworkAccess: &networkDisabled, WritableRoots: roots}, nil
}

// mapThreadSandbox maps the legacy thread-start mode. Detailed policy is
// repeated at turn/start, where the stable schema supports network control.
func mapThreadSandbox(policy provider.TurnPermissionPolicy, workingDir string) (string, error) {
	if policy.Filesystem == provider.FilesystemProposalResult {
		if _, err := MapTurnPermissions(provider.TurnPropose, policy, workingDir); err != nil {
			return "", err
		}
		return "workspace-write", nil
	}
	if _, err := mapDiscussionPermissions(policy, workingDir, defaultPermissionCapabilities()); err != nil {
		return "", err
	}
	return "read-only", nil
}
