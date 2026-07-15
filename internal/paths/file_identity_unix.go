//go:build !windows && (linux || darwin || freebsd || openbsd || netbsd)

package paths

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"runtime"
	"syscall"

	"github.com/Scottlr/nudge/internal/domain/repository"
)

func nativeFileIdentity(path string) (repository.NativeAliasEvidence, error) {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() {
		return repository.NativeAliasEvidence{}, ErrNativeIdentityUnavailable
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Nlink == 0 {
		return repository.NativeAliasEvidence{}, ErrNativeIdentityUnavailable
	}
	var volume, file [8]byte
	for index := range volume {
		volume[index] = byte(uint64(stat.Dev) >> (index * 8))
		file[index] = byte(uint64(stat.Ino) >> (index * 8))
	}
	return repository.NativeAliasEvidence{
		Platform:           runtime.GOOS,
		VolumeIdentityHash: identityHash(volume[:]),
		FileIdentityHash:   identityHash(file[:]),
		LinkCount:          uint64(stat.Nlink),
	}, nil
}

func identityHash(value []byte) string {
	digest := sha256.Sum256(value)
	return hex.EncodeToString(digest[:])
}
