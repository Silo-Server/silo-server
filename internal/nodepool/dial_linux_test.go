//go:build linux

package nodepool

import (
	"context"
	"net"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestNodeDialBoundsUnacknowledgedData(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer func() { _ = listener.Close() }()

	conn, err := NodeDialContext()(context.Background(), "tcp", listener.Addr().String())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = conn.Close() }()
	raw, err := conn.(*net.TCPConn).SyscallConn()
	if err != nil {
		t.Fatalf("raw conn: %v", err)
	}
	var got int
	var sockErr error
	if err := raw.Control(func(fd uintptr) {
		got, sockErr = unix.GetsockoptInt(int(fd), unix.IPPROTO_TCP, unix.TCP_USER_TIMEOUT)
	}); err != nil || sockErr != nil {
		t.Fatalf("read TCP_USER_TIMEOUT: %v %v", err, sockErr)
	}
	if want := int(NodeUnacknowledgedTimeout / time.Millisecond); got != want {
		t.Fatalf("TCP_USER_TIMEOUT = %d ms, want %d", got, want)
	}
}
