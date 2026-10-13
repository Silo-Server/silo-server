//go:build linux

package nodepool

import (
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

// controlNodeSocket bounds how long data sent to a node may go unacknowledged
// before the kernel drops the connection. A node whose host vanished stops
// acknowledging; without the bound, a request written to an idle pooled
// connection waits on it for the caller's whole deadline, because no new
// connection, and so no connect bound, is involved. A live node acknowledges
// within milliseconds even while it is busy, and a response that streams for
// minutes sends nothing that needs acknowledging, so neither is cut.
func controlNodeSocket(_, _ string, conn syscall.RawConn) error {
	var sockErr error
	err := conn.Control(func(fd uintptr) {
		sockErr = unix.SetsockoptInt(int(fd), unix.IPPROTO_TCP, unix.TCP_USER_TIMEOUT, int(NodeUnacknowledgedTimeout/time.Millisecond))
	})
	if err != nil {
		return err
	}
	return sockErr
}
