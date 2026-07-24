//go:build linux

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
		"linux:%x:%x:%x:%x:%x:%x",
		uint64(stat.Dev),
		stat.Ino,
		stat.Ctim.Sec,
		stat.Ctim.Nsec,
		info.ModTime().UnixNano(),
		info.Size(),
	), true
}
