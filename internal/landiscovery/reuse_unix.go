//go:build unix

package landiscovery

import (
	"syscall"

	"golang.org/x/sys/unix"
)

// shareMDNSPort lets the responder bind 5353 next to the operating system's
// own mDNS daemon (avahi, mDNSResponder).
func shareMDNSPort(_, _ string, c syscall.RawConn) error {
	var sockErr error
	err := c.Control(func(fd uintptr) {
		if sockErr = unix.SetsockoptInt(int(fd), unix.SOL_SOCKET, unix.SO_REUSEADDR, 1); sockErr != nil {
			return
		}
		sockErr = unix.SetsockoptInt(int(fd), unix.SOL_SOCKET, unix.SO_REUSEPORT, 1)
	})
	if err != nil {
		return err
	}
	return sockErr
}
