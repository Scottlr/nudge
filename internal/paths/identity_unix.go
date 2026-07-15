//go:build !windows && (linux || darwin || freebsd || openbsd || netbsd)

package paths

import (
	"encoding/binary"
	"os"
	"syscall"

	"github.com/Scottlr/nudge/internal/domain/repository"
)

func nativeDirectoryIdentity(path string) (repository.NativeIdentity, error) {
	info, err := os.Stat(path)
	if err != nil || !info.IsDir() {
		return "", ErrNativeIdentityUnavailable
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return "", ErrNativeIdentityUnavailable
	}
	identity := make([]byte, 16)
	binary.LittleEndian.PutUint64(identity[0:8], uint64(stat.Dev))
	binary.LittleEndian.PutUint64(identity[8:16], uint64(stat.Ino))
	return repository.NativeIdentity(string(identity)), nil
}
