package filelock

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"path/filepath"
	"strings"

	"github.com/Scottlr/nudge/internal/paths"
)

var ErrInvalidPermissionResource = errors.New("invalid protected permission resource")

// ProtectedPermissionLeaseManager provides one stable native lock per
// protected root. The lock directory is itself beneath a validated private
// state root, so a weak state root disables repair rather than weakening the
// owner proof.
type ProtectedPermissionLeaseManager struct {
	root string
}

// NewProtectedPermissionLeaseManager binds the manager to an existing private
// state root. It does not create or repair the root.
func NewProtectedPermissionLeaseManager(root string) (*ProtectedPermissionLeaseManager, error) {
	if err := paths.ValidatePrivateDir(root); err != nil {
		return nil, err
	}
	return &ProtectedPermissionLeaseManager{root: root}, nil
}

// Acquire obtains the cross-process lock for one registered protected root.
func (m *ProtectedPermissionLeaseManager) Acquire(ctx context.Context, resourceID string) (io.Closer, error) {
	if m == nil || m.root == "" || !safePermissionResource(resourceID) {
		return nil, ErrInvalidPermissionResource
	}
	if err := paths.ValidatePrivateDir(m.root); err != nil {
		return nil, err
	}
	digest := sha256.Sum256([]byte(resourceID))
	lockPath := filepath.Join(m.root, "permission-repairs", hex.EncodeToString(digest[:])+".lock")
	return Acquire(ctx, lockPath)
}

func safePermissionResource(value string) bool {
	return value != "" && len(value) <= 128 && strings.TrimSpace(value) == value && !strings.ContainsAny(value, "/\\\r\n\x00")
}
