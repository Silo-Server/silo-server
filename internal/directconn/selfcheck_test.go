package directconn

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/Silo-Server/shunpike/engine"

	"github.com/Silo-Server/silo-server/internal/tuning"
)

type memorySettings struct {
	mu     sync.Mutex
	values map[string]string
}

func (m *memorySettings) GetAll(context.Context) (map[string]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make(map[string]string, len(m.values))
	for k, v := range m.values {
		out[k] = v
	}
	return out, nil
}
func (m *memorySettings) set(k, v string) { m.mu.Lock(); defer m.mu.Unlock(); m.values[k] = v }
func testConfig(store *memorySettings) Config {
	return Config{Settings: store, Logger: slog.New(slog.DiscardHandler),
		Capabilities: func(context.Context, *slog.Logger) (engine.CapabilityReport, error) {
			return engine.CapabilityReport{Platform: "linux"}, nil
		},
		NAT: func(context.Context, *slog.Logger, []string) (engine.NATReport, error) {
			return engine.NATReport{}, nil
		},
		PortMap: func(context.Context, *slog.Logger) (engine.PortMappingReport, error) {
			return engine.PortMappingReport{}, nil
		},
	}
}
func refreshStatus(t *testing.T, svc *Service) <-chan error {
	t.Helper()
	done := make(chan error, 1)
	go func() {
		_, err := svc.Status(t.Context(), true)
		done <- err
	}()
	return done
}

func feature(t *testing.T, fs []Feature, name string) Feature {
	t.Helper()
	for _, f := range fs {
		if f.Feature == name {
			return f
		}
	}
	t.Fatalf("missing feature %s", name)
	return Feature{}
}

func TestSelfCheckNetworkProbesFollowToggle(t *testing.T) {
	for _, tc := range []struct {
		name             string
		enabled, portmap bool
		nat, pm          int
	}{
		{"off", false, true, 0, 0}, {"on", true, true, 1, 1}, {"mapping off", true, false, 1, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := &memorySettings{values: map[string]string{"direct_connections.enabled": "false", "direct_connections.port_mapping": "true"}}
			if tc.enabled {
				store.set("direct_connections.enabled", "true")
			}
			if !tc.portmap {
				store.set("direct_connections.port_mapping", "false")
			}
			var logs bytes.Buffer
			cfg := testConfig(store)
			if !tc.enabled {
				cfg.Capabilities = func(context.Context, *slog.Logger) (engine.CapabilityReport, error) {
					return engine.CapabilityReport{Platform: "darwin"}, nil
				}
			}
			cfg.Logger = slog.New(slog.NewJSONHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
			n, p := 0, 0
			cfg.NAT = func(context.Context, *slog.Logger, []string) (engine.NATReport, error) {
				n++
				return engine.NATReport{}, nil
			}
			cfg.PortMap = func(context.Context, *slog.Logger) (engine.PortMappingReport, error) {
				p++
				return engine.PortMappingReport{}, nil
			}
			svc := New(t.Context(), cfg)
			status, err := svc.Status(t.Context(), true)
			if err != nil {
				t.Fatal(err)
			}
			if n != tc.nat || p != tc.pm {
				t.Fatalf("probe calls NAT=%d PM=%d, want %d %d", n, p, tc.nat, tc.pm)
			}
			fs := status.Features
			if !tc.enabled {
				var off struct{ Level, Feature, State, Reason string }
				if err := json.Unmarshal(bytes.Split(logs.Bytes(), []byte("\n"))[0], &off); err != nil {
					t.Fatal(err)
				}
				if off.Level != "INFO" || off.Feature != "direct" || off.State != "off" || off.Reason != "disabled" {
					t.Fatalf("disabled Info record=%+v", off)
				}
				for _, f := range fs {
					if f.Kind == "network" && (f.State != "off" || f.Reason != "disabled") {
						t.Fatalf("off network row %+v", f)
					}
				}
			}
			wantInfo := len(fs)
			if !tc.enabled {
				wantInfo = 1
			}
			if got := strings.Count(logs.String(), `"level":"INFO"`); got != wantInfo {
				t.Fatalf("Info records %d, want %d: %s", got, wantInfo, &logs)
			}
			if !tc.portmap && feature(t, fs, "port_mapping").Reason != "admin_off" {
				t.Fatal("mapping not disabled")
			}
			if !tc.enabled {
				logs.Reset()
				store.set("direct_connections.enabled", "true")
				next, err := svc.Status(t.Context(), true)
				if err != nil {
					t.Fatal(err)
				}
				if got := strings.Count(logs.String(), `"level":"INFO"`); got != len(next.Features) {
					t.Fatalf("enable logs=%d want %d", got, len(next.Features))
				}
			}
			logs.Reset()
			if _, err := svc.Status(t.Context(), true); err != nil {
				t.Fatal(err)
			}
			if logs.Len() != 0 {
				t.Fatalf("unchanged run logged %s", &logs)
			}
		})
	}
}

func TestSelfCheckRefreshCoalesces(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		store := &memorySettings{values: map[string]string{"direct_connections.enabled": "true"}}
		cfg := testConfig(store)
		entered := make(chan string, 4)
		release := make(chan struct{})
		var releaseOnce sync.Once
		releaseProbes := func() { releaseOnce.Do(func() { close(release) }) }
		defer releaseProbes()
		var calls atomic.Int32
		cfg.NAT = func(ctx context.Context, _ *slog.Logger, servers []string) (engine.NATReport, error) {
			calls.Add(1)
			entered <- servers[0]
			select {
			case <-release:
			case <-ctx.Done():
			}
			return engine.NATReport{}, ctx.Err()
		}
		svc := New(t.Context(), cfg)
		first := refreshStatus(t, svc)
		<-entered
		second := refreshStatus(t, svc)
		synctest.Wait()
		if calls.Load() != 1 {
			t.Fatalf("concurrent calls=%d", calls.Load())
		}
		store.set("direct_connections.stun_servers", "first.example:3478")
		if err := svc.SettingsChanged(t.Context()); err != nil {
			t.Fatal(err)
		}
		store.set("direct_connections.stun_servers", "latest.example:3478")
		if err := svc.SettingsChanged(t.Context()); err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		if calls.Load() != 1 {
			t.Fatal("overlapping run")
		}
		releaseProbes()
		if err := <-first; err != nil {
			t.Fatal(err)
		}
		if err := <-second; err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		if calls.Load() != 2 {
			t.Fatalf("follow-up calls=%d, want 2", calls.Load())
		}
		if got := <-entered; got != "latest.example:3478" {
			t.Fatalf("follow-up STUN=%q", got)
		}
		if err := svc.SettingsChanged(t.Context()); err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		if calls.Load() != 2 {
			t.Fatal("duplicate signal ran probes")
		}
		store.set("direct_connections.stun_servers", "changed.example:3478")
		if err := svc.SettingsChanged(t.Context()); err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		if calls.Load() != 3 {
			t.Fatal("settings change without read did not run")
		}
		store.set("direct_connections.enabled", "false")
		if err := svc.SettingsChanged(t.Context()); err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		status, err := svc.Status(t.Context(), false)
		if err != nil {
			t.Fatal(err)
		}
		if status.Enabled || feature(t, status.Features, "nat_mapping").Reason != "disabled" || calls.Load() != 3 {
			t.Fatalf("toggle off snapshot=%+v calls=%d", status, calls.Load())
		}
		store.set("direct_connections.enabled", "true")
		requestCtx, cancelRequest := context.WithCancel(t.Context())
		if err := svc.SettingsChanged(requestCtx); err != nil {
			t.Fatal(err)
		}
		cancelRequest()
		synctest.Wait()
		status, err = svc.Status(t.Context(), false)
		if err != nil {
			t.Fatal(err)
		}
		if !status.Enabled || calls.Load() != 4 {
			t.Fatalf("toggle on snapshot=%+v calls=%d", status, calls.Load())
		}
	})
}

func TestSelfCheckRunBound(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		store := &memorySettings{values: map[string]string{"direct_connections.enabled": "true"}}
		cfg := testConfig(store)
		cfg.NAT = func(ctx context.Context, _ *slog.Logger, _ []string) (engine.NATReport, error) {
			<-ctx.Done()
			return engine.NATReport{}, ctx.Err()
		}
		start := time.Now()
		status, err := New(t.Context(), cfg).Status(t.Context(), true)
		if err != nil {
			t.Fatal(err)
		}
		if got := time.Since(start); got != 15*time.Second {
			t.Fatalf("duration=%s want 15s", got)
		}
		f := feature(t, status.Features, "nat_mapping")
		if f.State != "degraded" || f.Reason != "probe_failed" {
			t.Fatalf("timeout=%+v", f)
		}
	})
}

// A first Status with no snapshot reads settings under the caller's context;
// a stalled read must end at the check bound, not wait for the request.
func TestStatusFirstReadBound(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		store := &blockingSettings{memorySettings: &memorySettings{values: map[string]string{"direct_connections.enabled": "true"}}, entered: make(chan struct{}, 1), release: make(chan struct{})}
		store.block.Store(true)
		defer close(store.release)
		cfg := testConfig(store.memorySettings)
		cfg.Settings = store
		start := time.Now()
		_, err := New(t.Context(), cfg).Status(t.Context(), false)
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("status error = %v, want deadline exceeded", err)
		}
		if got := time.Since(start); got != checkTimeout {
			t.Fatalf("duration=%s want %s", got, checkTimeout)
		}
	})
}

// SettingsChanged with a context that never ends must still release the
// settings gate at the check bound, so one stalled read cannot wedge every
// later reader.
func TestSettingsChangedReadBound(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		store := &blockingSettings{memorySettings: &memorySettings{values: map[string]string{"direct_connections.enabled": "true"}}, entered: make(chan struct{}, 2), release: make(chan struct{})}
		store.block.Store(true)
		defer close(store.release)
		cfg := testConfig(store.memorySettings)
		cfg.Settings = store
		svc := New(t.Context(), cfg)
		start := time.Now()
		err := svc.SettingsChanged(context.Background())
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("settings change error = %v, want deadline exceeded", err)
		}
		if got := time.Since(start); got != checkTimeout {
			t.Fatalf("duration=%s want %s", got, checkTimeout)
		}
		if n := len(svc.settingsGate); n != 0 {
			t.Fatalf("settings gate still held after the bound (%d)", n)
		}
		store.block.Store(false)
		if err := svc.SettingsChanged(context.Background()); err != nil {
			t.Fatalf("settings change after release: %v", err)
		}
	})
}

// failingSettings fails its next failReads reads, then reads normally. reads
// counts every call.
type failingSettings struct {
	*memorySettings
	failReads atomic.Int32
	reads     atomic.Int32 // every read that reached the store, failed or not
}

var errSettingsRead = errors.New("settings read failed")

func (f *failingSettings) GetAll(ctx context.Context) (map[string]string, error) {
	f.reads.Add(1)
	if f.failReads.Load() > 0 {
		f.failReads.Add(-1)
		return nil, errSettingsRead
	}
	return f.memorySettings.GetAll(ctx)
}

// A failed settings read leaves a pending flag. The next Status without refresh
// reads again; a Status with a completed snapshot returns that snapshot at once
// and starts a check of the new values, which the following read returns. While
// the reread keeps failing, the snapshot stands and the flag stays set.
func TestStatusRetriesFailedSettingsRead(t *testing.T) {
	t.Run("no snapshot", retryNoSnapshot)
	// The reread of a call with a snapshot runs in the background; the bubble's
	// fake clock lets settleRetryCheck wait for it without sleeping.
	t.Run("snapshot", func(t *testing.T) { synctest.Test(t, retrySnapshot) })
	// A pending reread that finds the values unchanged starts no check.
	t.Run("unchanged values", func(t *testing.T) { synctest.Test(t, retryUnchangedValues) })
}

// newRetryService returns a service whose settings store fails reads on demand
// and a function that reports how many NAT probes ran and the servers of the last.
func newRetryService(t *testing.T) (*Service, *failingSettings, func() (int, string)) {
	t.Helper()
	store := &failingSettings{memorySettings: &memorySettings{values: map[string]string{"direct_connections.enabled": "true", "direct_connections.stun_servers": "first.example:3478"}}}
	var mu sync.Mutex
	calls, servers := 0, ""
	cfg := testConfig(store.memorySettings)
	cfg.Settings = store
	cfg.NAT = func(_ context.Context, _ *slog.Logger, s []string) (engine.NATReport, error) {
		mu.Lock()
		defer mu.Unlock()
		calls++
		servers = strings.Join(s, ",")
		return engine.NATReport{}, nil
	}
	return New(t.Context(), cfg), store, func() (int, string) { mu.Lock(); defer mu.Unlock(); return calls, servers }
}

// settleRetryCheck waits until the background reread and the check it started, if
// any, have finished. It must run inside a synctest bubble.
func settleRetryCheck() {
	synctest.Wait()
}

// failSettingsChange makes the next reads fail and reports the settings change
// that hits the first of them.
func failSettingsChange(t *testing.T, svc *Service, store *failingSettings, reads int32) {
	t.Helper()
	store.failReads.Store(reads)
	if err := svc.SettingsChanged(t.Context()); !errors.Is(err, errSettingsRead) {
		t.Fatalf("settings change error = %v", err)
	}
}

func retryNoSnapshot(t *testing.T) {
	svc, store, probes := newRetryService(t)
	failSettingsChange(t, svc, store, 1)
	store.set("direct_connections.stun_servers", "second.example:3478")
	if _, err := svc.Status(t.Context(), false); err != nil {
		t.Fatal(err)
	}
	if n, got := probes(); n != 1 || got != "second.example:3478" {
		t.Fatalf("probes=%d servers=%q", n, got)
	}
}

func retrySnapshot(t *testing.T) {
	svc, store, probes := newRetryService(t)
	first, err := svc.Status(t.Context(), true)
	if err != nil {
		t.Fatal(err)
	}
	// The bubble's clock stands still unless slept on; move it so the next check's
	// CheckedAt differs from the first.
	time.Sleep(time.Second) //nolint:forbidigo // Fake clock inside the synctest bubble: it moves CheckedAt, no wall time passes.
	store.set("direct_connections.stun_servers", "second.example:3478")
	failSettingsChange(t, svc, store, 1)
	// The background reread starts a check of the new values; this call returns the snapshot.
	got, err := svc.Status(t.Context(), false)
	if err != nil {
		t.Fatal(err)
	}
	if !got.CheckedAt.Equal(first.CheckedAt) {
		t.Fatalf("status returned a newer check %s, want the snapshot from %s", got.CheckedAt, first.CheckedAt)
	}
	settleRetryCheck()
	if n, got := probes(); n != 2 || got != "second.example:3478" {
		t.Fatalf("after reread: probes=%d servers=%q", n, got)
	}
	next, err := svc.Status(t.Context(), false)
	if err != nil {
		t.Fatal(err)
	}
	if !next.CheckedAt.After(first.CheckedAt) {
		t.Fatalf("following status is from %s, want the new check after %s", next.CheckedAt, first.CheckedAt)
	}
	retryFailingReread(t, svc, store, probes)
}

// retryFailingReread continues retrySnapshot: the reread fails too, the snapshot
// stands and the flag stays set until a read succeeds.
func retryFailingReread(t *testing.T, svc *Service, store *failingSettings, probes func() (int, string)) {
	t.Helper()
	// Two failures in a row: the reread fails too and the snapshot stands.
	store.set("direct_connections.stun_servers", "third.example:3478")
	failSettingsChange(t, svc, store, 2)
	if _, err := svc.Status(t.Context(), false); err != nil {
		t.Fatalf("status with a failing reread: %v", err)
	}
	settleRetryCheck()
	if n, _ := probes(); n != 2 {
		t.Fatalf("failed reread started a check: probes=%d", n)
	}
	// The flag stays set, so the next Status reads again and sees the change.
	if _, err := svc.Status(t.Context(), false); err != nil {
		t.Fatal(err)
	}
	settleRetryCheck()
	if n, got := probes(); n != 3 || got != "third.example:3478" {
		t.Fatalf("after second reread: probes=%d servers=%q", n, got)
	}
	// A successful read clears the flag: the next Status reads nothing and starts
	// nothing. An unchanged read starts no check either, so count the reads too.
	readsBefore := store.reads.Load()
	if _, err := svc.Status(t.Context(), false); err != nil {
		t.Fatal(err)
	}
	settleRetryCheck()
	if n, _ := probes(); n != 3 {
		t.Fatalf("flag not cleared: probes=%d", n)
	}
	if n := store.reads.Load() - readsBefore; n != 0 {
		t.Fatalf("flag not cleared: %d settings reads after a successful read, want 0", n)
	}
}

func retryUnchangedValues(t *testing.T) {
	svc, store, probes := newRetryService(t)
	if _, err := svc.Status(t.Context(), true); err != nil {
		t.Fatal(err)
	}
	failSettingsChange(t, svc, store, 1)
	if _, err := svc.Status(t.Context(), false); err != nil {
		t.Fatal(err)
	}
	settleRetryCheck()
	if n, _ := probes(); n != 1 {
		t.Fatalf("reread of unchanged values started a check: probes=%d", n)
	}
}

// A settings change whose event this replica missed reaches the snapshot once the
// last settings read is settingsRecheck old, and not before.
func TestStatusRereadsSettingsAfterMissedEvent(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		svc, store, probes := newRetryService(t)
		first, err := svc.Status(t.Context(), true)
		if err != nil {
			t.Fatal(err)
		}
		// Another replica saves a change; no SettingsChanged call reaches this one.
		store.set("direct_connections.stun_servers", "second.example:3478")
		if _, err := svc.Status(t.Context(), false); err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		if n, _ := probes(); n != 1 {
			t.Fatalf("a recent read was repeated: probes=%d", n)
		}
		time.Sleep(settingsRecheck) //nolint:forbidigo // Fake clock inside the synctest bubble: it ages the last read, no wall time passes.
		got, err := svc.Status(t.Context(), false)
		if err != nil {
			t.Fatal(err)
		}
		if !got.CheckedAt.Equal(first.CheckedAt) {
			t.Fatalf("status waited for the reread: got the check from %s, want the snapshot from %s", got.CheckedAt, first.CheckedAt)
		}
		synctest.Wait()
		if n, servers := probes(); n != 2 || servers != "second.example:3478" {
			t.Fatalf("after the recheck: probes=%d servers=%q", n, servers)
		}
	})
}

// stalledSettings fails reads like failingSettings, and once block is set it
// blocks every read until release closes or the read's context ends. entered
// receives one value per read that blocks.
type stalledSettings struct {
	*failingSettings
	block   atomic.Bool
	entered chan struct{}
	release chan struct{}
}

func (s *stalledSettings) GetAll(ctx context.Context) (map[string]string, error) {
	if s.block.Load() {
		s.entered <- struct{}{}
		select {
		case <-s.release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return s.failingSettings.GetAll(ctx)
}

// With a snapshot and a pending read, Status returns the snapshot at once while
// the reread is stalled in the settings store, and a second call during the stall
// starts no second reread. After the store answers, the reread starts a check of
// the new values and the next Status returns it.
func TestStatusReturnsSnapshotWhileRereadStalls(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		inner := &failingSettings{memorySettings: &memorySettings{values: map[string]string{"direct_connections.enabled": "true", "direct_connections.stun_servers": "first.example:3478"}}}
		store := &stalledSettings{failingSettings: inner, entered: make(chan struct{}, 4), release: make(chan struct{})}
		var releaseOnce sync.Once
		release := func() { releaseOnce.Do(func() { close(store.release) }) }
		defer release()
		cfg := testConfig(inner.memorySettings)
		cfg.Settings = store
		var mu sync.Mutex
		calls, servers := 0, ""
		cfg.NAT = func(_ context.Context, _ *slog.Logger, s []string) (engine.NATReport, error) {
			mu.Lock()
			defer mu.Unlock()
			calls++
			servers = strings.Join(s, ",")
			return engine.NATReport{}, nil
		}
		svc := New(t.Context(), cfg)
		first, err := svc.Status(t.Context(), true)
		if err != nil {
			t.Fatal(err)
		}
		time.Sleep(time.Second) //nolint:forbidigo // Fake clock inside the synctest bubble: it moves CheckedAt, no wall time passes.
		inner.set("direct_connections.stun_servers", "second.example:3478")
		failSettingsChange(t, svc, inner, 1)
		store.block.Store(true)
		readsBefore := inner.reads.Load()

		start := time.Now()
		got, err := svc.Status(t.Context(), false)
		if err != nil {
			t.Fatal(err)
		}
		if elapsed := time.Since(start); elapsed != 0 {
			t.Fatalf("status waited %s on the stalled reread, want 0", elapsed)
		}
		if !got.CheckedAt.Equal(first.CheckedAt) {
			t.Fatalf("status returned a check from %s, want the snapshot from %s", got.CheckedAt, first.CheckedAt)
		}
		synctest.Wait()
		if n := len(store.entered); n != 1 {
			t.Fatalf("reads blocked in the store = %d, want 1", n)
		}
		// A second call while the reread is stalled starts no second reread.
		if _, err := svc.Status(t.Context(), false); err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		if n := len(store.entered); n != 1 {
			t.Fatalf("reads blocked in the store = %d after the second call, want 1", n)
		}

		release()
		synctest.Wait()
		// A second reread would queue behind the gate, not enter the store, so the
		// count above misses it; once the stall ends it reads too.
		if n := inner.reads.Load() - readsBefore; n != 1 {
			t.Fatalf("store reads during and after the stall = %d, want 1: a second reread started", n)
		}
		next, err := svc.Status(t.Context(), false)
		if err != nil {
			t.Fatal(err)
		}
		mu.Lock()
		defer mu.Unlock()
		if calls != 2 || servers != "second.example:3478" {
			t.Fatalf("probes=%d servers=%q, want 2 and the second server", calls, servers)
		}
		if !next.CheckedAt.After(first.CheckedAt) {
			t.Fatalf("status is from %s, want the new check after %s", next.CheckedAt, first.CheckedAt)
		}
	})
}

// The background reread runs on the service's lifetime context, not on the
// call that started it: ending the service ends a reread stalled in the store at
// once, without waiting for the check bound.
func TestRereadEndsWithServiceContext(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		inner := &failingSettings{memorySettings: &memorySettings{values: map[string]string{"direct_connections.enabled": "true"}}}
		store := &stalledSettings{failingSettings: inner, entered: make(chan struct{}, 4), release: make(chan struct{})}
		defer close(store.release)
		cfg := testConfig(inner.memorySettings)
		cfg.Settings = store
		serviceCtx, stopService := context.WithCancel(t.Context())
		defer stopService()
		svc := New(serviceCtx, cfg)
		if _, err := svc.Status(t.Context(), true); err != nil {
			t.Fatal(err)
		}
		failSettingsChange(t, svc, inner, 1)
		store.block.Store(true)
		callCtx, endCall := context.WithCancel(t.Context())
		if _, err := svc.Status(callCtx, false); err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		if n := len(store.entered); n != 1 {
			t.Fatalf("reads blocked in the store = %d, want 1", n)
		}
		// The call that started the reread has returned; ending its context must
		// not touch the reread.
		endCall()
		synctest.Wait()
		if !rereading(svc) {
			t.Fatal("reread ended with the call that started it, want it bound to the service")
		}
		start := time.Now()
		stopService()
		synctest.Wait()
		if rereading(svc) {
			t.Fatal("reread still stalled after the service context ended")
		}
		if got := time.Since(start); got != 0 {
			t.Fatalf("reread took %s to end after the service context ended, want 0", got)
		}
	})
}

// A settings read ends with the service context on every caller path, including
// a caller that never ends, such as the settings-save callback's
// context.Background(). The read holds the gate, so a read that outlives the
// service would also keep a later reader waiting until the check bound.
func TestSettingsReadEndsWithServiceContext(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		store := &blockingSettings{memorySettings: &memorySettings{values: map[string]string{"direct_connections.enabled": "true"}}, entered: make(chan struct{}, 1), release: make(chan struct{})}
		store.block.Store(true)
		defer close(store.release)
		cfg := testConfig(store.memorySettings)
		cfg.Settings = store
		serviceCtx, stopService := context.WithCancel(t.Context())
		defer stopService()
		svc := New(serviceCtx, cfg)
		changed := make(chan error, 1)
		go func() { changed <- svc.SettingsChanged(context.Background()) }()
		synctest.Wait()
		if n := len(store.entered); n != 1 {
			t.Fatalf("reads blocked in the store = %d, want 1", n)
		}
		start := time.Now()
		stopService()
		synctest.Wait()
		select {
		case err := <-changed:
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("settings change error = %v, want canceled", err)
			}
		default:
			t.Fatal("settings read still blocked after the service context ended")
		}
		if got := time.Since(start); got != 0 {
			t.Fatalf("read took %s to end after the service context ended, want 0", got)
		}
		if n := len(svc.settingsGate); n != 0 {
			t.Fatalf("gate holders = %d after the read ended, want 0", n)
		}
	})
}

func rereading(svc *Service) bool {
	svc.mu.Lock()
	defer svc.mu.Unlock()
	return svc.rereading
}

// A refresh that finds a running check reads settings first when an earlier read
// failed, so the missed change is scheduled as a follow-up. Without a pending
// read, a refresh joins the running check without reading.
func TestRefreshReadsPendingChangeDuringRunningCheck(t *testing.T) {
	for _, tc := range []struct {
		name        string
		failRead    bool
		wantCalls   int32
		wantServers string
	}{
		{"pending read", true, 2, "second.example:3478"},
		{"no pending read", false, 1, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				store := &failingSettings{memorySettings: &memorySettings{values: map[string]string{"direct_connections.enabled": "true", "direct_connections.stun_servers": "first.example:3478"}}}
				cfg := testConfig(store.memorySettings)
				cfg.Settings = store
				entered := make(chan string, 4)
				release := make(chan struct{})
				var releaseOnce sync.Once
				releaseProbes := func() { releaseOnce.Do(func() { close(release) }) }
				defer releaseProbes()
				var calls atomic.Int32
				cfg.NAT = func(ctx context.Context, _ *slog.Logger, servers []string) (engine.NATReport, error) {
					calls.Add(1)
					entered <- servers[0]
					select {
					case <-release:
					case <-ctx.Done():
					}
					return engine.NATReport{}, ctx.Err()
				}
				svc := New(t.Context(), cfg)
				first := refreshStatus(t, svc)
				if got := <-entered; got != "first.example:3478" {
					t.Fatalf("first check STUN=%q", got)
				}
				if tc.failRead {
					failSettingsChange(t, svc, store, 1)
				}
				store.set("direct_connections.stun_servers", "second.example:3478")
				second := refreshStatus(t, svc)
				synctest.Wait()
				releaseProbes()
				if err := <-first; err != nil {
					t.Fatal(err)
				}
				if err := <-second; err != nil {
					t.Fatal(err)
				}
				synctest.Wait()
				if got := calls.Load(); got != tc.wantCalls {
					t.Fatalf("probe calls=%d, want %d", got, tc.wantCalls)
				}
				if tc.wantServers != "" {
					if got := <-entered; got != tc.wantServers {
						t.Fatalf("follow-up STUN=%q, want %q", got, tc.wantServers)
					}
				}
			})
		})
	}
}

// A caller whose context ends before the check bound ends its call at once; the
// settings read it started does not run on past the caller.
func TestStatusReadEndsWithCaller(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		store := &blockingSettings{memorySettings: &memorySettings{values: map[string]string{"direct_connections.enabled": "true"}}, entered: make(chan struct{}, 1), release: make(chan struct{})}
		store.block.Store(true)
		defer close(store.release)
		cfg := testConfig(store.memorySettings)
		cfg.Settings = store
		const callerBound = 2 * time.Second
		ctx, cancel := context.WithTimeout(t.Context(), callerBound)
		defer cancel()
		start := time.Now()
		_, err := New(t.Context(), cfg).Status(ctx, false)
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("status error = %v, want deadline exceeded", err)
		}
		if got := time.Since(start); got != callerBound {
			t.Fatalf("duration=%s want %s", got, callerBound)
		}
	})
}

type blockingSettings struct {
	*memorySettings
	block   atomic.Bool
	entered chan struct{}
	release chan struct{}
}

func (b *blockingSettings) GetAll(ctx context.Context) (map[string]string, error) {
	if b.block.Load() {
		b.entered <- struct{}{}
		select {
		case <-b.release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return b.memorySettings.GetAll(ctx)
}

// within waits for one value from ch on the real clock, so a regression that
// parks a goroutine on a mutex fails here instead of hanging the package.
func within[T any](t *testing.T, ch <-chan T, what string) T {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(5 * time.Second):
		t.Fatalf("%s did not happen within 5s", what)
		panic("unreachable")
	}
}

func TestSelfCheckBlockedSettings(t *testing.T) {
	for _, phase := range []string{"settings change", "refresh", "follow-up"} {
		t.Run(phase, func(t *testing.T) {
			store := &blockingSettings{memorySettings: &memorySettings{values: map[string]string{"direct_connections.enabled": "true"}}, entered: make(chan struct{}, 4), release: make(chan struct{})}
			defer close(store.release)
			cfg := testConfig(store.memorySettings)
			cfg.Settings = store
			probeEntered, probeRelease := make(chan struct{}, 4), make(chan struct{})
			var once sync.Once
			releaseProbe := func() { once.Do(func() { close(probeRelease) }) }
			defer releaseProbe()
			cfg.NAT = func(ctx context.Context, _ *slog.Logger, _ []string) (engine.NATReport, error) {
				probeEntered <- struct{}{}
				select {
				case <-probeRelease:
				case <-ctx.Done():
				}
				return engine.NATReport{}, ctx.Err()
			}
			svc := New(t.Context(), cfg)
			first := refreshStatus(t, svc)
			within(t, probeEntered, "first probe")
			if phase == "follow-up" {
				store.set("direct_connections.stun_servers", "latest.example:3478")
				if err := svc.SettingsChanged(t.Context()); err != nil {
					t.Fatal(err)
				}
			}
			if phase == "refresh" {
				releaseProbe()
				if err := within(t, first, "first run"); err != nil {
					t.Fatal(err)
				}
			}
			store.block.Store(true)
			switch phase {
			case "settings change":
				go func() { _ = svc.SettingsChanged(t.Context()) }()
			case "refresh":
				refreshStatus(t, svc)
			case "follow-up":
				releaseProbe()
			}
			within(t, store.entered, "blocked settings read")
			ctx, cancel := context.WithCancel(t.Context())
			cancel()
			canceled := make(chan error, 1)
			go func() { _, err := svc.Status(ctx, false); canceled <- err }()
			if err := within(t, canceled, "canceled status return"); !errors.Is(err, context.Canceled) {
				t.Fatalf("canceled status error = %v", err)
			}
			releaseProbe()
			if phase != "refresh" {
				if err := within(t, first, "run publication while settings read blocked"); err != nil {
					t.Fatal(err)
				}
			}
			type result struct {
				status Status
				err    error
			}
			final := make(chan result, 1)
			go func() { st, err := svc.Status(t.Context(), false); final <- result{st, err} }()
			if r := within(t, final, "cached status read"); r.err != nil || !r.status.Enabled {
				t.Fatalf("published status = %+v, error = %v", r.status, r.err)
			}
		})
	}
}

func TestStatusKnobMerge(t *testing.T) {
	for _, adjusted := range []bool{false, true} {
		t.Run(map[bool]string{false: "default", true: "adjusted"}[adjusted], func(t *testing.T) {
			cfg := testConfig(&memorySettings{})
			cfg.Tuning = tuning.Config{TCPCongestion: tuning.TCPCongestionBBR, TCPSendBuffer: tuning.DefaultTCPSendBuffer}
			if adjusted {
				cfg.Tuning.TCPCongestion = tuning.TCPCongestionOff
				cfg.Adjustments = []tuning.Adjustment{{Env: tuning.EnvTCPCongestion, Requested: "bad", Effective: "off", Reason: "invalid"}}
			}
			status, err := New(t.Context(), cfg).Status(t.Context(), true)
			if err != nil {
				t.Fatal(err)
			}
			var got Knob
			for _, k := range status.Knobs {
				if k.Env == tuning.EnvTCPCongestion {
					got = k
				}
			}
			want := string(cfg.Tuning.TCPCongestion)
			if got.Effective != want || got.Default != "bbr" {
				t.Fatalf("knob=%+v", got)
			}
			if adjusted {
				if got.Requested != "bad" || got.Reason != "invalid" {
					t.Fatalf("adjustment=%+v", got)
				}
			} else if got.Requested != "" || got.Reason != "" {
				t.Fatalf("unadjusted=%+v", got)
			}
		})
	}
}

// lateSettings fails reads like failingSettings. Once hold is set, a read takes
// its values at once, then waits for release or its context before returning
// them, so the caller gets values that are stale by the time it has them.
type lateSettings struct {
	*failingSettings
	hold    atomic.Bool
	entered chan struct{}
	release chan struct{}
}

func (l *lateSettings) GetAll(ctx context.Context) (map[string]string, error) {
	values, err := l.failingSettings.GetAll(ctx)
	if l.hold.Load() {
		l.entered <- struct{}{}
		select {
		case <-l.release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return values, err
}

// A settings read that began before a failed call must not clear that call's
// retry. Read A holds the gate with the old values; notification B ends while
// waiting for the gate and records its failure; A then succeeds. The flag stays
// set, so the next Status with a snapshot rereads and starts a check of the
// values B was told about.
func TestStaleReadSuccessKeepsRetry(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		inner := &failingSettings{memorySettings: &memorySettings{values: map[string]string{"direct_connections.enabled": "true", "direct_connections.stun_servers": "first.example:3478"}}}
		store := &lateSettings{failingSettings: inner, entered: make(chan struct{}, 2), release: make(chan struct{})}
		var releaseOnce sync.Once
		release := func() { releaseOnce.Do(func() { close(store.release) }) }
		defer release()
		cfg := testConfig(inner.memorySettings)
		cfg.Settings = store
		var mu sync.Mutex
		calls, servers := 0, ""
		cfg.NAT = func(_ context.Context, _ *slog.Logger, s []string) (engine.NATReport, error) {
			mu.Lock()
			defer mu.Unlock()
			calls++
			servers = strings.Join(s, ",")
			return engine.NATReport{}, nil
		}
		svc := New(t.Context(), cfg)
		if _, err := svc.Status(t.Context(), true); err != nil {
			t.Fatal(err)
		}

		store.hold.Store(true)
		readA := make(chan error, 1)
		go func() { readA <- svc.SettingsChanged(t.Context()) }()
		synctest.Wait()
		if n := len(store.entered); n != 1 {
			t.Fatalf("reads held in the store = %d, want 1", n)
		}

		const notificationBound = time.Second
		ctxB, cancelB := context.WithTimeout(t.Context(), notificationBound)
		defer cancelB()
		readB := make(chan error, 1)
		go func() { readB <- svc.SettingsChanged(ctxB) }()
		time.Sleep(2 * notificationBound) //nolint:forbidigo // Fake clock inside the synctest bubble: it ends B's wait, no wall time passes.
		if err := <-readB; !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("notification B error = %v, want deadline exceeded while waiting for the gate", err)
		}

		inner.set("direct_connections.stun_servers", "second.example:3478")
		store.hold.Store(false)
		release()
		if err := <-readA; err != nil {
			t.Fatalf("read A error = %v, want success with the old values", err)
		}

		if _, err := svc.Status(t.Context(), false); err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		mu.Lock()
		defer mu.Unlock()
		if calls != 2 || servers != "second.example:3478" {
			t.Fatalf("probes=%d servers=%q, want a second check of the second server: the stale success cleared the retry", calls, servers)
		}
	})
}

// A failed read is recorded before the gate is released. A read that fails must
// keep the gate until it has recorded the failure: if the gate were free first,
// a queued reader could read and succeed in between, and the failure recorded
// afterwards would re-mark that recovered state as pending.
//
// The service's test seam holds the failed read at the recording boundary, so the
// gate check runs when the worker is known to have reached it, not after a guess
// about scheduling.
func TestFailedReadIsRecordedBeforeGateReleases(t *testing.T) {
	store := &failingSettings{memorySettings: &memorySettings{values: map[string]string{"direct_connections.enabled": "true"}}}
	cfg := testConfig(store.memorySettings)
	cfg.Settings = store
	svc := New(t.Context(), cfg)
	store.failReads.Store(1)
	atBoundary, proceed := make(chan struct{}), make(chan struct{})
	var proceedOnce sync.Once
	resume := func() { proceedOnce.Do(func() { close(proceed) }) }
	defer resume()
	svc.beforeFailureRecorded = func() {
		atBoundary <- struct{}{}
		<-proceed
	}

	result := make(chan error, 1)
	go func() { result <- svc.SettingsChanged(t.Context()) }()
	within(t, atBoundary, "failed read to reach the failure-recording boundary")
	if n := len(svc.settingsGate); n != 1 {
		t.Fatalf("gate holders=%d at the failure-recording boundary: the gate was released before the failure was recorded", n)
	}
	resume()
	if err := within(t, result, "settings change to return"); !errors.Is(err, errSettingsRead) {
		t.Fatalf("settings change error = %v, want the settings read error", err)
	}
	svc.mu.Lock()
	defer svc.mu.Unlock()
	if !svc.readPending {
		t.Fatal("flag not set after a failed read")
	}
}

// cancelOnDoneContext is a real cancelable context that cancels itself the first
// time its Done channel is read, as a caller whose context ends after Status has
// checked it on entry and before it waits.
type cancelOnDoneContext struct {
	context.Context //nolint:containedctx // The embedded context supplies the Context methods the test does not override.
	cancel          context.CancelFunc
	once            sync.Once
}

func (c *cancelOnDoneContext) Done() <-chan struct{} {
	c.once.Do(c.cancel)
	return c.Context.Done()
}

// A caller whose context is done gets its error even when the snapshot is
// ready: with both select cases ready Go picks at random, so Status must check
// the caller again. Each call is a fresh chance for the random pick.
func TestStatusCallerCancellationWinsOverReadySnapshot(t *testing.T) {
	svc := New(t.Context(), testConfig(&memorySettings{values: map[string]string{"direct_connections.enabled": "true"}}))
	if _, err := svc.Status(t.Context(), true); err != nil {
		t.Fatal(err)
	}
	for i := range 64 {
		inner, cancel := context.WithCancel(t.Context())
		ctx := &cancelOnDoneContext{Context: inner, cancel: cancel}
		if _, err := svc.Status(ctx, false); !errors.Is(err, context.Canceled) {
			t.Fatalf("call %d: status error = %v, want the caller's context error", i, err)
		}
	}
}
