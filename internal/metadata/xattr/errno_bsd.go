//go:build darwin || freebsd

package xattr

import "golang.org/x/sys/unix"

// errNoAttr is the error getxattr returns when the attribute is absent. Linux
// reports ENODATA; the BSDs report ENOATTR, which is the errno this package
// would otherwise miss.
const errNoAttr = unix.ENOATTR
