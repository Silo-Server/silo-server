package directconn

import (
	"context"
	"io"
	"sync"

	"github.com/darthShadow/shunpike/coordination/protocol"
	"github.com/darthShadow/shunpike/coordination/server"
)

// Register stores the registration in body for the device the principal names.
// The principal comes from Authorize. It returns an error wrapping
// protocol.ErrTooLarge for a body over 64 KiB, protocol.ErrInvalid for a
// malformed one, server.ErrUnauthorized when the entry no longer holds the
// principal's node key or the body names another key, and server.ErrUnavailable
// when the store cannot answer. A stored registration is loaded into the peer
// cache at once, so the engine can accept the device before the next refresh.
func (c *Coordination) Register(ctx context.Context, principal server.Principal, body io.Reader) error {
	reg, err := protocol.DecodeRegistration(body)
	if err != nil {
		return err //nolint:wrapcheck // The caller maps the protocol sentinels.
	}
	if err := c.srv.Register(ctx, principal, reg); err != nil {
		return err //nolint:wrapcheck // The caller maps the server sentinels.
	}
	c.directory.Reload(ctx, principal.NodeKey)
	return nil
}

// Netmap returns the device's view of the hosts. A version equal to the current
// one holds the call until the view changes, the hold ends or ctx ends. Once the
// server is stopping, every call answers at once.
func (c *Coordination) Netmap(ctx context.Context, principal server.Principal, version string) (protocol.Netmap, error) {
	if c.stopping.Load() {
		version = ""
	}
	return c.srv.Netmap(ctx, principal, version) //nolint:wrapcheck // The caller maps the server sentinels.
}

// putHost publishes the API host's identity and endpoints. The entry lives for
// the server's presence period, so the caller repeats the call.
func (c *Coordination) putHost(ctx context.Context, reg protocol.Registration) error {
	return c.srv.PutHost(ctx, server.HostEntry{Label: coordinationHostLabel, NodeKey: reg.NodeKey, DiscoKey: reg.DiscoKey, Endpoints: reg.Endpoints}) //nolint:wrapcheck // The caller classifies store errors.
}

// setConfirmation records the API host's confirmation of a device. An unchanged
// confirmation renews its lifetime without waking the device's held netmap.
func (c *Coordination) setConfirmation(ctx context.Context, confirmation protocol.Confirmation) error {
	return c.srv.SetConfirmation(ctx, coordinationHostLabel, confirmation) //nolint:wrapcheck // The caller classifies store errors.
}

// clearConfirmation removes the API host's confirmation of a device.
func (c *Coordination) clearConfirmation(ctx context.Context, device protocol.NodeKey) error {
	return c.srv.ClearConfirmation(ctx, coordinationHostLabel, device) //nolint:wrapcheck // The caller classifies store errors.
}

// Directory is the peer cache of the replica that terminates direct connections.
func (c *Coordination) Directory() *server.Directory { return c.directory }

// run keeps the coordination server's host view and the peer cache fresh, and
// logs the region's drops and closes, until ctx ends.
func (c *Coordination) run(ctx context.Context) {
	var workers sync.WaitGroup
	workers.Go(func() { _ = c.srv.Run(ctx) }) // returns nil on cancellation
	workers.Go(func() { c.logRegionCounters(ctx) })
	_ = c.directory.Run(ctx) // returns nil on cancellation
	workers.Wait()
}
