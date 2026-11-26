package directconn

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/darthShadow/shunpike/engine"
	"github.com/darthShadow/shunpike/engine/n4"
	"github.com/redis/go-redis/v9"

	"github.com/Silo-Server/silo-server/internal/cache"
	"github.com/Silo-Server/silo-server/internal/tuning"
)

// rigPacketConn records what reaches the engine's socket.
type rigPacketConn struct {
	net.PacketConn
	reads    chan []byte
	writes   atomic.Int32
	segments atomic.Int32
}

func (c *rigPacketConn) ReadFrom(b []byte) (int, net.Addr, error) {
	p, ok := <-c.reads
	if !ok {
		return 0, nil, net.ErrClosed
	}
	return copy(b, p), &net.UDPAddr{}, nil
}

func (c *rigPacketConn) WriteTo(b []byte, _ net.Addr) (int, error) {
	c.writes.Add(1)
	return len(b), nil
}

func (c *rigPacketConn) WriteSegments(b []byte, _ net.Addr, _ uint16) (int, error) {
	c.segments.Add(1)
	return len(b), nil
}
func (c *rigPacketConn) SupportsGSO() bool { return true }
func (c *rigPacketConn) SupportsDF() bool  { return true }

// rigEngine is a lifecycleEngine with a packet connection that can reject peer
// lists: a peer with a zero Expires is invalid.
type rigEngine struct {
	*lifecycleEngine
	conn *rigPacketConn
}

func (e *rigEngine) PacketConn() net.PacketConn { return e.conn }
func (e *rigEngine) SetPeers(peers []engine.Peer) error {
	for _, p := range peers {
		if p.Expires.IsZero() {
			return errors.New("peer has no expiry")
		}
	}
	return e.lifecycleEngine.SetPeers(peers)
}

// rigServer serves the connection it is given and blocks until Shutdown. With
// holdShutdown set, Shutdown also waits for it to close, so a test can look at
// the packet path while teardown is stuck.
type rigServer struct {
	rig      *threadRig
	stopped  chan struct{}
	stopOnce sync.Once
}

func (s *rigServer) Serve(conn net.PacketConn) error {
	s.rig.serves.Add(1)
	s.rig.mu.Lock()
	s.rig.served = conn
	s.rig.mu.Unlock()
	<-s.stopped
	return http.ErrServerClosed
}

func (s *rigServer) Shutdown(ctx context.Context) error {
	s.rig.mu.Lock()
	s.rig.shutdownCtxErrs = append(s.rig.shutdownCtxErrs, ctx.Err())
	hold := s.rig.holdShutdown
	s.rig.mu.Unlock()
	s.stopOnce.Do(func() { close(s.stopped) })
	if hold != nil {
		<-hold
	}
	return nil
}

// threadRig runs a service on fake engines and a scripted Redis inside a
// synctest bubble. Every Redis command takes rtt on the fake clock.
type threadRig struct {
	t     *testing.T
	store *memorySettings
	svc   *Service
	logs  bytes.Buffer

	rtt       time.Duration
	construct time.Duration // how long newEngine takes
	renewErr  atomic.Bool
	renewGone atomic.Bool // renewals answer 0: another process took the key
	takenBy   atomic.Bool // acquisitions answer 0
	commands  atomic.Int32
	releases  atomic.Int32
	newErr    atomic.Pointer[error]

	calls, serves, closes atomic.Int32

	mu              sync.Mutex
	engines         []*rigEngine
	served          net.PacketConn
	shutdownCtxErrs []error
	holdShutdown    chan struct{}
	events          []string
}

type rigOptions struct {
	redis       bool
	requireOnly bool // RequireRedis with no client
	rtt         time.Duration
	enabled     bool
}

func newThreadRig(t *testing.T, o rigOptions) *threadRig {
	t.Helper()
	r := &threadRig{t: t, rtt: o.rtt}
	r.store = &memorySettings{values: map[string]string{"direct_connections.enabled": "false"}}
	if o.enabled {
		r.store.set("direct_connections.enabled", "true")
	}
	cfg := testConfig(r.store)
	cfg.Logger = slog.New(slog.NewTextHandler(&r.logs, nil))
	cfg.SecretKey = bytes.Repeat([]byte{0x41}, 32)
	cfg.Tuning, _ = tuning.Parse(func(string) string { return "" })
	cfg.recheckInterval = -1
	cfg.RequireRedis = o.requireOnly
	cfg.newEngine = r.newEngine
	cfg.newServer = func(engineHandle, n4.Policy) engineServer {
		return &rigServer{rig: r, stopped: make(chan struct{})}
	}
	if o.redis {
		client := redis.NewClient(&redis.Options{Addr: "127.0.0.1:6379"})
		t.Cleanup(func() { _ = client.Close() })
		client.AddHook(ctxCommandHook{process: r.command})
		cfg.RedisClient = client
		cfg.ReplicaPresence = cache.NewAPIReplicaPresence(client, "api")
	}
	r.svc = New(t.Context(), cfg)
	return r
}

func (r *threadRig) command(ctx context.Context, cmd redis.Cmder) error {
	c, ok := cmd.(*redis.Cmd)
	if !ok {
		return nil
	}
	r.commands.Add(1)
	if r.rtt > 0 {
		time.Sleep(r.rtt) //nolint:forbidigo // Fake clock inside the synctest bubble: it stands for the Redis round trip.
	}
	if err := ctx.Err(); err != nil {
		c.SetErr(err)
		return err
	}
	switch classifyLease(c) {
	case leaseRenew:
		if r.renewErr.Load() {
			return errors.New("renewal unavailable")
		}
		if r.renewGone.Load() {
			c.SetVal(int64(0))
			return nil
		}
	case leaseAcquire:
		if r.takenBy.Load() {
			c.SetVal(int64(0))
			return nil
		}
	case leaseRelease:
		r.releases.Add(1)
	}
	c.SetVal(int64(1))
	return nil
}

func (r *threadRig) newEngine(_ context.Context, c engine.Config) (engineHandle, error) {
	n := int(r.calls.Add(1))
	if r.construct > 0 {
		time.Sleep(r.construct) //nolint:forbidigo // Fake clock inside the synctest bubble: it stands for a slow engine construction.
	}
	if err := r.newErr.Load(); err != nil {
		return nil, *err
	}
	e := &rigEngine{
		lifecycleEngine: &lifecycleEngine{port: c.Port, changed: make(chan struct{}, 1), events: &r.events, eventsMu: &r.mu, id: n},
		conn:            &rigPacketConn{reads: make(chan []byte, 8)},
	}
	r.mu.Lock()
	r.engines = append(r.engines, e)
	r.mu.Unlock()
	return countingClose{e, &r.closes}, nil
}

// countingClose counts every Close of the wrapped engine.
type countingClose struct {
	*rigEngine
	closes *atomic.Int32
}

func (c countingClose) Close() error {
	c.closes.Add(1)
	return c.rigEngine.Close()
}

func (r *threadRig) sleep(d time.Duration) {
	r.t.Helper()
	time.Sleep(d) //nolint:forbidigo // Fake clock inside the synctest bubble: it crosses a lease boundary, no wall time passes.
	synctest.Wait()
}

func (r *threadRig) apply() {
	r.t.Helper()
	if err := r.svc.SettingsChanged(r.t.Context()); err != nil {
		r.t.Fatal(err)
	}
	synctest.Wait()
}

func (r *threadRig) engineIs(step, state, reason string) {
	r.t.Helper()
	if got := r.svc.engineFeature(); got.State != state || got.Reason != reason {
		r.t.Fatalf("%s: engine feature = %+v, want %s/%s", step, got, state, reason)
	}
}

func (r *threadRig) directRow() Feature {
	r.t.Helper()
	status, err := r.svc.Status(r.t.Context(), false)
	if err != nil {
		r.t.Fatal(err)
	}
	return feature(r.t, status.Features, featureDirect)
}

// A construction that outlasts the lease must not publish: the candidate is
// closed unpublished and Serve never runs on it.
func TestStartRechecksPermissionAfterConstruction(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := newThreadRig(t, rigOptions{redis: true, enabled: true})
		r.construct = 70 * time.Second
		defer shutdownService(t, r.svc)
		r.apply()
		// The hold ends and construction begins; from now on no renewal works.
		r.sleep(engineLeaseTTL)
		r.renewErr.Store(true)
		// Construction ends 70 seconds in, after the lease's 60-second self-stop.
		r.sleep(80 * time.Second)
		if serves, closes := r.serves.Load(), r.closes.Load(); serves != 0 || closes < 1 {
			t.Fatalf("engine built past its lease: serves=%d closes=%d, want 0 serves and the candidate closed", serves, closes)
		}
		if got := r.svc.engineFeature(); got.Reason == reasonEngineStartFailed || got.Reason == reasonPortBindFailed {
			t.Fatalf("a stale candidate is not a start failure: feature=%+v", got)
		}
	})
}

// The census and lease run only while direct connections are on, and every
// enable waits the full hold before the engine starts.
func TestMonitorRunsOnDemandAndEveryEnableWaitsTheHold(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := newThreadRig(t, rigOptions{redis: true})
		defer shutdownService(t, r.svc)
		r.apply()
		r.sleep(5 * time.Minute)
		if n := r.commands.Load(); n != 0 {
			t.Fatalf("a disabled feature ran %d Redis commands, want 0", n)
		}
		for round := 1; round <= 2; round++ {
			r.store.set("direct_connections.enabled", "true")
			r.apply()
			if r.calls.Load() != int32(round-1) || r.commands.Load() == 0 {
				t.Fatalf("round %d just after enabling: engine starts=%d, redis commands=%d, want %d starts and a running monitor", round, r.calls.Load(), r.commands.Load(), round-1)
			}
			r.sleep(engineLeaseTTL - time.Second)
			if r.calls.Load() != int32(round-1) {
				t.Fatalf("round %d: engine started %s into the hold", round, engineLeaseTTL-time.Second)
			}
			r.sleep(time.Second)
			if got := r.calls.Load(); got != int32(round) {
				t.Fatalf("round %d: engine starts after the hold = %d, want %d", round, got, round)
			}
			r.engineIs("after the hold", stateOn, "")
			r.store.set("direct_connections.enabled", "false")
			r.apply()
			if r.closes.Load() != int32(round) || r.releases.Load() != int32(round) {
				t.Fatalf("round %d after disabling: closes=%d releases=%d, want the engine closed and the lease released", round, r.closes.Load(), r.releases.Load())
			}
			before := r.commands.Load()
			r.sleep(5 * time.Minute)
			if n := r.commands.Load() - before; n != 0 {
				t.Fatalf("round %d: %d Redis commands after disabling, want 0", round, n)
			}
		}
	})
}

// A renewal that Redis answers with 0 ends the grant; the next acquisition by
// the same process is a fresh grant and waits the hold again.
func TestLostGrantReacquiredWaitsTheHoldAgain(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := newThreadRig(t, rigOptions{redis: true, enabled: true})
		defer shutdownService(t, r.svc)
		r.apply()
		r.sleep(engineLeaseTTL)
		r.engineIs("first grant", stateOn, "")
		// The key is gone: the next renewal answers 0 and the same update
		// acquires a new grant.
		r.renewGone.Store(true)
		r.sleep(censusInterval)
		r.renewGone.Store(false)
		if got := r.svc.ReplicaGate(); got.EngineMayRun || got.Reason != "replica_lease_pending" {
			t.Fatalf("gate in the new grant's hold = %+v, want pending", got)
		}
		r.engineIs("new grant's hold", stateOff, "replica_lease_pending")
		r.sleep(engineLeaseTTL - time.Second)
		if r.calls.Load() != 1 {
			t.Fatalf("engine restarted %d times inside the new grant's hold, want it still stopped after 1 start", r.calls.Load())
		}
		r.sleep(time.Second + censusInterval)
		if r.calls.Load() != 2 {
			t.Fatalf("engine starts after the second hold = %d, want 2", r.calls.Load())
		}
	})
}

// A stop the lease forces closes connections at once; a settings stop keeps the
// graceful drain.
func TestLeaseLossDrainsWithoutGrace(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := newThreadRig(t, rigOptions{redis: true, enabled: true})
		defer shutdownService(t, r.svc)
		r.apply()
		r.sleep(engineLeaseTTL)
		r.engineIs("running", stateOn, "")
		r.takenBy.Store(true)
		r.renewGone.Store(true)
		r.sleep(censusInterval)
		r.engineIs("after the lease loss", stateOff, "replica_lease_held")
		r.mu.Lock()
		errs := append([]error(nil), r.shutdownCtxErrs...)
		r.mu.Unlock()
		if len(errs) != 1 || errs[0] == nil {
			t.Fatalf("shutdown contexts after a lease loss: errors=%v, want one drain whose context had already ended", errs)
		}
		// Back to a free key: a new grant, hold, and then a settings stop.
		r.takenBy.Store(false)
		r.renewGone.Store(false)
		r.sleep(censusInterval + engineLeaseTTL + time.Second)
		r.engineIs("second grant", stateOn, "")
		r.store.set("direct_connections.stun_servers", "other.example:3478")
		r.apply()
		r.mu.Lock()
		errs = append([]error(nil), r.shutdownCtxErrs...)
		r.mu.Unlock()
		if len(errs) != 2 || errs[1] != nil {
			t.Fatalf("shutdown contexts after a settings restart: errors=%v, want the restart drain graceful", errs)
		}
	})
}

// Without a Redis client, RequireRedis keeps the engine off with the census
// reason, and the direct row follows it; without RequireRedis the engine runs.
func TestRequireRedisRefusesEngineWithoutRedis(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := newThreadRig(t, rigOptions{enabled: true, requireOnly: true})
		defer shutdownService(t, r.svc)
		r.apply()
		r.sleep(engineLeaseTTL + time.Minute)
		if r.calls.Load() != 0 {
			t.Fatalf("engine started %d times without the Redis it requires", r.calls.Load())
		}
		r.engineIs("refused", stateOff, "replica_census_unavailable")
		if row := r.directRow(); row.State != stateOff || row.Reason != "replica_census_unavailable" {
			t.Fatalf("direct row = %+v, want off/replica_census_unavailable", row)
		}
	})
	synctest.Test(t, func(t *testing.T) {
		r := newThreadRig(t, rigOptions{enabled: true})
		defer shutdownService(t, r.svc)
		r.apply()
		r.engineIs("single process without Redis", stateOn, "")
	})
}

// A start error other than a taken port reports engine_start_failed, and the
// direct row goes off with the engine's reason.
func TestStartFailureReasonsReachTheDirectRow(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want string
	}{
		{"port taken", errors.New("bind"), reasonPortBindFailed},
		{"other failure", errors.New("key derivation failed"), reasonEngineStartFailed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				r := newThreadRig(t, rigOptions{enabled: true})
				defer shutdownService(t, r.svc)
				err := tc.err
				if tc.want == reasonPortBindFailed {
					err = errors.Join(tc.err, engine.ErrPortUnavailable)
				}
				r.newErr.Store(&err)
				r.apply()
				r.engineIs("after the failed start", stateOff, tc.want)
				if row := r.directRow(); row.State != stateOff || row.Reason != tc.want {
					t.Fatalf("direct row = %+v, want off/%s while the engine is off/%s", row, tc.want, tc.want)
				}
			})
		})
	}
}

// A peer list the live engine rejects is not kept, so a settings restart does
// not replay it.
func TestRejectedPeersAreNotReplayed(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := newThreadRig(t, rigOptions{enabled: true})
		defer shutdownService(t, r.svc)
		r.apply()
		good := []engine.Peer{{Expires: time.Now().Add(time.Hour)}}
		if err := r.svc.SetPeers(good); err != nil {
			t.Fatal(err)
		}
		if err := r.svc.SetPeers([]engine.Peer{{}}); err == nil {
			t.Fatal("the engine accepted a peer with no expiry")
		}
		r.store.set("direct_connections.stun_servers", "other.example:3478")
		r.apply()
		r.engineIs("after the restart", stateOn, "")
		r.mu.Lock()
		last := r.engines[len(r.engines)-1]
		r.mu.Unlock()
		if len(last.peers) != 1 || last.peers[0].Expires.IsZero() {
			t.Fatalf("the restarted engine got peers %+v, want the last accepted list", last.peers)
		}
	})
}

// A one-off renewal failure with a real round trip must not stop the engine:
// the retry comes well before the 60-second self-stop.
func TestOneFailedRenewalKeepsTheLease(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var renewals atomic.Int32
		client := redis.NewClient(&redis.Options{Addr: "127.0.0.1:6379"})
		t.Cleanup(func() { _ = client.Close() })
		client.AddHook(ctxCommandHook{process: func(ctx context.Context, cmd redis.Cmder) error {
			c := cmd.(*redis.Cmd)
			time.Sleep(time.Millisecond) //nolint:forbidigo // Fake clock inside the synctest bubble: it stands for the Redis round trip.
			if classifyLease(c) == leaseRenew {
				// The first run renews at start; the second renewal, at the 30-second
				// tick, fails, and the 60-second tick is not the only chance left.
				if renewals.Add(1) == 2 {
					return errors.New("renewal unavailable")
				}
			}
			c.SetVal(int64(1))
			return nil
		}})
		m := newReplicaMonitor(&censusStub{count: 1}, &redisEngineLease{client: client, id: "p"}, slog.New(slog.DiscardHandler))
		m.lease.lastRenew = time.Now()
		done := make(chan struct{})
		go func() { defer close(done); m.run(context.Background()) }()
		// The monitor stops and joins on every exit, so a failed assertion does
		// not leave its goroutine behind.
		defer func() {
			m.stop <- context.Background()
			<-done
		}()
		for range 100 {
			time.Sleep(time.Second) //nolint:forbidigo // Fake clock inside the synctest bubble: it samples the gate each second.
			synctest.Wait()
			if got := m.snapshot(); !got.EngineMayRun {
				t.Fatalf("gate lost the engine after one failed renewal: %+v at renewal %d", got, renewals.Load())
			}
		}
	})
}

// The lease monitor starts a failed acquisition's retry from a real Redis: a
// reply lost after the write applied must not lock the holder out.
func TestLostAcquireReplyIsRecovered(t *testing.T) {
	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	var drop atomic.Bool
	drop.Store(true)
	client.AddHook(lostReplyHook{drop: &drop})
	// Load the script, so the commands reach the hook as EVALSHA.
	if _, err := acquireEngineLease.Load(t.Context(), client).Result(); err != nil {
		t.Fatal(err)
	}
	m := newReplicaMonitor(&censusStub{count: 1}, &redisEngineLease{client: client, id: "process"}, slog.New(slog.DiscardHandler))
	m.update(t.Context())
	if !m.lease.lastRenew.IsZero() {
		t.Fatal("the first acquisition took the lease although its reply was lost")
	}
	if owner, err := client.Get(t.Context(), engineLeaseKey).Result(); err != nil || owner != "process" {
		t.Fatalf("the write did not apply: owner=%q err=%v", owner, err)
	}
	drop.Store(false)
	m.update(t.Context())
	if got := m.snapshot(); got.Reason == "replica_lease_held" {
		t.Fatalf("the holder reads its own key as another process's: gate %+v", got)
	}
	if m.lease.lastRenew.IsZero() {
		t.Fatal("the retry did not take the lease it already holds")
	}
}

// lostReplyHook runs the command, then reports an error as a lost reply would.
type lostReplyHook struct{ drop *atomic.Bool }

func (lostReplyHook) DialHook(next redis.DialHook) redis.DialHook { return next }
func (h lostReplyHook) ProcessHook(next redis.ProcessHook) redis.ProcessHook {
	return func(ctx context.Context, cmd redis.Cmder) error {
		err := next(ctx, cmd)
		if err == nil && h.drop.Load() {
			if c, ok := cmd.(*redis.Cmd); ok && classifyLease(c) == leaseAcquire {
				return errors.New("reply lost")
			}
		}
		return err
	}
}
func (lostReplyHook) ProcessPipelineHook(next redis.ProcessPipelineHook) redis.ProcessPipelineHook {
	return next
}

// Stopping the monitor after the shutdown deadline passed still releases the
// lease: the release has its own bound.
func TestReleaseSurvivesExpiredShutdownContext(t *testing.T) {
	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	m := newReplicaMonitor(&censusStub{count: 1}, &redisEngineLease{client: client, id: "process"}, slog.New(slog.DiscardHandler))
	done := make(chan struct{})
	go func() { defer close(done); m.run(context.Background()) }()
	// The first update acquires; wait for it on the monitor's own state.
	for m.leaseSuccesses() == 0 {
		time.Sleep(time.Millisecond) //nolint:forbidigo // Real time: miniredis answers over loopback.
	}
	expired, cancel := context.WithCancel(context.Background())
	cancel()
	m.stop <- expired
	<-done
	if m.stopErr != nil {
		t.Fatalf("release with an expired shutdown context: %v", m.stopErr)
	}
	if exists, err := client.Exists(t.Context(), engineLeaseKey).Result(); err != nil || exists != 0 {
		t.Fatalf("lease left behind after stop: exists=%d err=%v", exists, err)
	}
}

// Past its deadline the engine's packet path drops both directions before any
// drain, and keeps its GSO and DF support for quic-go.
func TestPacketPathFencedAtLeaseDeadlineBeforeDrain(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := newThreadRig(t, rigOptions{redis: true, enabled: true})
		hold := make(chan struct{})
		r.holdShutdown = hold
		defer shutdownService(t, r.svc)
		defer close(hold)
		r.apply()
		r.sleep(engineLeaseTTL)
		r.engineIs("running", stateOn, "")
		r.mu.Lock()
		conn := r.served
		raw := r.engines[0].conn
		r.mu.Unlock()
		if _, ok := conn.(interface{ SupportsGSO() bool }); !ok || !conn.(interface{ SupportsGSO() bool }).SupportsGSO() {
			t.Fatal("the fence hides GSO support")
		}
		if df, ok := conn.(interface{ SupportsDF() bool }); !ok || !df.SupportsDF() {
			t.Fatal("the fence hides DF support")
		}
		seg := conn.(interface {
			WriteSegments([]byte, net.Addr, uint16) (int, error)
		})
		if _, err := conn.WriteTo([]byte("ok"), nil); err != nil || raw.writes.Load() != 1 {
			t.Fatalf("a write inside the lease: writes=%d err=%v, want 1", raw.writes.Load(), err)
		}
		// Every renewal now fails; the last worked at the 90-second tick.
		r.renewErr.Store(true)
		r.sleep(engineLeaseSelfStop + time.Second)
		// Teardown is stuck in Shutdown: only the fence stands between the engine and the wire.
		if _, err := conn.WriteTo([]byte("late"), nil); err != nil {
			t.Fatal(err)
		}
		if _, err := seg.WriteSegments([]byte("late late"), nil, 4); err != nil {
			t.Fatal(err)
		}
		if raw.writes.Load() != 1 || raw.segments.Load() != 0 {
			t.Fatalf("packets sent past the lease: writes=%d segments=%d, want none beyond the first write", raw.writes.Load(), raw.segments.Load())
		}
		raw.reads <- []byte("late packet")
		got := make(chan struct{})
		go func() {
			buf := make([]byte, 64)
			_, _, _ = conn.ReadFrom(buf)
			close(got)
		}()
		synctest.Wait()
		select {
		case <-got:
			t.Fatal("a packet received past the lease reached the server")
		default:
		}
		close(raw.reads)
		synctest.Wait()
	})
}

// heldReadSettings holds every read, once block is set, until release closes,
// as a database round trip that outlives the shutdown request.
type heldReadSettings struct {
	SettingsStore
	block   atomic.Bool
	entered chan struct{}
	release chan struct{}
}

func (b *heldReadSettings) GetAll(ctx context.Context) (map[string]string, error) {
	if b.block.Load() {
		select {
		case b.entered <- struct{}{}:
		default:
		}
		<-b.release
	}
	return b.SettingsStore.GetAll(ctx)
}

// Shutdown waits for a background settings reread that is inside the store: the
// reread a Status call starts, and the one an unexpected Serve exit starts.
func TestShutdownJoinsBackgroundSettingsRereads(t *testing.T) {
	for _, tc := range []struct {
		name    string
		trigger func(t *testing.T, f *serveExitFixture)
	}{
		{"status reread", func(t *testing.T, f *serveExitFixture) {
			f.sleep(settingsRecheck + time.Second)
			if _, err := f.svc.Status(t.Context(), false); err != nil {
				t.Fatal(err)
			}
		}},
		{"serve exit reread", func(_ *testing.T, f *serveExitFixture) { f.exitServe(1) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				f := newServeExitFixture(t, serveExitOptions{})
				f.settingsChanged()
				gate := &heldReadSettings{SettingsStore: f.store, entered: make(chan struct{}, 1), release: make(chan struct{})}
				f.svc.cfg.Settings = gate
				gate.block.Store(true)
				// A failed assertion must still free the held read, or the bubble
				// ends with a goroutine blocked inside the store.
				release := sync.OnceFunc(func() { close(gate.release) })
				defer release()
				tc.trigger(t, f)
				synctest.Wait()
				select {
				case <-gate.entered:
				default:
					t.Fatal("no background reread reached the store")
				}
				done := make(chan error, 1)
				go func() { done <- f.svc.Shutdown(context.Background()) }()
				synctest.Wait()
				select {
				case <-done:
					t.Fatal("Shutdown returned while a background reread was still inside the store")
				default:
				}
				release()
				synctest.Wait()
				select {
				case err := <-done:
					if err != nil {
						t.Fatalf("Shutdown: %v", err)
					}
				default:
					t.Fatal("Shutdown did not return after the reread finished")
				}
			})
		})
	}
}

// leaseHookMonitor is a monitor over a scripted Redis whose every lease
// operation answers 1 unless fail or delay says otherwise.
func leaseHookMonitor(t *testing.T, fail func(leaseOp) bool, delay func(leaseOp) time.Duration) *replicaMonitor {
	t.Helper()
	client := redis.NewClient(&redis.Options{Addr: "127.0.0.1:6379"})
	t.Cleanup(func() { _ = client.Close() })
	client.AddHook(ctxCommandHook{process: func(_ context.Context, cmd redis.Cmder) error {
		c := cmd.(*redis.Cmd)
		op := classifyLease(c)
		if d := delay(op); d > 0 {
			time.Sleep(d) //nolint:forbidigo // Fake clock inside the synctest bubble: it stands for the Redis round trip.
		}
		if fail(op) {
			return errors.New("lease operation failed")
		}
		c.SetVal(int64(1))
		return nil
	}})
	return newReplicaMonitor(&censusStub{count: 1}, &redisEngineLease{client: client, id: "process"}, slog.New(slog.DiscardHandler))
}

// A fresh grant waits 90 seconds from the first acquisition Redis confirmed,
// not from an earlier attempt that failed: a failed attempt says nothing about
// when the former holder's key was lost, so it cannot date the hold.
func TestFreshGrantHoldCountsFromTheConfirmedAcquisition(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var failAcquire atomic.Bool
		failAcquire.Store(true)
		m := leaseHookMonitor(t,
			func(op leaseOp) bool { return op == leaseAcquire && failAcquire.Load() },
			func(leaseOp) time.Duration { return 0 })
		for range 19 {
			if !m.update(t.Context()) {
				t.Fatal("a failed acquisition must ask for a retry")
			}
			time.Sleep(leaseRetryInterval) //nolint:forbidigo // Fake clock inside the synctest bubble: it spaces the failed attempts.
		}
		if got := m.snapshot(); got.EngineMayRun || got.Reason != "replica_lease_pending" {
			t.Fatalf("gate after 95 seconds of failed acquisitions = %+v, want pending", got)
		}
		failAcquire.Store(false)
		if m.update(t.Context()) {
			t.Fatal("the first acquisition after the failures did not succeed")
		}
		if got := m.snapshot(); got.EngineMayRun || got.Reason != "replica_lease_pending" {
			t.Fatalf("gate right after the first confirmed acquisition = %+v, want the fresh 90-second hold", got)
		}
		// Renewals every 30 seconds keep the grant held through the hold.
		for range 2 {
			time.Sleep(censusInterval) //nolint:forbidigo // Fake clock inside the synctest bubble: it crosses the renewal cadence.
			m.update(t.Context())
		}
		time.Sleep(engineLeaseTTL - 2*censusInterval - time.Second) //nolint:forbidigo // Fake clock inside the synctest bubble: it stops 1 second short of the hold's end.
		if got := m.snapshot(); got.EngineMayRun {
			t.Fatalf("gate 1 second before the hold ends = %+v, want pending", got)
		}
		time.Sleep(time.Second) //nolint:forbidigo // Fake clock inside the synctest bubble: it reaches the hold's end.
		if got := m.snapshot(); !got.EngineMayRun || !got.Available {
			t.Fatalf("gate when the hold ends = %+v, want the engine allowed", got)
		}
	})
}

// A renewal that completes after the grant's local deadline cannot extend
// the grant: the engine's packet path was cut at the deadline, so the success
// opens a new grant with its own hold, and the old engine's fence stays shut
// whether or not a packet had already latched it.
func TestLateRenewalOpensANewGrant(t *testing.T) {
	for _, tc := range []struct {
		name string
		// idle is how long the grant sits before the renewal is sent, and delay is
		// how long Redis takes to answer it.
		idle, delay time.Duration
		// latch sends a packet after the deadline and before the renewal, which
		// shuts the old engine's fence for good.
		latch bool
	}{
		{"sent after the deadline", engineLeaseSelfStop + time.Second, 0, false},
		{"answered after the deadline", engineLeaseSelfStop - time.Second, 1500 * time.Millisecond, false},
		{"after the fence latched", engineLeaseSelfStop + time.Second, 0, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				var slow atomic.Bool
				m := leaseHookMonitor(t,
					func(leaseOp) bool { return false },
					func(op leaseOp) time.Duration {
						if op == leaseRenew && slow.Load() {
							return tc.delay
						}
						return 0
					})
				m.update(t.Context())
				for range 3 {
					time.Sleep(censusInterval) //nolint:forbidigo // Fake clock inside the synctest bubble: it crosses the renewal cadence.
					m.update(t.Context())
				}
				if got := m.snapshot(); !got.EngineMayRun {
					t.Fatalf("the first grant did not mature: %+v", got)
				}
				oldGrant, _ := m.window()
				raw := &rigPacketConn{}
				conn := fenceFor(raw, m, oldGrant)
				time.Sleep(tc.idle) //nolint:forbidigo // Fake clock inside the synctest bubble: it ages the grant to its deadline.
				if tc.latch {
					if _, err := conn.WriteTo([]byte("after the deadline"), nil); err != nil {
						t.Fatal(err)
					}
				}
				slow.Store(true)
				m.update(t.Context())
				if _, err := conn.WriteTo([]byte("after the deadline"), nil); err != nil {
					t.Fatal(err)
				}
				if got := raw.writes.Load(); got != 0 {
					t.Fatalf("the packet path of an expired grant forwarded %d writes after a late renewal, want 0", got)
				}
				if grant, _ := m.window(); grant == oldGrant {
					t.Fatalf("the late renewal kept grant %d, want a new grant", grant)
				}
				if got := m.snapshot(); got.EngineMayRun || got.Reason != "replica_lease_pending" {
					t.Fatalf("gate after the late renewal = %+v, want the new grant's hold", got)
				}
			})
		})
	}
}

// A disable before the HTTP handler is ready stops the monitor and releases the
// lease, and a re-enable before readiness waits a full hold.
func TestDisableBeforeHandlerReadyStopsTheMonitor(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := newThreadRig(t, rigOptions{redis: true, enabled: true})
		synctest.Wait()
		r.svc.cfg.AwaitHandler = true
		defer shutdownService(t, r.svc)
		r.apply()
		if r.commands.Load() == 0 || r.calls.Load() != 0 {
			t.Fatalf("with the handler held: redis commands=%d, engine starts=%d, want a running monitor and no engine", r.commands.Load(), r.calls.Load())
		}
		r.sleep(10 * time.Second)
		r.store.set("direct_connections.enabled", "false")
		r.apply()
		if got := r.releases.Load(); got != 1 {
			t.Fatalf("lease releases after disabling before the handler is ready = %d, want 1", got)
		}
		before := r.commands.Load()
		r.sleep(censusInterval + time.Second)
		if n := r.commands.Load() - before; n != 0 {
			t.Fatalf("%d Redis commands after disabling before the handler is ready, want 0", n)
		}
		r.store.set("direct_connections.enabled", "true")
		r.apply()
		r.sleep(engineLeaseTTL - time.Second)
		r.svc.HandlerReady()
		synctest.Wait()
		if r.calls.Load() != 0 {
			t.Fatalf("engine started inside the re-enable's hold: starts=%d, want 0", r.calls.Load())
		}
		r.sleep(time.Second)
		if r.calls.Load() != 1 {
			t.Fatalf("engine starts when the re-enable's hold ends = %d, want 1", r.calls.Load())
		}
	})
}

// A quick re-enable, inside the old grant's 60-second window, still waits a
// fresh hold: halt resets the gate.
func TestQuickReenableWaitsTheHold(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := newThreadRig(t, rigOptions{redis: true, enabled: true, rtt: time.Millisecond})
		defer shutdownService(t, r.svc)
		r.apply()
		r.sleep(engineLeaseTTL + time.Second)
		r.engineIs("first grant", stateOn, "")
		r.store.set("direct_connections.enabled", "false")
		r.apply()
		r.store.set("direct_connections.enabled", "true")
		r.apply()
		if n := r.calls.Load(); n != 1 {
			t.Fatalf("engine starts right after a quick re-enable = %d, want 1: the re-enable skipped the hold", n)
		}
		r.sleep(engineLeaseTTL - 2*time.Second)
		if n := r.calls.Load(); n != 1 {
			t.Fatalf("engine starts inside the re-enable's hold = %d, want 1", n)
		}
	})
}

// A grant lost and reacquired inside one update moves the deadline forward; the
// old engine's fence still closes on the grant change while its teardown is stuck.
func TestFenceClosesOnGrantChange(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := newThreadRig(t, rigOptions{redis: true, enabled: true})
		hold := make(chan struct{})
		r.holdShutdown = hold
		defer shutdownService(t, r.svc)
		defer close(hold)
		r.apply()
		r.sleep(engineLeaseTTL)
		r.engineIs("running", stateOn, "")
		r.mu.Lock()
		conn := r.served
		raw := r.engines[0].conn
		r.mu.Unlock()
		r.renewGone.Store(true)
		r.sleep(censusInterval)
		r.renewGone.Store(false)
		if _, err := conn.WriteTo([]byte("late"), nil); err != nil {
			t.Fatal(err)
		}
		if raw.writes.Load() != 0 {
			t.Fatalf("old engine wrote %d packets after its grant ended, want 0", raw.writes.Load())
		}
		close(raw.reads)
		synctest.Wait()
	})
}
