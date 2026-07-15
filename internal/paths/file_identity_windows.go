//go:build windows

package paths

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"runtime"

	"github.com/Scottlr/nudge/internal/domain/repository"
	"golang.org/x/sys/windows"
)

func nativeFileIdentity(path string) (repository.NativeAliasEvidence, error) {
	path16, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return repository.NativeAliasEvidence{}, ErrNativeIdentityUnavailable
	}
	handle, err := windows.CreateFile(path16, windows.GENERIC_READ, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil, windows.OPEN_EXISTING, windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		return repository.NativeAliasEvidence{}, ErrNativeIdentityUnavailable
	}
	defer windows.CloseHandle(handle)
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(handle, &info); err != nil || info.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		return repository.NativeAliasEvidence{}, ErrNativeIdentityUnavailable
	}
	var volume [4]byte
	var file [8]byte
	binary.LittleEndian.PutUint32(volume[:], info.VolumeSerialNumber)
	binary.LittleEndian.PutUint32(file[0:4], info.FileIndexLow)
	binary.LittleEndian.PutUint32(file[4:8], info.FileIndexHigh)
	return repository.NativeAliasEvidence{
		Platform:           runtime.GOOS,
		VolumeIdentityHash: windowsIdentityHash(volume[:]),
		FileIdentityHash:   windowsIdentityHash(file[:]),
		LinkCount:          1,
	}, nil
}

func windowsIdentityHash(value []byte) string {
	digest := sha256.Sum256(value)
	return hex.EncodeToString(digest[:])
}
