//go:build windows

package paths

import (
	"encoding/binary"

	"github.com/Scottlr/nudge/internal/domain/repository"
	"golang.org/x/sys/windows"
)

func nativeDirectoryIdentity(path string) (repository.NativeIdentity, error) {
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
		return "", ErrNativeIdentityUnavailable
	}
	defer windows.CloseHandle(handle)
	var fileInfo windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(handle, &fileInfo); err != nil || fileInfo.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		return "", ErrNativeIdentityUnavailable
	}
	identity := make([]byte, 12)
	binary.LittleEndian.PutUint32(identity[0:4], fileInfo.VolumeSerialNumber)
	binary.LittleEndian.PutUint32(identity[4:8], fileInfo.FileIndexHigh)
	binary.LittleEndian.PutUint32(identity[8:12], fileInfo.FileIndexLow)
	return repository.NativeIdentity(string(identity)), nil
}
