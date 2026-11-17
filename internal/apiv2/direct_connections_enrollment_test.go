package apiv2

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/darthShadow/shunpike/coordination/protocol"
	"github.com/darthShadow/shunpike/coordination/server"

	"github.com/Silo-Server/silo-server/internal/directconn"
)

const (
	directLoginToken      = "tok-direct-login"       // account 1, a bounded login session
	directLoginOtherToken = "tok-direct-login-other" // account 3, a bounded login session
	directViewAsToken     = "tok-direct-view-as"     // account 1 viewed by admin 3, a bounded View as user session

	directTestSecret = "synthetic-direct-api-test-secret"
	directTestPin    = "dGVzdC1waW4tbm90LWEtcmVhbC1waW4="
	// directTestLease is longer than one enrollment lease.
	directTestLease = 11 * time.Minute
)

// directTestClock is the clock shared by the store, the coordination server and
// the ticket checks.
type directTestClock struct {
	mu  sync.Mutex
	now time.Time
	// live makes the clock the wall clock plus skew instead of a frozen time, for
	// tests whose real engines read the wall clock.
	live bool
	skew time.Duration
}

// newDirectLiveClock follows the wall clock; Advance adds to it.
func newDirectLiveClock() *directTestClock { return &directTestClock{live: true} }

func newDirectTestClock() *directTestClock {
	return &directTestClock{now: time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)}
}

func (c *directTestClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.live {
		return time.Now().Add(c.skew)
	}
	return c.now
}

func (c *directTestClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.live {
		c.skew += d
		return
	}
	c.now = c.now.Add(d)
}

// directTestHost is the API host the capability document reads.
// directTestHost serves host, or err when it is set, as a failed settings read.
type directTestHost struct {
	host directconn.CapabilityHost
	err  error
}

func (h *directTestHost) CapabilityHost(context.Context) (directconn.CapabilityHost, error) {
	return h.host, h.err
}

// directFlakyStore fails every call once down is set, as a Redis outage does.
type directFlakyStore struct {
	directconn.CoordinationStore
	mu    sync.Mutex
	down  bool
	cause error
}

// setDownCause fails every call as setDown does, with err wrapped the way the
// Redis adapter wraps the error it got.
func (s *directFlakyStore) setDownCause(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.down, s.cause = true, err
}

// outage is the error of a call made while the store is down.
func (s *directFlakyStore) outage() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cause != nil {
		return fmt.Errorf("%w: store: %w", server.ErrUnavailable, s.cause)
	}
	return server.ErrUnavailable
}

func (s *directFlakyStore) setDown(down bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.down = down
}

func (s *directFlakyStore) isDown() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.down
}

func (s *directFlakyStore) PutDevice(ctx context.Context, d server.Device, maxPerAccount int, now time.Time) (bool, error) {
	if s.isDown() {
		return false, s.outage()
	}
	return s.CoordinationStore.PutDevice(ctx, d, maxPerAccount, now)
}

func (s *directFlakyStore) Device(ctx context.Context, ref server.DeviceRef, now time.Time) (server.Device, error) {
	if s.isDown() {
		return server.Device{}, s.outage()
	}
	return s.CoordinationStore.Device(ctx, ref, now)
}

func (s *directFlakyStore) ListDevices(ctx context.Context, account server.AccountID, now time.Time) ([]server.Device, error) {
	if s.isDown() {
		return nil, s.outage()
	}
	return s.CoordinationStore.ListDevices(ctx, account, now)
}

func (s *directFlakyStore) DeleteDevice(ctx context.Context, ref server.DeviceRef) error {
	if s.isDown() {
		return s.outage()
	}
	return s.CoordinationStore.DeleteDevice(ctx, ref)
}

// directTestKey is a node key with a distinct first byte.
func directTestKey(b int) protocol.NodeKey {
	var k protocol.NodeKey
	k[0], k[1], k[31] = byte(b), byte(b>>8), 1
	return k
}

type directTestEnv struct {
	t     *testing.T
	deps  Dependencies
	coord *directconn.Coordination
	store *directFlakyStore
	clock *directTestClock
	host  *directTestHost
	logs  *bytes.Buffer
}

// newDirectTestEnv wires a real coordination service over the memory store,
// with knob values that differ from the defaults.
func newDirectTestEnv(t *testing.T) *directTestEnv {
	t.Helper()
	e := &directTestEnv{t: t, clock: newDirectTestClock(), logs: &bytes.Buffer{},
		store: &directFlakyStore{CoordinationStore: directconn.NewCoordinationStore(nil, "")},
		host: &directTestHost{host: directconn.CapabilityHost{Enabled: true, Available: true, NodeKey: directTestKey(250),
			STUNServers: []string{"stun.example.test:3478"}}}}
	coord, err := directconn.NewCoordination(directconn.CoordinationConfig{
		Store: e.store, AuthSecret: directTestSecret, Clock: e.clock.Now, Logger: slog.New(slog.NewTextHandler(e.logs, nil)),
		Host: e.host, TLSPin: directTestPin, ACKDecimation: false, ReceiveWindowMiB: 32,
	})
	if err != nil {
		t.Fatal(err)
	}
	e.coord = coord
	e.deps = pilotDeps(nil, nil)
	e.deps.DirectCoordination = coord
	return e
}

func (e *directTestEnv) handler() http.Handler { return newTestHandler(e.t, e.deps) }

func directViewer(token string) map[string]string {
	return with(bearer(token), "X-Profile-Id", "p-owner")
}

func (e *directTestEnv) enroll(h http.Handler, token, device string, key protocol.NodeKey, path string) *httptest.ResponseRecorder {
	e.t.Helper()
	headers := with(directViewer(token), "X-Silo-Device-Id", device)
	return do(e.t, h, http.MethodPost, Prefix+"/direct-connections/enrollment", `{"node_key":"`+key.String()+`","connection_path":"`+path+`"}`, headers)
}

type directEnrollmentBody struct {
	Ticket          string    `json:"ticket"`
	ExpiresAt       time.Time `json:"expires_at"`
	CoordinationURL string    `json:"coordination_url"`
}

func (e *directTestEnv) mustEnroll(h http.Handler, token, device string, key protocol.NodeKey) directEnrollmentBody {
	e.t.Helper()
	rec := e.enroll(h, token, device, key, "automatic")
	if rec.Code != http.StatusOK {
		e.t.Fatalf("enroll %s = %d: %s", device, rec.Code, rec.Body.String())
	}
	var body directEnrollmentBody
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		e.t.Fatal(err)
	}
	return body
}

func TestDirectConnectionEnrollment(t *testing.T) {
	t.Run("returns a ticket that expires at the lease end", func(t *testing.T) {
		e := newDirectTestEnv(t)
		body := e.mustEnroll(e.handler(), directLoginToken, "tv", directTestKey(1))
		if want := e.clock.Now().Add(10 * time.Minute); !body.ExpiresAt.Equal(want) {
			t.Fatalf("expires_at = %v, want the lease end %v", body.ExpiresAt, want)
		}
		if body.CoordinationURL != Prefix+"/direct-connections" {
			t.Fatalf("coordination_url = %q", body.CoordinationURL)
		}
		principal, _, err := e.coord.Authorize(t.Context(), body.Ticket)
		if err != nil || principal.NodeKey != directTestKey(1) || principal.Ref.Device != "tv" || principal.Ref.Account != "1" {
			t.Fatalf("Authorize(ticket) = %+v, %v", principal, err)
		}
		if got := principal.Expires.Unix(); got != body.ExpiresAt.Unix() {
			t.Fatalf("ticket expires at %d, lease ends at %d", got, body.ExpiresAt.Unix())
		}
	})

	t.Run("refusals", func(t *testing.T) {
		e := newDirectTestEnv(t)
		h := e.handler()
		// An API key is not a bounded login session.
		rec := e.enroll(h, apiKeyToken, "tv", directTestKey(1), "automatic")
		requireProblem(t, rec, TypePermissionDenied)
		// The toggle is off.
		e.host.host.Enabled = false
		requireProblem(t, e.enroll(h, directLoginToken, "tv", directTestKey(1), "automatic"), TypeCapabilityDisabled)
		e.host.host.Enabled = true
		// A malformed node key and an unknown path.
		headers := with(directViewer(directLoginToken), "X-Silo-Device-Id", "tv")
		requireProblem(t, do(t, h, http.MethodPost, Prefix+"/direct-connections/enrollment", `{"node_key":"nodekey:zz","connection_path":"automatic"}`, headers), TypeValidationFailed)
		requireProblem(t, do(t, h, http.MethodPost, Prefix+"/direct-connections/enrollment", `{"node_key":"`+directTestKey(1).String()+`","connection_path":"relay"}`, headers), TypeValidationFailed)
		zero := protocol.NodeKey{}
		requireProblem(t, e.enroll(h, directLoginToken, "tv", zero, "automatic"), TypeValidationFailed)
		// No refusal left an entry behind.
		if devices, err := e.store.ListDevices(t.Context(), "1", e.clock.Now()); err != nil || len(devices) != 0 {
			t.Fatalf("entries after refusals = %v, %v", devices, err)
		}
	})

	t.Run("device cap and node key conflict", func(t *testing.T) {
		e := newDirectTestEnv(t)
		h := e.handler()
		for i := range 32 {
			e.mustEnroll(h, directLoginToken, "d-"+strconv.Itoa(i), directTestKey(i+1))
		}
		rec := e.enroll(h, directLoginToken, "d-32", directTestKey(100), "automatic")
		requireProblem(t, rec, TypeDirectDeviceLimit)
		if !bytes.Contains(e.logs.Bytes(), []byte("direct connections: device limit reached")) {
			t.Fatalf("the limit was not logged:\n%s", e.logs.String())
		}
		// A renewal of a held device is not a new device.
		e.mustEnroll(h, directLoginToken, "d-0", directTestKey(1))
		// A lapsed lease frees every slot.
		e.clock.Advance(directTestLease)
		e.mustEnroll(h, directLoginToken, "d-32", directTestKey(100))
		// Another account cannot enroll a node key a live entry holds.
		requireProblem(t, e.enroll(h, directLoginOtherToken, "other", directTestKey(100), "automatic"), TypeDirectNodeKeyConflict)
	})

	t.Run("new node key replaces the entry and withdraw ends it", func(t *testing.T) {
		e := newDirectTestEnv(t)
		h := e.handler()
		old := e.mustEnroll(h, directLoginToken, "tv", directTestKey(1))
		fresh := e.mustEnroll(h, directLoginToken, "tv", directTestKey(2))
		if _, _, err := e.coord.Authorize(t.Context(), old.Ticket); !errors.Is(err, directconn.ErrInvalidTicket) {
			t.Fatalf("old ticket after re-enrollment = %v, want ErrInvalidTicket", err)
		}
		if _, _, err := e.coord.Authorize(t.Context(), fresh.Ticket); err != nil {
			t.Fatalf("new ticket = %v", err)
		}
		if !bytes.Contains(e.logs.Bytes(), []byte("direct connections: enrollment replaced")) {
			t.Fatalf("the replacement was not logged:\n%s", e.logs.String())
		}
		headers := with(directViewer(directLoginToken), "X-Silo-Device-Id", "tv")
		if rec := do(t, h, http.MethodDelete, Prefix+"/direct-connections/enrollment", "", headers); rec.Code != http.StatusNoContent {
			t.Fatalf("withdraw = %d: %s", rec.Code, rec.Body.String())
		}
		if _, _, err := e.coord.Authorize(t.Context(), fresh.Ticket); !errors.Is(err, directconn.ErrInvalidTicket) {
			t.Fatalf("ticket after withdraw = %v, want ErrInvalidTicket", err)
		}
		// Withdrawing again is not an error.
		if rec := do(t, h, http.MethodDelete, Prefix+"/direct-connections/enrollment", "", headers); rec.Code != http.StatusNoContent {
			t.Fatalf("second withdraw = %d", rec.Code)
		}
	})

	t.Run("store outage is a retryable 503", func(t *testing.T) {
		e := newDirectTestEnv(t)
		h := e.handler()
		e.store.setDown(true)
		rec := e.enroll(h, directLoginToken, "tv", directTestKey(1), "automatic")
		requireProblem(t, rec, TypeDependencyUnavailable)
		if got := rec.Header().Get("Retry-After"); got != "5" {
			t.Fatalf("Retry-After = %q, want 5", got)
		}
		headers := with(directViewer(directLoginToken), "X-Silo-Device-Id", "tv")
		rec = do(t, h, http.MethodDelete, Prefix+"/direct-connections/enrollment", "", headers)
		requireProblem(t, rec, TypeDependencyUnavailable)
	})
}

// A View as user session can neither enroll a device nor renew one: the refusal
// writes no entry, issues no ticket and records no audit row, and the entry the
// viewed account's own session made stays as it was. The capability reads as not
// allowed for it. The viewed account's own session and the administrator's own
// session still enroll.
func TestDirectConnectionEnrollmentRefusesViewAsUser(t *testing.T) {
	e := newDirectTestEnv(t)
	var audited []string
	coord, err := directconn.NewCoordination(directconn.CoordinationConfig{
		Store: e.store, AuthSecret: directTestSecret, Clock: e.clock.Now, Logger: slog.New(slog.NewTextHandler(e.logs, nil)),
		Host: e.host, TLSPin: directTestPin,
		Audit: func(_ context.Context, device string, _ protocol.NodeKey) error {
			audited = append(audited, device)
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	e.coord, e.deps.DirectCoordination = coord, coord
	h := e.handler()
	ctx := t.Context()

	own := e.mustEnroll(h, directLoginToken, "tv", directTestKey(1))
	ref := server.DeviceRef{Account: "1", Device: "tv"}
	before, err := e.store.Device(ctx, ref, e.clock.Now())
	if err != nil {
		t.Fatal(err)
	}
	// A renewal that went through would move the lease end.
	e.clock.Advance(time.Minute)

	for _, attempt := range []struct {
		name, device string
		key          protocol.NodeKey
	}{
		{"a first enrollment", "viewing-tablet", directTestKey(2)},
		{"a renewal of the viewed account's device", "tv", directTestKey(1)},
	} {
		rec := e.enroll(h, directViewAsToken, attempt.device, attempt.key, "automatic")
		if rec.Code != TypePermissionDenied.Status {
			t.Fatalf("%s by a View as user = %d, want %d: %s", attempt.name, rec.Code, TypePermissionDenied.Status, rec.Body.String())
		}
		requireProblem(t, rec, TypePermissionDenied)
	}
	devices, err := e.store.ListDevices(ctx, "1", e.clock.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(devices) != 1 || devices[0].Ref != ref || devices[0].NodeKey != before.NodeKey ||
		!bytes.Equal(devices[0].Policy, before.Policy) || !devices[0].LeaseExpires.Equal(before.LeaseExpires) {
		t.Fatalf("entries after the refusals = %+v, want only the own session's tv entry %+v", devices, before)
	}
	if _, _, err := e.coord.Authorize(ctx, own.Ticket); err != nil {
		t.Fatalf("the own session's ticket after the refusals: %v, want valid", err)
	}
	if got := e.capability(directViewAsToken); got.Allowed {
		t.Fatalf("capability for a View as user: allowed = true, want false")
	}

	e.mustEnroll(h, directLoginOtherToken, "admin-tv", directTestKey(3))
	if want := []string{"tv", "admin-tv"}; fmt.Sprint(audited) != fmt.Sprint(want) {
		t.Fatalf("audited devices = %v, want %v", audited, want)
	}
}

// directDrainingCoordination is the coordination service of a server that has
// begun to shut down: it refuses enrollment as the real one does.
type directDrainingCoordination struct{ DirectCoordinationService }

func (directDrainingCoordination) Enroll(context.Context, directconn.EnrollRequest) (directconn.Enrollment, error) {
	return directconn.Enrollment{}, directconn.ErrDraining
}

// A server that has begun to shut down answers enrollment with a retryable 503.
func TestDirectConnectionEnrollmentDraining(t *testing.T) {
	e := newDirectTestEnv(t)
	e.deps.DirectCoordination = directDrainingCoordination{e.coord}
	rec := e.enroll(e.handler(), directLoginToken, "tv", directTestKey(1), "automatic")
	requireProblem(t, rec, TypeDependencyUnavailable)
	if got := rec.Header().Get("Retry-After"); got != "5" {
		t.Fatalf("Retry-After = %q, want 5", got)
	}
}

type directCapabilityBody struct {
	Revision         string   `json:"revision"`
	State            string   `json:"state"`
	Allowed          bool     `json:"allowed"`
	ProtocolRevision string   `json:"protocol_revision"`
	CoordinationURL  string   `json:"coordination_url"`
	TLSPin           string   `json:"tls_pin"`
	STUNServers      []string `json:"stun_servers"`
	ACKDecimation    *bool    `json:"ack_decimation"`
	ReceiveWindowMiB *int     `json:"receive_window_mib"`
	Hosts            []struct {
		HostID  string `json:"host_id"`
		NodeKey string `json:"node_key"`
		State   string `json:"state"`
	} `json:"hosts"`
}

func (e *directTestEnv) capability(token string) directCapabilityBody {
	e.t.Helper()
	rec := do(e.t, e.handler(), http.MethodGet, Prefix+"/capabilities/direct-connections", "", directViewer(token))
	if rec.Code != http.StatusOK {
		e.t.Fatalf("capability = %d: %s", rec.Code, rec.Body.String())
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
		e.t.Fatal(err)
	}
	if string(raw["hosts"]) == "null" || string(raw["stun_servers"]) == "null" {
		e.t.Fatalf("hosts or stun_servers is null: %s", rec.Body.String())
	}
	var body directCapabilityBody
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		e.t.Fatal(err)
	}
	return body
}

// A host whose engine is down reads as unavailable, and a server with no
// coordination service reads as not configured. The capability fixtures pin the
// published values.
func TestDirectConnectionsCapability(t *testing.T) {
	e := newDirectTestEnv(t)

	e.host.host.Available = false
	if got := e.capability(directLoginToken); len(got.Hosts) != 1 || got.Hosts[0].State != "unavailable" {
		t.Fatalf("hosts with the engine down = %+v", got.Hosts)
	}

	e.deps.DirectCoordination = nil
	rec := do(t, e.handler(), http.MethodGet, Prefix+"/capabilities/direct-connections", "", directViewer(directLoginToken))
	var unwired directCapabilityBody
	if err := json.Unmarshal(rec.Body.Bytes(), &unwired); err != nil || rec.Code != http.StatusOK || unwired.State != StateNotConfigured || unwired.Allowed {
		t.Fatalf("unwired = %d %+v %v", rec.Code, unwired, err)
	}
	var unwiredRaw map[string]json.RawMessage
	_ = json.Unmarshal(rec.Body.Bytes(), &unwiredRaw)
	if string(unwiredRaw["hosts"]) != "[]" {
		t.Fatalf("unwired hosts = %s", unwiredRaw["hosts"])
	}
}
