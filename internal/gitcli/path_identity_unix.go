//go:build !windows && (linux || darwin || freebsd || openbsd || netbsd)

package gitcli

import (
	"encoding/binary"
	"os"
	"syscall"

	"github.com/Scottlr/nudge/internal/domain/repository"
)

func nativePathIdentity(path string) (repository.NativeIdentity, error) {
	info, err := os.Stat(path)
	if err != nil {
		return "", nativeIdentityError(err)
	}
	if !info.IsDir() {
		return "", nativeIdentityError(os.ErrInvalid)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return "", nativeIdentityError(nil)
	}
	native := make([]byte, 16)
	binary.LittleEndian.PutUint64(native[0:8], uint64(stat.Dev))
	binary.LittleEndian.PutUint64(native[8:16], uint64(stat.Ino))
	return repository.NativeIdentity(string(native)), nil
}
