//go:build linux || darwin

package downloadstorage

import (
	"golang.org/x/sys/unix"
)

// statFS reports a filesystem's capacity the way `df` does: used is every
// taken block, and total is used plus what this process may still write, so
// root-reserved blocks count as neither.
func statFS(path string) (fsStats, error) {
	var st unix.Statfs_t
	if err := unix.Statfs(path, &st); err != nil {
		return fsStats{}, err
	}
	blocks, free, available, size := uint64(st.Blocks), uint64(st.Bfree), uint64(st.Bavail), uint64(st.Bsize) //nolint:unconvert // widths vary by OS
	if free > blocks {
		free = blocks
	}
	used := (blocks - free) * size
	return fsStats{used: int64(used), total: int64(used + available*size), fsType: fsTypeName(&st)}, nil
}

// deviceOf returns the device a path lives on. Two paths on one filesystem
// share it, including FUSE mounts that publish no filesystem id.
func deviceOf(path string) (uint64, error) {
	var st unix.Stat_t
	if err := unix.Stat(path, &st); err != nil {
		return 0, err
	}
	return uint64(st.Dev), nil //nolint:unconvert // Dev is int32 on darwin
}
