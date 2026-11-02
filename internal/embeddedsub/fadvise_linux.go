//go:build linux

package embeddedsub

import (
	"os"

	"golang.org/x/sys/unix"
)

// adviseRandom applies POSIX_FADV_RANDOM to f. It is advice: a failure only
// costs read-ahead.
func adviseRandom(f *os.File) {
	conn, err := f.SyscallConn()
	if err != nil {
		return
	}
	_ = conn.Control(func(fd uintptr) {
		_ = unix.Fadvise(int(fd), 0, 0, unix.FADV_RANDOM)
	})
}
