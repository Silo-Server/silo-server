//go:build unix

package plugins

import (
	"fmt"
	"io/fs"
	"os"
	"syscall"
)

// EnsurePrivateCacheRoot creates the plugin cache root with mode 0700 when it
// is missing and refuses an existing one that another local account could
// control: a symlink, a non-directory, or a directory owned by another user or
// writable by its group or other users. Whoever can write the cache can replace
// a plugin binary together with the manifest checksum it is checked against,
// and the host runs that binary as this process's user.
func EnsurePrivateCacheRoot(root string) error {
	return ensurePrivateCacheRoot(root, os.Geteuid())
}

// ensurePrivateCacheRoot takes the owner uid so tests can reach the owner check
// without root.
func ensurePrivateCacheRoot(root string, uid int) error {
	if err := os.MkdirAll(root, 0o700); err != nil {
		return fmt.Errorf("create plugin cache dir %q: %w", root, err)
	}
	info, err := os.Lstat(root)
	if err != nil {
		return fmt.Errorf("plugin cache dir %q: %w", root, err)
	}
	if info.Mode()&fs.ModeSymlink != 0 {
		return fmt.Errorf("plugin cache dir %q is a symlink", root)
	}
	if !info.IsDir() {
		return fmt.Errorf("plugin cache dir %q is not a directory", root)
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok || st == nil {
		return fmt.Errorf("plugin cache dir %q has no ownership information", root)
	}
	if int64(st.Uid) != int64(uid) {
		return fmt.Errorf("plugin cache dir %q is owned by uid %d, not this process's uid %d", root, st.Uid, uid)
	}
	if perm := info.Mode().Perm(); perm&0o022 != 0 {
		return fmt.Errorf("plugin cache dir %q has mode %#o, writable by its group or other users", root, perm)
	}
	return nil
}
