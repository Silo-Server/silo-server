//go:build darwin

package nfo

import (
	"io/fs"
	"syscall"
)

// changeTime returns a file's inode change time, which no tool can set back:
// a directory's moves whenever an entry is added or removed.
func changeTime(info fs.FileInfo) int64 {
	if st, ok := info.Sys().(*syscall.Stat_t); ok {
		return st.Ctimespec.Nano()
	}
	return 0
}
