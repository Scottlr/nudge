//go:build windows

package gitcli

import (
	"encoding/binary"
	"os"

	"github.com/Scottlr/nudge/internal/domain/repository"
	"golang.org/x/sys/windows"
)

func nativePathIdentity(path string) (repository.NativeIdentity, error) {
	info, err := os.Stat(path)
	if err != nil {
		return "", nativeIdentityError(err)
	}
	if !info.IsDir() {
		return "", nativeIdentityError(os.ErrInvalid)
	}
	handle, err := windows.CreateFile(
		windows.StringToUTF16Ptr(path),
		0,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil,
		windows.OPEN_EXISTING,
		windows.FILE_FLAG_BACKUP_SEMANTICS|windows.FILE_FLAG_OPEN_REPARSE_POINT,
		0,
	)
	if err != nil {
		return "", nativeIdentityError(err)
	}
	defer windows.CloseHandle(handle)
	var fileInfo windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(handle, &fileInfo); err != nil {
		return "", nativeIdentityError(err)
	}
	if fileInfo.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		return "", nativeIdentityError(errNativeIdentityUnavailable)
	}
	native := make([]byte, 12)
	binary.LittleEndian.PutUint32(native[0:4], fileInfo.VolumeSerialNumber)
	binary.LittleEndian.PutUint32(native[4:8], fileInfo.FileIndexHigh)
	binary.LittleEndian.PutUint32(native[8:12], fileInfo.FileIndexLow)
	return repository.NativeIdentity(string(native)), nil
}
