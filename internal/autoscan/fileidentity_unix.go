//go:build unix

package autoscan

import (
	"io/fs"
	"strconv"
	"syscall"
)

// fileIdentity returns the inode number of info's file, so a file replaced at
// the same path with an identical size and modification time still reads as a
// new debounce state. It returns "" when the platform stat data is unavailable.
func fileIdentity(info fs.FileInfo) string {
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok || st == nil {
		return ""
	}
	return strconv.FormatUint(st.Ino, 10)
}
