package directconn

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"net/http"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/darthShadow/shunpike/coordination/protocol"
	"github.com/darthShadow/shunpike/coordination/server"
	"github.com/darthShadow/shunpike/engine"
	"github.com/darthShadow/shunpike/engine/n4"

	"github.com/Silo-Server/silo-server/internal/lantls"
	"github.com/Silo-Server/silo-server/internal/tuning"
)

// shutdownRig is a service over a real loopback engine and HTTP/3 server, with a
// memory coordination store.
type shutdownRig struct {
	svc   *Service
	coord *Coordination
	store CoordinationStore
	port  int
	// drainGate holds the HTTP/3 drain, as a long media request does, until the
	// test closes it.
	drainGate chan struct{}
}

// gatedServer waits for the gate before it drains the wrapped server. When ctx
// ends first, it closes the wrapped server and reports ctx's error, as the HTTP/3
// server does when a stream is still open at the deadline.
type gatedServer struct {
	engineServer
	gate <-chan struct{}
}

func (g gatedServer) Shutdown(ctx context.Context) error {
	select {
	case <-g.gate:
	case <-ctx.Done():
		_ = g.engineServer.Shutdown(ctx)
		return ctx.Err() //nolint:wrapcheck // A test double reports the context's own error.
	}
	return g.engineServer.Shutdown(ctx) //nolint:wrapcheck // A test double passes the server's result through.
}

// freeUDPPort returns a UDP port that was free a moment ago.
func freeUDPPort(t *testing.T) int {
	t.Helper()
	conn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4zero})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	return conn.LocalAddr().(*net.UDPAddr).Port
}

// newShutdownRig starts the engine on a free port.
func newShutdownRig(t *testing.T) *shutdownRig {
	t.Helper()
	rig := &shutdownRig{port: freeUDPPort(t), store: newMemoryStore(), drainGate: make(chan struct{})}
	settings := &memorySettings{values: map[string]string{"direct_connections.enabled": "true"}}
	cfg := testConfig(settings)
	cfg.SecretKey = bytes.Repeat([]byte{0x41}, 32)
	cfg.Tuning, _ = tuning.Parse(func(string) string { return "" })
	cfg.Tuning.DirectUDPPort = rig.port
	identity, err := lantls.New([]byte("01234567890123456789012345678901"))
	if err != nil {
		t.Fatal(err)
	}
	cfg.newEngine = func(ctx context.Context, c engine.Config) (engineHandle, error) {
		c.StaticNetwork = true
		e, err := engine.New(ctx, c)
		if err != nil {
			return nil, err //nolint:wrapcheck // The test reports the engine's own error.
		}
		return e, nil
	}
	cfg.newServer = func(e engineHandle, policy n4.Policy) engineServer {
		lookup := func(ctx context.Context, k protocol.NodeKey) (server.PeerEntry, bool) {
			return rig.coord.directory.Lookup(ctx, k)
		}
		srv := newHTTP3Server(e.(*engine.Engine), identity.HTTP3Config(), policy, lookup, nil, http.NotFoundHandler())
		return gatedServer{engineServer: srv, gate: rig.drainGate}
	}
	rig.svc = New(t.Context(), cfg)
	coord, err := NewCoordination(CoordinationConfig{Store: rig.store, AuthSecret: testAuthSecret, Clock: time.Now})
	if err != nil {
		t.Fatal(err)
	}
	rig.coord = coord
	rig.svc.SetCoordination(coord)
	if err := rig.svc.SettingsChanged(t.Context()); err != nil {
		t.Fatal(err)
	}
	deadline := time.After(10 * time.Second)
	for rig.svc.engineFeature().State != stateOn {
		select {
		case <-deadline:
			t.Fatalf("the engine did not start: %+v", rig.svc.engineFeature())
		case <-time.After(10 * time.Millisecond):
		}
	}
	return rig
}

// TestShutdownOrder: shutdown wakes held netmaps at its start, while the HTTP/3
// drain is still running, answers later netmap calls at once, and leaves no host
// entry, no region connection and no bound UDP port behind. The engine is real,
// so the clock is the wall clock; the coordination server's reconcile tick (5 s)
// is far longer than the test, so it cannot be what releases a held call.
func TestShutdownOrder(t *testing.T) {
	rig := newShutdownRig(t)
	device := newRegionDevice(t, rig.coord, "tv")
	device.register(t, rig.coord)
	first, err := rig.coord.Netmap(t.Context(), device.principal, "")
	if err != nil {
		t.Fatal(err)
	}
	closed := device.connect(t, rig.coord, device.principal)

	type answer struct {
		m   protocol.Netmap
		err error
		at  time.Time
	}
	held := make(chan answer, 1)
	go func() {
		m, err := rig.coord.Netmap(t.Context(), device.principal, first.Version)
		held <- answer{m, err, time.Now()}
	}()
	time.Sleep(200 * time.Millisecond) //nolint:forbidigo // A call parked in the module's hold is not observable from outside; the wait lets it get there.

	shutdownDone := make(chan error, 1)
	begin := time.Now()
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		shutdownDone <- rig.svc.Shutdown(ctx)
	}()

	select {
	case a := <-held:
		if a.err != nil {
			t.Fatalf("held netmap: %v", a.err)
		}
		if took := a.at.Sub(begin); took > time.Second {
			t.Fatalf("held netmap returned %v after Shutdown began, want under 1s", took)
		}
	case <-time.After(time.Second):
		t.Fatal("a netmap held before Shutdown is still waiting 1s after it began")
	}

	late := make(chan error, 1)
	go func() {
		_, err := rig.coord.Netmap(t.Context(), device.principal, first.Version)
		late <- err
	}()
	select {
	case err := <-late:
		if err != nil {
			t.Fatalf("netmap after Shutdown began: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("a netmap requested after Shutdown began did not answer at once")
	}

	// The drain is still held, so the host entry is still there.
	if hosts, err := rig.store.Hosts(t.Context(), time.Now()); err != nil || len(hosts) != 1 {
		t.Fatalf("Hosts during the drain = %+v, %v; want the api host", hosts, err)
	}
	close(rig.drainGate)
	select {
	case err := <-shutdownDone:
		if err != nil {
			t.Fatalf("Shutdown: %v", err)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("Shutdown did not return")
	}
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("the region connection is still open after Shutdown")
	}
	hosts, err := rig.store.Hosts(t.Context(), time.Now())
	if err != nil || len(hosts) != 0 {
		t.Fatalf("Hosts after Shutdown = %+v, %v; want none", hosts, err)
	}
	conn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4zero, Port: rig.port})
	if err != nil {
		t.Fatalf("the engine's UDP port is still bound: %v", err)
	}
	_ = conn.Close()
}

// TestShutdownReservesTimeAfterOpenStream: a direct request that stays open
// through the drain is cut at the caller's deadline minus drainReserve, and the
// steps after the drain finish before the caller's deadline instead of failing on
// a spent context.
func TestShutdownReservesTimeAfterOpenStream(t *testing.T) {
	rig := newShutdownRig(t) // The drain gate stays shut: the stream never ends by itself.
	device := newRegionDevice(t, rig.coord, "tv")
	device.register(t, rig.coord)
	closed := device.connect(t, rig.coord, device.principal)

	const budget = drainReserve + 3*time.Second
	ctx, cancel := context.WithTimeout(context.Background(), budget)
	defer cancel()
	deadline, _ := ctx.Deadline()
	begin := time.Now()
	if err := rig.svc.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown: %v, want nil: the cut of an open stream is not a failure", err)
	}
	finished := time.Now()
	if took := finished.Sub(begin); took < budget-drainReserve {
		t.Fatalf("Shutdown returned after %v, before the open stream's time (%v) ended", took, budget-drainReserve)
	}
	if left := deadline.Sub(finished); left < drainReserve/2 {
		t.Fatalf("Shutdown returned %v before the caller's deadline, want at least %v kept for the later steps", left, drainReserve/2)
	}
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("the region connection is still open after Shutdown")
	}
	if hosts, err := rig.store.Hosts(t.Context(), time.Now()); err != nil || len(hosts) != 0 {
		t.Fatalf("Hosts after Shutdown = %+v, %v; want none", hosts, err)
	}
}

// TestDrainRefusesEnrollmentAndMarksHost: after BeginDrain the capability host is
// draining and enrollment is refused, while netmap calls still wait out their
// hold.
func TestDrainRefusesEnrollmentAndMarksHost(t *testing.T) {
	rig := newShutdownRig(t)
	close(rig.drainGate)
	defer func() { _ = rig.svc.Shutdown(context.Background()) }()
	rig.coord.cfg.Host = rig.svc
	device := newRegionDevice(t, rig.coord, "tv")
	device.register(t, rig.coord)
	first, err := rig.coord.Netmap(t.Context(), device.principal, "")
	if err != nil {
		t.Fatal(err)
	}
	held := make(chan error, 1)
	go func() {
		_, err := rig.coord.Netmap(t.Context(), device.principal, first.Version)
		held <- err
	}()
	time.Sleep(200 * time.Millisecond) //nolint:forbidigo // A call parked in the module's hold is not observable from outside; the wait lets it get there.
	if host, err := rig.coord.Capability(t.Context()); err != nil || host.Host.Draining {
		t.Fatalf("Capability before drain = %+v, %v; want a host that is not draining", host.Host, err)
	}
	rig.svc.BeginDrain()
	capability, err := rig.coord.Capability(t.Context())
	if err != nil || !capability.Host.Draining || !capability.Host.Available {
		t.Fatalf("Capability while draining = %+v, %v; want an available, draining host", capability.Host, err)
	}
	_, err = rig.coord.Enroll(t.Context(), EnrollRequest{UserID: 7, ProfileID: "p", SessionID: testSessionID, DeviceID: "tv", NodeKey: protocol.NodeKey{0x01}, ConnectionPath: "automatic"})
	if err != ErrDraining { //nolint:errorlint // The sentinel is returned unwrapped.
		t.Fatalf("Enroll while draining = %v, want ErrDraining", err)
	}
	select {
	case err := <-held:
		t.Fatalf("a netmap held before the drain ended with %v when it began, want it still held", err)
	case <-time.After(500 * time.Millisecond):
	}
}

// refusingHostStore refuses DeleteHost as the fenced Redis store does once
// another process holds the engine lease, from the moment refuse is set, and
// counts the refusals.
type refusingHostStore struct {
	CoordinationStore
	refuse  atomic.Bool
	refused atomic.Int32
}

func (s *refusingHostStore) DeleteHost(ctx context.Context, label string) error {
	if s.refuse.Load() {
		s.refused.Add(1)
		return fmt.Errorf("delete host: %w", errNotLeaseHolder)
	}
	return s.CoordinationStore.DeleteHost(ctx, label) //nolint:wrapcheck // A test double passes the store's result through.
}

// TestShutdownRefusedDeleteIsNotAnError: at shutdown, a host entry removal that
// the store refuses because another process holds the lease leaves the
// successor's entry and is not a shutdown failure.
func TestShutdownRefusedDeleteIsNotAnError(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		store := &refusingHostStore{CoordinationStore: newMemoryStore()}
		f, _ := startReplica(t, store, nil)
		defer f.cleanup()
		f.apply()
		f.initial()
		store.refuse.Store(true)
		if err := f.svc.Shutdown(t.Context()); err != nil {
			t.Fatalf("Shutdown = %v, want nil: the refused entry is the successor's", err)
		}
		if n := store.refused.Load(); n != 1 {
			t.Fatalf("DeleteHost calls after Shutdown began = %d, want 1", n)
		}
	})
}
