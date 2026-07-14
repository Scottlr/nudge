package filelock

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"path/filepath"
	"sync"

	"github.com/Scottlr/nudge/internal/app"
	"github.com/Scottlr/nudge/internal/domain"
)

// SessionLeaseManager is the protected native adapter for application-owned
// writable review sessions. It uses a stable compatibility lock by default;
// distinct sessions receive an explicitly different lock identity.
type SessionLeaseManager struct {
	root string
}

// NewSessionLeaseManager creates an adapter rooted below a Nudge-owned state
// directory. The directory is created lazily by the protected lock primitive.
func NewSessionLeaseManager(root string) (*SessionLeaseManager, error) {
	if root == "" || !filepath.IsAbs(root) || filepath.Clean(root) != root {
		return nil, ErrInvalidPath
	}
	return &SessionLeaseManager{root: filepath.Join(root, "sessions")}, nil
}

// Acquire implements app.SessionLeaseManager with a non-blocking OS lock.
func (m *SessionLeaseManager) Acquire(ctx context.Context, request app.SessionLeaseRequest) (app.SessionLease, error) {
	if m == nil || ctx == nil || request.Validate() != nil {
		return nil, app.ErrReviewStoreInput
	}
	lockPath, err := m.lockPath(request)
	if err != nil {
		return nil, err
	}
	lock, err := TryAcquire(ctx, lockPath)
	if errors.Is(err, ErrBusy) {
		return nil, app.ErrSessionBusy
	}
	if err != nil {
		return nil, err
	}
	return &sessionLease{id: request.LeaseID, lock: lock}, nil
}

func (m *SessionLeaseManager) lockPath(request app.SessionLeaseRequest) (string, error) {
	key, err := json.Marshal(request.Key)
	if err != nil {
		return "", err
	}
	if request.Distinct {
		key = append(key, 0)
		key = append(key, []byte(request.SessionID)...)
	}
	digest := sha256.Sum256(key)
	return filepath.Join(m.root, hex.EncodeToString(digest[:])+".lock"), nil
}

type sessionLease struct {
	id   domain.SessionLeaseID
	lock *Lock
	once sync.Once
	err  error
}

func (l *sessionLease) LeaseID() domain.SessionLeaseID {
	if l == nil {
		return ""
	}
	return l.id
}

func (l *sessionLease) Close() error {
	if l == nil || l.lock == nil {
		return ErrClosed
	}
	l.once.Do(func() {
		l.err = l.lock.Close()
	})
	return l.err
}

var _ app.SessionLeaseManager = (*SessionLeaseManager)(nil)
var _ app.SessionLease = (*sessionLease)(nil)
