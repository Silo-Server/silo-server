//go:build windows

package playback

import (
	"fmt"
	"os"
	"unsafe"

	"golang.org/x/sys/windows"
)

type windowsFileBasicInfo struct {
	CreationTime   int64
	LastAccessTime int64
	LastWriteTime  int64
	ChangeTime     int64
	FileAttributes uint32
}

func directPlayFilesystemRevision(file *os.File, _ os.FileInfo) (string, bool) {
	handle := windows.Handle(file.Fd())

	var identity windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(handle, &identity); err != nil {
		return "", false
	}

	var basic windowsFileBasicInfo
	if err := windows.GetFileInformationByHandleEx(
		handle,
		windows.FileBasicInfo,
		(*byte)(unsafe.Pointer(&basic)),
		uint32(unsafe.Sizeof(basic)),
	); err != nil {
		return "", false
	}

	size := uint64(identity.FileSizeHigh)<<32 | uint64(identity.FileSizeLow)
	fileID := uint64(identity.FileIndexHigh)<<32 | uint64(identity.FileIndexLow)
	return fmt.Sprintf(
		"windows:%x:%x:%x:%x:%x",
		identity.VolumeSerialNumber,
		fileID,
		basic.ChangeTime,
		basic.LastWriteTime,
		size,
	), true
}
