//go:build freebsd

package playback

import (
	"fmt"
	"os"
	"syscall"
)

func directPlayFilesystemRevision(_ *os.File, info os.FileInfo) (string, bool) {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return "", false
	}
	return fmt.Sprintf(
		"freebsd:%x:%x:%x:%x:%x:%x",
		stat.Dev,
		stat.Ino,
		stat.Ctimespec.Sec,
		stat.Ctimespec.Nsec,
		info.ModTime().UnixNano(),
		info.Size(),
	), true
}
