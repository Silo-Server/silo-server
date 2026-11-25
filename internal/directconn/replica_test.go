package directconn

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/darthShadow/shunpike/engine"
	"github.com/redis/go-redis/v9"

	"github.com/Silo-Server/silo-server/internal/cache"
	"github.com/Silo-Server/silo-server/internal/tuning"
)

func TestReplicaServiceGate(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		client := redis.NewClient(&redis.Options{Addr: "127.0.0.1:6379"})
		t.Cleanup(func() { _ = client.Close() })
		var count atomic.Int32
		count.Store(2)
		client.AddHook(leaseCommandHook{process: func(cmd redis.Cmder) error {
			if cmd, ok := cmd.(*redis.Cmd); ok {
				if op := classifyLease(cmd); op == leaseCensusCount || op == leaseCensusTouch {
					cmd.SetVal(int64(count.Load()))
				} else {
					cmd.SetVal(int64(1))
				}
			}
			return nil
		}})
		var logs bytes.Buffer
		cfg := testConfig(&memorySettings{values: map[string]string{"direct_connections.enabled": "true"}})
		cfg.Tuning, _ = tuning.Parse(func(string) string { return "" })
		cfg.SecretKey = bytes.Repeat([]byte{0x41}, 32)
		cfg.newEngine = func(_ context.Context, c engine.Config) (engineHandle, error) {
			return &lifecycleEngine{port: c.Port, changed: make(chan struct{}, 1), events: new([]string), eventsMu: new(sync.Mutex)}, nil
		}
		cfg.Logger = slog.New(slog.NewTextHandler(&logs, nil))
		cfg.ReplicaPresence = cache.NewAPIReplicaPresence(client, "api")
		cfg.RedisClient = client
		svc := New(ctx, cfg)
		synctest.Wait()
		// The first status read starts the census, which runs only while the feature is on.
		if _, err := svc.Status(t.Context(), false); err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		status, err := svc.Status(t.Context(), false)
		if err != nil {
			t.Fatal(err)
		}
		got := feature(t, status.Features, featureDirect)
		if got.State != stateOff || got.Reason != "multiple_api_replicas" || svc.ReplicaGate().EngineMayRun {
			t.Fatalf("multiple replicas: feature = %+v, gate = %+v", got, svc.ReplicaGate())
		}
		if !strings.Contains(logs.String(), "reason=multiple_api_replicas") {
			t.Fatalf("missing gate reason in feature log: %s", logs.String())
		}
		count.Store(1)
		// The next census sees one replica and acquires; the fresh grant's hold ends
		// 90 seconds after that.
		time.Sleep(censusInterval + engineLeaseTTL) //nolint:forbidigo // Advance synctest virtual time past the grant's hold, then wait for publication.
		synctest.Wait()
		status, err = svc.Status(t.Context(), false)
		if err != nil {
			t.Fatal(err)
		}
		got = feature(t, status.Features, featureDirect)
		if got.State != stateOn || got.Reason != "" || !svc.ReplicaGate().EngineMayRun {
			t.Fatalf("single replica: feature = %+v, gate = %+v", got, svc.ReplicaGate())
		}
		if !strings.Contains(logs.String(), "feature=direct kind=local state=on") {
			t.Fatalf("missing recovery in feature log: %s", logs.String())
		}
	})
}

func TestReplicaGate(t *testing.T) {
	censusErr := errors.New("census unavailable")
	for _, row := range []struct {
		name          string
		redis         bool
		count         int
		err           error
		held, pending bool
		want          ReplicaGate
	}{
		{"no redis", false, 0, censusErr, false, false, ReplicaGate{true, true, ""}},
		{"single holder", true, 1, nil, true, false, ReplicaGate{true, true, ""}},
		{"single free", true, 1, nil, false, true, ReplicaGate{false, false, "replica_lease_pending"}},
		{"single previous holder", true, 1, nil, false, false, ReplicaGate{false, false, "replica_lease_held"}},
		{"zero holder", true, 0, nil, true, false, ReplicaGate{true, true, ""}},
		{"zero free", true, 0, nil, false, true, ReplicaGate{false, false, "replica_lease_pending"}},
		{"zero previous holder", true, 0, nil, false, false, ReplicaGate{false, false, "replica_lease_held"}},
		{"multiple holder", true, 2, nil, true, false, ReplicaGate{true, false, "multiple_api_replicas"}},
		{"multiple nonholder", true, 2, nil, false, false, ReplicaGate{false, false, "multiple_api_replicas"}},
		{"census error holder", true, 1, censusErr, true, false, ReplicaGate{true, false, "replica_census_unavailable"}},
		{"census error nonholder", true, 1, censusErr, false, false, ReplicaGate{false, false, "replica_census_unavailable"}},
	} {
		t.Run(row.name, func(t *testing.T) {
			if got := replicaGate(row.redis, row.count, row.err, row.held, row.pending); got != row.want {
				t.Fatalf("gate = %+v, want %+v", got, row.want)
			}
		})
	}
}

type censusStub struct {
	mu        sync.Mutex
	count     int
	err       error
	refreshes int
}

func (c *censusStub) Count(context.Context) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.count, c.err
}
func (c *censusStub) Refresh(context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.refreshes++
	c.count = 1
	return nil
}

func TestReplicaCensusRefreshAndSelfStop(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		client := redis.NewClient(&redis.Options{Addr: "127.0.0.1:6379"})
		t.Cleanup(func() { _ = client.Close() })
		var unavailable atomic.Bool
		client.AddHook(leaseCommandHook{process: func(cmd redis.Cmder) error {
			if unavailable.Load() {
				return errors.New("redis down")
			}
			if cmd, ok := cmd.(*redis.Cmd); ok {
				cmd.SetVal(int64(1))
			}
			return nil
		}})
		presence := &censusStub{}
		monitor := newReplicaMonitor(presence, &redisEngineLease{client: client, id: "process"}, slog.New(slog.DiscardHandler))
		done := make(chan struct{})
		go func() { defer close(done); monitor.run(ctx) }()
		synctest.Wait()
		if got := monitor.snapshot(); got.EngineMayRun || got.Reason != "replica_lease_pending" {
			t.Fatalf("gate during a fresh grant's hold = %+v, want a pending lease", got)
		}
		time.Sleep(engineLeaseTTL) //nolint:forbidigo // Advance synctest virtual time past the fresh grant's hold.
		synctest.Wait()
		if got := monitor.snapshot(); !got.EngineMayRun || !got.Available {
			t.Fatalf("initial gate = %+v", got)
		}
		presence.mu.Lock()
		if presence.refreshes != 1 {
			t.Errorf("zero-count refreshes = %d, want 1", presence.refreshes)
		}
		presence.err = errors.New("census unavailable")
		presence.mu.Unlock()
		unavailable.Store(true)
		time.Sleep(30 * time.Second) //nolint:forbidigo // Advance synctest virtual time to the first renewal deadline.
		synctest.Wait()
		if got := monitor.snapshot(); !got.EngineMayRun || got.Available || got.Reason != "replica_census_unavailable" {
			t.Fatalf("30s gate = %+v", got)
		}
		time.Sleep(30 * time.Second) //nolint:forbidigo // Advance synctest virtual time to the 60-second self-stop deadline.
		synctest.Wait()
		if got := monitor.snapshot(); got.EngineMayRun {
			t.Fatalf("60s gate = %+v, must stop before TTL", got)
		}
		monitor.stop <- context.Background()
		<-done
	})
}

func TestAPIReplicaRefreshWritesOnce(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		client := redis.NewClient(&redis.Options{Addr: "127.0.0.1:6379"})
		t.Cleanup(func() { _ = client.Close() })
		var writes atomic.Int32
		client.AddHook(leaseCommandHook{process: func(cmd redis.Cmder) error {
			if cmd.Name() == "evalsha" {
				writes.Add(1)
				cmd.(*redis.Cmd).SetVal(int64(1))
			}
			return nil
		}})
		presence := cache.NewAPIReplicaPresence(client, "api")
		if err := presence.Refresh(t.Context()); err != nil {
			t.Fatal(err)
		}
		time.Sleep(31 * time.Second) //nolint:forbidigo // Advance synctest virtual time past one renewal interval to detect an extra goroutine.
		synctest.Wait()
		if got := writes.Load(); got != 1 {
			t.Fatalf("Refresh writes = %d, want one with no renewal goroutine", got)
		}
		var absent *cache.APIReplicaPresence
		if err := absent.Refresh(context.Background()); err != nil {
			t.Fatal(err)
		}
	})
}

// slowCensus answers after d on the fake clock, failing like Redis when its
// context ended first.
type slowCensus struct{ d time.Duration }

func (c slowCensus) Count(ctx context.Context) (int, error) {
	time.Sleep(c.d) //nolint:forbidigo // Fake clock inside the synctest bubble: it stands for a long census scan.
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	return 1, nil
}
func (slowCensus) Refresh(context.Context) error { return nil }

// contextLeaseHook answers lease commands after rtt on the fake clock, failing
// like go-redis when the command's context has ended.
type contextLeaseHook struct{ rtt time.Duration }

func (contextLeaseHook) DialHook(next redis.DialHook) redis.DialHook { return next }
func (h contextLeaseHook) ProcessHook(redis.ProcessHook) redis.ProcessHook {
	return func(ctx context.Context, cmd redis.Cmder) error {
		if err := ctx.Err(); err != nil {
			cmd.SetErr(err)
			return err
		}
		time.Sleep(h.rtt) //nolint:forbidigo // Fake clock inside the synctest bubble: it stands for the Redis round trip.
		if err := ctx.Err(); err != nil {
			cmd.SetErr(err)
			return err
		}
		if cmd, ok := cmd.(*redis.Cmd); ok {
			cmd.SetVal(int64(1))
		}
		return nil
	}
}
func (contextLeaseHook) ProcessPipelineHook(next redis.ProcessPipelineHook) redis.ProcessPipelineHook {
	return next
}

// The lease renewal has its own budget: a census that uses nearly all of its own
// 2 s must not leave the renewal an ended context and stop a healthy holder.
func TestReplicaCensusDoesNotStarveLeaseRenewal(t *testing.T) {
	for _, tc := range []struct {
		name    string
		census  time.Duration
		wantRun bool
	}{
		{"fast census", 10 * time.Millisecond, true},
		{"census near its budget", 1999 * time.Millisecond, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				client := redis.NewClient(&redis.Options{Addr: "127.0.0.1:6379", Dialer: func(context.Context, string, string) (net.Conn, error) {
					return nil, errors.New("unexpected network call")
				}})
				t.Cleanup(func() { _ = client.Close() })
				client.AddHook(contextLeaseHook{rtt: 2 * time.Millisecond})
				m := newReplicaMonitor(slowCensus{d: tc.census}, &redisEngineLease{client: client, id: "p"}, slog.New(slog.DiscardHandler))
				// Seed a held lease, as a successful earlier acquisition leaves it.
				m.lease.lastRenew = time.Now()
				done := make(chan struct{})
				go func() { defer close(done); m.run(context.Background()) }()
				time.Sleep(61 * time.Second) //nolint:forbidigo // Advance synctest virtual time past the 60-second self-stop.
				synctest.Wait()
				if got := m.snapshot(); got.EngineMayRun != tc.wantRun {
					t.Errorf("EngineMayRun after 61s = %v, want %v (gate %+v)", got.EngineMayRun, tc.wantRun, got)
				}
				m.stop <- context.Background()
				<-done
			})
		})
	}
}

// ctxCommandHook runs process with the command's context.
type ctxCommandHook struct {
	process func(context.Context, redis.Cmder) error
}

func (ctxCommandHook) DialHook(next redis.DialHook) redis.DialHook { return next }
func (h ctxCommandHook) ProcessHook(redis.ProcessHook) redis.ProcessHook {
	return h.process
}
func (ctxCommandHook) ProcessPipelineHook(next redis.ProcessPipelineHook) redis.ProcessPipelineHook {
	return next
}

// Shutdown ends a census that is in flight when it stops the monitor, instead of
// waiting for that census to end by itself.
func TestShutdownInterruptsInFlightReplicaUpdate(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var stall, stalled atomic.Bool
		client := redis.NewClient(&redis.Options{Addr: "127.0.0.1:6379"})
		t.Cleanup(func() { _ = client.Close() })
		client.AddHook(ctxCommandHook{process: func(ctx context.Context, cmd redis.Cmder) error {
			if cmd, ok := cmd.(*redis.Cmd); ok {
				if op := classifyLease(cmd); stall.Load() && (op == leaseCensusCount || op == leaseCensusTouch) {
					stalled.Store(true)
					<-ctx.Done()
					cmd.SetErr(ctx.Err())
					return ctx.Err()
				}
				cmd.SetVal(int64(1))
			}
			return nil
		}})
		// The census and lease run only while direct connections are on.
		cfg := testConfig(&memorySettings{values: map[string]string{"direct_connections.enabled": "true"}})
		cfg.RedisClient = client
		cfg.ReplicaPresence = cache.NewAPIReplicaPresence(client, "api")
		svc := New(t.Context(), cfg)
		// New reads no settings; this first read starts the monitor and its census.
		if err := svc.SettingsChanged(t.Context()); err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		stall.Store(true)
		time.Sleep(censusInterval) //nolint:forbidigo // Advance synctest virtual time to the next census, which stalls.
		synctest.Wait()
		if !stalled.Load() {
			t.Fatal("no census reached the stalled Redis call before Shutdown")
		}
		start := time.Now()
		shutdownService(t, svc)
		if elapsed := time.Since(start); elapsed != 0 {
			t.Fatalf("Shutdown waited %s for the in-flight census, want it ended at once", elapsed)
		}
	})
}

// Redis that is configured but unusable never reads as a deployment without
// Redis: the gate fails closed and the engine stays off.
func TestReplicaGateFailsClosedWhenRedisIsConfiguredButUnusable(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(t *testing.T, cfg *Config)
	}{
		{"client failed to build", func(_ *testing.T, cfg *Config) { cfg.RedisUnavailable = true }},
		{"presence has no identity", func(t *testing.T, cfg *Config) {
			client := redis.NewClient(&redis.Options{Addr: "127.0.0.1:6379"})
			t.Cleanup(func() { _ = client.Close() })
			client.AddHook(leaseCommandHook{process: func(cmd redis.Cmder) error {
				t.Errorf("Redis command %s ran without a presence identity", cmd.Name())
				return errors.New("unexpected command")
			}})
			cfg.RedisClient = client
			cfg.ReplicaPresence = cache.NewAPIReplicaPresence(client, "")
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				cfg := testConfig(&memorySettings{values: map[string]string{"direct_connections.enabled": "true"}})
				cfg.SecretKey = bytes.Repeat([]byte{0x41}, 32)
				cfg.Tuning, _ = tuning.Parse(func(string) string { return "" })
				var starts atomic.Int32
				cfg.newEngine = func(context.Context, engine.Config) (engineHandle, error) {
					starts.Add(1)
					return nil, errors.New("engine must not start")
				}
				tc.setup(t, &cfg)
				svc := New(t.Context(), cfg)
				defer shutdownService(t, svc)
				if err := svc.SettingsChanged(t.Context()); err != nil {
					t.Fatal(err)
				}
				synctest.Wait()
				time.Sleep(censusInterval + time.Second) //nolint:forbidigo // Advance synctest virtual time past a census interval.
				synctest.Wait()
				if want := (ReplicaGate{false, false, "replica_census_unavailable"}); svc.ReplicaGate() != want {
					t.Fatalf("gate = %+v, want %+v", svc.ReplicaGate(), want)
				}
				if got := svc.engineFeature(); starts.Load() != 0 || got.State != stateOff || got.Reason != "replica_census_unavailable" {
					t.Fatalf("starts=%d feature=%+v, want 0 and off/replica_census_unavailable", starts.Load(), got)
				}
			})
		})
	}
}
