package directconn

import (
	"errors"
	"net"
	"sync/atomic"
	"time"
)

// grantFence is the lease window of one engine: the grant it starts under and
// that grant's local deadline. It closes once the grant ends or the deadline
// passes, and never opens again. The HTTP/3 packet path, the engine's discovery
// (engine.Config.Window) and the coordination writes share one fence, so they
// close together. The check cannot cover a process paused between the check and
// the act it guards; the lease's TTL margin bounds that case.
type grantFence struct {
	// window returns the monitor's current grant and its local deadline.
	window func() (grant uint64, until time.Time)
	grant  uint64
	closed atomic.Bool
}

// newGrantFence returns the fence of grant on m. A nil monitor, as without
// Redis, has no lease and returns a nil fence, which is always open.
func newGrantFence(m *replicaMonitor, grant uint64) *grantFence {
	if m == nil {
		return nil
	}
	return &grantFence{window: m.window, grant: grant}
}

// until returns the grant's local deadline while the fence is open, and the
// zero time once it has closed.
func (f *grantFence) until() time.Time {
	if f.closed.Load() {
		return time.Time{}
	}
	grant, until := f.window()
	if grant != f.grant || !time.Now().Before(until) {
		f.closed.Store(true)
		return time.Time{}
	}
	return until
}

// open reports whether the fence still admits packets and writes.
func (f *grantFence) open() bool {
	return f == nil || !f.until().IsZero()
}

// fencedPacketConn is the packet connection handed to HTTP/3. Once its fence
// closes it drops every packet in both directions, before teardown starts. A
// packet is dropped as a lost packet would be, so the transport sees loss and
// not an error that would end its server.
//
// It keeps the optional methods the pinned quic-go reads from the engine's
// connection: DF support, and GSO with segmented writes.
type fencedPacketConn struct {
	net.PacketConn
	fence *grantFence
}

// fenceFor wraps conn with fence. A nil conn or fence leaves conn as it is.
func fenceFor(conn net.PacketConn, fence *grantFence) net.PacketConn {
	if conn == nil || fence == nil {
		return conn
	}
	return &fencedPacketConn{PacketConn: conn, fence: fence}
}

func (c *fencedPacketConn) open() bool { return c.fence.open() }

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
