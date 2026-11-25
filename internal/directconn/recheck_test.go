package directconn

import (
	"bytes"
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/darthShadow/shunpike/engine"

	"github.com/Silo-Server/silo-server/internal/tuning"
)

// newRecheckService starts a service that reads store and counts engine starts.
// Only Shutdown ends it, so a worker that Shutdown does not stop outlives the
// synctest bubble and fails the test. It must run inside a bubble.
func newRecheckService(t *testing.T, values *memorySettings, store SettingsStore) (*Service, *atomic.Int32) {
	t.Helper()
	cfg := testConfig(values)
	cfg.Settings = store
	cfg.SecretKey = bytes.Repeat([]byte{0x41}, 32)
	cfg.Tuning, _ = tuning.Parse(func(string) string { return "" })
	var starts atomic.Int32
	var mu sync.Mutex
	var events []string
	cfg.newEngine = func(_ context.Context, c engine.Config) (engineHandle, error) {
		n := starts.Add(1)
		return &lifecycleEngine{port: c.Port, changed: make(chan struct{}, 1), events: &events, eventsMu: &mu, id: int(n)}, nil
	}
	return New(context.Background(), cfg), &starts
}

func shutdownService(t *testing.T, svc *Service) {
	t.Helper()
	if err := svc.Shutdown(t.Context()); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
}

// A disable whose settings-change event this replica missed stops the engine at
// the next minute recheck.
func TestRecheckStopsEngineAfterMissedDisable(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		store := &memorySettings{values: map[string]string{"direct_connections.enabled": "true"}}
		svc, starts := newRecheckService(t, store, store)
		defer shutdownService(t, svc)
		if err := svc.SettingsChanged(t.Context()); err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		if got := svc.engineFeature(); starts.Load() != 1 || got.State != stateOn {
			t.Fatalf("before the missed disable: starts=%d feature=%+v, want 1 on", starts.Load(), got)
		}
		// Another replica saves the change; no SettingsChanged call reaches this one.
		store.set("direct_connections.enabled", "false")
		time.Sleep(time.Minute) //nolint:forbidigo // Fake clock inside the synctest bubble: it reaches the first recheck, no wall time passes.
		synctest.Wait()
		if got := svc.engineFeature(); got.State != stateOff || got.Reason != reasonDisabled {
			t.Fatalf("after the recheck: feature=%+v, want off/disabled", got)
		}
	})
}

// An enable that the first settings read missed, because that read failed,
// starts the engine at the next minute recheck.
func TestRecheckStartsEngineAfterFailedFirstRead(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		store := &failingSettings{memorySettings: &memorySettings{values: map[string]string{"direct_connections.enabled": "true"}}}
		store.failReads.Store(1)
		svc, starts := newRecheckService(t, store.memorySettings, store)
		defer shutdownService(t, svc)
		if err := svc.SettingsChanged(t.Context()); !errors.Is(err, errSettingsRead) {
			t.Fatalf("first read error = %v, want %v", err, errSettingsRead)
		}
		synctest.Wait()
		if n := starts.Load(); n != 0 {
			t.Fatalf("starts=%d after a failed first read, want 0", n)
		}
		time.Sleep(time.Minute) //nolint:forbidigo // Fake clock inside the synctest bubble: it reaches the first recheck, no wall time passes.
		synctest.Wait()
		if got := svc.engineFeature(); starts.Load() != 1 || got.State != stateOn {
			t.Fatalf("after the recheck: starts=%d feature=%+v, want 1 on", starts.Load(), got)
		}
	})
}
