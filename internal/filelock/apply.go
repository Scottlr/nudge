package filelock

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"path/filepath"
	"sync"

	"github.com/Scottlr/nudge/internal/app"
	"github.com/Scottlr/nudge/internal/domain"
)

// DestinationLeaseManager provides the native cross-process lock for one
// canonical repository/worktree apply destination.
type DestinationLeaseManager struct {
	root string
}

// NewDestinationLeaseManager creates a lazily-created protected destination
// lock root below Nudge-owned state.
func NewDestinationLeaseManager(root string) (*DestinationLeaseManager, error) {
	if root == "" || !filepath.IsAbs(root) || filepath.Clean(root) != root {
		return nil, ErrInvalidPath
	}
	return &DestinationLeaseManager{root: filepath.Join(root, "destinations")}, nil
}

// Acquire locks the stable pair of Nudge repository and worktree identities.
func (m *DestinationLeaseManager) Acquire(ctx context.Context, repositoryID domain.RepositoryID, worktreeID domain.WorktreeID) (app.ApplyExecutionLease, error) {
	if m == nil || ctx == nil || repositoryID == "" || worktreeID == "" {
		return nil, app.ErrInvalidApplyPreflight
	}
	key := []byte(string(repositoryID) + "\x00" + string(worktreeID))
	digest := sha256.Sum256(key)
	path := filepath.Join(m.root, hex.EncodeToString(digest[:])+".lock")
	lock, err := Acquire(ctx, path)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return nil, err
		}
		return nil, err
	}
	return &destinationLease{lock: lock}, nil
}

type destinationLease struct {
	lock *Lock
	once sync.Once
	err  error
}

func (l *destinationLease) Close() error {
	if l == nil || l.lock == nil {
		return ErrClosed
	}
	l.once.Do(func() {
		l.err = l.lock.Close()
	})
	return l.err
}

var _ app.ApplyDestinationLockManager = (*DestinationLeaseManager)(nil)
var _ app.ApplyExecutionLease = (*destinationLease)(nil)
