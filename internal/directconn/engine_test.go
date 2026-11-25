package directconn

import (
	"bytes"
	"context"
	"crypto/ecdh"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"reflect"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/darthShadow/shunpike/coordination/protocol"
	"github.com/darthShadow/shunpike/engine"
	"github.com/darthShadow/shunpike/engine/n4"
	"github.com/redis/go-redis/v9"

	"github.com/Silo-Server/silo-server/internal/cache"
	"github.com/Silo-Server/silo-server/internal/tuning"
)

type lifecycleEngine struct {
	port         uint16
	changed      chan struct{}
	peers        []engine.Peer
	closes       atomic.Int32
	events       *[]string
	eventsMu     *sync.Mutex
	id           int
	closeStarted chan struct{}
	closeRelease <-chan struct{}
}

func (e *lifecycleEngine) LocalPort() uint16          { return e.port }
func (e *lifecycleEngine) Changed() <-chan struct{}   { return e.changed }
func (e *lifecycleEngine) PacketConn() net.PacketConn { return nil }
func (e *lifecycleEngine) SetPeers(peers []engine.Peer) error {
	e.eventsMu.Lock()
	defer e.eventsMu.Unlock()
	e.peers = append([]engine.Peer(nil), peers...)
	*e.events = append(*e.events, fmt.Sprintf("peers %d", e.id))
	return nil
}
func (e *lifecycleEngine) Close() error {
	if e.closeStarted != nil {
		close(e.closeStarted)
		<-e.closeRelease
	}
	e.eventsMu.Lock()
	defer e.eventsMu.Unlock()
	e.closes.Add(1)
	*e.events = append(*e.events, fmt.Sprintf("close %d", e.id))
	return nil
}

// lifecycleServer's Serve blocks until Shutdown, as http3.Server.Serve does; a
// Serve that returned on its own would be an unexpected exit.
type lifecycleServer struct {
	engine   *lifecycleEngine
	events   *[]string
	eventsMu *sync.Mutex
	stopped  chan struct{}
	stopOnce sync.Once
}

func (s *lifecycleServer) Serve(net.PacketConn) error {
	s.eventsMu.Lock()
	*s.events = append(*s.events, fmt.Sprintf("serve %d", s.engine.id))
	s.eventsMu.Unlock()
	<-s.stopped
	return http.ErrServerClosed
}
func (s *lifecycleServer) Shutdown(context.Context) error {
	s.eventsMu.Lock()
	defer s.eventsMu.Unlock()
	*s.events = append(*s.events, fmt.Sprintf("shutdown %d", s.engine.id))
	s.stopOnce.Do(func() { close(s.stopped) })
	return nil
}

type lifecycleFixture struct {
	t           *testing.T
	name        string
	cancel      context.CancelFunc
	store       *memorySettings
	cfg         Config
	svc         *Service
	logs        bytes.Buffer
	engines     []*lifecycleEngine
	events      []string
	eventsMu    sync.Mutex
	calls       int
	serverPeers [][]engine.Peer
	renewals    int
	start       time.Time
	blocked     chan struct{}
	published   chan struct{}
	deadline    <-chan time.Time
	// leased is set when the fixture installs a Redis lease; its first grant
	// waits engineLeaseTTL before an engine starts. From failFrom after start,
	// renewals fail or block as the name says. leaseLost makes the key belong to
	// another process.
	leased    bool
	failFrom  time.Duration
	leaseLost atomic.Bool
}

type lifecycleLogWriter struct {
	buffer    *bytes.Buffer
	published chan struct{}
}

func (w *lifecycleLogWriter) Write(p []byte) (int, error) {
	n, err := w.buffer.Write(p)
	if bytes.Contains(p, []byte("feature=direct")) && bytes.Contains(p, []byte("reason=multiple_api_replicas")) {
		select {
		case w.published <- struct{}{}:
		default:
		}
	}
	return n, err
}

func newLifecycleFixture(t *testing.T, name string) *lifecycleFixture {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	f := &lifecycleFixture{t: t, name: name, cancel: cancel, start: time.Now(), blocked: make(chan struct{})}
	f.store = &memorySettings{values: map[string]string{"direct_connections.enabled": "true"}}
	if name == "disabled" {
		f.store.set("direct_connections.enabled", "false")
	}
	f.cfg = testConfig(f.store)
	f.cfg.SecretKey = bytes.Repeat([]byte{0x41}, 32)
	f.cfg.Tuning, _ = tuning.Parse(func(string) string { return "" })
	if name == "invalid port" {
		f.cfg.Tuning.DirectUDPPort = 0
	}
	f.cfg.Logger = slog.New(slog.NewTextHandler(&f.logs, nil))
	if name == "blocked close" {
		f.published = make(chan struct{}, 1)
		f.cfg.Logger = slog.New(slog.NewTextHandler(&lifecycleLogWriter{buffer: &f.logs, published: f.published}, nil))
	}
	f.cfg.newEngine = f.newEngine
	f.cfg.newServer = f.newServer
	// These cases sleep past settingsRecheck; a recheck tick at the same fake
	// instant as a renewal or an assertion would race it.
	f.cfg.recheckInterval = -1
	if strings.HasPrefix(name, "renewal") || name == "lease loss" || name == "blocked close" {
		f.failFrom = engineLeaseTTL + censusInterval
		f.installLease()
	}
	f.svc = New(ctx, f.cfg)
	return f
}

func (f *lifecycleFixture) newEngine(_ context.Context, c engine.Config) (engineHandle, error) {
	f.calls++
	f.eventsMu.Lock()
	f.events = append(f.events, fmt.Sprintf("new %d", f.calls))
	f.eventsMu.Unlock()
	if int(c.Port) != f.cfg.Tuning.DirectUDPPort {
		f.t.Errorf("requested port = %d, want %d", c.Port, f.cfg.Tuning.DirectUDPPort)
	}
	if f.name == "bind failure" || (f.name == "drift failure" && f.calls == 2) {
		return nil, fmt.Errorf("bind: %w", engine.ErrPortUnavailable)
	}
	e := &lifecycleEngine{port: c.Port, changed: make(chan struct{}, 1), events: &f.events, eventsMu: &f.eventsMu, id: f.calls}
	f.engines = append(f.engines, e)
	return e, nil
}

func (f *lifecycleFixture) newServer(e engineHandle, p n4.Policy) engineServer {
	fake, ok := e.(*lifecycleEngine)
	if !ok {
		f.t.Fatalf("engine type %T", e)
	}
	f.serverPeers = append(f.serverPeers, append([]engine.Peer(nil), fake.peers...))
	f.eventsMu.Lock()
	f.events = append(f.events, fmt.Sprintf("server %d", fake.id))
	f.eventsMu.Unlock()
	if p.StreamWindow != 0 || p.InitialStreamWindow != 0 || p.ACKDecimation {
		f.t.Errorf("server policy widens measured arm: %+v", p)
	}
	return &lifecycleServer{engine: fake, events: &f.events, eventsMu: &f.eventsMu, stopped: make(chan struct{})}
}

func (f *lifecycleFixture) installLease() {
	client := redis.NewClient(&redis.Options{Addr: "127.0.0.1:6379"})
	client.AddHook(leaseCommandHook{process: f.leaseCommand})
	f.cfg.RedisClient = client
	f.cfg.ReplicaPresence = cache.NewAPIReplicaPresence(client, "api")
	f.leased = true
}

func (f *lifecycleFixture) leaseCommand(cmd redis.Cmder) error {
	c, ok := cmd.(*redis.Cmd)
	if !ok {
		return nil
	}
	switch classifyLease(c) {
	case leaseAcquire:
		if f.leaseLost.Load() {
			c.SetVal(int64(0))
			return nil
		}
	case leaseRenew:
		f.renewals++
		if f.leaseLost.Load() {
			c.SetVal(int64(0))
			return nil
		}
		if time.Since(f.start) >= f.failFrom {
			if f.name == "renewal blocked past deadline" {
				<-f.blocked
				return errors.New("renewal blocked")
			}
			// "renewal recovers at sixty" answers one renewal, exactly sixty seconds
			// after the last renewal that worked, which is the self-stop instant.
			if f.name != "renewal recovers at sixty" || time.Since(f.start) != f.failFrom+30*time.Second {
				return errors.New("renewal unavailable")
			}
		}
	}
	c.SetVal(int64(1))
	return nil
}

// advance moves the fake clock forward and waits for the service to settle.
func (f *lifecycleFixture) advance(d time.Duration) {
	f.t.Helper()
	time.Sleep(d) //nolint:forbidigo // Fake clock inside the synctest bubble: it crosses a lease boundary, no wall time passes.
	synctest.Wait()
}

func (f *lifecycleFixture) cleanup() {
	f.cancel()
	close(f.blocked)
	synctest.Wait()
	if f.cfg.RedisClient != nil {
		if err := f.cfg.RedisClient.Close(); err != nil {
			f.t.Error(err)
		}
	}
}

func (f *lifecycleFixture) apply() {
	f.t.Helper()
	if err := f.svc.SettingsChanged(f.t.Context()); err != nil {
		f.t.Fatal(err)
	}
	synctest.Wait()
}

func (f *lifecycleFixture) initial() bool {
	f.t.Helper()
	wantCalls, reason := 0, "disabled"
	switch f.name {
	case "invalid port":
		reason = "udp_port_invalid"
	case "bind failure":
		wantCalls, reason = 1, "udp_port_bind_failed"
	case "disabled":
	default:
		if f.calls != 1 || len(f.engines) != 1 {
			f.t.Fatalf("starts=%d engines=%d, want 1", f.calls, len(f.engines))
		}
		if got := f.svc.engineFeature(); got.State != stateOn {
			f.t.Fatalf("started feature=%+v", got)
		}
		return true
	}
	got := f.svc.engineFeature()
	if f.calls != wantCalls || got.State != stateOff || got.Reason != reason {
		f.t.Fatalf("calls=%d feature=%+v, want %d off/%s", f.calls, got, wantCalls, reason)
	}
	return false
}

func (f *lifecycleFixture) exercise() {
	f.t.Helper()
	f.apply()
	if f.leased {
		// A fresh grant keeps the engine off until its hold ends.
		f.advance(engineLeaseTTL)
	}
	if !f.initial() {
		return
	}
	switch f.name {
	case "enabled":
		if !strings.Contains(f.logs.String(), "feature=engine") {
			f.t.Fatalf("missing engine state log: %s", &f.logs)
		}
	case "disable closes":
		f.store.set("direct_connections.enabled", "false")
		f.apply()
		f.assertCloses(1)
	case "blocked close":
		f.checkBlockedClose()
	case "lease loss":
		f.leaseLost.Store(true)
		f.advance(31 * time.Second)
		f.assertCloses(1)
	case "coalesced settings":
		f.apply()
		f.apply()
		if f.calls != 1 {
			f.t.Fatalf("duplicate events started %d engines", f.calls)
		}
	case "drift restart", "drift failure", "restart replays peers":
		f.checkDrift()
	case "renewal fails after zero", "renewal recovers at sixty", "renewal blocked past deadline":
		f.checkRenewal()
	}
	f.cancel()
	synctest.Wait()
	for _, e := range f.engines {
		if e.closes.Load() != 1 {
			f.t.Errorf("engine %d closes=%d after cancellation, want 1", e.id, e.closes.Load())
		}
	}
}

func (f *lifecycleFixture) assertCloses(want int32) {
	f.t.Helper()
	if got := f.engines[0].closes.Load(); got != want {
		f.t.Fatalf("closes=%d, want %d", got, want)
	}
}

func (f *lifecycleFixture) checkBlockedClose() {
	f.t.Helper()
	e := f.engines[0]
	e.closeStarted = make(chan struct{})
	release := make(chan struct{})
	e.closeRelease = release
	defer func() {
		if release != nil {
			close(release)
		}
	}()
	f.store.set("direct_connections.port_mapping", "false")
	settingsDone := make(chan error, 1)
	go func() { settingsDone <- f.svc.SettingsChanged(f.t.Context()) }()
	select {
	case <-e.closeStarted:
	case <-f.deadline:
		f.t.Fatal("restart did not reach Close within 1s")
	}
	statusDone := make(chan error, 1)
	go func() {
		_, err := f.svc.Status(f.t.Context(), false)
		statusDone <- err
	}()
	published := make(chan string, 1)
	// The observer ends with the check, also when the publication never comes.
	observerDone := make(chan struct{})
	defer close(observerDone)
	go func() {
		m := f.svc.replica
		m.mu.Lock()
		m.count = 2
		m.mu.Unlock()
		select {
		case m.changed <- struct{}{}:
		case <-observerDone:
			return
		}
		select {
		case <-f.published:
		case <-observerDone:
			return
		}
		f.svc.mu.Lock()
		reason := f.svc.logged[featureDirect].reason
		f.svc.mu.Unlock()
		published <- reason
	}()
wait:
	for settingsDone != nil || statusDone != nil || published != nil {
		select {
		case err := <-settingsDone:
			if err != nil {
				f.t.Errorf("SettingsChanged: %v", err)
			}
			settingsDone = nil
		case err := <-statusDone:
			if err != nil {
				f.t.Errorf("Status: %v", err)
			}
			statusDone = nil
		case reason := <-published:
			if reason != "multiple_api_replicas" {
				f.t.Errorf("published reason=%q, want multiple_api_replicas", reason)
			}
			published = nil
		case <-f.deadline:
			if settingsDone != nil {
				f.t.Error("SettingsChanged waits on blocked Close for more than 1s")
			}
			if statusDone != nil {
				f.t.Error("Status waits on blocked Close for more than 1s")
			}
			if published != nil {
				f.t.Error("replica publication waits on blocked Close for more than 1s")
			}
			break wait
		}
	}
	if f.calls != 1 || e.closes.Load() != 0 {
		f.t.Errorf("while Close blocked starts=%d closes=%d, want 1/0", f.calls, e.closes.Load())
	}
	close(release)
	release = nil
	synctest.Wait()
	f.assertCloses(1)
	shutdown, closed, next := slices.Index(f.events, "shutdown 1"), slices.Index(f.events, "close 1"), slices.Index(f.events, "new 2")
	if shutdown < 0 || closed <= shutdown || next <= closed || f.calls != 2 {
		f.t.Fatalf("want Shutdown -> Close -> new engine, starts=%d events=%v", f.calls, f.events)
	}
}

func (f *lifecycleFixture) checkDrift() {
	f.t.Helper()
	peers := []engine.Peer{{NodeKey: protocol.NodeKey{1}, Expires: time.Now().Add(time.Hour)}, {NodeKey: protocol.NodeKey{2}, Expires: time.Now().Add(time.Hour)}}
	if f.name == "restart replays peers" {
		if err := f.svc.SetPeers(peers); err != nil {
			f.t.Fatal(err)
		}
	}
	f.engines[0].port++
	f.engines[0].changed <- struct{}{}
	synctest.Wait()
	// A restart is not a stop: the engine row goes from on straight to on, with no
	// off row with an empty reason in between. The off row with reason disabled
	// before the first start is not part of the restart.
	if f.name == "drift restart" && (strings.Count(f.logs.String(), "feature=engine kind=local state=on") != 1 || strings.Contains(f.logs.String(), `feature=engine kind=local state=off reason=""`)) {
		f.t.Fatalf("a restart must publish no off row for the engine: %s", &f.logs)
	}
	if f.calls != 2 || f.engines[0].closes.Load() != 1 {
		f.t.Fatalf("after drift calls=%d closes=%d, want 2/1", f.calls, f.engines[0].closes.Load())
	}
	if f.name == "drift failure" {
		f.checkBindRetry()
	} else {
		if int(f.engines[1].port) != f.cfg.Tuning.DirectUDPPort {
			f.t.Errorf("restart bound %d", f.engines[1].port)
		}
		if f.name == "restart replays peers" && (len(f.serverPeers) != 2 || !reflect.DeepEqual(f.serverPeers[1], peers)) {
			f.t.Fatalf("peers at server factory=%+v, want %+v", f.serverPeers, peers)
		}
	}
	shutdown, closed := -1, -1
	for i, event := range f.events {
		if event == "shutdown 1" {
			shutdown = i
		}
		if event == "close 1" {
			closed = i
		}
	}
	if shutdown < 0 || closed <= shutdown {
		f.t.Fatalf("shutdown must precede Close: %v", f.events)
	}
}

func (f *lifecycleFixture) checkBindRetry() {
	f.t.Helper()
	time.Sleep(2 * time.Minute) //nolint:forbidigo // Advance virtual time to prove there is no periodic bind retry.
	synctest.Wait()
	got := f.svc.engineFeature()
	if f.calls != 2 || got.Reason != "udp_port_bind_failed" {
		f.t.Fatalf("failed restart calls=%d feature=%+v", f.calls, got)
	}
	f.apply()
	if f.calls != 3 || f.svc.engineFeature().State != stateOn {
		f.t.Fatalf("settings event did not retry: calls=%d feature=%+v", f.calls, f.svc.engineFeature())
	}
}

func (f *lifecycleFixture) checkRenewal() {
	f.t.Helper()
	// The last renewal that worked is at failFrom-30s, so the engine stops 60
	// seconds after it, or 60 seconds after the one renewal that recovers.
	deadline := f.failFrom + 30*time.Second
	if f.name == "renewal recovers at sixty" {
		deadline += 60 * time.Second
	}
	for time.Since(f.start) < deadline-time.Second {
		step := min(10*time.Second, deadline-time.Second-time.Since(f.start))
		time.Sleep(step) //nolint:forbidigo // Advance virtual time while checking the self-stop boundary.
		synctest.Wait()
		if f.engines[0].closes.Load() != 0 {
			f.t.Fatalf("closed at %s before %s", time.Since(f.start), deadline)
		}
	}
	time.Sleep(2 * time.Second) //nolint:forbidigo // Cross last-renewal-plus-60-seconds without reaching the Redis TTL.
	synctest.Wait()
	if f.engines[0].closes.Load() != 1 || f.calls != 1 {
		f.t.Fatalf("at %s closes=%d starts=%d renewals=%d, want 1/1", time.Since(f.start), f.engines[0].closes.Load(), f.calls, f.renewals)
	}
}

func TestEngineLifecycle(t *testing.T) {
	for _, name := range []string{"disabled", "enabled", "invalid port", "bind failure", "disable closes", "blocked close", "lease loss", "coalesced settings", "drift restart", "drift failure", "restart replays peers", "renewal fails after zero", "renewal recovers at sixty", "renewal blocked past deadline"} {
		t.Run(name, func(t *testing.T) {
			// A wall-clock deadline can bound mutex waits, which do not advance synctest time.
			var deadline <-chan time.Time
			if name == "blocked close" {
				deadline = time.After(time.Second)
			}
			synctest.Test(t, func(t *testing.T) {
				f := newLifecycleFixture(t, name)
				f.deadline = deadline
				defer f.cleanup()
				f.exercise()
			})
		})
	}
}

func TestKnobsNeverWidenAuthority(t *testing.T) {
	base, _ := tuning.Parse(func(string) string { return "" })
	knobs := []tuning.Config{base}
	for _, port := range []int{1, 65535} {
		c := base
		c.DirectUDPPort = port
		knobs = append(knobs, c)
	}
	for _, enabled := range []bool{false, true} {
		c := base
		c.DirectGSO = enabled
		knobs = append(knobs, c)
		c = base
		c.DirectACKDecimation = enabled
		knobs = append(knobs, c)
	}
	for _, loss := range []string{"both", "no_lower", "loss_thresh", "off"} {
		c := base
		c.DirectBBRLoss = loss
		knobs = append(knobs, c)
	}
	for _, window := range []int{4, 128} {
		c := base
		c.DirectReceiveWindowMiB = window
		knobs = append(knobs, c)
	}
	for i, c := range knobs {
		for _, enabled := range []bool{false, true} {
			t.Run(fmt.Sprintf("knob_%d/enabled_%t", i, enabled), func(t *testing.T) {
				values := settings{enabled: enabled, portMapping: true, stunServers: "stun.example:3478"}
				cfg := Config{SecretKey: bytes.Repeat([]byte{1}, 32), Tuning: c, Logger: slog.New(slog.DiscardHandler)}
				got, p, allowed, err := buildEngineConfig(cfg, values)
				if err != nil {
					t.Fatal(err)
				}
				automaticDevice := protocol.NodeKey{3}
				if got.RelayedDataAllowed != nil && got.RelayedDataAllowed(automaticDevice) {
					t.Fatal("engine knob permits relayed data for automatic device")
				}
				if allowed != enabled {
					t.Fatalf("enabled=%t, stored=%t", allowed, enabled)
				}
				if string(p.Loss) != c.DirectBBRLoss || p.StreamWindow != 0 || p.InitialStreamWindow != 0 || p.ACKDecimation || !p.ScaledGapLimit {
					t.Fatalf("server policy=%+v for knobs=%+v", p, c)
				}
				if got.Port != uint16(c.DirectUDPPort) || got.GSO != c.DirectGSO || !got.PortMapping || !reflect.DeepEqual(got.STUNServers, []string{"stun.example:3478"}) || got.Signaling != nil {
					t.Fatalf("engine config port=%d GSO=%v port mapping=%t STUN=%v signaling set=%t; want port %d, GSO %v, port mapping, STUN [stun.example:3478], no signaling",
						got.Port, got.GSO, got.PortMapping, got.STUNServers, got.Signaling != nil, c.DirectUDPPort, c.DirectGSO)
				}
			})
		}
	}
}

func TestAPINodeKey(t *testing.T) {
	// Independent RFC 5869 expansion for 32 ASCII 'A' bytes and the specified info.
	raw, err := hex.DecodeString("b23c0256eefcf411039ce202b34efadfadd9f7b29e0aa23b96fe313d7a70b071")
	if err != nil {
		t.Fatal(err)
	}
	expected, err := ecdh.X25519().NewPrivateKey(raw)
	if err != nil {
		t.Fatal(err)
	}
	key, err := apiNodeKey(bytes.Repeat([]byte{'A'}, 32))
	if err != nil {
		t.Fatal(err)
	}
	public := key.Public()
	if !bytes.Equal(public[:], expected.PublicKey().Bytes()) {
		t.Fatalf("public key differs from HKDF vector")
	}
}

// A failed start waits for changed settings: the minute recheck's unchanged read
// starts nothing, and a changed read retries once.
func TestRetryRuleUnchangedSettingsKeepFailedStart(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		store := &memorySettings{values: map[string]string{"direct_connections.enabled": "true"}}
		cfg := testConfig(store)
		cfg.SecretKey = bytes.Repeat([]byte{0x41}, 32)
		cfg.Tuning, _ = tuning.Parse(func(string) string { return "" })
		var starts atomic.Int32
		cfg.newEngine = func(context.Context, engine.Config) (engineHandle, error) {
			starts.Add(1)
			return nil, fmt.Errorf("bind: %w", engine.ErrPortUnavailable)
		}
		svc := New(context.Background(), cfg)
		defer shutdownService(t, svc)
		if err := svc.SettingsChanged(t.Context()); err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		if got := svc.engineFeature(); starts.Load() != 1 || got.Reason != "udp_port_bind_failed" {
			t.Fatalf("first start: starts=%d feature=%+v, want 1 off/udp_port_bind_failed", starts.Load(), got)
		}
		time.Sleep(settingsRecheck) //nolint:forbidigo // Fake clock inside the synctest bubble: it reaches the recheck's unchanged read, no wall time passes.
		synctest.Wait()
		if n := starts.Load(); n != 1 {
			t.Fatalf("unchanged recheck read: starts=%d, want 1", n)
		}
		store.set("direct_connections.stun_servers", "second.example:3478")
		if err := svc.SettingsChanged(t.Context()); err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		if n := starts.Load(); n != 2 {
			t.Fatalf("changed read: starts=%d, want 2", n)
		}
	})
}

// exitServer is an HTTP/3 server whose Serve blocks until Shutdown or until the
// test closes exit, which stands in for an unexpected Serve return.
type exitServer struct {
	exit     chan struct{}
	stopped  chan struct{}
	stopOnce sync.Once
}

func (s *exitServer) Serve(net.PacketConn) error {
	select {
	case <-s.exit:
		return errors.New("listener failed")
	case <-s.stopped:
		return http.ErrServerClosed
	}
}

func (s *exitServer) Shutdown(context.Context) error {
	s.stopOnce.Do(func() { close(s.stopped) })
	return nil
}

// serveExitFixture runs a service whose engines serve on exitServers. With lease
// set it holds the engine lease through a hook-faked Redis client whose
// renewals fail while renewFail is set. It must run inside a synctest bubble.
type serveExitFixture struct {
	t         *testing.T
	store     *failingSettings
	logs      bytes.Buffer
	svc       *Service
	mu        sync.Mutex
	servers   []*exitServer
	renewFail atomic.Bool
	leased    bool
}

type serveExitOptions struct {
	lease bool
	// recheck keeps the minute settings recheck on.
	recheck bool
	// capabilities, when set, blocks every check's capability probe until closed,
	// so no status snapshot exists.
	capabilities chan struct{}
	// logs, when set, receives the service log instead of the fixture's buffer.
	logs io.Writer
}

func newServeExitFixture(t *testing.T, opts serveExitOptions) *serveExitFixture {
	t.Helper()
	f := &serveExitFixture{t: t, store: &failingSettings{memorySettings: &memorySettings{values: map[string]string{"direct_connections.enabled": "true"}}}}
	cfg := testConfig(f.store.memorySettings)
	cfg.Settings = f.store
	var logs io.Writer = &f.logs
	if opts.logs != nil {
		logs = opts.logs
	}
	cfg.Logger = slog.New(slog.NewTextHandler(logs, nil))
	cfg.SecretKey = bytes.Repeat([]byte{0x41}, 32)
	cfg.Tuning, _ = tuning.Parse(func(string) string { return "" })
	if !opts.recheck {
		cfg.recheckInterval = -1
	}
	if release := opts.capabilities; release != nil {
		cfg.Capabilities = func(context.Context, *slog.Logger) (engine.CapabilityReport, error) {
			<-release
			return engine.CapabilityReport{Platform: "linux"}, nil
		}
	}
	var events []string
	var eventsMu sync.Mutex
	cfg.newEngine = func(_ context.Context, c engine.Config) (engineHandle, error) {
		f.mu.Lock()
		defer f.mu.Unlock()
		return &lifecycleEngine{port: c.Port, changed: make(chan struct{}, 1), events: &events, eventsMu: &eventsMu, id: len(f.servers) + 1}, nil
	}
	cfg.newServer = func(engineHandle, n4.Policy) engineServer {
		f.mu.Lock()
		defer f.mu.Unlock()
		srv := &exitServer{exit: make(chan struct{}), stopped: make(chan struct{})}
		f.servers = append(f.servers, srv)
		return srv
	}
	if opts.lease {
		f.leased = true
		client := redis.NewClient(&redis.Options{Addr: "127.0.0.1:6379"})
		t.Cleanup(func() { _ = client.Close() })
		client.AddHook(leaseCommandHook{process: f.leaseCommand})
		cfg.RedisClient = client
		cfg.ReplicaPresence = cache.NewAPIReplicaPresence(client, "api")
	}
	f.svc = New(context.Background(), cfg)
	return f
}

func (f *serveExitFixture) leaseCommand(cmd redis.Cmder) error {
	c, ok := cmd.(*redis.Cmd)
	if !ok {
		return nil
	}
	if classifyLease(c) == leaseRenew && f.renewFail.Load() {
		return errors.New("renewal unavailable")
	}
	c.SetVal(int64(1))
	return nil
}

// begin applies the settings and, with a lease, waits out the fresh grant's
// hold, so the first engine is running when it returns.
func (f *serveExitFixture) begin() {
	f.t.Helper()
	f.settingsChanged()
	if f.leased {
		f.want("during the hold", 0, stateOff, "replica_lease_pending")
		f.sleep(engineLeaseTTL)
	}
}

func (f *serveExitFixture) starts() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.servers)
}

// exitServe makes the n-th started server's Serve return, then settles.
func (f *serveExitFixture) exitServe(n int) {
	f.t.Helper()
	f.mu.Lock()
	srv := f.servers[n-1]
	f.mu.Unlock()
	close(srv.exit)
	synctest.Wait()
}

func (f *serveExitFixture) settingsChanged() {
	f.t.Helper()
	if err := f.svc.SettingsChanged(f.t.Context()); err != nil {
		f.t.Fatal(err)
	}
	synctest.Wait()
}

func (f *serveExitFixture) want(step string, starts int, state, reason string) {
	f.t.Helper()
	if got := f.svc.engineFeature(); f.starts() != starts || got.State != state || got.Reason != reason {
		f.t.Fatalf("%s: starts=%d feature=%+v, want %d %s/%s", step, f.starts(), got, starts, state, reason)
	}
}

// wantWithdrawn checks the state right after an unexpected exit: admission off
// with engine_start_failed and no current engine.
func (f *serveExitFixture) wantWithdrawn(step string, starts int) {
	f.t.Helper()
	f.want(step, starts, stateOff, "engine_start_failed")
	l := f.svc.lifecycle
	l.mu.Lock()
	current := l.current
	l.mu.Unlock()
	if current != nil {
		f.t.Fatalf("%s: current engine still set after Serve returned", step)
	}
}

func (f *serveExitFixture) shutdown() {
	f.t.Helper()
	if err := f.svc.Shutdown(f.t.Context()); err != nil {
		f.t.Fatalf("Shutdown: %v", err)
	}
}

func (f *serveExitFixture) sleep(d time.Duration) {
	time.Sleep(d) //nolint:forbidigo // Fake clock inside the synctest bubble: it reaches the next census, no wall time passes.
	synctest.Wait()
}

// After an unexpected Serve exit the engine restarts once a settings read and a
// lease renewal have both completed after the exit, in either order.
func TestServeExitRestartsAfterReadAndRenewal(t *testing.T) {
	t.Run("read first", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			f := newServeExitFixture(t, serveExitOptions{lease: true})
			defer f.shutdown()
			f.begin()
			f.want("started", 1, stateOn, "")
			// The exit triggers a background settings read, which succeeds.
			f.exitServe(1)
			f.wantWithdrawn("after the exit and its read", 1)
			f.sleep(censusInterval)
			f.want("after the renewal", 2, stateOn, "")
		})
	})
	t.Run("renewal first", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			f := newServeExitFixture(t, serveExitOptions{lease: true})
			defer f.shutdown()
			f.begin()
			// The exit's background read fails, so the renewal completes first.
			f.store.failReads.Store(1)
			f.exitServe(1)
			f.wantWithdrawn("after the exit", 1)
			f.sleep(censusInterval)
			f.wantWithdrawn("after the renewal alone", 1)
			// The failed read left a read pending; this Status rereads in the background.
			if _, err := f.svc.Status(t.Context(), false); err != nil {
				t.Fatal(err)
			}
			synctest.Wait()
			f.want("after the read", 2, stateOn, "")
		})
	})
	t.Run("without redis", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			f := newServeExitFixture(t, serveExitOptions{})
			defer f.shutdown()
			f.settingsChanged()
			// With no lease, the exit's own settings read is enough.
			f.exitServe(1)
			f.want("after the exit and its read", 2, stateOn, "")
			if !strings.Contains(f.logs.String(), "feature=engine kind=local state=off reason=engine_start_failed") {
				t.Fatalf("withdrawal was not published: %s", &f.logs)
			}
		})
	})
}

// A failed renewal after the exit does not count; the next successful one does.
func TestServeExitIgnoresFailedRenewal(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f := newServeExitFixture(t, serveExitOptions{lease: true})
		defer f.shutdown()
		f.begin()
		f.exitServe(1)
		f.renewFail.Store(true)
		f.sleep(censusInterval)
		f.wantWithdrawn("after a failed renewal", 1)
		f.renewFail.Store(false)
		f.sleep(censusInterval)
		f.want("after a successful renewal", 2, stateOn, "")
	})
}

// An engine that exits before the first status snapshot still restarts on the
// next successful renewal.
func TestServeExitWithoutSnapshotWakesOnRenewal(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		release := make(chan struct{})
		f := newServeExitFixture(t, serveExitOptions{lease: true, capabilities: release})
		defer f.shutdown()
		// Shutdown joins the blocked check, so release it first.
		defer close(release)
		f.begin()
		f.want("started", 1, stateOn, "")
		f.exitServe(1)
		f.wantWithdrawn("after the exit", 1)
		f.sleep(censusInterval)
		f.want("after the renewal", 2, stateOn, "")
	})
}

// Settings that disable the engine after the exit cancel the restart.
func TestServeExitThenDisabledStaysOff(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f := newServeExitFixture(t, serveExitOptions{lease: true})
		defer f.shutdown()
		f.begin()
		f.store.set("direct_connections.enabled", "false")
		f.exitServe(1)
		f.want("after the exit's read", 1, stateOff, reasonDisabled)
		f.sleep(censusInterval)
		f.want("after the renewal", 1, stateOff, reasonDisabled)
	})
}

// The budget is one restart: a second exit leaves the engine failed until the
// settings change.
func TestServeExitSecondExitWaitsForSettingsChange(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f := newServeExitFixture(t, serveExitOptions{lease: true, recheck: true})
		defer f.shutdown()
		f.begin()
		f.exitServe(1)
		f.sleep(censusInterval)
		f.want("restarted", 2, stateOn, "")
		f.exitServe(2)
		f.wantWithdrawn("after the second exit", 2)
		// The minute recheck reads the unchanged settings; renewals succeed.
		f.sleep(settingsRecheck)
		f.wantWithdrawn("after an unchanged recheck", 2)
		f.store.set("direct_connections.stun_servers", "second.example:3478")
		f.settingsChanged()
		f.want("after a settings change", 3, stateOn, "")
	})
}

// gatedLog blocks the writer of the first engine_start_failed feature row until
// release closes, which holds the publication of a Serve-exit withdrawal.
type gatedLog struct {
	writing chan struct{}
	release chan struct{}
	seen    atomic.Bool
}

func (g *gatedLog) Write(p []byte) (int, error) {
	if bytes.Contains(p, []byte("feature=engine kind=local state=off reason=engine_start_failed")) && g.seen.CompareAndSwap(false, true) {
		close(g.writing)
		<-g.release
	}
	return len(p), nil
}

// The exit detaches the engine and records both restart marks before it
// publishes: a peer update during the publication must not reach the failed
// engine, and a renewal that completes during it counts as after the exit.
func TestServeExitDetachesBeforePublishAndCountsRenewalDuringPublish(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		gate := &gatedLog{writing: make(chan struct{}), release: make(chan struct{})}
		f := newServeExitFixture(t, serveExitOptions{lease: true, logs: gate})
		defer f.shutdown()
		// Registered after the shutdown above, so it runs first: a failed check
		// must free the gated log write before Shutdown waits for it.
		var releaseOnce sync.Once
		releaseGate := func() { releaseOnce.Do(func() { close(gate.release) }) }
		defer releaseGate()
		f.begin()
		f.want("started", 1, stateOn, "")
		f.mu.Lock()
		close(f.servers[0].exit)
		f.mu.Unlock()
		<-gate.writing

		l := f.svc.lifecycle
		l.mu.Lock()
		current, feature := l.current, l.feature
		l.mu.Unlock()
		if current != nil || feature.State != stateOff || feature.Reason != reasonEngineStartFailed {
			t.Errorf("while the withdrawal is published: current=%t feature=%+v, want a detached engine and off/engine_start_failed", current != nil, feature)
		}
		before := f.svc.replica.leaseSuccesses()
		f.svc.replica.update(t.Context())
		if after := f.svc.replica.leaseSuccesses(); after != before+1 {
			t.Fatalf("lease successes during the publication: %d -> %d, want one more", before, after)
		}

		releaseGate()
		synctest.Wait()
		if n := f.starts(); n != 2 {
			t.Errorf("a settings read and a lease renewal both completed after the exit: %d starts, want 2", n)
		}
	})
}

// An internal follow-up reread is not an event: after a failed start, a minute
// reread that sees a change while a check runs schedules a follow-up whose
// unchanged values must not retry the start.
func TestRetryRuleFollowUpOfFailedStartIsNotAnEvent(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		store := &memorySettings{values: map[string]string{"direct_connections.enabled": "false"}}
		cfg := testConfig(store)
		cfg.SecretKey = bytes.Repeat([]byte{0x41}, 32)
		cfg.Tuning, _ = tuning.Parse(func(string) string { return "" })
		var starts, checks atomic.Int32
		release := make(chan struct{})
		cfg.Capabilities = func(ctx context.Context, _ *slog.Logger) (engine.CapabilityReport, error) {
			if checks.Add(1) == 2 {
				select {
				case <-release:
				case <-ctx.Done():
					return engine.CapabilityReport{}, ctx.Err()
				}
			}
			return engine.CapabilityReport{Platform: "linux"}, nil
		}
		var events []string
		var eventsMu sync.Mutex
		cfg.newEngine = func(_ context.Context, c engine.Config) (engineHandle, error) {
			if starts.Add(1) == 1 {
				return nil, fmt.Errorf("bind: %w", engine.ErrPortUnavailable)
			}
			return &lifecycleEngine{port: c.Port, changed: make(chan struct{}), events: &events, eventsMu: &eventsMu}, nil
		}
		svc := New(t.Context(), cfg)
		defer shutdownService(t, svc)
		if err := svc.SettingsChanged(t.Context()); err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		time.Sleep(settingsRecheck - time.Second) //nolint:forbidigo // Fake clock inside the synctest bubble: it reaches the second before the recheck.
		refresh := make(chan error, 1)
		go func() {
			_, err := svc.Status(t.Context(), true)
			refresh <- err
		}()
		synctest.Wait()
		if checks.Load() != 2 {
			t.Fatalf("refresh did not start a check: %d checks, want 2", checks.Load())
		}
		store.set("direct_connections.enabled", "true")
		time.Sleep(time.Second) //nolint:forbidigo // Fake clock inside the synctest bubble: it reaches the recheck, which sees the change while the check runs.
		synctest.Wait()
		if starts.Load() != 1 {
			t.Fatalf("the changed minute read: %d starts, want 1", starts.Load())
		}
		close(release)
		if err := <-refresh; err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		if got := svc.engineFeature(); starts.Load() != 1 || got.State != stateOff {
			t.Errorf("after the follow-up of unchanged values: starts=%d feature=%+v, want 1 start and off", starts.Load(), got)
		}
	})
}

// A changed read retries a failed start even when it is not an event: the
// minute recheck is a read, not a settings-change signal.
func TestRetryRuleChangedValuesRetryOnNonEventRead(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		store := &memorySettings{values: map[string]string{"direct_connections.enabled": "true"}}
		cfg := testConfig(store)
		cfg.SecretKey = bytes.Repeat([]byte{0x41}, 32)
		cfg.Tuning, _ = tuning.Parse(func(string) string { return "" })
		var starts atomic.Int32
		cfg.newEngine = func(context.Context, engine.Config) (engineHandle, error) {
			starts.Add(1)
			return nil, fmt.Errorf("bind: %w", engine.ErrPortUnavailable)
		}
		svc := New(t.Context(), cfg)
		defer shutdownService(t, svc)
		if err := svc.SettingsChanged(t.Context()); err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		if n := starts.Load(); n != 1 {
			t.Fatalf("first start: %d starts, want 1", n)
		}
		store.set("direct_connections.stun_servers", "second.example:3478")
		time.Sleep(settingsRecheck) //nolint:forbidigo // Fake clock inside the synctest bubble: it reaches the minute recheck, no wall time passes.
		synctest.Wait()
		if n := starts.Load(); n != 2 {
			t.Fatalf("minute recheck read of changed values: %d starts, want 2", n)
		}
	})
}

// newAwaitHandlerFixture starts a service that holds engine starts until
// HandlerReady. With lease set, renewals fail after the first acquisition, so
// the lease is lost 60 seconds in. The minute recheck stays on.
func newAwaitHandlerFixture(t *testing.T, lease bool) *lifecycleFixture {
	t.Helper()
	f := &lifecycleFixture{t: t, name: "await handler", start: time.Now()}
	if lease {
		f.name = "renewal fails after zero"
	}
	f.store = &memorySettings{values: map[string]string{"direct_connections.enabled": "true"}}
	f.cfg = testConfig(f.store)
	f.cfg.SecretKey = bytes.Repeat([]byte{0x41}, 32)
	f.cfg.Tuning, _ = tuning.Parse(func(string) string { return "" })
	f.cfg.newEngine = f.newEngine
	f.cfg.newServer = f.newServer
	f.cfg.AwaitHandler = true
	if lease {
		f.installLease()
		t.Cleanup(func() { _ = f.cfg.RedisClient.Close() })
	}
	f.svc = New(t.Context(), f.cfg)
	return f
}

// No settings path binds the engine before the HTTP handler exists: the first
// read, an event, and the minute recheck all wait for HandlerReady, which starts
// the engine once.
func TestEngineStartWaitsForHandler(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f := newAwaitHandlerFixture(t, false)
		defer shutdownService(t, f.svc)
		f.apply()
		f.apply()
		time.Sleep(settingsRecheck + time.Second) //nolint:forbidigo // Fake clock inside the synctest bubble: the minute recheck reads settings.
		synctest.Wait()
		if f.calls != 0 {
			t.Fatalf("engine constructed %d times before HandlerReady, want 0", f.calls)
		}
		if got := f.svc.engineFeature(); got.State != stateOff {
			t.Fatalf("before HandlerReady: feature=%+v, want off", got)
		}
		f.svc.HandlerReady()
		f.svc.HandlerReady()
		synctest.Wait()
		if got := f.svc.engineFeature(); f.calls != 1 || got.State != stateOn {
			t.Fatalf("after HandlerReady: starts=%d feature=%+v, want 1 on", f.calls, got)
		}
	})
}

// A lease lost while the handler was pending keeps the engine off once the
// handler is ready, instead of binding the port under a stale lease.
func TestEngineStartAfterHandlerRechecksLease(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f := newAwaitHandlerFixture(t, true)
		defer shutdownService(t, f.svc)
		f.apply()
		time.Sleep(engineLeaseTTL + time.Second) //nolint:forbidigo // Fake clock inside the synctest bubble: renewals fail, so the lease is lost.
		synctest.Wait()
		if f.svc.ReplicaGate().EngineMayRun {
			t.Fatalf("precondition: the lease is still held: %+v", f.svc.ReplicaGate())
		}
		f.svc.HandlerReady()
		synctest.Wait()
		if f.calls != 0 {
			t.Fatalf("engine constructed %d times with a lost lease, want 0", f.calls)
		}
	})
}

// recordingLeaseRelease appends "release" to events when the lease release
// script runs, before any other hook answers it.
type recordingLeaseRelease struct {
	events   *[]string
	eventsMu *sync.Mutex
}

func (recordingLeaseRelease) DialHook(next redis.DialHook) redis.DialHook { return next }
func (h recordingLeaseRelease) ProcessHook(next redis.ProcessHook) redis.ProcessHook {
	return func(ctx context.Context, cmd redis.Cmder) error {
		if c, ok := cmd.(*redis.Cmd); ok && fmt.Sprint(c.Args()[1]) == releaseEngineLease.Hash() {
			h.eventsMu.Lock()
			*h.events = append(*h.events, "release")
			h.eventsMu.Unlock()
		}
		return next(ctx, cmd)
	}
}
func (recordingLeaseRelease) ProcessPipelineHook(next redis.ProcessPipelineHook) redis.ProcessPipelineHook {
	return next
}

// Shutdown closes the engine before it releases the lease, so a successor that
// acquires the lease never overlaps an engine of the same identity.
func TestShutdownClosesEngineBeforeReleasingLease(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f := &lifecycleFixture{t: t, name: "renewal recovers at sixty", start: time.Now(), failFrom: engineLeaseTTL + censusInterval, leased: true}
		f.store = &memorySettings{values: map[string]string{"direct_connections.enabled": "true"}}
		f.cfg = testConfig(f.store)
		f.cfg.SecretKey = bytes.Repeat([]byte{0x41}, 32)
		f.cfg.Tuning, _ = tuning.Parse(func(string) string { return "" })
		f.cfg.newEngine = f.newEngine
		f.cfg.newServer = f.newServer
		client := redis.NewClient(&redis.Options{Addr: "127.0.0.1:6379"})
		t.Cleanup(func() { _ = client.Close() })
		// The first hook added is the outermost, so the recorder sees the release
		// before the lease hook answers it.
		client.AddHook(recordingLeaseRelease{events: &f.events, eventsMu: &f.eventsMu})
		client.AddHook(leaseCommandHook{process: f.leaseCommand})
		f.cfg.RedisClient = client
		f.cfg.ReplicaPresence = cache.NewAPIReplicaPresence(client, "api")
		f.svc = New(t.Context(), f.cfg)
		f.apply()
		f.advance(engineLeaseTTL)
		if f.calls != 1 {
			t.Fatalf("starts=%d, want 1", f.calls)
		}
		shutdownService(t, f.svc)
		f.eventsMu.Lock()
		defer f.eventsMu.Unlock()
		closed, released := slices.Index(f.events, "close 1"), slices.Index(f.events, "release")
		if closed < 0 || released < 0 || closed > released {
			t.Fatalf("shutdown events %v: want close 1 before release", f.events)
		}
	})
}
