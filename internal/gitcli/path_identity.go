package gitcli

import (
	"errors"
	"os"
	"path/filepath"
)

var errNativeIdentityUnavailable = errors.New("native path identity unavailable")

func canonicalExistingDirectory(path string) (string, error) {
	if path == "" {
		return "", os.ErrInvalid
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	canonical, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(canonical)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", os.ErrInvalid
	}
	return filepath.Clean(canonical), nil
}

func nativeIdentityError(cause error) error {
	if cause == nil {
		cause = errNativeIdentityUnavailable
	}
	return &GitError{Code: ErrorNativeIdentityUnavailable, Cause: cause}
}
