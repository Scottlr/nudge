//go:build windows

package process

import (
	"encoding/binary"
	"os"

	"golang.org/x/sys/windows"
)

func nativeExecutableIdentity(path string) (fileEvidence, error) {
	info, err := os.Stat(path)
	if err != nil {
		return fileEvidence{}, executableError(ExecutableErrorNotFound, err)
	}
	if !info.Mode().IsRegular() {
		return fileEvidence{}, executableError(ExecutableErrorUnsupported, nil)
	}
	file, err := os.Open(path)
	if err != nil {
		return fileEvidence{}, executableError(ExecutableErrorNotFound, err)
	}
	defer file.Close()
	var handleInfo windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(windows.Handle(file.Fd()), &handleInfo); err != nil {
		return fileEvidence{}, executableError(ExecutableErrorUnsupported, err)
	}
	if handleInfo.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		return fileEvidence{}, executableError(ExecutableErrorExcluded, nil)
	}
	native := make([]byte, 28)
	binary.LittleEndian.PutUint32(native[0:4], handleInfo.VolumeSerialNumber)
	binary.LittleEndian.PutUint32(native[4:8], handleInfo.FileIndexHigh)
	binary.LittleEndian.PutUint32(native[8:12], handleInfo.FileIndexLow)
	binary.LittleEndian.PutUint32(native[12:16], handleInfo.FileAttributes)
	binary.LittleEndian.PutUint32(native[16:20], handleInfo.NumberOfLinks)
	binary.LittleEndian.PutUint32(native[20:24], handleInfo.FileSizeHigh)
	binary.LittleEndian.PutUint32(native[24:28], handleInfo.FileSizeLow)
	return fileEvidence{NativeID: native, Size: info.Size(), ModTime: info.ModTime()}, nil
}
