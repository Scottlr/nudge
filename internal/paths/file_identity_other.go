//go:build !windows && !linux && !darwin && !freebsd && !openbsd && !netbsd

package paths

import "github.com/Scottlr/nudge/internal/domain/repository"

func nativeFileIdentity(string) (repository.NativeAliasEvidence, error) {
	return repository.NativeAliasEvidence{}, ErrNativeIdentityUnavailable
}
