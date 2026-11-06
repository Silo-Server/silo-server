//go:build unix

package uploads

import (
	"fmt"
	"io/fs"
	"syscall"
)

// checkRootOwner refuses an upload root owned by another user or writable by
// its group or other users: either could rename session directories and plant
// their own in their place.
func checkRootOwner(dir string, info fs.FileInfo, uid int) error {
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok || st == nil {
		return fmt.Errorf("upload root %q has no ownership information", dir)
	}
	if int64(st.Uid) != int64(uid) {
		return fmt.Errorf("upload root %q is owned by uid %d, not this process's uid %d", dir, st.Uid, uid)
	}
	if perm := info.Mode().Perm(); perm&0o022 != 0 {
		return fmt.Errorf("upload root %q has mode %#o, writable by its group or other users", dir, perm)
	}
	return nil
}
