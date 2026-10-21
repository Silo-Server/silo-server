//go:build linux

package tonemap

import (
	"os"
	"strconv"
	"syscall"

	"golang.org/x/sys/unix"
)

// charDeviceNumber returns the "major:minor" of the character device at path,
// following links, or false when path is not one.
func charDeviceNumber(path string) (string, bool) {
	info, err := os.Stat(path)
	if err != nil || info.Mode()&os.ModeCharDevice == 0 {
		return "", false
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return "", false
	}
	rdev := uint64(stat.Rdev) //nolint:unconvert // Rdev's width differs by architecture.
	return strconv.FormatUint(uint64(unix.Major(rdev)), 10) + ":" + strconv.FormatUint(uint64(unix.Minor(rdev)), 10), true
}
