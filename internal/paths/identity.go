package paths

import (
	"errors"
	"os"

	"github.com/Scottlr/nudge/internal/domain/repository"
)

// ErrNativeIdentityUnavailable reports that the platform could not prove a
// stable identity for a directory handle.
var ErrNativeIdentityUnavailable = errors.New("native directory identity unavailable")

// NativeDirectoryIdentity returns the platform-native identity of an existing
// real directory. Platform implementations reject aliases and reparse points;
// callers must retain the canonical path separately.
func NativeDirectoryIdentity(path string) (repository.NativeIdentity, error) {
	info, err := os.Lstat(path)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return "", ErrNativeIdentityUnavailable
	}
	return nativeDirectoryIdentity(path)
}
