//go:build !windows

package process

import (
	"encoding/binary"
	"os"
	"syscall"
)

func nativeExecutableIdentity(path string) (fileEvidence, error) {
	info, err := os.Stat(path)
	if err != nil {
		return fileEvidence{}, executableError(ExecutableErrorNotFound, err)
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0111 == 0 {
		return fileEvidence{}, executableError(ExecutableErrorUnsupported, nil)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return fileEvidence{}, executableError(ExecutableErrorUnsupported, nil)
	}
	native := make([]byte, 24)
	binary.LittleEndian.PutUint64(native[0:8], uint64(stat.Dev))
	binary.LittleEndian.PutUint64(native[8:16], uint64(stat.Ino))
	binary.LittleEndian.PutUint32(native[16:20], uint32(info.Mode().Perm()))
	binary.LittleEndian.PutUint32(native[20:24], uint32(info.Mode().Type()))
	return fileEvidence{NativeID: native, Size: info.Size(), ModTime: info.ModTime()}, nil
}
