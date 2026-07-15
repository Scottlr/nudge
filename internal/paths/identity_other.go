//go:build !windows && !linux && !darwin && !freebsd && !openbsd && !netbsd

package paths

import "github.com/Scottlr/nudge/internal/domain/repository"

func nativeDirectoryIdentity(string) (repository.NativeIdentity, error) {
	return "", ErrNativeIdentityUnavailable
}
