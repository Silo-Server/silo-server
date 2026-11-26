//go:build linux

package xattr

import "golang.org/x/sys/unix"

// errNoAttr is the error getxattr returns when the attribute is absent.
const errNoAttr = unix.ENODATA
