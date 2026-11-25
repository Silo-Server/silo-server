package directconn

import (
	"errors"
	"net"
	"sync/atomic"
	"time"
)

// fencedPacketConn is the packet connection handed to HTTP/3. Once the engine's
// lease grant ends or its local deadline passes it drops every packet in both
// directions, before teardown starts, and never opens again. A packet is dropped
// as a lost packet would be, so the transport sees loss and not an error that
// would end its server. The check cannot cover a process paused between the
// check and the send; the lease's TTL margin bounds that case.
//
// It keeps the optional methods the pinned quic-go reads from the engine's
// connection: DF support, and GSO with segmented writes.
type fencedPacketConn struct {
	net.PacketConn
	// window returns the monitor's current grant and its local deadline.
	window func() (grant uint64, until time.Time)
	grant  uint64
	closed atomic.Bool
}

// fenceFor wraps conn with the fence of the grant the engine starts under. A
// nil conn or monitor leaves conn as it is.
func fenceFor(conn net.PacketConn, m *replicaMonitor, grant uint64) net.PacketConn {
	if conn == nil || m == nil {
		return conn
	}
	return &fencedPacketConn{PacketConn: conn, window: m.window, grant: grant}
}

func (c *fencedPacketConn) open() bool {
	if c.closed.Load() {
		return false
	}
	grant, until := c.window()
	if grant != c.grant || !time.Now().Before(until) {
		c.closed.Store(true)
		return false
	}
	return true
}

// ReadFrom checks after the read returns, so a packet that arrived while the
// caller waited is judged by the time it is handed on.
func (c *fencedPacketConn) ReadFrom(b []byte) (int, net.Addr, error) {
	for {
		n, addr, err := c.PacketConn.ReadFrom(b)
		if err != nil || c.open() {
			return n, addr, err
		}
	}
}

func (c *fencedPacketConn) WriteTo(b []byte, addr net.Addr) (int, error) {
	if !c.open() {
		return len(b), nil
	}
	return c.PacketConn.WriteTo(b, addr)
}

func (c *fencedPacketConn) SupportsGSO() bool {
	segmented, ok := c.PacketConn.(interface{ SupportsGSO() bool })
	return ok && segmented.SupportsGSO()
}

func (c *fencedPacketConn) WriteSegments(b []byte, addr net.Addr, size uint16) (int, error) {
	if !c.open() {
		return len(b), nil
	}
	segmented, ok := c.PacketConn.(interface {
		WriteSegments([]byte, net.Addr, uint16) (int, error)
	})
	if !ok {
		return 0, errors.ErrUnsupported
	}
	return segmented.WriteSegments(b, addr, size)
}

func (c *fencedPacketConn) SupportsDF() bool {
	df, ok := c.PacketConn.(interface{ SupportsDF() bool })
	return ok && df.SupportsDF()
}
