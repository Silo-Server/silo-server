package nodeconfig

import (
	"context"
	"errors"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/config"
)

// testJWTSecret is the signing secret every settings fixture carries: the
// watcher refuses a snapshot without one.
const testJWTSecret = "watcher-test-secret"

func newTestWatcher(t *testing.T, bootstrap BootstrapOverrides) *Watcher {
	t.Helper()
	return NewWatcher(nil, nil, nil, bootstrap)
}

func TestApplySettingsAppliesRedisDBToTheSavedURLOnly(t *testing.T) {
	settings := map[string]string{
		"auth.jwt_secret": testJWTSecret,
		"redis.url":       "redis://db-host:6379/3",
		"redis.db":        "5",
	}

	saved := newTestWatcher(t, BootstrapOverrides{})
	if err := saved.applySettings(context.Background(), settings); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if db, ok := saved.Config().Redis.Database(); !ok || db != 5 {
		t.Errorf("database number = (%d, %v), want redis.db's 5 on the saved URL", db, ok)
	}

	// REDIS_URL names the whole connection, database number included.
	fromEnv := newTestWatcher(t, BootstrapOverrides{RedisURL: "redis://env-host:6379/1"})
	if err := fromEnv.applySettings(context.Background(), settings); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if db, ok := fromEnv.Config().Redis.Database(); !ok || db != 1 {
		t.Errorf("database number = (%d, %v), want the 1 in REDIS_URL", db, ok)
	}
}

// A process setting from the environment is never stored, so every reload
// must lay it over the stored value again.
func TestApplySettingsOverlaysProcessSettingsOnEveryLoad(t *testing.T) {
	stored := map[string]string{"auth.jwt_secret": testJWTSecret, "scanner.workers": "2"}
	w := newTestWatcher(t, BootstrapOverrides{Settings: map[string]string{"scanner.workers": "9"}})
	for range 2 {
		if err := w.applySettings(t.Context(), stored); err != nil {
			t.Fatalf("apply: %v", err)
		}
		if got := w.Config().Scanner.Workers; got != 9 {
			t.Fatalf("scanner workers = %d, want the 9 the environment sets", got)
		}
	}
	if stored["scanner.workers"] != "2" {
		t.Errorf("the stored snapshot was changed to %q", stored["scanner.workers"])
	}
}

// A config without auth.jwt_secret would let anyone sign stream tokens the
// node then honors. The first load must fail rather than start the node, and a
// reload must keep the last config that had a secret.
func TestReloadRefusesConfigWithoutJWTSecret(t *testing.T) {
	w := &Watcher{}
	var changes int
	w.OnChange(func(_, _ *config.Config) { changes++ })

	snapshot := map[string]string{"server.log_level": "info"}
	w.fetchSettingsFn = func(context.Context) (map[string]string, error) { return snapshot, nil }

	if err := w.reload(t.Context()); !errors.Is(err, ErrNoJWTSecret) {
		t.Fatalf("first load without a secret: error = %v, want ErrNoJWTSecret", err)
	}
	if w.Config() != nil || changes != 0 {
		t.Fatalf("first load without a secret published config %v (%d callbacks)", w.Config(), changes)
	}

	snapshot = map[string]string{"auth.jwt_secret": testJWTSecret, "server.log_level": "info"}
	if err := w.reload(t.Context()); err != nil {
		t.Fatalf("load with a secret: %v", err)
	}
	valid := w.Config()

	for _, secret := range []string{"", "  \t"} {
		snapshot = map[string]string{"auth.jwt_secret": secret, "server.log_level": "debug"}
		if err := w.reload(t.Context()); !errors.Is(err, ErrNoJWTSecret) {
			t.Errorf("reload with secret %q: error = %v, want ErrNoJWTSecret", secret, err)
		}
		if got := w.Config(); got != valid || got.Auth.JWTSecret != testJWTSecret {
			t.Errorf("reload with secret %q replaced the last valid config", secret)
		}
	}
	if changes != 1 {
		t.Errorf("OnChange calls = %d, want 1 (only the valid load)", changes)
	}
}

func TestRequestReloadCoalesces(t *testing.T) {
	w := newTestWatcher(t, BootstrapOverrides{})

	// Multiple requests without a draining poll goroutine must neither block
	// nor queue more than one pending reload.
	w.RequestReload()
	w.RequestReload()
	w.RequestReload()

	if got := len(w.reloadCh); got != 1 {
		t.Fatalf("pending reloads = %d, want 1 (coalesced)", got)
	}
}

// staleReloadWindow is how long the poll's fetch waits for a concurrent forced
// reload to overtake it. It is a bound on a violation, not a wait for
// completion: serialized, nothing can overtake and the wait expires; unserialized,
// the forced reload finishes far inside it and the poll's stale snapshot lands
// last.
const staleReloadWindow = 250 * time.Millisecond

// The poll, the settings-changed event, and ForceReload all reload, and only
// the first two share a goroutine. A poll that read server_settings before an
// operator's edit must not be able to put that pre-edit snapshot back after
// ForceReload has already applied the edit — the node would answer the endpoint
// 204 while running the old policy.
func TestReloadDoesNotLetAnEarlierSnapshotSupersedeALaterOne(t *testing.T) {
	w := &Watcher{}

	var mu sync.Mutex
	var events []string
	record := func(event string) {
		mu.Lock()
		defer mu.Unlock()
		events = append(events, event)
	}
	w.OnChange(func(_, updated *config.Config) { record("apply " + updated.Server.LogLevel) })

	polling := make(chan struct{})
	forced := make(chan struct{})
	var fetches atomic.Int32
	w.fetchSettingsFn = func(context.Context) (map[string]string, error) {
		if fetches.Add(1) == 1 {
			record("fetch info")
			close(polling)
			select {
			case <-forced:
			case <-time.After(staleReloadWindow):
			}
			return map[string]string{"auth.jwt_secret": testJWTSecret, "server.log_level": "info"}, nil
		}
		record("fetch error")
		return map[string]string{"auth.jwt_secret": testJWTSecret, "server.log_level": "error"}, nil
	}

	polled := make(chan error, 1)
	go func() { polled <- w.reload(context.Background()) }()
	<-polling

	go func() {
		if err := w.ForceReload(context.Background()); err != nil {
			t.Errorf("ForceReload() error = %v", err)
		}
		close(forced)
	}()

	if err := <-polled; err != nil {
		t.Fatalf("poll reload error = %v", err)
	}
	<-forced

	if got := w.Config().Server.LogLevel; got != "error" {
		t.Errorf("Server.LogLevel = %q, want the value the later read saw", got)
	}
	mu.Lock()
	defer mu.Unlock()
	want := []string{"fetch info", "apply info", "fetch error", "apply error"}
	if !slices.Equal(events, want) {
		t.Errorf("reloads interleaved:\n got %v\nwant %v", events, want)
	}
}
