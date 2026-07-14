// Package filelock contains the sole native OS-lock seam used by Nudge.
package filelock

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/Scottlr/nudge/internal/paths"
)

var (
	// ErrInvalidPath reports a lock path that is not an absolute direct file.
	ErrInvalidPath = errors.New("invalid lock path")
	// ErrClosed reports an operation on a released lock.
	ErrClosed = errors.New("lock is closed")
)

const retryInterval = 10 * time.Millisecond

// Lock is an exclusive cross-process lock. Closing it releases the native
// lock and its file descriptor; ownership is never inferred from timestamps.
type Lock struct {
	file *os.File
	once sync.Once
	terr error
}

// Acquire opens or creates a protected lock file and waits cancellably for its
// native exclusive lock. The caller must hold returned ownership until Close.
func Acquire(ctx context.Context, path string) (*Lock, error) {
	if ctx == nil || path == "" || !filepath.IsAbs(path) || filepath.Clean(path) != path || filepath.Base(path) == "." || filepath.Base(path) == string(filepath.Separator) {
		return nil, ErrInvalidPath
	}
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}
	file, err := openLockFile(path)
	if err != nil {
		return nil, err
	}
	lock := &Lock{file: file}
	for {
		acquired, err := tryLock(file)
		if err != nil {
			_ = file.Close()
			return nil, err
		}
		if acquired {
			return lock, nil
		}
		timer := time.NewTimer(retryInterval)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			_ = file.Close()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
}

// Close releases the native lock. It is safe to call more than once.
func (l *Lock) Close() error {
	if l == nil || l.file == nil {
		return ErrClosed
	}
	l.once.Do(func() {
		l.terr = unlock(l.file)
		if err := l.file.Close(); l.terr == nil {
			l.terr = err
		}
	})
	return l.terr
}

func openLockFile(path string) (*os.File, error) {
	root := filepath.Dir(path)
	if err := paths.EnsurePrivateDir(root); err != nil {
		return nil, err
	}
	file, err := paths.OpenProtectedFile(root, filepath.Base(path), os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
	if err == nil {
		return file, nil
	}
	if !os.IsExist(err) {
		return nil, err
	}
	return paths.OpenExistingProtectedFile(root, filepath.Base(path))
}
