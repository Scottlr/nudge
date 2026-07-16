package filelock

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"path/filepath"

	"github.com/Scottlr/nudge/internal/app"
	"github.com/Scottlr/nudge/internal/domain"
)

// RepositoryMaintenanceGate owns one stable native lock per repository. It
// is deliberately separate from session locks so cleanup can acquire the
// gate before enumerating and claiming session writers.
type RepositoryMaintenanceGate struct {
	root string
}

// NewRepositoryMaintenanceGate creates a gate rooted below Nudge state.
func NewRepositoryMaintenanceGate(root string) (*RepositoryMaintenanceGate, error) {
	if root == "" || !filepath.IsAbs(root) || filepath.Clean(root) != root {
		return nil, ErrInvalidPath
	}
	return &RepositoryMaintenanceGate{root: filepath.Join(root, "maintenance")}, nil
}

// Acquire holds the repository maintenance lock until the returned closer is
// closed. It never treats a lock-file's existence as ownership evidence.
func (g *RepositoryMaintenanceGate) Acquire(ctx context.Context, repositoryID domain.RepositoryID) (io.Closer, error) {
	if g == nil || ctx == nil || repositoryID == "" {
		return nil, app.ErrReviewStoreInput
	}
	digest := sha256.Sum256([]byte(string(repositoryID)))
	path := filepath.Join(g.root, hex.EncodeToString(digest[:])+".lock")
	lock, err := Acquire(ctx, path)
	if err != nil {
		if errors.Is(err, ErrBusy) {
			return nil, app.ErrRepositoryMaintenance
		}
		return nil, err
	}
	return lock, nil
}

var _ app.RepositoryMaintenanceGate = (*RepositoryMaintenanceGate)(nil)
