//go:build !windows

package paths

import (
	"io/fs"
	"os"
	"syscall"
)

func createPrivateDirNative(path string) error {
	return os.Mkdir(path, 0o700)
}

func validatePrivateDirNative(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return ErrProtectedAlias
	}
	if !info.IsDir() {
		return ErrProtectedPath
	}
	if info.Mode().Perm()&0o077 != 0 {
		return ErrProtectedPermissions
	}
	return nil
}

func openProtectedFileNative(path string, flag int, perm fs.FileMode) (*os.File, error) {
	file, err := os.OpenFile(path, flag|syscall.O_NOFOLLOW, perm)
	if err != nil {
		return nil, err
	}
	return file, nil
}
