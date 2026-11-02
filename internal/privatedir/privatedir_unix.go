//go:build unix

package privatedir

import (
	"fmt"
	"io/fs"
	"syscall"
)

// checkOwner refuses a directory owned by another uid or writable by its group
// or other users.
func checkOwner(dir string, info fs.FileInfo, uid int) error {
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok || st == nil {
		return fmt.Errorf("%q has no ownership information", dir)
	}
	if int64(st.Uid) != int64(uid) {
		return fmt.Errorf("%q is owned by uid %d, not this process's uid %d", dir, st.Uid, uid)
	}
	if perm := info.Mode().Perm(); perm&0o022 != 0 {
		return fmt.Errorf("%q has mode %#o, writable by its group or other users", dir, perm)
	}
	return nil
}
