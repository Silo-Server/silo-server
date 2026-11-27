package directconn

import (
	"context"
	"fmt"
	"net/netip"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/darthShadow/shunpike/coordination/protocol"
	"github.com/darthShadow/shunpike/coordination/server"
	"github.com/darthShadow/shunpike/engine"
)

// An engine starts with a packet window that is its grant's local deadline,
// follows the renewals of that grant, and ends for good once the deadline
// passes.
func TestEngineWindowIsTheGrantWindow(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := newThreadRig(t, rigOptions{redis: true, enabled: true})
		defer shutdownService(t, r.svc)
		var mu sync.Mutex
		var windows []func() time.Time
		build := r.svc.cfg.newEngine
		r.svc.cfg.newEngine = func(ctx context.Context, c engine.Config) (engineHandle, error) {
			mu.Lock()
			windows = append(windows, c.Window)
			mu.Unlock()
			return build(ctx, c)
		}
		r.apply()
		r.sleep(engineLeaseTTL)
		r.engineIs("running", stateOn, "")
		mu.Lock()
		started := slices.Clone(windows)
		mu.Unlock()
		if len(started) == 0 {
			t.Fatal("no engine started")
		}
		window := started[len(started)-1]
		if window == nil {
			t.Fatal("the running engine has no packet window")
		}
		_, until := r.svc.replica.window()
		if got := window(); !got.Equal(until) {
			t.Fatalf("window = %v, want the grant's deadline %v", got, until)
		}
		r.sleep(censusInterval)
		_, renewed := r.svc.replica.window()
		if got := window(); !renewed.After(until) || !got.Equal(renewed) {
			t.Fatalf("window after a renewal = %v, want the renewed deadline %v (was %v)", got, renewed, until)
		}
		r.renewErr.Store(true)
		r.sleep(engineLeaseSelfStop + time.Second)
		if got := window(); !got.IsZero() {
			t.Fatalf("window after the deadline = %v, want the zero time", got)
		}
	})
}

// writeLogStore records each host and confirmation write that reaches the
// store, in order.
type writeLogStore struct {
	CoordinationStore
	mu     sync.Mutex
	writes []string
}

func (s *writeLogStore) note(op string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.writes = append(s.writes, op)
}

func (s *writeLogStore) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.writes)
}

// since returns the writes recorded after the first n.
func (s *writeLogStore) since(n int) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.writes[n:])
}

func (s *writeLogStore) PutHost(ctx context.Context, h server.HostEntry, now time.Time) error {
	s.note("put host")
	return s.CoordinationStore.PutHost(ctx, h, now) //nolint:wrapcheck // A test double passes the store's result through.
}

func (s *writeLogStore) DeleteHost(ctx context.Context, label string) error {
	s.note("delete host")
	return s.CoordinationStore.DeleteHost(ctx, label) //nolint:wrapcheck // A test double passes the store's result through.
}

func (s *writeLogStore) PutConfirmation(ctx context.Context, hostLabel string, ref server.DeviceRef, c *protocol.Confirmation, expires, now time.Time) error {
	if c == nil {
		s.note("clear confirmation")
	} else {
		s.note("put confirmation")
	}
	return s.CoordinationStore.PutConfirmation(ctx, hostLabel, ref, c, expires, now) //nolint:wrapcheck // A test double passes the store's result through.
}

// Coordination writes stop at the grant's local deadline. Inside the window,
// publishing writes the cleanup, the host entry and the confirmation. At the
// deadline publishHost writes nothing, and syncConfirmations neither sets nor
// clears a confirmation.
func TestCoordinationWritesStopAtTheGrantDeadline(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		m := leaseHookMonitor(t, func(leaseOp) bool { return false }, func(leaseOp) time.Duration { return 0 })
		m.update(t.Context())
		grant, until := m.window()
		r := newThreadRig(t, rigOptions{})
		defer shutdownService(t, r.svc)
		store := &writeLogStore{CoordinationStore: newMemoryStore()}
		coord, err := NewCoordination(CoordinationConfig{Store: store, AuthSecret: testAuthSecret, Clock: time.Now})
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = coord.Close() }()
		r.svc.SetCoordination(coord)
		d := newRegionDevice(t, coord, "tv")
		d.register(t, coord)
		confirmation := protocol.Confirmation{Peer: d.nodeKey, DiscoKey: protocol.DiscoKey{0xD1}, Endpoint: netip.MustParseAddrPort("203.0.113.9:4000")}
		e := &lifecycleEngine{changed: make(chan struct{}, 1), events: &r.events, eventsMu: &r.mu,
			reg: protocol.Registration{NodeKey: protocol.NodeKey{0xA1}, DiscoKey: protocol.DiscoKey{0xA2},
				Endpoints: []netip.AddrPort{netip.MustParseAddrPort("198.51.100.1:8094")}, Confirmations: []protocol.Confirmation{confirmation}}}
		host := &hostPublication{gate: newDialGate(), fence: newGrantFence(m, grant)}
		publish := func() []string {
			before := store.count()
			if reg, ok := r.svc.publishHost(coord, e, host); ok {
				r.svc.syncConfirmations(t.Context(), coord, host.fence, reg.Confirmations, map[protocol.NodeKey]protocol.Confirmation{}, true)
			}
			return store.since(before)
		}
		if got, want := publish(), []string{"delete host", "put host", "put confirmation"}; !slices.Equal(got, want) {
			t.Fatalf("writes inside the window = %v, want %v", got, want)
		}
		time.Sleep(time.Until(until)) //nolint:forbidigo // Fake clock inside the synctest bubble: it reaches the grant's local deadline.
		if got := publish(); len(got) != 0 {
			t.Fatalf("publishing at the grant's deadline wrote %v, want nothing", got)
		}
		before := store.count()
		r.svc.syncConfirmations(t.Context(), coord, host.fence, []protocol.Confirmation{confirmation}, map[protocol.NodeKey]protocol.Confirmation{}, true)
		r.svc.syncConfirmations(t.Context(), coord, host.fence, nil, map[protocol.NodeKey]protocol.Confirmation{confirmation.Peer: confirmation}, false)
		if got := store.since(before); len(got) != 0 {
			t.Fatalf("syncConfirmations wrote %v at the grant's deadline, want nothing", got)
		}
	})
}

// failingConfirmStore fails every confirmation set while failSets is true.
// Clears pass through.
type failingConfirmStore struct {
	CoordinationStore
	failSets atomic.Bool
}

func (s *failingConfirmStore) PutConfirmation(ctx context.Context, hostLabel string, ref server.DeviceRef, c *protocol.Confirmation, expires, now time.Time) error {
	if c != nil && s.failSets.Load() {
		return fmt.Errorf("%w: test outage", server.ErrUnavailable)
	}
	return s.CoordinationStore.PutConfirmation(ctx, hostLabel, ref, c, expires, now) //nolint:wrapcheck // A test double passes the store's result through.
}

// A confirmation update that fails leaves the previous confirmation in the
// store, so the published set keeps it: when the engine then drops the peer, the
// next call clears the stored confirmation.
func TestFailedConfirmationUpdateKeepsThePreviousOneToClear(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := newThreadRig(t, rigOptions{})
		defer shutdownService(t, r.svc)
		store := &failingConfirmStore{CoordinationStore: newMemoryStore()}
		coord, err := NewCoordination(CoordinationConfig{Store: store, AuthSecret: testAuthSecret, Clock: time.Now})
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = coord.Close() }()
		r.svc.SetCoordination(coord)
		d := newRegionDevice(t, coord, "tv")
		d.register(t, coord)
		first := protocol.Confirmation{Peer: d.nodeKey, DiscoKey: protocol.DiscoKey{0xD1}, Endpoint: netip.MustParseAddrPort("203.0.113.9:4000")}
		second := first
		second.Endpoint = netip.MustParseAddrPort("203.0.113.9:4001")

		published := r.svc.syncConfirmations(t.Context(), coord, nil, []protocol.Confirmation{first}, map[protocol.NodeKey]protocol.Confirmation{}, true)
		store.failSets.Store(true)
		published = r.svc.syncConfirmations(t.Context(), coord, nil, []protocol.Confirmation{second}, published, false)
		store.failSets.Store(false)
		r.svc.syncConfirmations(t.Context(), coord, nil, nil, published, false)

		stored, err := store.Confirmations(t.Context(), deviceRef(7, "tv"), time.Now())
		if err != nil {
			t.Fatal(err)
		}
		if len(stored) != 0 {
			t.Fatalf("stored confirmations after the engine dropped the peer = %v, want none", stored)
		}
	})
}
