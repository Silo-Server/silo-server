// Package privatedir creates and verifies directories that only this process's
// user may control. A cache under the shared temp dir sits at a predictable
// path, so another local account could create it first and plant entries the
// service would then trust.
package privatedir

import (
	"fmt"
	"io/fs"
	"os"
)

// Ensure creates dir with mode 0700 when it is missing and refuses an existing
// one that another local account could control: a symlink, a non-directory, or
// a directory owned by another user or writable by its group or other users.
func Ensure(dir string) error {
	_, err := ensure(dir, os.Geteuid())
	return err
}

// OpenRoot ensures dir as Ensure does and opens it as an os.Root, refusing it
// if the path was swapped for another directory between the check and the
// open. Entries opened through the root cannot resolve outside dir.
func OpenRoot(dir string) (*os.Root, error) {
	info, err := ensure(dir, os.Geteuid())
	if err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, fmt.Errorf("%q: %w", dir, err)
	}
	opened, err := root.Stat(".")
	if err != nil || !os.SameFile(info, opened) {
		_ = root.Close()
		return nil, fmt.Errorf("%q changed while it was being opened", dir)
	}
	return root, nil
}

// ensure takes the owner uid so tests can reach the owner check without root.
func ensure(dir string, uid int) (fs.FileInfo, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("%q could not be created: %w", dir, err)
	}
	info, err := os.Lstat(dir)
	if err != nil {
		return nil, fmt.Errorf("%q: %w", dir, err)
	}
	if info.Mode()&fs.ModeSymlink != 0 {
		return nil, fmt.Errorf("%q is a symlink", dir)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("%q is not a directory", dir)
	}
	if err := checkOwner(dir, info, uid); err != nil {
		return nil, err
	}
	return info, nil
}
