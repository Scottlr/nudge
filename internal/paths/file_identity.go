package paths

import "github.com/Scottlr/nudge/internal/domain/repository"

// NativeFileIdentity returns opaque native identity evidence for one regular
// file. The path is checked without following a final symlink; callers still
// recheck the identity after reading the file.
func NativeFileIdentity(path string) (repository.NativeAliasEvidence, error) {
	if path == "" {
		return repository.NativeAliasEvidence{}, ErrNativeIdentityUnavailable
	}
	return nativeFileIdentity(path)
}
