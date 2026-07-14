//go:build !windows && !linux && !darwin && !freebsd && !openbsd && !netbsd

package gitcli

import "github.com/Scottlr/nudge/internal/domain/repository"

func nativePathIdentity(string) (repository.NativeIdentity, error) {
	return "", nativeIdentityError(nil)
}
