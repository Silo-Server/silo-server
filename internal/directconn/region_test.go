package directconn

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"maps"
	"net"
	"net/netip"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/darthShadow/shunpike/coordination/protocol"
	"github.com/darthShadow/shunpike/coordination/server"
	"github.com/darthShadow/shunpike/engine"
	"tailscale.com/derp"
	"tailscale.com/types/key"
)

// gatedHostStore delays host writes, or refuses them while down is set.
type gatedHostStore struct {
	CoordinationStore
	delay time.Duration
	down  atomic.Bool
}

func (s *gatedHostStore) PutHost(ctx context.Context, h server.HostEntry, now time.Time) error {
	if s.down.Load() {
		return server.ErrUnavailable
	}
	if s.delay > 0 {
		time.Sleep(s.delay) //nolint:forbidigo // Runs in a synctest bubble: the delay is fake time, not a wait.
	}
	return s.CoordinationStore.PutHost(ctx, h, now) //nolint:wrapcheck // A test double passes the store's result through.
}

// dialResult is what the API engine's signaling dial returned and the hosts the
// store held at that moment.
type dialResult struct {
	conn  net.Conn
	err   error
	hosts []server.HostEntry
}

// TestRegionConnectionDialGate: the API engine's first region dial waits for the
// host entry, in the order the start steps write it.
func TestRegionConnectionDialGate(t *testing.T) {
	setup := func(t *testing.T, store *gatedHostStore) (*lifecycleFixture, *Coordination, <-chan dialResult) {
		f := newLifecycleFixture(t, "enabled")
		coord, err := NewCoordination(CoordinationConfig{Store: store, AuthSecret: testAuthSecret, Clock: time.Now})
		if err != nil {
			t.Fatal(err)
		}
		dialed := make(chan dialResult, 1)
		f.onNew = func(c engine.Config) {
			if c.Signaling == nil || c.Signaling.RegionID != 0 || len(c.Regions) != 0 {
				t.Errorf("engine config signaling = %+v, regions = %v; want region 0 and no listed regions", c.Signaling, c.Regions)
				return
			}
			// The engine dials its region as soon as it is built.
			go func() {
				conn, err := c.Signaling.Dial(t.Context(), "")
				hosts, _ := store.Hosts(t.Context(), time.Now())
				dialed <- dialResult{conn, err, hosts}
			}()
		}
		f.svc.SetCoordination(coord)
		return f, coord, dialed
	}
	assertAfterHostEntry := func(t *testing.T, got dialResult) {
		t.Helper()
		if got.err != nil {
			t.Fatalf("dial: %v", got.err)
		}
		_ = got.conn.Close()
		if len(got.hosts) != 1 || got.hosts[0].Label != CapabilityHostID {
			t.Fatalf("hosts when the dial returned = %+v, want the api host", got.hosts)
		}
	}

	t.Run("waits for the host entry", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			store := &gatedHostStore{CoordinationStore: newMemoryStore(), delay: time.Second}
			f, coord, dialed := setup(t, store)
			defer f.cleanup()
			defer func() { _ = coord.Close() }()
			f.apply()
			assertAfterHostEntry(t, <-dialed)
		})
	})

	t.Run("a store outage keeps the gate shut until a write lands", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			store := &gatedHostStore{CoordinationStore: newMemoryStore()}
			store.down.Store(true)
			f, coord, dialed := setup(t, store)
			defer f.cleanup()
			defer func() { _ = coord.Close() }()
			f.apply()
			synctest.Wait()
			select {
			case got := <-dialed:
				t.Fatalf("dial returned (%v) before any host entry was written", got.err)
			default:
			}
			store.down.Store(false)
			time.Sleep(hostRefreshInterval + time.Second) //nolint:forbidigo // Runs in a synctest bubble: fake time moves the refresh tick.
			synctest.Wait()
			assertAfterHostEntry(t, <-dialed)
		})
	})
}

// regionDevice is a device with a real node key, enrolled; register loads it into
// the peer cache so the region admits its stream.
type regionDevice struct {
	priv      key.NodePrivate
	nodeKey   protocol.NodeKey
	principal server.Principal
}

func newRegionDevice(t *testing.T, c *Coordination, device string) regionDevice {
	t.Helper()
	return newRegionDeviceFor(t, c, 7, device)
}

// newRegionDeviceFor enrolls the device on the account userID.
func newRegionDeviceFor(t *testing.T, c *Coordination, userID int, device string) regionDevice {
	t.Helper()
	priv := key.NewNode()
	d := regionDevice{priv: priv}
	priv.Public().AppendTo(d.nodeKey[:0])
	enrollment, err := c.Enroll(t.Context(), EnrollRequest{UserID: userID, ProfileID: "profile-1", SessionID: testSessionID, DeviceID: device, NodeKey: d.nodeKey, ConnectionPath: "automatic"})
	if err != nil {
		t.Fatal(err)
	}
	if d.principal, _, err = c.Authorize(t.Context(), enrollment.Ticket); err != nil {
		t.Fatal(err)
	}
	return d
}

// register stores the device's registration, which also loads it into the peer
// cache the region admits from.
func (d regionDevice) register(t *testing.T, c *Coordination) {
	t.Helper()
	body, err := json.Marshal(protocol.Registration{NodeKey: d.nodeKey, DiscoKey: protocol.DiscoKey{0xD1}, Endpoints: []netip.AddrPort{netip.MustParseAddrPort("203.0.113.9:4000")}})
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Register(t.Context(), d.principal, bytes.NewReader(body)); err != nil {
		t.Fatal(err)
	}
}

// connect opens the device's stream and returns a channel that receives the
// error that ends it.
func (d regionDevice) connect(t *testing.T, c *Coordination, principal server.Principal) <-chan error {
	t.Helper()
	_, closed := d.connectClient(t, c, principal)
	return closed
}

// connectClient is connect that also returns the DERP client, so a test can send
// frames on the stream.
func (d regionDevice) connectClient(t *testing.T, c *Coordination, principal server.Principal) (*derp.Client, <-chan error) {
	t.Helper()
	a, b := net.Pipe()
	t.Cleanup(func() { _ = a.Close() })
	go func() { _ = c.ServeRegion(t.Context(), b, principal) }()
	client, err := derp.NewClient(d.priv, a, bufio.NewReadWriter(bufio.NewReader(a), bufio.NewWriter(a)), t.Logf)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Recv(); err != nil {
		t.Fatal(err)
	}
	return client, clientClosed(client)
}

// clientClosed reads the client until its stream ends and returns a channel that
// receives the error that ended it.
func clientClosed(client *derp.Client) <-chan error {
	closed := make(chan error, 1)
	go func() {
		for {
			if _, err := client.Recv(); err != nil {
				closed <- err
				return
			}
		}
	}()
	return closed
}

// TestRegionConnection: a held stream ends at the ticket's expiry and when the
// entry is withdrawn. The module's own tests cover the key check, replacement
// and the envelope.
func TestRegionConnection(t *testing.T) {
	// offset moves the service clock forward, so a ticket's remaining life can
	// be cut to a few hundred milliseconds of real time.
	var offset atomic.Int64
	clock := func() time.Time { return time.Now().Add(time.Duration(offset.Load())) }
	newCoord := func(t *testing.T) *Coordination {
		c, err := NewCoordination(CoordinationConfig{Store: newMemoryStore(), AuthSecret: testAuthSecret, Clock: clock})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = c.Close() })
		return c
	}
	waitClosed := func(t *testing.T, closed <-chan error, within time.Duration) time.Time {
		t.Helper()
		select {
		case <-closed:
			return time.Now()
		case <-time.After(within):
			t.Fatal("the stream is still open")
			return time.Time{}
		}
	}

	t.Run("closes at the ticket's expiry", func(t *testing.T) {
		offset.Store(0)
		c := newCoord(t)
		d := newRegionDevice(t, c, "tv")
		offset.Store(int64(time.Until(d.principal.Expires) - 400*time.Millisecond))
		d.register(t, c)
		closed := d.connect(t, c, d.principal)
		if at := waitClosed(t, closed, 3*time.Second); at.Before(clockToWall(d.principal.Expires, offset.Load())) {
			t.Fatal("the stream closed before the ticket's expiry")
		}
	})

	t.Run("closes when the entry is withdrawn", func(t *testing.T) {
		offset.Store(0)
		c := newCoord(t)
		d := newRegionDevice(t, c, "tv")
		d.register(t, c)
		closed := d.connect(t, c, d.principal)
		if err := c.Withdraw(t.Context(), 7, "tv"); err != nil {
			t.Fatal(err)
		}
		waitClosed(t, closed, 3*time.Second)
	})

	t.Run("refuses a principal whose entry is gone", func(t *testing.T) {
		offset.Store(0)
		c := newCoord(t)
		d := newRegionDevice(t, c, "tv")
		if err := c.Withdraw(t.Context(), 7, "tv"); err != nil {
			t.Fatal(err)
		}
		a, b := net.Pipe()
		defer func() { _ = a.Close() }()
		if err := c.ServeRegion(t.Context(), b, d.principal); err == nil {
			t.Fatal("ServeRegion accepted a principal with no entry")
		}
	})
}

// syncBuffer is a log destination that a test reads while loggers write to it.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// regionHostWindow is the number of one-second steps a host-stream test runs: it
// ends inside the module's 90-second window and before the 90-second life of the
// test entries.
const regionHostWindow = 85

// regionHostStream is the API host's local stream to a region whose devices are
// registered, driven in one-second steps of the service clock.
type regionHostStream struct {
	c      *Coordination
	dsts   []key.NodePublic
	client *derp.Client
	offset atomic.Int64
	closed chan error
	pongs  chan struct{}
}

// newRegionHostStream connects the API host's stream to a region with devices
// registered devices, each on an account of its own because an account holds a
// few.
func newRegionHostStream(t *testing.T, devices int) *regionHostStream {
	t.Helper()
	h := &regionHostStream{closed: make(chan error, 1), pongs: make(chan struct{}, 1)}
	clock := func() time.Time { return time.Now().Add(time.Duration(h.offset.Load())) }
	c, err := NewCoordination(CoordinationConfig{Store: newMemoryStore(), AuthSecret: testAuthSecret, Clock: clock})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	h.c = c
	for i := range devices {
		d := newRegionDeviceFor(t, c, 100+i, "tv")
		d.register(t, c)
		h.dsts = append(h.dsts, d.priv.Public())
	}
	hostPriv := key.NewNode()
	var hostKey protocol.NodeKey
	hostPriv.Public().AppendTo(hostKey[:0])
	if err := c.putHost(t.Context(), protocol.Registration{NodeKey: hostKey, DiscoKey: regionHostDisco, Endpoints: []netip.AddrPort{netip.MustParseAddrPort("198.51.100.1:4000")}}); err != nil {
		t.Fatal(err)
	}
	conn, err := c.DialLocal(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	h.client, err = derp.NewClient(hostPriv, conn, bufio.NewReadWriter(bufio.NewReader(conn), bufio.NewWriter(conn)), t.Logf)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.client.Recv(); err != nil {
		t.Fatal(err)
	}
	go func() {
		for {
			m, err := h.client.Recv()
			if err != nil {
				h.closed <- err
				return
			}
			if _, ok := m.(derp.PongMessage); ok {
				h.pongs <- struct{}{}
			}
		}
	}()
	return h
}

// send sends one discovery packet of size bytes to every device, then waits until
// the region has read them, because it reads the clock when it reads a frame. It
// reports whether the stream is still open.
func (h *regionHostStream) send(size int) bool {
	packet := make([]byte, size)
	copy(packet, []byte{'T', 'S', 0xf0, 0x9f, 0x92, 0xac})
	copy(packet[6:38], regionHostDisco[:])
	for _, dst := range h.dsts {
		if h.client.Send(dst, packet) != nil {
			return false
		}
	}
	if h.client.SendPing([8]byte{1}) != nil {
		return false
	}
	select {
	case <-h.pongs:
		return true
	case err := <-h.closed:
		h.closed <- err
		return false
	}
}

// run sends perStep packets of size bytes to every device in each step of the
// window and reports whether the stream stayed open.
func (h *regionHostStream) run(perStep, size int) bool {
	for range regionHostWindow {
		for range perStep {
			if !h.send(size) {
				return false
			}
		}
		h.offset.Add(int64(time.Second))
	}
	return true
}

// regionHostDisco is the API host's discovery key in the host-stream tests.
var regionHostDisco = protocol.DiscoKey{0xD2}

// TestRegionHostStream: the API host's local stream carries the frames of every
// device pair, and its connection byte limit grows with the pairs it carries, from
// a floor. A few devices at the most the pair budgets admit stay under the floor.
// Beyond the floor's share of pairs, a steady rate under each pair's share stays
// open and the most the pair budgets admit closes the stream with
// connection_budget, which the region counts.
func TestRegionHostStream(t *testing.T) {
	// frame is a packet's bytes in the window: its body and the 5-byte header.
	frame := func(packet int) int { return packet + 32 + 5 }
	// A pair at the most its budgets admit sends 4 packets of 1,280 bytes a second,
	// inside regionPairByteRate bytes per second.
	const fewDevices, manyDevices = 6, regionHostBytesFloor/regionHostBytesPerPair + 10
	requireOpen := func(t *testing.T, h *regionHostStream, what string) {
		t.Helper()
		select {
		case err := <-h.closed:
			t.Fatalf("the host stream closed under %s: %v; closes %v", what, err, h.c.RegionStats().Closes)
		default:
		}
	}

	t.Run("a few devices at the most the pair budgets admit stay open", func(t *testing.T) {
		if total := fewDevices * regionHostWindow * 4 * frame(1280); total >= regionHostBytesFloor {
			t.Fatalf("the test sends %d bytes, which is not under the floor %d", total, regionHostBytesFloor)
		}
		h := newRegionHostStream(t, fewDevices)
		if !h.run(4, 1280) {
			t.Fatal("the host stream closed under its floor")
		}
		requireOpen(t, h, "its floor")
	})

	t.Run("a steady rate under each pair's share stays open past the floor", func(t *testing.T) {
		// Each pair sends 95 percent of its share, so the total is over the floor
		// and under the scaled limit.
		size := regionHostBytesPerPair*95/100/(regionHostWindow*2) - 32 - 5
		if total := manyDevices * regionHostWindow * 2 * frame(size); size < 78 || size > 1280 || total <= regionHostBytesFloor || total >= manyDevices*regionHostBytesPerPair {
			t.Fatalf("a packet of %d bytes sends %d in total, want a discovery packet that is over the floor %d and under %d", size, total, regionHostBytesFloor, manyDevices*regionHostBytesPerPair)
		}
		h := newRegionHostStream(t, manyDevices)
		if !h.run(2, size) {
			t.Fatalf("the host stream closed under its scaled limit; closes %v", h.c.RegionStats().Closes)
		}
		requireOpen(t, h, "its scaled limit")
	})

	t.Run("the most the pair budgets admit closes it", func(t *testing.T) {
		h := newRegionHostStream(t, manyDevices)
		if h.run(4, 1280) {
			t.Fatal("the host stream stayed open past its scaled limit")
		}
		select {
		case <-h.closed:
		case <-time.After(time.Second):
			t.Fatal("the host stream is still open")
		}
		if got := h.c.RegionStats().Closes["connection_budget"]; got != 1 {
			t.Fatalf("connection_budget closes = %d, want 1", got)
		}
	})
}

// startReplica attaches a coordination service to a lifecycle fixture, as the
// replica that runs the engine, and returns it. The caller defers the cleanup.
func startReplica(t *testing.T, store CoordinationStore, logger *slog.Logger) (*lifecycleFixture, *Coordination) {
	t.Helper()
	f := newLifecycleFixture(t, "enabled")
	coord, err := NewCoordination(CoordinationConfig{Store: store, AuthSecret: testAuthSecret, Clock: time.Now, Logger: logger})
	if err != nil {
		t.Fatal(err)
	}
	f.svc.SetCoordination(coord)
	return f, coord
}

// TestRegionStreamEndsWhenAnotherReplicaWithdraws: a revocation handled on a
// replica that holds no stream reaches the engine replica through the store, and
// the stream closes when the peer cache's next refresh drops the device.
func TestRegionStreamEndsWhenAnotherReplicaWithdraws(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		store := newMemoryStore()
		f, replica := startReplica(t, store, nil)
		defer f.cleanup()
		defer func() { _ = replica.Close() }()
		other, err := NewCoordination(CoordinationConfig{Store: store, AuthSecret: testAuthSecret, Clock: time.Now})
		if err != nil {
			t.Fatal(err)
		}
		d := newRegionDevice(t, replica, "tv")
		d.register(t, replica)
		closed := d.connect(t, replica, d.principal)
		synctest.Wait()
		if err := other.Withdraw(t.Context(), 7, "tv"); err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		select {
		case <-closed:
			t.Fatal("the stream closed before the engine replica's peer cache refreshed")
		default:
		}
		time.Sleep(11 * time.Second) //nolint:forbidigo // Runs in a synctest bubble: fake time moves the cache refresh (10 s).
		synctest.Wait()
		select {
		case <-closed:
		default:
			t.Fatal("the stream is still open after the peer cache dropped the device")
		}
	})
}

// sweepStore fails or holds DeviceByNodeKey for chosen node keys, as a store
// outage does, and counts each key's reads.
type sweepStore struct {
	CoordinationStore
	mu    sync.Mutex
	reads map[protocol.NodeKey]int
	fail  map[protocol.NodeKey]bool
	// holds counts the key's next reads that wait for their deadline and then
	// fail; started receives the key as each one begins.
	holds   map[protocol.NodeKey]int
	holding int
	started chan protocol.NodeKey
}

func newSweepStore() *sweepStore {
	return &sweepStore{CoordinationStore: newMemoryStore(), reads: map[protocol.NodeKey]int{}, fail: map[protocol.NodeKey]bool{},
		holds: map[protocol.NodeKey]int{}, started: make(chan protocol.NodeKey, 4)}
}

func (s *sweepStore) DeviceByNodeKey(ctx context.Context, k protocol.NodeKey, now time.Time) (server.Device, error) {
	s.mu.Lock()
	s.reads[k]++
	fail, hold := s.fail[k], s.holds[k] > 0
	if hold {
		s.holds[k]--
		s.holding++
	}
	s.mu.Unlock()
	if hold {
		s.started <- k
		<-ctx.Done()
		s.mu.Lock()
		s.holding--
		s.mu.Unlock()
		return server.Device{}, fmt.Errorf("device by node key: %w", server.ErrUnavailable)
	}
	if fail {
		return server.Device{}, fmt.Errorf("device by node key: %w", server.ErrUnavailable)
	}
	return s.CoordinationStore.DeviceByNodeKey(ctx, k, now) //nolint:wrapcheck // A test double passes the store's result through.
}

func (s *sweepStore) readsOf(k protocol.NodeKey) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.reads[k]
}

func (s *sweepStore) setFail(k protocol.NodeKey, fail bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.fail[k] = fail
}

func (s *sweepStore) hold(k protocol.NodeKey, reads int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.holds[k] = reads
}

func (s *sweepStore) holdInProgress() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.holding > 0
}

// streamOpen reports whether the stream that closed reports on is still open.
func streamOpen(closed <-chan error) bool {
	select {
	case <-closed:
		return false
	default:
		return true
	}
}

// TestRevocationSweepRetriesFailedRead: a device withdrawn on another replica
// keeps its stream while the store cannot confirm the withdrawal, and loses it in
// the next pass whose read confirms it.
func TestRevocationSweepRetriesFailedRead(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		store := newSweepStore()
		f, replica := startReplica(t, store, nil)
		defer f.cleanup()
		defer func() { _ = replica.Close() }()
		other, err := NewCoordination(CoordinationConfig{Store: store, AuthSecret: testAuthSecret, Clock: time.Now})
		if err != nil {
			t.Fatal(err)
		}
		d := newRegionDevice(t, replica, "tv")
		d.register(t, replica)
		closed := d.connect(t, replica, d.principal)
		synctest.Wait()
		store.setFail(d.nodeKey, true)
		before := store.readsOf(d.nodeKey)
		if err := other.Withdraw(t.Context(), 7, "tv"); err != nil {
			t.Fatal(err)
		}
		time.Sleep(11 * time.Second) //nolint:forbidigo // Runs in a synctest bubble: fake time moves the cache refresh (10 s) that drops the device.
		synctest.Wait()
		if got := store.readsOf(d.nodeKey); got == before {
			t.Fatal("no store read confirmed the withdrawal after the peer cache dropped the device")
		}
		if !streamOpen(closed) {
			t.Fatal("the stream closed although the read that would confirm the withdrawal failed")
		}
		store.setFail(d.nodeKey, false)
		time.Sleep(10 * time.Second) //nolint:forbidigo // Runs in a synctest bubble: fake time moves the 10 s sweep trigger.
		synctest.Wait()
		if streamOpen(closed) {
			t.Fatal("the stream is still open after the next pass could read the withdrawal")
		}
	})
}

// TestRevocationSweepDoesNotHoldPeerUpdates: a revocation read held for the whole
// store timeout delays neither the engine's peer update nor the check of the
// other orphaned streams in the same pass.
func TestRevocationSweepDoesNotHoldPeerUpdates(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		store := newSweepStore()
		f, replica := startReplica(t, store, nil)
		defer f.cleanup()
		defer func() { _ = replica.Close() }()
		f.apply()
		f.initial()
		other, err := NewCoordination(CoordinationConfig{Store: store, AuthSecret: testAuthSecret, Clock: time.Now})
		if err != nil {
			t.Fatal(err)
		}
		first, second := newRegionDevice(t, replica, "tv"), newRegionDevice(t, replica, "phone")
		if bytes.Compare(first.nodeKey[:], second.nodeKey[:]) > 0 {
			first, second = second, first
		}
		first.register(t, replica)
		second.register(t, replica)
		heldClosed := first.connect(t, replica, first.principal)
		otherClosed := second.connect(t, replica, second.principal)
		synctest.Wait()
		// A pass reads in node-key order: the held key comes first, and the pass
		// after it reads the held key first again.
		store.hold(first.nodeKey, 2)
		for _, device := range []string{"tv", "phone"} {
			if err := other.Withdraw(t.Context(), 7, device); err != nil {
				t.Fatal(err)
			}
		}
		if k := <-store.started; k != first.nodeKey {
			t.Fatalf("held read for %v, want %v", k, first.nodeKey)
		}
		late := newRegionDeviceFor(t, replica, 8, "tv")
		late.register(t, replica)
		synctest.Wait()
		if !store.holdInProgress() {
			t.Fatal("the held read ended before the check")
		}
		if !f.engines[0].IsPeer(late.nodeKey) {
			t.Fatal("the engine's peer update waits behind a held revocation read")
		}
		time.Sleep(storeTimeout) //nolint:forbidigo // Runs in a synctest bubble: fake time ends the held read.
		synctest.Wait()
		if streamOpen(otherClosed) {
			t.Fatal("the other orphaned stream was not checked in the pass that held the first read")
		}
		if !streamOpen(heldClosed) {
			t.Fatal("the stream whose read failed closed")
		}
		time.Sleep(15 * time.Second) //nolint:forbidigo // Runs in a synctest bubble: the second held read ends, then the 10 s trigger starts a pass that reads.
		synctest.Wait()
		if streamOpen(heldClosed) {
			t.Fatal("the stream is still open after a pass could read the withdrawal")
		}
	})
}

// TestRevocationSweepKeepsLiveDevice: a device that leaves the peer cache while
// the store still enrolls it, as during a registration reload, keeps its stream.
func TestRevocationSweepKeepsLiveDevice(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		store := newSweepStore()
		f, replica := startReplica(t, store, nil)
		defer f.cleanup()
		defer func() { _ = replica.Close() }()
		d := newRegionDevice(t, replica, "tv")
		d.register(t, replica)
		closed := d.connect(t, replica, d.principal)
		synctest.Wait()
		before := store.readsOf(d.nodeKey)
		replica.directory.Invalidate(d.nodeKey)
		synctest.Wait()
		time.Sleep(10 * time.Second) //nolint:forbidigo // Runs in a synctest bubble: fake time moves the 10 s sweep trigger.
		synctest.Wait()
		if got := store.readsOf(d.nodeKey); got == before {
			t.Fatal("no store read checked the device that left the peer cache")
		}
		if !streamOpen(closed) {
			t.Fatal("the stream of a device the store still enrolls closed")
		}
	})
}

// TestRegionDropLog: the region's drop counters reach the log as one Debug line
// per reason per minute, with the number dropped since the last line, and its
// close counters as one Info line per reason per minute. The counters
// are a fake, because a real region's DERP server cannot run for a fake minute
// under the race detector.
func TestRegionDropLog(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		logs := &syncBuffer{}
		var mu sync.Mutex
		drops, closes := map[string]uint64{}, map[string]uint64{}
		add := func(reason string, n uint64) {
			mu.Lock()
			defer mu.Unlock()
			drops[reason] += n
		}
		addClose := func(reason string, n uint64) {
			mu.Lock()
			defer mu.Unlock()
			closes[reason] += n
		}
		stats := func() server.RegionStats {
			mu.Lock()
			defer mu.Unlock()
			return server.RegionStats{Drops: maps.Clone(drops), Closes: maps.Clone(closes)}
		}
		ctx, cancel := context.WithCancel(t.Context())
		logger := slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
		done := make(chan struct{})
		go func() {
			defer close(done)
			logRegionStats(ctx, logger, stats)
		}()
		defer func() {
			cancel()
			<-done
		}()
		wantLines := func(want ...string) {
			t.Helper()
			synctest.Wait()
			var got []string
			for _, line := range strings.Split(logs.String(), "\n") {
				if strings.Contains(line, "region dropped frames") {
					got = append(got, line)
				}
			}
			if len(got) != len(want) {
				t.Fatalf("drop log lines = %q, want %d", got, len(want))
			}
			for i, line := range got {
				if !strings.Contains(line, want[i]) || !strings.Contains(line, "level=DEBUG") {
					t.Fatalf("drop log line %d = %q, want a Debug line with %q", i, line, want[i])
				}
			}
		}

		add("pair", 3)
		wantLines()
		time.Sleep(regionLogInterval) //nolint:forbidigo // Runs in a synctest bubble: fake time moves the log tick.
		wantLines("reason=pair count=3")
		// Two bursts and a second reason inside one interval give one line each.
		add("pair", 2)
		time.Sleep(20 * time.Second) //nolint:forbidigo // Runs in a synctest bubble: fake time.
		add("pair", 2)
		add("sender", 1)
		time.Sleep(30 * time.Second) //nolint:forbidigo // Runs in a synctest bubble: fake time.
		wantLines("reason=pair count=3")
		time.Sleep(regionLogInterval - 50*time.Second) //nolint:forbidigo // Runs in a synctest bubble: fake time moves the log tick.
		wantLines("reason=pair count=3", "reason=pair count=4", "reason=sender count=1")
		// An interval with no new drops logs nothing.
		time.Sleep(regionLogInterval) //nolint:forbidigo // Runs in a synctest bubble: fake time moves the log tick.
		wantLines("reason=pair count=3", "reason=pair count=4", "reason=sender count=1")
		// A closed stream logs once per interval at Info, with the rise.
		addClose("connection_budget", 2)
		time.Sleep(regionLogInterval) //nolint:forbidigo // Runs in a synctest bubble: fake time moves the log tick.
		synctest.Wait()
		var closed []string
		for line := range strings.SplitSeq(logs.String(), "\n") {
			if strings.Contains(line, "region closed streams") {
				closed = append(closed, line)
			}
		}
		if len(closed) != 1 || !strings.Contains(closed[0], "level=INFO") || !strings.Contains(closed[0], "reason=connection_budget count=2") {
			t.Fatalf("close log lines = %q, want one Info line for connection_budget with count 2", closed)
		}
	})
}

// clockToWall is the wall-clock time at which a service clock that runs offset
// ahead reaches t.
func clockToWall(t time.Time, offset int64) time.Time { return t.Add(-time.Duration(offset)) }
