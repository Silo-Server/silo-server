package directconn

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/darthShadow/shunpike/coordination/protocol"
	"github.com/darthShadow/shunpike/coordination/server"
	"github.com/darthShadow/shunpike/engine"

	"github.com/Silo-Server/silo-server/internal/netaccess"
)

var (
	testDeviceDisco = protocol.DiscoKey{0xD1}
	testDeviceAddr  = netip.MustParseAddrPort("203.0.113.9:4000")
)

// hostFixture is a started lifecycle fixture with a real coordination service
// over the memory store, all on synctest time.
type hostFixture struct {
	*lifecycleFixture
	coord *Coordination
	store CoordinationStore
}

func newHostFixture(t *testing.T, name string) *hostFixture {
	t.Helper()
	f := newLifecycleFixture(t, name)
	store := newMemoryStore()
	coord, err := NewCoordination(CoordinationConfig{Store: store, AuthSecret: testAuthSecret, Clock: time.Now})
	if err != nil {
		t.Fatal(err)
	}
	f.svc.SetCoordination(coord)
	f.apply()
	return &hostFixture{lifecycleFixture: f, coord: coord, store: store}
}

// enrollAndRegister enrolls a device and stores its registration, as a client
// does, and returns the principal its ticket names.
func (f *hostFixture) enrollAndRegister(uid int, device string, key protocol.NodeKey, path string) server.Principal {
	f.t.Helper()
	enrollment, err := f.coord.Enroll(f.t.Context(), EnrollRequest{UserID: uid, ProfileID: "profile-1", SessionID: testSessionID, DeviceID: device, NodeKey: key, ConnectionPath: path})
	if err != nil {
		f.t.Fatal(err)
	}
	principal, _, err := f.coord.Authorize(f.t.Context(), enrollment.Ticket)
	if err != nil {
		f.t.Fatal(err)
	}
	f.register(principal)
	return principal
}

func (f *hostFixture) register(principal server.Principal) {
	f.t.Helper()
	body, err := json.Marshal(protocol.Registration{NodeKey: principal.NodeKey, DiscoKey: testDeviceDisco, Endpoints: []netip.AddrPort{testDeviceAddr}})
	if err != nil {
		f.t.Fatal(err)
	}
	if err := f.coord.Register(f.t.Context(), principal, bytes.NewReader(body)); err != nil {
		f.t.Fatalf("Register: %v", err)
	}
	synctest.Wait()
}

func (f *hostFixture) netmap(principal server.Principal, version string) protocol.Netmap {
	f.t.Helper()
	m, err := f.coord.Netmap(f.t.Context(), principal, version)
	if err != nil {
		f.t.Fatalf("Netmap: %v", err)
	}
	return m
}

// apiHost returns the "api" host of a netmap.
func apiHost(t *testing.T, m protocol.Netmap) protocol.Host {
	t.Helper()
	for _, h := range m.Hosts {
		if h.Label == CapabilityHostID {
			return h
		}
	}
	t.Fatalf("netmap has no api host: %+v", m)
	return protocol.Host{}
}

func TestDirectConnectionHostEntryAtStart(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f := newHostFixture(t, "enabled")
		defer f.cleanup()
		// No Changed event and no timer tick has run: the entry comes from the start.
		hosts, err := f.store.Hosts(t.Context(), time.Now())
		if err != nil || len(hosts) != 1 {
			t.Fatalf("Hosts = %+v, %v; want the api host", hosts, err)
		}
		reg, _ := f.engines[0].Registration()
		if h := hosts[0]; h.Label != CapabilityHostID || h.NodeKey != reg.NodeKey || h.DiscoKey != reg.DiscoKey || len(h.Endpoints) != 1 || h.Endpoints[0] != reg.Endpoints[0] {
			t.Fatalf("host entry = %+v, want the engine's identity %+v", h, reg)
		}
	})
}

func TestDirectConnectionEngineStartReason(t *testing.T) {
	for _, row := range []struct{ name, reason string }{
		{"peers failure", "engine_start_failed"},
		{"start failure", "engine_start_failed"},
		{"bind failure", "udp_port_bind_failed"},
	} {
		t.Run(row.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				f := newLifecycleFixture(t, row.name)
				defer f.cleanup()
				f.apply()
				got := f.svc.engineFeature()
				if got.State != stateOff || got.Reason != row.reason {
					t.Fatalf("feature = %+v, want off/%s", got, row.reason)
				}
			})
		})
	}
}

func TestDirectConnectionHeldNetmapWakes(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f := newHostFixture(t, "enabled")
		defer f.cleanup()
		principal := f.enrollAndRegister(7, "tv", testNodeKey(1), "automatic")
		before := f.netmap(principal, "")
		type result struct {
			m   protocol.Netmap
			err error
		}
		done := make(chan result, 1)
		start := time.Now()
		go func() {
			m, err := f.coord.Netmap(t.Context(), principal, before.Version)
			done <- result{m, err}
		}()
		synctest.Wait()
		select {
		case r := <-done:
			t.Fatalf("held netmap returned before any change: %+v, %v", r.m, r.err)
		default:
		}
		reg, _ := f.engines[0].Registration()
		reg.Endpoints = []netip.AddrPort{netip.MustParseAddrPort("198.51.100.2:8094")}
		f.engines[0].setRegistration(reg, true)
		synctest.Wait()
		select {
		case r := <-done:
			if r.err != nil {
				t.Fatal(r.err)
			}
			if got := time.Since(start); got >= 25*time.Second {
				t.Errorf("held netmap returned after %v, want early", got)
			}
			if host := apiHost(t, r.m); len(host.Endpoints) != 1 || host.Endpoints[0] != reg.Endpoints[0] || r.m.Version == before.Version {
				t.Errorf("woken netmap = %+v, want the new endpoint and a new version", r.m)
			}
		default:
			t.Fatal("held netmap did not return after the engine changed")
		}
	})
}

// TestConfirmationWakesHeldNetmap: the API host's confirmation of a device
// reaches that device's held netmap through the in-process wake. No fake time
// passes, so no 30 s host tick, 25 s hold or client refresh can deliver it, and
// no host write runs, so the wake of a host write cannot either.
func TestConfirmationWakesHeldNetmap(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f := newHostFixture(t, "enabled")
		defer f.cleanup()
		key := testNodeKey(1)
		principal := f.enrollAndRegister(7, "tv", key, "automatic")
		before := f.netmap(principal, "")
		done := make(chan protocol.Netmap, 1)
		go func() {
			m, _ := f.coord.Netmap(t.Context(), principal, before.Version)
			done <- m
		}()
		synctest.Wait()
		select {
		case m := <-done:
			t.Fatalf("held netmap returned before any change: %+v", m)
		default:
		}
		start := time.Now()
		confirmation := protocol.Confirmation{Peer: key, DiscoKey: testDeviceDisco, Endpoint: testDeviceAddr}
		if err := f.coord.setConfirmation(t.Context(), confirmation); err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		select {
		case m := <-done:
			if got := apiHost(t, m).Confirmation; got == nil || *got != confirmation {
				t.Fatalf("woken netmap confirmation = %+v, want %+v", got, confirmation)
			}
			if got := time.Since(start); got != 0 {
				t.Fatalf("held netmap returned after %v of fake time, want an immediate wake", got)
			}
		default:
			t.Fatal("held netmap did not return after the confirmation was set")
		}
	})
}

// A confirmation lives 90 s in the store and an unchanged pong fires no event, so
// the 30 s tick renews it.
func TestConfirmationRenewal(t *testing.T) {
	confirmation := protocol.Confirmation{Peer: testNodeKey(1), DiscoKey: testDeviceDisco, Endpoint: testDeviceAddr}
	confirmed := func(t *testing.T, f *hostFixture) protocol.Registration {
		t.Helper()
		reg, _ := f.engines[0].Registration()
		reg.Confirmations = []protocol.Confirmation{confirmation}
		f.engines[0].setRegistration(reg, true)
		synctest.Wait()
		return reg
	}
	t.Run("renewed across four periods", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			f := newHostFixture(t, "enabled")
			defer f.cleanup()
			principal := f.enrollAndRegister(7, "tv", testNodeKey(1), "automatic")
			confirmed(t, f)
			first := f.netmap(principal, "")
			if apiHost(t, first).Confirmation == nil {
				t.Fatal("the netmap lacks the API confirmation after the engine reported it")
			}
			for step := 1; step <= 12; step++ {
				time.Sleep(30 * time.Second) //nolint:forbidigo // Advance virtual time one renewal step.
				synctest.Wait()
				f.register(principal) // the client re-registers on its own timer
				m := f.netmap(principal, "")
				if apiHost(t, m).Confirmation == nil || m.Version != first.Version {
					t.Fatalf("after %d s: confirmation=%v version=%q, want kept and %q", step*30, apiHost(t, m).Confirmation, m.Version, first.Version)
				}
				if step == 5 {
					// A held netmap returns only at the hold: no renewal wakes it.
					held := time.Now()
					done := make(chan protocol.Netmap, 1)
					go func() {
						m, err := f.coord.Netmap(t.Context(), principal, first.Version)
						if err != nil {
							t.Error(err)
						}
						done <- m
					}()
					time.Sleep(24 * time.Second) //nolint:forbidigo // Stay inside the 25 s hold.
					synctest.Wait()
					select {
					case <-done:
						t.Fatal("held netmap returned before the hold ended")
					default:
					}
					time.Sleep(time.Second) //nolint:forbidigo // Reach the hold.
					synctest.Wait()
					select {
					case m := <-done:
						if got := time.Since(held); got != 25*time.Second || m.Version != first.Version {
							t.Fatalf("held netmap returned after %v with version %q, want 25s and %q", got, m.Version, first.Version)
						}
					default:
						t.Fatal("held netmap did not return at the hold")
					}
					time.Sleep(5 * time.Second) //nolint:forbidigo // Return to the 30 s grid.
					synctest.Wait()
				}
			}
		})
	})
	t.Run("dropped confirmation clears on its event", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			f := newHostFixture(t, "enabled")
			defer f.cleanup()
			principal := f.enrollAndRegister(7, "tv", testNodeKey(1), "automatic")
			reg := confirmed(t, f)
			first := f.netmap(principal, "")
			reg.Confirmations = nil
			f.engines[0].setRegistration(reg, true)
			synctest.Wait()
			m := f.netmap(principal, "")
			if apiHost(t, m).Confirmation != nil || m.Version == first.Version {
				t.Fatalf("netmap after the drop = %+v, want no confirmation and a new version", m)
			}
		})
	})
	t.Run("dropped confirmation without an event lapses after its last renewal", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			f := newHostFixture(t, "enabled")
			defer f.cleanup()
			principal := f.enrollAndRegister(7, "tv", testNodeKey(1), "automatic")
			reg := confirmed(t, f)
			reg.Confirmations = nil
			f.engines[0].setRegistration(reg, false)
			// Renewals stop, and the stored copy goes 90 s after the last one: the
			// set at engine start of the confirmation, at 0 s.
			for _, at := range []time.Duration{60 * time.Second, 89 * time.Second} {
				time.Sleep(at - time.Since(f.start)) //nolint:forbidigo // Advance virtual time.
				synctest.Wait()
				f.register(principal)
				if apiHost(t, f.netmap(principal, "")).Confirmation == nil {
					t.Fatalf("confirmation gone at %v, before 90 s", at)
				}
			}
			time.Sleep(2 * time.Second) //nolint:forbidigo // Cross 90 s.
			synctest.Wait()
			f.register(principal)
			if apiHost(t, f.netmap(principal, "")).Confirmation != nil {
				t.Fatal("confirmation still present after 90 s without a renewal")
			}
		})
	})
}

func TestPeerCacheRevocation(t *testing.T) {
	setup := func(t *testing.T) (*hostFixture, server.Principal) {
		f := newHostFixture(t, "enabled")
		principal := f.enrollAndRegister(7, "tv", testNodeKey(1), "automatic")
		if !f.svc.IsPeer(principal.NodeKey) {
			t.Fatal("a registered device is not an engine peer")
		}
		return f, principal
	}
	t.Run("another replica withdraws", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			f, principal := setup(t)
			defer f.cleanup()
			// The store changes without this replica's cache hearing of it.
			if err := f.store.DeleteDevice(t.Context(), principal.Ref); err != nil {
				t.Fatal(err)
			}
			time.Sleep(10*time.Second + time.Second) //nolint:forbidigo // One Directory refresh.
			synctest.Wait()
			if f.svc.IsPeer(principal.NodeKey) {
				t.Fatal("a withdrawn device is still an engine peer after one Directory refresh")
			}
		})
	})
	t.Run("this replica withdraws", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			f, principal := setup(t)
			defer f.cleanup()
			if err := f.coord.Withdraw(t.Context(), 7, "tv"); err != nil {
				t.Fatal(err)
			}
			synctest.Wait()
			if f.svc.IsPeer(principal.NodeKey) {
				t.Fatal("a withdrawn device is still an engine peer")
			}
		})
	})
}

// A request from a peer reaches the handler with the policy the peer cache holds
// now, not the one it held at the first lookup.
func TestDirectWrapperPolicyFromDirectory(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f := newHostFixture(t, "enabled")
		defer f.cleanup()
		key := testNodeKey(1)
		f.enrollAndRegister(7, "tv", key, "automatic")
		var policy string
		next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			policy = netaccess.PathFromContext(r.Context()).Direct.Policy
			w.WriteHeader(http.StatusNoContent)
		})
		source := func(context.Context) observedPeer { return observedPeer{Key: key, Endpoint: testDeviceAddr} }
		h := wrapDirect(CapabilityHostID, source, f.coord.directory.Lookup, next)
		serve := func() {
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "https://peer.invalid/probe", nil))
			if rec.Code != http.StatusNoContent {
				t.Fatalf("status = %d", rec.Code)
			}
		}
		serve()
		if policy != "automatic" {
			t.Fatalf("policy = %q, want automatic", policy)
		}
		// The device enrolls again with the other policy; the key is unchanged, so
		// no event reaches the cache and only its refresh can carry the change.
		if _, err := f.coord.Enroll(t.Context(), EnrollRequest{UserID: 7, ProfileID: "profile-1", SessionID: testSessionID, DeviceID: "tv", NodeKey: key, ConnectionPath: "direct_only"}); err != nil {
			t.Fatal(err)
		}
		serve()
		if policy != "automatic" {
			t.Fatalf("policy = %q before the refresh, want the cached automatic", policy)
		}
		time.Sleep(11 * time.Second) //nolint:forbidigo // One Directory refresh.
		synctest.Wait()
		serve()
		if policy != "direct_only" {
			t.Fatalf("policy = %q after one Directory refresh, want direct_only", policy)
		}
	})
}

// storeReads counts Device reads on the wrapped store.
type storeReads struct {
	CoordinationStore
	devices atomic.Int32
}

func (s *storeReads) Device(ctx context.Context, ref server.DeviceRef, now time.Time) (server.Device, error) {
	s.devices.Add(1)
	return s.CoordinationStore.Device(ctx, ref, now) //nolint:wrapcheck // A test double passes the store's result through.
}

func TestDirectStatePathRecord(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f := newLifecycleFixture(t, "enabled")
		defer f.cleanup()
		store := &storeReads{CoordinationStore: newMemoryStore()}
		coord, err := NewCoordination(CoordinationConfig{Store: store, AuthSecret: testAuthSecret, Clock: time.Now})
		if err != nil {
			t.Fatal(err)
		}
		f.svc.SetCoordination(coord)
		f.apply()
		host := &hostFixture{lifecycleFixture: f, coord: coord, store: store}
		principal := host.enrollAndRegister(7, "tv", testNodeKey(1), "automatic")
		setPath := func(addr string) {
			f.engines[0].stateMu.Lock()
			f.engines[0].paths = map[protocol.NodeKey]engine.PathStatus{
				principal.NodeKey: {Class: pathClassDirect, Endpoint: netip.MustParseAddrPort(addr)},
			}
			f.engines[0].stateMu.Unlock()
		}
		_, paths := f.svc.coordination()
		paths.setConfirmed(principal.NodeKey, true)
		device := func() DeviceDirect {
			t.Helper()
			before := store.devices.Load()
			d, ok, err := f.svc.Device(t.Context(), 7, "tv")
			if err != nil || !ok {
				t.Fatalf("Device = %+v, %v, %v", d, ok, err)
			}
			if reads := store.devices.Load() - before; reads != 1 {
				t.Fatalf("Device made %d store reads, want 1", reads)
			}
			return d
		}

		setPath("203.0.113.9:4000")
		first := device()
		if !first.APIReady || first.PathGeneration == 0 || first.ConnectionKey == "" {
			t.Fatalf("direct path = %+v, want a ready path with a generation and a key", first)
		}
		if again := device(); again.PathGeneration != first.PathGeneration {
			t.Fatalf("generation = %d after a read with an unchanged path, want %d", again.PathGeneration, first.PathGeneration)
		}
		setPath("203.0.113.10:4000")
		moved := device()
		if moved.PathGeneration == first.PathGeneration || moved.PathGeneration == 0 {
			t.Fatalf("generation = %d after the path changed, want a new nonzero one (was %d)", moved.PathGeneration, first.PathGeneration)
		}
		if got := f.svc.PathGeneration(7, "tv"); got != moved.PathGeneration {
			t.Fatalf("PathGeneration = %d, want %d", got, moved.PathGeneration)
		}
	})
}

// Peers lists the devices whose path is direct and leaves out a relayed device,
// even one that has an observed endpoint.
func TestPeersListsOnlyDirectDevices(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		host := newHostFixture(t, "enabled")
		defer host.cleanup()
		f := host.lifecycleFixture
		direct := host.enrollAndRegister(7, "tv", testNodeKey(1), "automatic")
		relayed := host.enrollAndRegister(7, "phone", testNodeKey(2), "automatic")
		endpoint := netip.MustParseAddrPort("203.0.113.9:4000")
		f.engines[0].stateMu.Lock()
		f.engines[0].paths = map[protocol.NodeKey]engine.PathStatus{
			direct.NodeKey:  {Class: pathClassDirect, Endpoint: endpoint},
			relayed.NodeKey: {Class: engine.PathClass("relay"), Endpoint: netip.MustParseAddrPort("203.0.113.10:4000")},
		}
		f.engines[0].stateMu.Unlock()
		peers := f.svc.Peers(t.Context())
		if len(peers) != 1 || peers[0].UserID != 7 || peers[0].DeviceID != "tv" || peers[0].Endpoint != endpoint {
			t.Fatalf("Peers = %+v, want only the direct device tv with its endpoint", peers)
		}
	})
}

// While the toggle is off, Device answers ok=false for an enrolled device and
// reads no store: the caller needs no setting read of its own.
func TestDirectStateToggleOff(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f := newLifecycleFixture(t, "enabled")
		defer f.cleanup()
		store := &storeReads{CoordinationStore: newMemoryStore()}
		coord, err := NewCoordination(CoordinationConfig{Store: store, AuthSecret: testAuthSecret, Clock: time.Now})
		if err != nil {
			t.Fatal(err)
		}
		f.svc.SetCoordination(coord)
		f.apply()
		host := &hostFixture{lifecycleFixture: f, coord: coord, store: store}
		host.enrollAndRegister(7, "tv", testNodeKey(1), "automatic")
		if _, ok, err := f.svc.Device(t.Context(), 7, "tv"); err != nil || !ok {
			t.Fatalf("Device with the toggle on = ok %v, %v; want ok", ok, err)
		}
		f.store.set("direct_connections.enabled", "false")
		f.apply()
		before := store.devices.Load()
		d, ok, err := f.svc.Device(t.Context(), 7, "tv")
		if err != nil || ok || d != (DeviceDirect{}) {
			t.Fatalf("Device with the toggle off = %+v, ok %v, %v; want the zero value and ok=false", d, ok, err)
		}
		if reads := store.devices.Load() - before; reads != 0 {
			t.Fatalf("Device made %d store reads with the toggle off, want 0", reads)
		}
		if enrolled, err := f.svc.Enrolled(t.Context(), 7, "tv"); err != nil || enrolled {
			t.Fatalf("Enrolled with the toggle off = %v, %v; want false", enrolled, err)
		}
	})
}

func TestPeerConnectedAtUsesEngineObservationTime(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f := newHostFixture(t, "enabled")
		defer f.cleanup()
		principal := f.enrollAndRegister(7, "tv", testNodeKey(1), "automatic")
		e := f.engines[0]
		e.stateMu.Lock()
		e.paths = map[protocol.NodeKey]engine.PathStatus{
			principal.NodeKey: {Class: pathClassDirect, Endpoint: testDeviceAddr},
		}
		e.stateMu.Unlock()
		reg, err := e.Registration()
		if err != nil {
			t.Fatal(err)
		}
		reg.Confirmations = []protocol.Confirmation{{
			Peer: principal.NodeKey, DiscoKey: testDeviceDisco, Endpoint: testDeviceAddr,
		}}
		observedAt := time.Now()
		e.setRegistration(reg, true)
		synctest.Wait()
		time.Sleep(time.Second) //nolint:forbidigo // Advance the synctest clock between observation and the first read.
		peers := f.svc.Peers(t.Context())
		if len(peers) != 1 {
			t.Fatalf("peers = %d, want one direct peer", len(peers))
		}
		if !peers[0].Since.Equal(observedAt) {
			t.Fatalf("connected_at = %s, want the engine observation at %s", peers[0].Since, observedAt)
		}
	})
}
