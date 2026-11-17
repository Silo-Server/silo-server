package apiv2

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"testing/iotest"
	"time"

	"github.com/darthShadow/shunpike/coordination/protocol"
	"github.com/darthShadow/shunpike/coordination/server"
	"github.com/darthShadow/shunpike/engine"
	"github.com/darthShadow/shunpike/engine/coordclient"
	"github.com/redis/go-redis/v9"
	"tailscale.com/derp"
	"tailscale.com/types/key"

	"github.com/Silo-Server/silo-server/internal/cache"
	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/directconn"
	"github.com/Silo-Server/silo-server/internal/tuning"
)

const (
	directRegistrationPath = Prefix + "/direct-connections/registration"
	directNetmapPath       = Prefix + "/direct-connections/netmap"
)

// directPost sends a registration the way the engine's client does: JSON with no
// Content-Type header.
func directPost(t *testing.T, h http.Handler, ticket, body string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(http.MethodPost, directRegistrationPath, strings.NewReader(body))
	if ticket != "" {
		r.Header.Set(directconn.SignalingTicketHeader, ticket)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	return rec
}

func directGetNetmap(t *testing.T, h http.Handler, ticket, version string) *httptest.ResponseRecorder {
	t.Helper()
	path := directNetmapPath
	if version != "" {
		path += "?version=" + version
	}
	r := httptest.NewRequest(http.MethodGet, path, nil)
	if ticket != "" {
		r.Header.Set(directconn.SignalingTicketHeader, ticket)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	return rec
}

// directRegistrationBody is a valid registration for key.
func directRegistrationBody(t *testing.T, key protocol.NodeKey, endpoints ...netip.AddrPort) string {
	t.Helper()
	if endpoints == nil {
		endpoints = []netip.AddrPort{netip.MustParseAddrPort("203.0.113.9:4000")}
	}
	body, err := json.Marshal(protocol.Registration{NodeKey: key, DiscoKey: protocol.DiscoKey{0xD1}, Endpoints: endpoints})
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}

type directNetmapDoc struct {
	Version string `json:"version"`
	Hosts   []struct {
		Label    string `json:"label"`
		NodeKey  string `json:"node_key"`
		DiscoKey string `json:"disco_key"`
	} `json:"hosts"`
}

// directRegistrationEnv is an environment with one device enrolled as "tv".
func directRegistrationEnv(t *testing.T) (*directTestEnv, http.Handler, directEnrollmentBody) {
	t.Helper()
	e := newDirectTestEnv(t)
	h := e.handler()
	enrollment := e.mustEnroll(h, directLoginToken, "tv", directTestKey(1))
	return e, h, enrollment
}

// directRegistered reports whether the "tv" device holds a stored registration.
func directRegistered(t *testing.T, e *directTestEnv) bool {
	t.Helper()
	d, err := e.store.Device(t.Context(), server.DeviceRef{Account: "1", Device: "tv"}, e.clock.Now())
	return err == nil && d.Registration != nil
}

func TestDirectRegistrationNegatives(t *testing.T) {
	newEnv := directRegistrationEnv
	registered := func(e *directTestEnv) bool { return directRegistered(t, e) }
	t.Run("a body with no Content-Type registers", func(t *testing.T) {
		e, h, enrollment := newEnv(t)
		if rec := directPost(t, h, enrollment.Ticket, directRegistrationBody(t, directTestKey(1))); rec.Code != http.StatusNoContent {
			t.Fatalf("registration = %d: %s", rec.Code, rec.Body.String())
		}
		if !registered(e) {
			t.Fatal("the registration was not stored")
		}
	})
	t.Run("a missing ticket is 401 before any write", func(t *testing.T) {
		e, h, _ := newEnv(t)
		requireProblem(t, directPost(t, h, "", directRegistrationBody(t, directTestKey(1))), TypeInvalidToken)
		if registered(e) {
			t.Fatal("a registration was stored without a ticket")
		}
	})
	t.Run("an expired ticket is 401 before any write", func(t *testing.T) {
		e, h, enrollment := newEnv(t)
		e.clock.Advance(directTestLease)
		requireProblem(t, directPost(t, h, enrollment.Ticket, directRegistrationBody(t, directTestKey(1))), TypeInvalidToken)
		if registered(e) {
			t.Fatal("a registration was stored with an expired ticket")
		}
	})
	t.Run("a registration from another key is 401", func(t *testing.T) {
		e, h, enrollment := newEnv(t)
		requireProblem(t, directPost(t, h, enrollment.Ticket, directRegistrationBody(t, directTestKey(2))), TypeInvalidToken)
		if registered(e) {
			t.Fatal("a registration from another key was stored")
		}
	})
	t.Run("more than 64 endpoints is 422", func(t *testing.T) {
		e, h, enrollment := newEnv(t)
		endpoints := make([]netip.AddrPort, 65)
		for i := range endpoints {
			endpoints[i] = netip.AddrPortFrom(netip.AddrFrom4([4]byte{203, 0, 113, byte(i + 1)}), 4000)
		}
		requireProblem(t, directPost(t, h, enrollment.Ticket, directRegistrationBody(t, directTestKey(1), endpoints...)), TypeValidationFailed)
		if registered(e) {
			t.Fatal("an invalid registration was stored")
		}
	})
	t.Run("a body over 64 KiB is 413", func(t *testing.T) {
		_, h, enrollment := newEnv(t)
		body := `{"node_key":"` + strings.Repeat("a", 64<<10) + `"}`
		requireProblem(t, directPost(t, h, enrollment.Ticket, body), TypeForStatus(http.StatusRequestEntityTooLarge))
	})
	t.Run("a body with another media type is 415", func(t *testing.T) {
		e, h, enrollment := newEnv(t)
		r := httptest.NewRequest(http.MethodPost, directRegistrationPath, strings.NewReader(directRegistrationBody(t, directTestKey(1))))
		r.Header.Set(directconn.SignalingTicketHeader, enrollment.Ticket)
		r.Header.Set("Content-Type", "text/plain")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, r)
		requireProblem(t, rec, TypeUnsupportedMediaType)
		if registered(e) {
			t.Fatal("a registration was stored with another media type")
		}
	})
	t.Run("a body that stalls past the read deadline is 408", func(t *testing.T) {
		e, h, enrollment := newEnv(t)
		r := httptest.NewRequest(http.MethodPost, directRegistrationPath, io.NopCloser(iotest.ErrReader(os.ErrDeadlineExceeded)))
		r.Header.Set(directconn.SignalingTicketHeader, enrollment.Ticket)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, r)
		requireProblem(t, rec, TypeRequestTimeout)
		if registered(e) {
			t.Fatal("a registration was stored after a stalled body")
		}
	})
}

// TestDirectSignalingGateRefusals: the ticket, toggle, engine and store checks
// that registration and netmap run before any work.
func TestDirectSignalingGateRefusals(t *testing.T) {
	newEnv := directRegistrationEnv
	registered := func(e *directTestEnv) bool { return directRegistered(t, e) }
	t.Run("a replica that does not run the engine is 503", func(t *testing.T) {
		e, h, enrollment := newEnv(t)
		e.host.host.Available = false
		for name, rec := range map[string]*httptest.ResponseRecorder{
			"registration": directPost(t, h, enrollment.Ticket, directRegistrationBody(t, directTestKey(1))),
			"netmap":       directGetNetmap(t, h, enrollment.Ticket, ""),
		} {
			requireProblem(t, rec, TypeDependencyUnavailable)
			if got := rec.Header().Get("Retry-After"); got != "5" {
				t.Errorf("%s: Retry-After = %q, want 5", name, got)
			}
		}
		if registered(e) {
			t.Fatal("a registration was stored on a replica without the engine")
		}
	})
	for name, set := range map[string]func(*directTestEnv){
		"toggle off":         func(e *directTestEnv) { e.host.host.Enabled = false },
		"engine not running": func(e *directTestEnv) { e.host.host.Available = false },
	} {
		t.Run("a bad ticket is 401 with the "+name, func(t *testing.T) {
			e, h, _ := newEnv(t)
			set(e)
			requireProblem(t, directPost(t, h, "bad-ticket", directRegistrationBody(t, directTestKey(1))), TypeInvalidToken)
			requireProblem(t, directGetNetmap(t, h, "bad-ticket", ""), TypeInvalidToken)
		})
	}
	t.Run("the toggle off is 409", func(t *testing.T) {
		e, h, enrollment := newEnv(t)
		e.host.host.Enabled = false
		requireProblem(t, directPost(t, h, enrollment.Ticket, directRegistrationBody(t, directTestKey(1))), TypeCapabilityDisabled)
		requireProblem(t, directGetNetmap(t, h, enrollment.Ticket, ""), TypeCapabilityDisabled)
	})
	t.Run("a store timeout is 503 with Retry-After, not 408", func(t *testing.T) {
		for name, cause := range map[string]error{
			"a deadline":         context.DeadlineExceeded,
			"a socket deadline":  os.ErrDeadlineExceeded,
			"a canceled context": context.Canceled,
		} {
			e, h, enrollment := newEnv(t)
			e.store.setDownCause(cause)
			for op, rec := range map[string]*httptest.ResponseRecorder{
				"registration": directPost(t, h, enrollment.Ticket, directRegistrationBody(t, directTestKey(1))),
				"netmap":       directGetNetmap(t, h, enrollment.Ticket, ""),
			} {
				requireProblem(t, rec, TypeDependencyUnavailable)
				if got := rec.Header().Get("Retry-After"); got != "5" {
					t.Errorf("%s after %s: Retry-After = %q, want 5", op, name, got)
				}
			}
		}
	})
}

// While the API host is unavailable and the engine still runs, as on a lease
// holder that sees a second replica, the signaling gate keeps serving a device
// whose entry holds a live registration and refuses every other device. A
// replica whose engine does not run refuses every device.
func TestDirectSignalingGateKeepsEstablishedDevices(t *testing.T) {
	const notAccepting = "This server replica is not accepting new direct connections; try again shortly."
	const noEngine = "This server replica does not run the direct connection engine; try again shortly."
	// presence is the coordination server's registration presence.
	const presence = 90 * time.Second
	refusedWith := func(t *testing.T, name string, rec *httptest.ResponseRecorder, detail string) {
		t.Helper()
		if p := requireProblem(t, rec, TypeDependencyUnavailable); p.Detail != detail {
			t.Errorf("%s: detail %q, want %q", name, p.Detail, detail)
		}
		if got := rec.Header().Get("Retry-After"); got != "5" {
			t.Errorf("%s: Retry-After = %q, want 5", name, got)
		}
	}
	// signalRefusal sends a signal upgrade straight to the handler, which can
	// only answer a refusal: a recorder cannot be hijacked.
	signalRefusal := func(f *directFlow, ticket string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodGet, directSignalPath, nil)
		r.Header.Set("Connection", "Upgrade")
		r.Header.Set("Upgrade", "websocket")
		r.Header.Set("Sec-WebSocket-Version", "13")
		r.Header.Set("Sec-WebSocket-Key", base64.StdEncoding.EncodeToString([]byte("0123456789abcdef")))
		r.Header.Set("Sec-WebSocket-Protocol", "derp")
		r.Header.Set(directconn.SignalingTicketHeader, ticket)
		rec := httptest.NewRecorder()
		f.handler().ServeHTTP(rec, r)
		return rec
	}
	// setup enrolls "tv", registers it while the gate is open when registered
	// is set, then closes the gate with the engine running or not.
	setup := func(t *testing.T, registered, engineRuns bool) (*directTestEnv, *directFlow, directEnrollmentBody) {
		t.Helper()
		e := newDirectTestEnv(t)
		f := newDirectFlow(t, e)
		enrollment := e.mustEnroll(f.handler(), directLoginToken, "tv", directTestKey(1))
		if registered {
			if rec := directPost(t, f.handler(), enrollment.Ticket, directRegistrationBody(t, directTestKey(1))); rec.Code != http.StatusNoContent {
				t.Fatalf("registration while the gate is open = %d: %s", rec.Code, rec.Body.String())
			}
		}
		e.host.host.Available, e.host.host.EngineRunning = false, engineRuns
		return e, f, enrollment
	}

	t.Run("an established device keeps registration, netmap and signal past one presence period", func(t *testing.T) {
		e, f, enrollment := setup(t, true, true)
		e.clock.Advance(presence * 2 / 3)
		if rec := directPost(t, f.handler(), enrollment.Ticket, directRegistrationBody(t, directTestKey(1))); rec.Code != http.StatusNoContent {
			t.Fatalf("registration refresh = %d: %s", rec.Code, rec.Body.String())
		}
		if rec := directGetNetmap(t, f.handler(), enrollment.Ticket, ""); rec.Code != http.StatusOK {
			t.Fatalf("netmap = %d: %s", rec.Code, rec.Body.String())
		}
		resp := f.upgrade(enrollment.Ticket, "derp")
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusSwitchingProtocols {
			t.Fatalf("signal upgrade = %d, want 101", resp.StatusCode)
		}
		// Past the first registration's presence, only the refresh keeps the
		// device in the peer set the engine is fed from.
		e.clock.Advance(presence * 2 / 3)
		if _, ok := e.coord.Directory().Lookup(t.Context(), directTestKey(1)); !ok {
			t.Fatal("the established device left the engine peer set one presence period after it registered")
		}
	})
	t.Run("a device that never registered is refused", func(t *testing.T) {
		_, f, enrollment := setup(t, false, true)
		refusedWith(t, "registration", directPost(t, f.handler(), enrollment.Ticket, directRegistrationBody(t, directTestKey(1))), notAccepting)
		refusedWith(t, "netmap", directGetNetmap(t, f.handler(), enrollment.Ticket, ""), notAccepting)
		refusedWith(t, "signal", signalRefusal(f, enrollment.Ticket), notAccepting)
	})
	t.Run("a device whose presence lapsed is refused", func(t *testing.T) {
		e, f, enrollment := setup(t, true, true)
		e.clock.Advance(presence + time.Second)
		refusedWith(t, "registration", directPost(t, f.handler(), enrollment.Ticket, directRegistrationBody(t, directTestKey(1))), notAccepting)
		refusedWith(t, "netmap", directGetNetmap(t, f.handler(), enrollment.Ticket, ""), notAccepting)
		refusedWith(t, "signal", signalRefusal(f, enrollment.Ticket), notAccepting)
	})
	t.Run("a replica whose engine does not run refuses an established device", func(t *testing.T) {
		_, f, enrollment := setup(t, true, false)
		refusedWith(t, "registration", directPost(t, f.handler(), enrollment.Ticket, directRegistrationBody(t, directTestKey(1))), noEngine)
		refusedWith(t, "netmap", directGetNetmap(t, f.handler(), enrollment.Ticket, ""), noEngine)
		refusedWith(t, "signal", signalRefusal(f, enrollment.Ticket), noEngine)
	})
}

// A directconn service restarted on the memory store has lost every entry, so a
// ticket it issued before the restart no longer registers.
func TestDirectRegistrationMemoryRestart(t *testing.T) {
	e := newDirectTestEnv(t)
	enrollment := e.mustEnroll(e.handler(), directLoginToken, "tv", directTestKey(1))
	restarted := newDirectTestEnv(t)
	restarted.clock = e.clock
	coord, err := directconn.NewCoordination(directconn.CoordinationConfig{Store: directconn.NewCoordinationStore(nil, ""), AuthSecret: directTestSecret,
		Clock: e.clock.Now, Host: e.host, TLSPin: directTestPin})
	if err != nil {
		t.Fatal(err)
	}
	restarted.deps.DirectCoordination = coord
	requireProblem(t, directPost(t, restarted.handler(), enrollment.Ticket, directRegistrationBody(t, directTestKey(1))), TypeInvalidToken)
}

// The enroll operation records the caller's login session in the entry: the
// session "s1" behind directLoginToken. Ending another session leaves the entry;
// ending s1 withdraws it.
func TestDirectEnrollmentRecordsTheCallersLoginSession(t *testing.T) {
	e := newDirectTestEnv(t)
	enrollment := e.mustEnroll(e.handler(), directLoginToken, "tv", directTestKey(1))

	e.coord.RevokeSession(t.Context(), 1, "s2")
	if _, _, err := e.coord.Authorize(t.Context(), enrollment.Ticket); err != nil {
		t.Fatalf("entry after another session ended: %v", err)
	}
	e.coord.RevokeSession(t.Context(), 1, "s1")
	if _, _, err := e.coord.Authorize(t.Context(), enrollment.Ticket); !errors.Is(err, directconn.ErrInvalidTicket) {
		t.Fatalf("entry after its issuing session ended: %v, want invalid", err)
	}
}

// A ticket names the login session that enrolled the entry. When another session
// of the account renews the same device and key, the older issuer's ticket gets
// 401 on registration and netmap, and the newer ticket still works.
func TestDirectSignalingRefusesAStaleIssuersTicket(t *testing.T) {
	e := newDirectTestEnv(t)
	h := e.handler()
	nodeKey := directTestKey(1)
	older := e.mustEnroll(h, directLoginToken, "tv", nodeKey)
	newer, err := e.coord.Enroll(t.Context(), directconn.EnrollRequest{UserID: 1, ProfileID: "p-owner", SessionID: "s2", DeviceID: "tv", NodeKey: nodeKey, ConnectionPath: "automatic"})
	if err != nil {
		t.Fatal(err)
	}

	requireProblem(t, directPost(t, h, older.Ticket, directRegistrationBody(t, nodeKey)), TypeInvalidToken)
	requireProblem(t, directGetNetmap(t, h, older.Ticket, ""), TypeInvalidToken)
	if rec := directPost(t, h, newer.Ticket, directRegistrationBody(t, nodeKey)); rec.Code != http.StatusNoContent {
		t.Fatalf("registration with the newer issuer's ticket = %d: %s", rec.Code, rec.Body.String())
	}
}

// freeUDPPort returns a UDP port that was free a moment ago.
func freeUDPPort(t *testing.T) int {
	t.Helper()
	conn, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	return conn.LocalAddr().(*net.UDPAddr).Port
}

// directTestSTUNServer is the STUN server both engines are given. Nothing listens
// on it: a UDP send to loopback succeeds, which is all the engine's first netcheck
// needs to see to keep IPv4 sending on. An engine with no STUN server sends no
// probe and rebinds about 3 s after it starts.
const directTestSTUNServer = "127.0.0.1:3478"

type directStaticSettings map[string]string

func (s directStaticSettings) GetAll(context.Context) (map[string]string, error) { return s, nil }

// directRedis is the Redis setup of a service whose store is the Redis store:
// the client and the process presence the service's replica monitor takes the
// engine lease with. The store's fence must be the presence's ID, or every host
// and confirmation write is refused. The redislab tests build it with
// newDirectRedis.
type directRedis struct {
	client   *redis.Client
	presence *cache.APIReplicaPresence
}

// startDirectService starts the API engine on a real UDP socket with the real
// directconn service over store, and returns it once the engine is up. Its
// coordination service reads clock. It opens UDP sockets, so a test that calls it
// runs on the host. The secret derives the API node key, so a restart that keeps
// it keeps the key. With rds, the service runs the replica census and engine
// lease on rds's client and presence; without it, there is no lease, which only
// the memory store accepts.
func startDirectService(t *testing.T, store directconn.CoordinationStore, clock func() time.Time, secret [32]byte, rds ...directRedis) *directService {
	t.Helper()
	var (
		redisClient *redis.Client
		presence    *cache.APIReplicaPresence
	)
	if len(rds) > 0 {
		redisClient, presence = rds[0].client, rds[0].presence
	}
	tune, _ := tuning.Parse(func(string) string { return "" })
	tune.DirectUDPPort = freeUDPPort(t)
	logs := &lockedBuffer{}
	t.Cleanup(func() {
		if t.Failed() {
			t.Logf("API engine log:\n%s", logs.String())
		}
	})
	svc := directconn.New(t.Context(), directconn.Config{
		SecretKey: secret[:], Logger: slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug})), Tuning: tune,
		Settings: directStaticSettings{config.DirectConnectionsEnabledSettingKey: "true", config.DirectConnectionsSTUNServersSettingKey: directTestSTUNServer},
		NAT: func(context.Context, *slog.Logger, []string) (engine.NATReport, error) {
			return engine.NATReport{}, nil
		},
		PortMap: func(context.Context, *slog.Logger) (engine.PortMappingReport, error) {
			return engine.PortMappingReport{}, nil
		},
		Capabilities: func(context.Context, *slog.Logger) (engine.CapabilityReport, error) {
			return engine.CapabilityReport{Platform: "linux"}, nil
		},
		RedisClient: redisClient, ReplicaPresence: presence,
	})
	coord, err := directconn.NewCoordination(directconn.CoordinationConfig{Store: store, AuthSecret: directTestSecret, Clock: clock, Host: svc, TLSPin: directTestPin,
		Logger: slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug}))})
	if err != nil {
		t.Fatal(err)
	}
	ds := &directService{svc: svc, coord: coord}
	ds.stop = sync.OnceFunc(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := svc.Shutdown(ctx); err != nil {
			t.Error(err)
		}
		_ = coord.Close()
	})
	t.Cleanup(ds.stop)
	svc.SetCoordination(coord)
	if err := svc.SettingsChanged(t.Context()); err != nil {
		t.Fatal(err)
	}
	var host directconn.CapabilityHost
	deadline := time.Now().Add(10 * time.Second)
	for !host.Available {
		var err error
		if host, err = svc.CapabilityHost(t.Context()); err != nil {
			t.Fatal(err)
		}
		if time.Now().After(deadline) {
			t.Fatalf("the engine did not start:\n%s", logs.String())
		}
		time.Sleep(10 * time.Millisecond) //nolint:forbidigo // Real engine start has no event to wait on.
	}
	ds.host = host
	return ds
}

// directService is a running API engine with its directconn service and
// coordination. stop shuts both down; it is safe to call twice.
type directService struct {
	svc   *directconn.Service
	coord *directconn.Coordination
	host  directconn.CapabilityHost
	stop  func()
}

func newDirectSecret(t *testing.T) [32]byte {
	t.Helper()
	var secret [32]byte
	if _, err := rand.Read(secret[:]); err != nil {
		t.Fatal(err)
	}
	return secret
}

// TestDirectRegistrationAndNetmap runs the plain HTTP flow against a real API
// engine on the pinned magicsock. It opens UDP sockets, so it runs on the host.
func TestDirectRegistrationAndNetmap(t *testing.T) {
	clock := newDirectLiveClock()
	store := directconn.NewCoordinationStore(nil, "")
	ds := startDirectService(t, store, clock.Now, newDirectSecret(t))
	svc, coord, host := ds.svc, ds.coord, ds.host
	// The host entry is written by the start itself: nothing has waited for an
	// event or the 30 s tick.
	hosts, err := store.Hosts(t.Context(), clock.Now())
	if err != nil || len(hosts) != 1 || hosts[0].Label != "api" || hosts[0].NodeKey != host.NodeKey {
		t.Fatalf("hosts right after start = %+v, %v; want the api host %s", hosts, err, host.NodeKey)
	}

	e := newDirectTestEnv(t)
	e.coord = coord
	e.deps.DirectCoordination = coord
	e.clock = clock
	h := e.handler()
	key := directTestKey(1)
	enrollment := e.mustEnroll(h, directLoginToken, "tv", key)
	if rec := directPost(t, h, enrollment.Ticket, directRegistrationBody(t, key)); rec.Code != http.StatusNoContent {
		t.Fatalf("registration = %d: %s", rec.Code, rec.Body.String())
	}

	// The registration puts the device in the peer cache at once; the cache's own
	// refresh is 10 s away.
	for deadline := time.Now().Add(5 * time.Second); !svc.IsPeer(key); {
		if time.Now().After(deadline) {
			t.Fatal("the engine did not take the device as a peer after its registration")
		}
		time.Sleep(10 * time.Millisecond) //nolint:forbidigo // The peer cache loads asynchronously.
	}

	rec := directGetNetmap(t, h, enrollment.Ticket, "")
	if rec.Code != http.StatusOK || rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("netmap = %d, Cache-Control %q: %s", rec.Code, rec.Header().Get("Cache-Control"), rec.Body.String())
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
		t.Fatal(err)
	}
	if _, ok := raw["regions"]; ok {
		t.Error("the netmap carries regions")
	}
	var doc directNetmapDoc
	if err := json.Unmarshal(rec.Body.Bytes(), &doc); err != nil {
		t.Fatal(err)
	}
	if len(doc.Hosts) != 1 || doc.Hosts[0].Label != "api" || doc.Hosts[0].NodeKey != host.NodeKey.String() ||
		doc.Hosts[0].DiscoKey == "" || doc.Hosts[0].DiscoKey == (protocol.DiscoKey{}).String() {
		t.Fatalf("netmap hosts = %+v, want the api host %s with a disco key", doc.Hosts, host.NodeKey)
	}
}

const (
	directCoordinationBase = Prefix + "/direct-connections"
	directSignalPath       = directCoordinationBase + "/signal"
)

// directWait polls until cond holds or the deadline passes.
func directWait(t *testing.T, what string, within time.Duration, cond func() bool) {
	t.Helper()
	for deadline := time.Now().Add(within); !cond(); {
		if time.Now().After(deadline) {
			t.Fatalf("timed out after %v waiting for %s", within, what)
		}
		time.Sleep(20 * time.Millisecond) //nolint:forbidigo // Real sockets and engines have no event to wait on.
	}
}

// waitRegionStreams waits until the region holds the API engine's stream and the
// device's, which a device engine that is redialing its stream does not yet give.
func waitRegionStreams(t *testing.T, ds *directService) {
	t.Helper()
	directWait(t, "the API engine's and the device's region connections", 5*time.Second, func() bool {
		return ds.coord.RegionStats().Connections == 2
	})
}

// directFlow serves the real registry over TLS, as a device's engine reaches it,
// and keeps the headers of every coordination request.
type directFlow struct {
	t    *testing.T
	env  *directTestEnv
	srv  *httptest.Server
	tls  *tls.Config
	mu   sync.Mutex
	h    http.Handler
	seen []http.Header
	// signals counts the signal upgrades the handler received. The API engine's
	// stream is in-process, so each one is a device's stream being opened.
	signals int
}

func newDirectFlow(t *testing.T, env *directTestEnv) *directFlow {
	t.Helper()
	f := &directFlow{t: t, env: env, h: env.handler()}
	f.srv = httptest.NewTLSServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	pool := x509.NewCertPool()
	pool.AddCert(f.srv.Certificate())
	f.tls = &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}
	return f
}

func (f *directFlow) handler() http.Handler {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.h
}

// rebuild serves the environment's current dependencies, as a restart does.
func (f *directFlow) rebuild() {
	h := f.env.handler()
	f.mu.Lock()
	f.h = h
	f.mu.Unlock()
}

func (f *directFlow) serve(w http.ResponseWriter, r *http.Request) {
	if strings.HasPrefix(r.URL.Path, directCoordinationBase+"/") && !strings.HasSuffix(r.URL.Path, "/enrollment") {
		f.mu.Lock()
		f.seen = append(f.seen, r.Header.Clone())
		if r.URL.Path == directSignalPath {
			f.signals++
		}
		f.mu.Unlock()
	}
	f.handler().ServeHTTP(w, r)
}

func (f *directFlow) requests() []http.Header {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.seen)
}

// signalUpgrades is how many signal upgrades the handler has received.
func (f *directFlow) signalUpgrades() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.signals
}

// upgrade sends a WebSocket upgrade for the signal operation and returns the
// response; the caller closes its body. An empty protocols header offers none.
func (f *directFlow) upgrade(ticket, protocols string) *http.Response {
	f.t.Helper()
	req, err := http.NewRequestWithContext(f.t.Context(), http.MethodGet, f.srv.URL+directSignalPath, nil)
	if err != nil {
		f.t.Fatal(err)
	}
	req.Header.Set("Connection", "Upgrade")
	req.Header.Set("Upgrade", "websocket")
	req.Header.Set("Sec-WebSocket-Version", "13")
	req.Header.Set("Sec-WebSocket-Key", base64.StdEncoding.EncodeToString([]byte("0123456789abcdef")))
	if protocols != "" {
		req.Header.Set("Sec-WebSocket-Protocol", protocols)
	}
	if ticket != "" {
		req.Header.Set(directconn.SignalingTicketHeader, ticket)
	}
	resp, err := f.srv.Client().Do(req)
	if err != nil {
		f.t.Fatal(err)
	}
	return resp
}

// TestSignalUpgradeRefusals: every refusal of the signal operation happens
// before the upgrade, and a valid ticket upgrades with the derp subprotocol.
func TestSignalUpgradeRefusals(t *testing.T) {
	e := newDirectTestEnv(t)
	f := newDirectFlow(t, e)
	enrollment := e.mustEnroll(f.handler(), directLoginToken, "tv", directTestKey(1))
	status := func(ticket, protocols string) (int, http.Header) {
		resp := f.upgrade(ticket, protocols)
		_ = resp.Body.Close()
		return resp.StatusCode, resp.Header
	}
	if code, header := status(enrollment.Ticket, "derp"); code != http.StatusSwitchingProtocols || header.Get("Sec-WebSocket-Protocol") != "derp" {
		t.Fatalf("a valid ticket: status %d, subprotocol %q; want 101 selecting derp", code, header.Get("Sec-WebSocket-Protocol"))
	}
	if code, _ := status(enrollment.Ticket, "derp, other"); code != http.StatusSwitchingProtocols {
		t.Fatalf("derp among other subprotocols: status %d, want 101", code)
	}
	for name, tc := range map[string]struct {
		ticket, protocols string
		want              int
	}{
		"a valid ticket with no subprotocol":     {enrollment.Ticket, "", http.StatusBadRequest},
		"a valid ticket for another subprotocol": {enrollment.Ticket, "other", http.StatusBadRequest},
	} {
		if code, _ := status(tc.ticket, tc.protocols); code != tc.want {
			t.Errorf("%s: status %d, want %d", name, code, tc.want)
		}
	}
	// A plain GET with a valid ticket never upgrades.
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, f.srv.URL+directSignalPath, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set(directconn.SignalingTicketHeader, enrollment.Ticket)
	resp, err := f.srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode < 400 || resp.StatusCode >= 500 {
		t.Errorf("a GET that is not an upgrade: status %d, want a 4xx refusal", resp.StatusCode)
	}

	// A refusal before the upgrade is a Problem Details document, as on every
	// other operation.
	answered := map[int]bool{}
	refused := func(ticket string, want ProblemType) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(http.MethodGet, directSignalPath, nil)
		r.Header.Set("Connection", "Upgrade")
		r.Header.Set("Upgrade", "websocket")
		r.Header.Set("Sec-WebSocket-Version", "13")
		r.Header.Set("Sec-WebSocket-Key", base64.StdEncoding.EncodeToString([]byte("0123456789abcdef")))
		r.Header.Set("Sec-WebSocket-Protocol", "derp")
		if ticket != "" {
			r.Header.Set(directconn.SignalingTicketHeader, ticket)
		}
		rec := httptest.NewRecorder()
		f.handler().ServeHTTP(rec, r)
		requireProblem(t, rec, want)
		answered[rec.Code] = true
		return rec
	}
	refused("", TypeInvalidToken)
	refused("bad-ticket", TypeInvalidToken)
	e.host.host.Enabled = false
	refused(enrollment.Ticket, TypeCapabilityDisabled)
	e.host.host.Enabled, e.host.host.Available = true, false
	if got := refused(enrollment.Ticket, TypeDependencyUnavailable).Header().Get("Retry-After"); got != "5" {
		t.Errorf("a replica without the engine: Retry-After %q, want 5", got)
	}
	refused("bad-ticket", TypeInvalidToken) // The ticket is checked first.
	e.host.host.Available = true
	e.host.err = errors.New("settings read failed")
	refused(enrollment.Ticket, TypeInternalError)
	e.host.err = unreadSettingsError()
	if got := refused(enrollment.Ticket, TypeDependencyUnavailable).Header().Get("Retry-After"); got != "5" {
		t.Errorf("unread settings: Retry-After %q, want 5", got)
	}
	refused("bad-ticket", TypeInvalidToken) // The ticket is checked before the settings are.
	e.host.err = nil
	e.clock.Advance(directTestLease)
	refused(enrollment.Ticket, TypeInvalidToken)

	// The operation declares every status its gate answered.
	var declared map[string]any
	if op, ok := generatedDocument(t)["paths"].(map[string]any)[directSignalPath].(map[string]any)["get"].(map[string]any); ok {
		declared, _ = op["responses"].(map[string]any)
	}
	for code := range answered {
		if _, ok := declared[strconv.Itoa(code)]; !ok {
			t.Errorf("the signal operation answers %d before the upgrade but does not declare it; declared %v", code, slices.Sorted(maps.Keys(declared)))
		}
	}
}

// unreadSettingsError is the error the service's CapabilityHost returns on a
// replica that has not read the settings yet.
func unreadSettingsError() error {
	return fmt.Errorf("capability host: %w", directconn.ErrSettingsNotRead)
}

// TestDirectConnectionSettingsNotRead: a replica that has not read the
// direct-connection settings answers 503 dependency_unavailable with a
// Retry-After of 5 on the capability document, enrollment, registration and
// netmap; signaling has its own case in TestSignalUpgradeRefusals. Registration
// and netmap still check the ticket first, so an invalid ticket is 401.
func TestDirectConnectionSettingsNotRead(t *testing.T) {
	e, h, enrollment := directRegistrationEnv(t)
	e.host.err = unreadSettingsError()
	for name, rec := range map[string]*httptest.ResponseRecorder{
		"capability":   do(t, h, http.MethodGet, Prefix+"/capabilities/direct-connections", "", directViewer(directLoginToken)),
		"enrollment":   e.enroll(h, directLoginToken, "tv", directTestKey(1), "automatic"),
		"netmap":       directGetNetmap(t, h, enrollment.Ticket, ""),
		"registration": directPost(t, h, enrollment.Ticket, directRegistrationBody(t, directTestKey(1))),
	} {
		requireProblem(t, rec, TypeDependencyUnavailable)
		if got := rec.Header().Get("Retry-After"); got != "5" {
			t.Errorf("%s: Retry-After = %q, want 5", name, got)
		}
	}
	if directRegistered(t, e) {
		t.Fatal("a registration was stored while the settings were unread")
	}
	requireProblem(t, directPost(t, h, "bad-ticket", directRegistrationBody(t, directTestKey(1))), TypeInvalidToken)
	requireProblem(t, directGetNetmap(t, h, "bad-ticket", ""), TypeInvalidToken)
}

// directDevice is a device's engine and coordination client against the flow.
type directDevice struct {
	t      *testing.T
	f      *directFlow
	id     string
	priv   engine.NodePrivate
	key    protocol.NodeKey
	mu     sync.Mutex
	ticket string

	cancel    context.CancelFunc
	e         *engine.Engine
	runCancel context.CancelFunc
}

func (f *directFlow) newDevice(id string) *directDevice {
	f.t.Helper()
	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		f.t.Fatal(err)
	}
	d := &directDevice{t: f.t, f: f, id: id, priv: engine.NodePrivateFromBytes(raw)}
	d.key = d.priv.Public()
	return d
}

// enroll enrolls or renews the device through the real operation, as the app
// does, and keeps the ticket it returns.
func (d *directDevice) enroll() {
	d.t.Helper()
	enrollment := d.f.env.mustEnroll(d.f.handler(), directLoginToken, d.id, d.key)
	d.mu.Lock()
	d.ticket = enrollment.Ticket
	d.mu.Unlock()
}

func (d *directDevice) currentTicket() string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.ticket
}

// headers is the engine's only credential: the ticket, and nothing else.
func (d *directDevice) headers(context.Context) (http.Header, error) {
	return http.Header{directconn.SignalingTicketHeader: {d.currentTicket()}}, nil
}

// start builds the engine, which binds a new UDP port, and runs the client. The
// engine gets the STUN server a client reads from the enrollment capability.
func (d *directDevice) start() {
	d.t.Helper()
	ctx, cancel := context.WithCancel(d.t.Context())
	d.cancel = cancel
	logs := &lockedBuffer{}
	d.t.Cleanup(func() {
		if d.t.Failed() {
			d.t.Logf("device %s engine log:\n%s", d.id, logs.String())
		}
	})
	e, err := engine.New(ctx, engine.Config{PrivateKey: d.priv, StaticNetwork: true, STUNServers: []string{directTestSTUNServer}, Logger: slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug})),
		Signaling: &engine.Signaling{RegionID: 0, Dial: engine.WebSocketDialer(d.f.tls, d.headers)}})
	if err != nil {
		d.t.Fatal(err)
	}
	d.e = e
	d.startClient()
}

// startClient runs the coordination client on the engine; it registers and reads
// the first netmap before it returns.
func (d *directDevice) startClient() {
	d.t.Helper()
	ctx, cancel := context.WithCancel(d.t.Context())
	d.runCancel = cancel
	if err := coordclient.Run(ctx, coordclient.Options{BaseURL: d.f.srv.URL + directCoordinationBase, TLSConfig: d.f.tls, Headers: d.headers}, d.e); err != nil {
		d.t.Fatalf("coordination client: %v", err)
	}
}

func (d *directDevice) stopClient() { d.runCancel() }

func (d *directDevice) stop() {
	d.runCancel()
	d.cancel()
	_ = d.e.Close()
}

// ready waits for direct evidence in both directions between the device and the
// API engine.
func (d *directDevice) ready(api protocol.NodeKey, within time.Duration) {
	d.t.Helper()
	ctx, cancel := context.WithTimeout(d.t.Context(), within)
	defer cancel()
	if err := d.e.Ready(ctx, api); err != nil {
		d.t.Fatalf("device %s never reached the API engine directly: %v", d.id, err)
	}
}

// watchConfirmation holds netmap requests for the device, one after another,
// each at the version the last one returned, until a returned netmap carries the
// API host's confirmation of the device. It sends that netmap on the returned
// channel. The requests run through the real handler with the device's ticket.
func (f *directFlow) watchConfirmation(d *directDevice) <-chan protocol.Netmap {
	confirmed := make(chan protocol.Netmap, 1)
	ctx := f.t.Context()
	go func() {
		version := ""
		for ctx.Err() == nil {
			path := directNetmapPath
			if version != "" {
				path += "?version=" + version
			}
			r := httptest.NewRequestWithContext(ctx, http.MethodGet, path, nil)
			r.Header.Set(directconn.SignalingTicketHeader, d.currentTicket())
			rec := httptest.NewRecorder()
			f.handler().ServeHTTP(rec, r)
			if rec.Code != http.StatusOK {
				return
			}
			m, err := protocol.DecodeNetmap(rec.Body)
			if err != nil {
				return
			}
			version = m.Version
			for _, h := range m.Hosts {
				if h.Confirmation != nil && h.Confirmation.Peer == d.key {
					confirmed <- m
					return
				}
			}
		}
	}()
	return confirmed
}

// startFlow starts the API engine and serves the real registry over TLS.
func startFlow(t *testing.T, store directconn.CoordinationStore, clock *directTestClock, secret [32]byte, rds ...directRedis) (*directService, *directFlow) {
	t.Helper()
	ds := startDirectService(t, store, clock.Now, secret, rds...)
	e := newDirectTestEnv(t)
	e.coord, e.clock = ds.coord, clock
	e.deps.DirectCoordination = ds.coord
	return ds, newDirectFlow(t, e)
}

// TestSignalUpgradeOverWebSocket: a real device engine opens its signaling stream
// through the real registry with the engine's own WebSocket dialer, which refuses
// any 101 that does not select derp, and a discovery frame crosses the region to
// the API engine and comes back. It opens UDP sockets, so it runs on the host.
func TestSignalUpgradeOverWebSocket(t *testing.T) {
	ds, f := startFlow(t, directconn.NewCoordinationStore(nil, ""), newDirectLiveClock(), newDirectSecret(t))
	d := f.newDevice("tv")
	d.enroll()
	d.start()
	d.ready(ds.host.NodeKey, 30*time.Second)
	// The region holds two streams: the API engine's own and the device's. The
	// device engine may still be redialing its stream after a rebind when Ready
	// returns, so wait for the second stream instead of reading the count once.
	directWait(t, "the API engine's and the device's region connections", 5*time.Second, func() bool {
		st := ds.coord.RegionStats()
		if st.Connections != 2 {
			t.Logf("region stats: connections %d, closes %v, drops %v", st.Connections, st.Closes, st.Drops)
		}
		return st.Connections == 2
	})
	// The device registers its endpoints after its first netcheck, about when
	// Ready returns. The API engine then drops its confirmation of the device
	// and revalidates the path, so its view is briefly not ready; wait for it
	// instead of reading it once.
	directWait(t, "the API engine's ready direct path to the device", 5*time.Second, func() bool {
		st, ok, err := ds.svc.Device(t.Context(), 1, "tv")
		if err != nil || !ok {
			t.Fatalf("API engine's view of the device = %+v, ok %v, %v; want an enrolled device", st, ok, err)
		}
		return st.APIReady
	})
}

// TestDirectSignalingSequence runs the signaling sequence against a real API
// engine, the real registry and a real device engine.
func TestDirectSignalingSequence(t *testing.T) {
	clock := newDirectLiveClock()
	secret := newDirectSecret(t)
	store := directconn.NewCoordinationStore(nil, "")
	ds, f := startFlow(t, store, clock, secret)
	api := ds.host.NodeKey

	// The API engine's own stream is up before any device registers, and the
	// region refused none: its first dial found the host entry.
	directWait(t, "the API engine's region connection", 5*time.Second, func() bool {
		st := ds.coord.RegionStats()
		if st.Connections != 1 {
			t.Logf("region stats: connections %d, closes %v, drops %v", st.Connections, st.Closes, st.Drops)
		}
		return st.Connections == 1
	})
	if st := ds.coord.RegionStats(); len(st.Closes) != 0 || len(st.Drops) != 0 {
		t.Fatalf("region closes %v and drops %v before any device; want none", st.Closes, st.Drops)
	}

	d := f.newDevice("tv")
	d.enroll()
	// A netmap request for the device is held from before its engine starts, so
	// it is waiting when the API engine confirms the device.
	confirmed := f.watchConfirmation(d)
	d.start()
	d.ready(api, 30*time.Second)
	// registered returns the endpoints in the device's stored registration. The
	// first registration of a new engine carries none, and the next one carries
	// them once the engine has found its addresses, so it waits for them.
	registered := func() []netip.AddrPort {
		t.Helper()
		var endpoints []netip.AddrPort
		directWait(t, "the device's registration to carry endpoints", 10*time.Second, func() bool {
			device, err := store.Device(t.Context(), server.DeviceRef{Account: "1", Device: "tv"}, clock.Now())
			if err != nil || device.Registration == nil {
				t.Fatalf("store entry of the device = %+v, %v", device, err)
			}
			endpoints = device.Registration.Endpoints
			return len(endpoints) > 0
		})
		return endpoints
	}

	t.Run("the API confirmation reaches the held netmap within 5 s", func(t *testing.T) {
		requireConfirmationHeld(t, ds, confirmed)
		d.ready(api, 5*time.Second)
	})

	t.Run("a rebind to a new port reaches the API and the path recovers", func(t *testing.T) {
		before := registered()
		d.stop()
		d.start()
		d.ready(api, 30*time.Second)
		if slices.Equal(before, registered()) {
			t.Fatalf("the registration kept its endpoints %v after the device rebound", before)
		}
	})

	t.Run("ticket renewals keep the engine working", func(t *testing.T) {
		first := d.currentTicket()
		for range 2 {
			clock.Advance(time.Second)
			d.enroll()
		}
		if d.currentTicket() == first {
			t.Fatal("renewal returned the same ticket")
		}
		d.stopClient()
		d.startClient()
		d.ready(api, 30*time.Second)
		seen := f.requests()
		if last := seen[len(seen)-1]; last.Get(directconn.SignalingTicketHeader) != d.currentTicket() {
			t.Fatal("the client's request after renewal did not carry the renewed ticket")
		}
		for _, ticket := range []string{first, d.currentTicket()} {
			if _, _, err := ds.coord.Authorize(t.Context(), ticket); err != nil {
				t.Fatalf("a ticket of the same lease stopped authorizing: %v", err)
			}
		}
	})

	t.Run("a restarted service gives 401, the app enrolls again and discovery recovers", func(t *testing.T) {
		old := d.currentTicket()
		d.stopClient()
		ds.stop()
		f.srv.CloseClientConnections()
		restarted := startDirectService(t, directconn.NewCoordinationStore(nil, ""), clock.Now, secret)
		if restarted.host.NodeKey != api {
			t.Fatalf("restarted API node key = %s, want %s: the key derives from the secret", restarted.host.NodeKey, api)
		}
		f.env.coord = restarted.coord
		f.env.deps.DirectCoordination = restarted.coord
		f.rebuild()
		requireProblem(t, directPost(t, f.handler(), old, directRegistrationBody(t, d.key)), TypeInvalidToken)
		d.enroll()
		d.startClient()
		d.ready(api, 30*time.Second)
	})

	t.Run("no request carried a credential but the ticket", func(t *testing.T) { requireTicketOnly(t, f.requests()) })
}

// requireConfirmationHeld checks that the API engine's confirmation of the device
// reaches a netmap request held before the engine started, within 5 s of the API
// publishing it. The wake from the confirmation delivers it: the other ways a
// netmap refreshes are all far beyond the window. The server's hold ends at 25 s,
// the client's refresh interval is 30 s, and the API engine's host refresh tick
// is 30 s.
func requireConfirmationHeld(t *testing.T, ds *directService, confirmed <-chan protocol.Netmap) {
	t.Helper()
	directWait(t, "the API engine's confirmation of the device", 5*time.Second, func() bool {
		st, ok, err := ds.svc.Device(t.Context(), 1, "tv")
		return err == nil && ok && st.APIReady
	})
	published := time.Now()
	select {
	case m := <-confirmed:
		if time.Duration(m.RefreshSeconds)*time.Second <= 5*time.Second {
			t.Fatalf("the netmap's refresh interval is %d s: a refresh could deliver inside the window", m.RefreshSeconds)
		}
		if got := time.Since(published); got > 5*time.Second {
			t.Fatalf("the confirmation reached the held netmap %v after the API published it, want within 5 s", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the held netmap did not carry the API's confirmation within 5 s of the API publishing it")
	}
}

// requireTicketOnly fails for a recorded request that has no ticket or carries
// another credential.
func requireTicketOnly(t *testing.T, seen []http.Header) {
	t.Helper()
	if len(seen) == 0 {
		t.Fatal("no coordination request was recorded")
	}
	for i, header := range seen {
		if header.Get(directconn.SignalingTicketHeader) == "" {
			t.Errorf("request %d carried no ticket", i)
		}
		for _, name := range []string{"Authorization", "Cookie", "X-Profile-Id", "X-Profile-Token"} {
			if header.Get(name) != "" {
				t.Errorf("request %d carried %s", i, name)
			}
		}
	}
}

// directRevocationEvents are the events that end an entry, each driven through
// the operation the handler calls: sign-out and session deletion call
// RevokeSession for the login session "s1" that enrolled the device, device
// removal runs the real handler, and disabling an account ends all its
// sessions, which revokes the account.
func directRevocationEvents() []struct {
	name string
	run  func(t *testing.T, e *directTestEnv, h http.Handler)
} {
	return []struct {
		name string
		run  func(t *testing.T, e *directTestEnv, h http.Handler)
	}{
		{"v2 logout", func(t *testing.T, e *directTestEnv, _ http.Handler) { e.coord.RevokeSession(t.Context(), 1, "s1") }},
		{"v2 deleteSession", func(t *testing.T, e *directTestEnv, _ http.Handler) { e.coord.RevokeSession(t.Context(), 1, "s1") }},
		{"v2 device removal", func(t *testing.T, _ *directTestEnv, h http.Handler) {
			if rec := do(t, h, http.MethodDelete, Prefix+"/devices/tv", "", directViewer(directLoginToken)); rec.Code != http.StatusNoContent {
				t.Fatalf("forgetDevice = %d: %s", rec.Code, rec.Body.String())
			}
		}},
		{"admin account disable", func(t *testing.T, e *directTestEnv, _ http.Handler) { e.coord.RevokeAccount(t.Context(), 1) }},
	}
}

// TestDirectSignalingRevocation: after each revocation event, on a device that
// holds a region stream and on one the peer cache never held, the device's
// registration and netmap get 401 invalid_token, its signal upgrade gets 401
// before any upgrade, and a held stream closes.
func TestDirectSignalingRevocation(t *testing.T) {
	for _, event := range directRevocationEvents() {
		for _, held := range []bool{true, false} {
			name := event.name + "/absent from the cache"
			if held {
				name = event.name + "/holds a region stream"
			}
			t.Run(name, func(t *testing.T) {
				e := newDirectTestEnv(t)
				e.deps.DeviceSettings = newDirectDeviceHandler(t, e.coord)
				f := newDirectFlow(t, e)
				h := f.handler()
				priv := key.NewNode()
				var nodeKey protocol.NodeKey
				priv.Public().AppendTo(nodeKey[:0])
				enrollment := e.mustEnroll(h, directLoginToken, "tv", nodeKey)
				var closed <-chan error
				if held {
					e.register("tv", nodeKey)
					closed = f.holdStream(priv, enrollment.Ticket)
				}

				event.run(t, e, h)

				requireProblem(t, directPost(t, h, enrollment.Ticket, directRegistrationBody(t, nodeKey)), TypeInvalidToken)
				requireProblem(t, directGetNetmap(t, h, enrollment.Ticket, ""), TypeInvalidToken)
				resp := f.upgrade(enrollment.Ticket, "derp")
				_ = resp.Body.Close()
				if resp.StatusCode != http.StatusUnauthorized {
					t.Fatalf("signal upgrade after the event = %d, want 401 before any upgrade", resp.StatusCode)
				}
				if held {
					select {
					case <-closed:
					case <-time.After(5 * time.Second):
						t.Fatal("the held region stream is still open after the event")
					}
				}
			})
		}
	}
}

// holdStream opens a signaling stream for the device and returns a channel that
// receives the error that ends it. It returns once the region has admitted the
// stream.
func (f *directFlow) holdStream(priv key.NodePrivate, ticket string) <-chan error {
	f.t.Helper()
	headers := func(context.Context) (http.Header, error) {
		return http.Header{directconn.SignalingTicketHeader: {ticket}}, nil
	}
	conn, err := engine.WebSocketDialer(f.tls, headers)(f.t.Context(), f.srv.URL+directSignalPath)
	if err != nil {
		f.t.Fatalf("signal upgrade: %v", err)
	}
	f.t.Cleanup(func() { _ = conn.Close() })
	client, err := derp.NewClient(priv, conn, bufio.NewReadWriter(bufio.NewReader(conn), bufio.NewWriter(conn)), f.t.Logf)
	if err != nil {
		f.t.Fatalf("derp handshake: %v", err)
	}
	if _, err := client.Recv(); err != nil {
		f.t.Fatalf("derp server info: %v", err)
	}
	closed := make(chan error, 1)
	go func() {
		for {
			if _, err := client.Recv(); err != nil {
				closed <- err
				return
			}
		}
	}()
	// A ping the region answers shows it admitted the stream.
	if err := client.SendPing([8]byte{1}); err != nil {
		f.t.Fatal(err)
	}
	directWait(f.t, "the region to admit the stream", 5*time.Second, func() bool { return f.env.coord.RegionStats().Connections == 1 })
	return closed
}

// TestEnvelopeTenMinutes (lab row L8) runs the real router, the real API engine
// and one device engine through the coordination client for 10 minutes on the
// real clock. The lease is renewed at half its length, which rotates the ticket
// once inside the run, and the device rebinds after the rotation. The device's
// region stream may close at the first ticket's expiry and reconnect with the
// renewed ticket; it never drops frames, and discovery to the API works again
// after the rotation and after the rebind. Both engines are given a STUN server,
// so neither rebinds on its own; the device's stream is opened once before the
// rotation. The test waits for both streams before its first check and after its
// own rebind, and it asserts that the region never drops a frame and never closes
// a stream. It skips unless
// SILO_DIRECT_TEST_ENVELOPE_RUN=1 and is not part of CI.
func TestEnvelopeTenMinutes(t *testing.T) {
	if os.Getenv("SILO_DIRECT_TEST_ENVELOPE_RUN") != "1" {
		t.Skip("set SILO_DIRECT_TEST_ENVELOPE_RUN=1 to run the 10-minute envelope test")
	}
	const run, rotateAt = 10 * time.Minute, 5 * time.Minute
	start := time.Now()
	ds := startDirectService(t, directconn.NewCoordinationStore(nil, ""), time.Now, newDirectSecret(t))
	e := newDirectTestEnv(t)
	e.coord = ds.coord
	e.deps.DirectCoordination = ds.coord
	f := newDirectFlow(t, e)
	api := ds.host.NodeKey
	d := f.newDevice("tv")
	d.enroll()
	first := d.currentTicket()
	d.start()
	d.ready(api, 30*time.Second)
	waitRegionStreams(t, ds)

	rotated, rebound := false, false
	for tick := time.NewTicker(5 * time.Second); time.Since(start) < run; <-tick.C {
		elapsed := time.Since(start)
		st := ds.coord.RegionStats()
		var drops uint64
		for _, n := range st.Drops {
			drops += n
		}
		if drops != 0 {
			t.Fatalf("region drops %v after %v, want none", st.Drops, elapsed)
		}
		// Until the test's own rebind, the device's stream is opened once and never
		// reconnects: a second upgrade means the engine closed it.
		if !rotated {
			if n := f.signalUpgrades(); n != 1 {
				t.Fatalf("after %v: the device opened %d signal streams before the rotation, want 1", elapsed, n)
			}
		}
		if !rotated && elapsed >= rotateAt {
			d.enroll() // the renewal at half the lease
			if d.currentTicket() == first {
				t.Fatal("the renewal returned the first ticket")
			}
			rotated = true
			d.ready(api, 5*time.Second)
			if path := d.e.Path(api); path.Class != "direct" {
				t.Fatalf("path after the rotation = %q, want direct", path.Class)
			}
			// The engine's exported API cannot move its port, so this is a rebind
			// that resets mappings and confirmations on the current port.
			d.e.Rebind()
			rebound = true
			d.ready(api, 5*time.Second)
			if path := d.e.Path(api); path.Class != "direct" {
				t.Fatalf("path after the rebind = %q, want direct", path.Class)
			}
			// The rebind may close the device's stream. Its redial is the test's own
			// doing, so wait for it, then count again.
			waitRegionStreams(t, ds)
			st = ds.coord.RegionStats()
		}
		// Before the first ticket's expiry nothing closes the stream: the API
		// engine's and the device's are both held.
		if elapsed < enrollmentLeaseForTest-5*time.Second {
			if st.Connections != 2 || len(st.Closes) != 0 {
				t.Fatalf("after %v: connections %d, closes %v; want the two held streams and no close", elapsed, st.Connections, st.Closes)
			}
		}
	}
	if !rotated || !rebound {
		t.Fatal("the run ended before the rotation and the rebind")
	}
	d.ready(api, 5*time.Second)
}

// enrollmentLeaseForTest is the enrollment lease, which is also how long the first
// ticket lives.
const enrollmentLeaseForTest = 10 * time.Minute

// lockedBuffer collects log lines from goroutines that can outlive the test, which
// a test's own log cannot take once the test ends.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p) //nolint:wrapcheck // A bytes.Buffer write never fails.
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}
