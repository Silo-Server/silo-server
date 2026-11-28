package directconn

import (
	"context"
	"errors"
	"fmt"
	"iter"
	"log/slog"
	"maps"
	"net"
	"slices"
	"sync"
	"time"

	"github.com/darthShadow/shunpike/coordination/protocol"
	"github.com/darthShadow/shunpike/coordination/server"
)

// The region's envelope numbers for each device and API-host pair, and for each
// stream. Each is 4 times the largest p99 the shunpike discoload lab measured
// over its shaped cells, both drivers and both STUN arms; the frame and byte
// rates come from the arm that never goes direct. They are set here, in this
// one place.
const (
	// regionPairFrameBurst is the pair's frame bucket capacity, in frames.
	regionPairFrameBurst = 96
	// regionPairFrameRate is the pair's frame refill, in frames per second.
	regionPairFrameRate = 38
	// regionPairByteBurst is the pair's byte bucket capacity, in bytes.
	regionPairByteBurst = 14368
	// regionPairByteRate is the pair's byte refill, in bytes per second.
	regionPairByteRate = 5790
	// regionConnectionBytes is the most bytes a device's stream may send in the
	// module's 90-second window.
	regionConnectionBytes = 179344
	// regionHostBytesPerPair and regionHostBytesFloor set the most bytes the API
	// host's local stream may send in the module's 90-second window. That one stream
	// carries the frames of every device pair, so its limit is
	// regionHostBytesPerPair for each device it sent to in the window, and never
	// less than regionHostBytesFloor. A device whose entry expires in the window
	// still counts until the window ends, so the limit never falls within a window.
	regionHostBytesPerPair = 156962
	regionHostBytesFloor   = 7848076

	// regionLogInterval is how often the region's drop and close counters are
	// read for the log.
	regionLogInterval = time.Minute

	// revocationSweepInterval is the time from the end of one revocation pass to
	// the start of the next when no directory change wakes the sweep earlier.
	revocationSweepInterval = 10 * time.Second
)

// regionSlot holds the region a Coordination builds on first use, and the node
// keys of the device streams it serves.
type regionSlot struct {
	mu     sync.Mutex
	region *server.Region
	closed bool
	// streams counts each device's open streams; a replaced stream briefly
	// overlaps its successor. The engine's own local stream is not counted.
	streams map[protocol.NodeKey]int
}

// track counts one open stream for k until the returned function runs.
func (s *regionSlot) track(k protocol.NodeKey) (untrack func()) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.streams == nil {
		s.streams = make(map[protocol.NodeKey]int)
	}
	s.streams[k]++
	return func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.streams[k]--; s.streams[k] <= 0 {
			delete(s.streams, k)
		}
	}
}

// streamKeys returns the node keys with an open device stream, in key order.
func (s *regionSlot) streamKeys() []protocol.NodeKey {
	s.mu.Lock()
	defer s.mu.Unlock()
	keys := slices.Collect(maps.Keys(s.streams))
	slices.SortFunc(keys, func(a, b protocol.NodeKey) int { return slices.Compare(a[:], b[:]) })
	return keys
}

// get returns the region, building it on the first call. It returns net.ErrClosed
// after close.
func (s *regionSlot) get(build func() (*server.Region, error)) (*server.Region, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, net.ErrClosed
	}
	if s.region == nil {
		r, err := build()
		if err != nil {
			return nil, err
		}
		s.region = r
	}
	return s.region, nil
}

// peek returns the region if it exists, and builds none.
func (s *regionSlot) peek() *server.Region {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.region
}

func (s *regionSlot) close() error {
	s.mu.Lock()
	r := s.region
	s.closed = true
	s.mu.Unlock()
	if r == nil {
		return nil
	}
	return r.Close() //nolint:wrapcheck // The region reports its own close error.
}

// getRegion builds the signaling region of the replica that runs the engine. It
// shares the coordination server and the peer cache, so a revoked entry stops
// admitting at once.
func (c *Coordination) getRegion() (*server.Region, error) {
	return c.region.get(func() (*server.Region, error) {
		region, err := server.NewRegion(server.RegionOptions{
			Server: c.srv, Directory: c.directory, Clock: c.cfg.Clock,
			Logger: c.log.With(slog.String("component", "directconn")),
			Budget: protocol.Budget{Limits: protocol.BudgetLimits{
				PairFrameBurst: regionPairFrameBurst, PairFrameRate: regionPairFrameRate,
				PairByteBurst: regionPairByteBurst, PairByteRate: regionPairByteRate,
				ConnectionBytes: regionConnectionBytes,
			}},
			LocalConnectionBytes:        regionHostBytesFloor,
			LocalConnectionBytesPerPair: regionHostBytesPerPair,
		})
		if err != nil {
			return nil, fmt.Errorf("build signaling region: %w", err)
		}
		return region, nil
	})
}

// ServeRegion serves one device's signaling stream until the stream ends, the
// context ends, the principal's expiry passes or the entry is revoked. It owns
// and closes conn, which carries the DERP stream as binary messages. The
// principal comes from Authorize, so its expiry is the ticket's. While it
// serves, the device's node key is in the set the revocation sweep checks.
func (c *Coordination) ServeRegion(ctx context.Context, conn net.Conn, principal server.Principal) error {
	region, err := c.getRegion()
	if err != nil {
		_ = conn.Close()
		return err
	}
	untrack := c.region.track(principal.NodeKey)
	defer untrack()
	return region.Serve(ctx, conn, principal) //nolint:wrapcheck // The caller only ends the connection on the result.
}

// DialLocal opens the API engine's own stream to the region. The engine's first
// message must name a live host entry, so the caller opens it after the host
// entry is written.
func (c *Coordination) DialLocal(ctx context.Context) (net.Conn, error) {
	region, err := c.getRegion()
	if err != nil {
		return nil, err
	}
	return region.DialLocal(ctx) //nolint:wrapcheck // The engine retries on any error.
}

// RegionStats returns the region's counters: admitted streams, dropped frames and
// closes by reason, and replaced streams.
func (c *Coordination) RegionStats() server.RegionStats {
	if r := c.region.peek(); r != nil {
		return r.Stats()
	}
	return server.RegionStats{}
}

// logRegionCounters logs the region's drops and closes until ctx ends; see
// logRegionStats.
func (c *Coordination) logRegionCounters(ctx context.Context) {
	logRegionStats(ctx, c.log, c.RegionStats)
}

// logRegionStats reads the drop and close counters from stats every
// regionLogInterval and logs one line per reason whose count rose, with the
// rise: Debug for dropped frames, Info for closed streams. The region only
// counts, so a flood costs at most one line per reason per interval. It returns
// when ctx ends.
func logRegionStats(ctx context.Context, log *slog.Logger, stats func() server.RegionStats) {
	ticker := time.NewTicker(regionLogInterval)
	defer ticker.Stop()
	droppedLogged, closedLogged := map[string]uint64{}, map[string]uint64{}
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s := stats()
			for reason, n := range newRises(s.Drops, droppedLogged) {
				log.DebugContext(ctx, "direct connections: region dropped frames", "component", "directconn", "reason", reason, "count", n)
			}
			for reason, n := range newRises(s.Closes, closedLogged) {
				log.InfoContext(ctx, "direct connections: region closed streams", "component", "directconn", "reason", reason, "count", n)
			}
		}
	}
}

// newRises yields, in reason order, each reason in counts whose count is above the
// one in logged, with the rise, and records the new count.
func newRises(counts, logged map[string]uint64) iter.Seq2[string, uint64] {
	return func(yield func(string, uint64) bool) {
		for _, reason := range slices.Sorted(maps.Keys(counts)) {
			n := counts[reason]
			if n <= logged[reason] {
				continue
			}
			rise := n - logged[reason]
			logged[reason] = n
			if !yield(reason, rise) {
				return
			}
		}
	}
}

// sweepRevoked is one revocation pass. For each device with an open stream that
// the peer cache no longer holds, in key order, it reads the store, each read
// bounded by storeTimeout, and closes the device's streams when the store no
// longer holds its lease. A device that left the peer cache only while its entry
// reloads, as a registration does, keeps its stream. A store error closes
// nothing and leaves the key for the next pass; the ticket's expiry stays the
// backstop. A pass over N such keys takes at most N times storeTimeout.
func (c *Coordination) sweepRevoked(ctx context.Context) {
	r := c.region.peek()
	if r == nil {
		return
	}
	keys := c.region.streamKeys()
	if len(keys) == 0 {
		return
	}
	held := make(map[protocol.NodeKey]bool)
	for _, e := range c.directory.Devices() {
		held[e.NodeKey] = true
	}
	for _, k := range keys {
		if held[k] {
			continue
		}
		readCtx, cancel := context.WithTimeout(ctx, storeTimeout)
		_, err := c.cfg.Store.DeviceByNodeKey(readCtx, k, c.clock())
		cancel()
		if errors.Is(err, server.ErrNotFound) {
			r.Disconnect(k)
		}
	}
}

// sweepRevocations runs revocation passes, one at a time, until ctx ends: on a
// wake and revocationSweepInterval after the previous pass ends. A slow pass
// delays only the next pass.
func (c *Coordination) sweepRevocations(ctx context.Context, wake <-chan struct{}) {
	timer := time.NewTimer(revocationSweepInterval)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-wake:
		case <-timer.C:
		}
		c.sweepRevoked(ctx)
		timer.Reset(revocationSweepInterval)
	}
}

// Close stops the region and closes every signaling stream. It is safe to call
// more than once.
func (c *Coordination) Close() error { return c.region.close() }

// dialGate holds the API engine's first region dial until the host entry exists.
type dialGate struct {
	once sync.Once
	open chan struct{}
}

func newDialGate() *dialGate { return &dialGate{open: make(chan struct{})} }

// release runs before once, then opens the gate for good. Later calls do nothing.
func (g *dialGate) release(before func()) {
	g.once.Do(func() {
		before()
		close(g.open)
	})
}

// wait blocks until the gate opens or ctx ends.
func (g *dialGate) wait(ctx context.Context) error {
	select {
	case <-g.open:
		return nil
	case <-ctx.Done():
		return ctx.Err() //nolint:wrapcheck // The engine reads the context's own error.
	}
}

// dialLocal is the engine's signaling dialer: it waits for the gate, then opens
// the in-process stream. The region serves the stream under the context it is
// dialed with, and the engine cancels the dial's own context once the dial
// returns, so the stream gets life, which ends with the service.
func (c *Coordination) dialLocal(gate *dialGate, life context.Context) func(context.Context, string) (net.Conn, error) {
	return func(ctx context.Context, _ string) (net.Conn, error) {
		if err := gate.wait(ctx); err != nil {
			return nil, err
		}
		return c.DialLocal(life)
	}
}
