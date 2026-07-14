//go:build windows

package paths

import (
	"io/fs"
	"os"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

const ownerOnlySDDL = "D:P(A;;FA;;;OW)"

func createPrivateDirNative(path string) error {
	if err := rejectReparsePoint(path); err != nil {
		return err
	}
	sd, err := windows.SecurityDescriptorFromString(ownerOnlySDDL)
	if err != nil {
		return err
	}
	security := &windows.SecurityAttributes{
		Length:             uint32(unsafe.Sizeof(windows.SecurityAttributes{})),
		SecurityDescriptor: sd,
	}
	path16, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return err
	}
	if err := windows.CreateDirectory(path16, security); err != nil {
		return err
	}
	return validatePrivateDirNative(path)
}

func validatePrivateDirNative(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || rejectReparsePoint(path) != nil {
		return ErrProtectedAlias
	}
	if !info.IsDir() {
		return ErrProtectedPath
	}
	sd, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return err
	}
	if !strings.Contains(sd.String(), ownerOnlySDDL) {
		return ErrProtectedPermissions
	}
	return nil
}

func openProtectedFileNative(path string, flag int, perm fs.FileMode) (*os.File, error) {
	if err := rejectReparsePoint(path); err != nil {
		return nil, err
	}
	access := uint32(windows.GENERIC_READ)
	if flag&os.O_WRONLY != 0 {
		access = windows.GENERIC_WRITE
	}
	if flag&os.O_RDWR != 0 {
		access = windows.GENERIC_READ | windows.GENERIC_WRITE
	}
	disposition := uint32(windows.OPEN_EXISTING)
	if flag&os.O_CREATE != 0 {
		disposition = windows.CREATE_NEW
	}
	attrs := uint32(windows.FILE_ATTRIBUTE_NORMAL | windows.FILE_FLAG_OPEN_REPARSE_POINT)
	path16, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	sd, err := windows.SecurityDescriptorFromString(ownerOnlySDDL)
	if err != nil {
		return nil, err
	}
	security := &windows.SecurityAttributes{
		Length:             uint32(unsafe.Sizeof(windows.SecurityAttributes{})),
		SecurityDescriptor: sd,
	}
	handle, err := windows.CreateFile(path16, access, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, security, disposition, attrs, 0)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(handle), path), nil
}

func rejectReparsePoint(path string) error {
	path16, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return err
	}
	attributes, err := windows.GetFileAttributes(path16)
	if err != nil {
		if err == windows.ERROR_FILE_NOT_FOUND || err == windows.ERROR_PATH_NOT_FOUND {
			return nil
		}
		return err
	}
	if attributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		return ErrProtectedAlias
	}
	return nil
}
