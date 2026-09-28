//go:build linux

package librarymonitor

import (
	"fmt"
	"io/fs"
	"os"
	"strconv"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"
)

// platformSupported reports whether this build can monitor at all.
const platformSupported = true

// classifyRoot classifies the filesystem holding root with statfs f_type.
func classifyRoot(root string) (fsClass, error) {
	var st unix.Statfs_t
	if err := unix.Statfs(root, &st); err != nil {
		return fsClass{}, fmt.Errorf("statfs %s: %w", root, err)
	}
	return classifyFSType(int64(st.Type)), nil //nolint:unconvert // f_type is int32 on some architectures.
}

// linkCount reports a file's hard link count, or 1 when it is unknown.
func linkCount(info fs.FileInfo) uint64 {
	if st, ok := info.Sys().(*syscall.Stat_t); ok {
		return uint64(st.Nlink) //nolint:unconvert // Nlink is uint32 on some architectures.
	}
	return 1
}

// identityOf identifies the directory a root path names, so a remounted or
// replaced folder is recorded again.
func identityOf(info fs.FileInfo) fileID {
	if st, ok := info.Sys().(*syscall.Stat_t); ok {
		return fileID{dev: uint64(st.Dev), ino: st.Ino} //nolint:unconvert // Dev is uint32 on some architectures.
	}
	return fileID{}
}

const mountInfoPath = "/proc/self/mountinfo"

// readMounts reads the mount table.
func readMounts() ([]mountEntry, error) {
	raw, err := os.ReadFile(mountInfoPath)
	if err != nil {
		return nil, err
	}
	return parseMountInfo(string(raw)), nil
}

const maxUserWatchesPath = "/proc/sys/fs/inotify/max_user_watches"

// readMaxUserWatches returns fs.inotify.max_user_watches, or 0 when it cannot
// be read.
func readMaxUserWatches() int {
	raw, err := os.ReadFile(maxUserWatchesPath)
	if err != nil {
		return 0
	}
	n, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil {
		return 0
	}
	return n
}
