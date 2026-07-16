//go:build windows

package paths

import (
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/Scottlr/nudge/internal/domain/repository"
	"golang.org/x/sys/windows"
)

// Windows reports directory ACLs with the auto-inherited control bit on some
// filesystems even when inheritance is protected. The policy accepts that
// stable control bit but still requires the exact single owner ACE.
const windowsProtectedPermission = "D:PAI(A;;FA;;;OW)"

func inspectProtectedPermission(path string) (protectedPermissionObservation, error) {
	handle, err := openProtectedPermissionHandle(path)
	if err != nil {
		return protectedPermissionObservation{}, err
	}
	defer windows.CloseHandle(handle)
	return observeProtectedPermissionHandle(handle)
}

func repairProtectedPermission(path, expectedIdentityHash, desiredPermissionHash string) (protectedPermissionObservation, error) {
	handle, err := openProtectedPermissionHandle(path)
	if err != nil {
		return protectedPermissionObservation{}, err
	}
	defer windows.CloseHandle(handle)

	before, err := observeProtectedPermissionHandle(handle)
	if err != nil {
		return protectedPermissionObservation{}, err
	}
	if nativeIdentityHash(before.identity) != expectedIdentityHash || before.desiredHash != desiredPermissionHash {
		return protectedPermissionObservation{}, ErrProtectedPermissionIdentity
	}
	if before.currentHash != desiredPermissionHash {
		descriptor, err := windows.SecurityDescriptorFromString(windowsProtectedPermission)
		if err != nil {
			return protectedPermissionObservation{}, err
		}
		dacl, _, err := descriptor.DACL()
		if err != nil {
			return protectedPermissionObservation{}, err
		}
		if err := windows.SetSecurityInfo(handle, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, dacl, nil); err != nil {
			return protectedPermissionObservation{}, err
		}
	}
	after, err := observeProtectedPermissionHandle(handle)
	if err != nil {
		return protectedPermissionObservation{}, err
	}
	if nativeIdentityHash(after.identity) != expectedIdentityHash || after.currentHash != desiredPermissionHash {
		return protectedPermissionObservation{}, ErrProtectedPermissionIdentity
	}
	return after, nil
}

func openProtectedPermissionHandle(path string) (windows.Handle, error) {
	if path == "" || !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return 0, ErrProtectedPermissionAlias
	}
	path16, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return 0, ErrProtectedPermissionAlias
	}
	handle, err := windows.CreateFile(path16, windows.READ_CONTROL|windows.WRITE_DAC, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil, windows.OPEN_EXISTING, windows.FILE_FLAG_BACKUP_SEMANTICS|windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		return 0, mapWindowsPermissionError(err)
	}
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(handle, &info); err != nil {
		windows.CloseHandle(handle)
		return 0, err
	}
	if info.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		windows.CloseHandle(handle)
		return 0, ErrProtectedPermissionAlias
	}
	finalPath, err := finalPathFromHandle(handle)
	if err != nil || !strings.EqualFold(normalizeWindowsPath(finalPath), normalizeWindowsPath(path)) {
		windows.CloseHandle(handle)
		return 0, ErrProtectedPermissionAlias
	}
	return handle, nil
}

func observeProtectedPermissionHandle(handle windows.Handle) (protectedPermissionObservation, error) {
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(handle, &info); err != nil {
		return protectedPermissionObservation{}, err
	}
	if info.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		return protectedPermissionObservation{}, ErrProtectedPermissionAlias
	}
	identity := windowsPermissionIdentity(info)
	ownerDescriptor, err := windows.GetSecurityInfo(handle, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION)
	if err != nil {
		return protectedPermissionObservation{}, err
	}
	owner, _, err := ownerDescriptor.Owner()
	if err != nil {
		return protectedPermissionObservation{}, err
	}
	token := windows.GetCurrentProcessToken()
	owned, err := tokenOwnsSID(token, owner)
	if err != nil || !owned {
		return protectedPermissionObservation{}, ErrProtectedPermissionOwnership
	}
	daclDescriptor, err := windows.GetSecurityInfo(handle, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION)
	if err != nil {
		return protectedPermissionObservation{}, err
	}
	control, _, err := daclDescriptor.Control()
	if err != nil {
		return protectedPermissionObservation{}, err
	}
	currentDescriptor := daclDescriptor.String()
	currentHash := windowsPermissionHash(currentDescriptor)
	desiredHash := windowsDesiredPermissionHash()
	if control&windows.SE_DACL_PROTECTED != 0 && (currentDescriptor == ownerOnlySDDL || currentDescriptor == windowsProtectedPermission) {
		currentHash = desiredHash
	}
	return protectedPermissionObservation{identity: identity, currentHash: currentHash, desiredHash: desiredHash}, nil
}

func tokenOwnsSID(token windows.Token, owner *windows.SID) (bool, error) {
	if owner == nil {
		return false, nil
	}
	user, err := token.GetTokenUser()
	if err != nil {
		return false, err
	}
	if user != nil && user.User.Sid != nil && owner.Equals(user.User.Sid) {
		return true, nil
	}
	groups, err := token.GetTokenGroups()
	if err != nil {
		return false, err
	}
	for _, group := range groups.AllGroups() {
		if group.Attributes&windows.SE_GROUP_ENABLED != 0 && group.Sid != nil && owner.Equals(group.Sid) {
			return true, nil
		}
	}
	return false, nil
}

func windowsPermissionIdentity(info windows.ByHandleFileInformation) repository.NativeIdentity {
	identity := make([]byte, 12)
	binary.LittleEndian.PutUint32(identity[0:4], info.VolumeSerialNumber)
	binary.LittleEndian.PutUint32(identity[4:8], info.FileIndexHigh)
	binary.LittleEndian.PutUint32(identity[8:12], info.FileIndexLow)
	return repository.NativeIdentity(string(identity))
}

func windowsPermissionHash(value string) string {
	digest := sha256.Sum256([]byte("windows-dacl:" + value))
	return fmt.Sprintf("%x", digest[:])
}

func windowsDesiredPermissionHash() string {
	return windowsPermissionHash("owner-only-protected")
}

func finalPathFromHandle(handle windows.Handle) (string, error) {
	buffer := make([]uint16, 256)
	for len(buffer) <= 32768 {
		length, err := windows.GetFinalPathNameByHandle(handle, &buffer[0], uint32(len(buffer)), 0)
		if err != nil {
			return "", err
		}
		if length < uint32(len(buffer)) {
			return windows.UTF16ToString(buffer[:length]), nil
		}
		buffer = make([]uint16, len(buffer)*2)
	}
	return "", errors.New("protected path is too long")
}

func normalizeWindowsPath(path string) string {
	lower := strings.ToLower(path)
	if strings.HasPrefix(lower, `\\?\unc\`) {
		path = `\\` + path[8:]
	} else if strings.HasPrefix(lower, `\\?\`) {
		path = path[4:]
	}
	return filepath.Clean(path)
}

func mapWindowsPermissionError(err error) error {
	if err == windows.ERROR_FILE_NOT_FOUND || err == windows.ERROR_PATH_NOT_FOUND {
		return ErrProtectedPermissionMissing
	}
	if err == windows.ERROR_CANT_ACCESS_FILE || err == windows.ERROR_REPARSE_TAG_INVALID {
		return ErrProtectedPermissionAlias
	}
	return err
}
