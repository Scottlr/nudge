package paths

import (
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
)

var (
	// ErrProtectedPath reports a path that cannot be opened under the required contract.
	ErrProtectedPath = errors.New("protected path rejected")
	// ErrProtectedAlias reports a symlink, junction, or reparse substitution.
	ErrProtectedAlias = errors.New("protected path alias rejected")
	// ErrProtectedPermissions reports a location that is not owner-only.
	ErrProtectedPermissions = errors.New("protected path permissions rejected")
	// ErrProtectedTooLarge reports a configuration/source file above the bounded read.
	ErrProtectedTooLarge = errors.New("protected file exceeds limit")
)

const maxProtectedRead = 1 << 20

// EnsurePrivateDir creates a directory tree with private newly-created
// components and verifies that the final directory is owner-only and not an
// alias. Existing non-Nudge ancestors such as a user's home directory may be
// traversed, but any alias in the chain fails closed.
func EnsurePrivateDir(path string) error {
	clean, err := absoluteClean(path)
	if err != nil {
		return err
	}
	if err := ensureAncestors(clean); err != nil {
		return err
	}
	info, err := os.Lstat(clean)
	if errors.Is(err, os.ErrNotExist) {
		if err := createPrivateDirNative(clean); err != nil {
			return err
		}
	} else if err != nil {
		return err
	} else if info.Mode()&os.ModeSymlink != 0 {
		return ErrProtectedAlias
	} else if !info.IsDir() {
		return ErrProtectedPath
	}
	return validatePrivateDirNative(clean)
}

// OpenProtectedFile opens a direct or nested child beneath a private root.
// Creation must use O_CREATE|O_EXCL so a source-bearing file is private at its
// first observable instant and every component is checked without following
// aliases.
func OpenProtectedFile(root, relative string, flag int, perm fs.FileMode) (*os.File, error) {
	cleanRoot, err := absoluteClean(root)
	if err != nil {
		return nil, err
	}
	if filepath.IsAbs(relative) || relative == "" {
		return nil, ErrProtectedPath
	}
	cleanRelative := filepath.Clean(relative)
	if cleanRelative == "." || cleanRelative == ".." || isParentRelative(cleanRelative) {
		return nil, ErrProtectedPath
	}
	if flag&os.O_CREATE != 0 && flag&os.O_EXCL == 0 {
		return nil, ErrProtectedPath
	}
	if err := EnsurePrivateDir(cleanRoot); err != nil {
		return nil, err
	}
	path := filepath.Join(cleanRoot, cleanRelative)
	if !contained(cleanRoot, path) {
		return nil, ErrProtectedPath
	}
	if err := EnsurePrivateDir(filepath.Dir(path)); err != nil {
		return nil, err
	}
	return openProtectedFileNative(path, flag, perm)
}

// OpenExistingProtectedFile opens an existing child without creating the root
// or any parent. It is the read-only path used by configuration loading.
func OpenExistingProtectedFile(root, relative string) (*os.File, error) {
	return openExistingProtectedFile(root, relative, os.O_RDONLY)
}

// OpenExistingProtectedFileForUpdate opens an existing protected child for a
// bounded owner-state rewrite without following a symlink or reparse alias.
// The caller must not include O_CREATE; the file must already exist beneath
// the validated private root.
func OpenExistingProtectedFileForUpdate(root, relative string, flag int) (*os.File, error) {
	if flag&os.O_CREATE != 0 || flag&(os.O_WRONLY|os.O_RDWR) == 0 {
		return nil, ErrProtectedPath
	}
	return openExistingProtectedFile(root, relative, flag)
}

func openExistingProtectedFile(root, relative string, flag int) (*os.File, error) {
	cleanRoot, err := absoluteClean(root)
	if err != nil {
		return nil, err
	}
	if filepath.IsAbs(relative) || relative == "" {
		return nil, ErrProtectedPath
	}
	cleanRelative := filepath.Clean(relative)
	if cleanRelative == "." || cleanRelative == ".." || isParentRelative(cleanRelative) {
		return nil, ErrProtectedPath
	}
	if err := validatePrivateDirNative(cleanRoot); err != nil {
		return nil, err
	}
	path := filepath.Join(cleanRoot, cleanRelative)
	if !contained(cleanRoot, path) {
		return nil, ErrProtectedPath
	}
	if err := validatePrivateDirNative(filepath.Dir(path)); err != nil {
		return nil, err
	}
	return openProtectedFileNative(path, flag, 0)
}

// ReadProtectedFile reads an existing protected file with a bounded buffer.
func ReadProtectedFile(root, relative string) ([]byte, error) {
	return ReadProtectedFileBounded(root, relative, maxProtectedRead)
}

// ReadProtectedFileBounded reads an existing protected file with a caller
// selected bound no larger than the platform maximum. It preserves the
// protected no-follow and owner-only checks while keeping stricter consumers
// from allocating the full generic configuration limit.
func ReadProtectedFileBounded(root, relative string, maxBytes int64) ([]byte, error) {
	if maxBytes <= 0 || maxBytes > maxProtectedRead {
		return nil, ErrProtectedPath
	}
	file, err := OpenExistingProtectedFile(root, relative)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maxBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > maxBytes {
		return nil, ErrProtectedTooLarge
	}
	return data, nil
}

func absoluteClean(path string) (string, error) {
	if path == "" || !filepath.IsAbs(path) {
		return "", ErrProtectedPath
	}
	clean := filepath.Clean(path)
	if clean != path {
		return "", ErrProtectedPath
	}
	return clean, nil
}

func ensureAncestors(path string) error {
	parent := filepath.Dir(path)
	if parent == path {
		return nil
	}
	info, err := os.Lstat(parent)
	if errors.Is(err, os.ErrNotExist) {
		if err := ensureAncestors(parent); err != nil {
			return err
		}
		if err := createPrivateDirNative(parent); err != nil {
			return err
		}
		return validatePrivateDirNative(parent)
	}
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return ErrProtectedAlias
	}
	if !info.IsDir() {
		return ErrProtectedPath
	}
	return nil
}
