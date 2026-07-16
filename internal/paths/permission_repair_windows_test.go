//go:build windows

package paths

import (
	"context"
	"testing"

	"golang.org/x/sys/windows"
)

func TestProtectedPermissionServiceRepairsWindowsACLOnSameObject(t *testing.T) {
	locations := testPermissionLocations(t)
	path16, err := windows.UTF16PtrFromString(locations.ConfigRoot)
	if err != nil {
		t.Fatal(err)
	}
	handle, err := windows.CreateFile(path16, windows.READ_CONTROL|windows.WRITE_DAC, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil, windows.OPEN_EXISTING, windows.FILE_FLAG_BACKUP_SEMANTICS|windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer windows.CloseHandle(handle)
	descriptor, err := windows.SecurityDescriptorFromString("D:P(A;;FA;;;OW)(A;;FR;;;WD)")
	if err != nil {
		t.Fatal(err)
	}
	dacl, _, err := descriptor.DACL()
	if err != nil {
		t.Fatal(err)
	}
	if err := windows.SetSecurityInfo(handle, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, dacl, nil); err != nil {
		t.Fatal(err)
	}

	service, err := NewProtectedPermissionService(locations)
	if err != nil {
		t.Fatal(err)
	}
	target, err := service.LoadProtectedPermissionTarget(context.Background(), "config-root")
	if err != nil {
		t.Fatal(err)
	}
	if target.CurrentPermissionHash == target.DesiredPermissionHash {
		t.Fatal("ACL drift was not observed")
	}
	proof, err := service.Inspect(context.Background(), target)
	if err != nil {
		t.Fatal(err)
	}
	after, err := service.Repair(context.Background(), target, proof)
	if err != nil {
		t.Fatal(err)
	}
	if after.NativeIdentityHash != target.NativeIdentityHash || after.AfterPermissionHash != target.DesiredPermissionHash {
		t.Fatalf("after proof = %#v", after)
	}
}
