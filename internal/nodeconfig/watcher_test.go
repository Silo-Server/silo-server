package nodeconfig

import (
	"context"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/config"
)

func newTestWatcher(t *testing.T, bootstrap BootstrapOverrides) *Watcher {
	t.Helper()
	return NewWatcher(nil, nil, nil, bootstrap)
}

func TestApplySettingsSkipsCallbacksOnNoopReload(t *testing.T) {
	w := newTestWatcher(t, BootstrapOverrides{})

	var calls int
	w.OnChange(func(old, updated *config.Config) {
		calls++
	})

	settings := map[string]string{"server.log_level": "debug"}
	if err := w.applySettings(context.Background(), settings); err != nil {
		t.Fatalf("first apply: %v", err)
	}
	if calls != 1 {
		t.Fatalf("after initial apply: calls = %d, want 1", calls)
	}

	// Same settings again — pointer swaps, but callbacks must not fire.
	if err := w.applySettings(context.Background(), settings); err != nil {
		t.Fatalf("second apply: %v", err)
	}
	if calls != 1 {
		t.Fatalf("after no-op apply: calls = %d, want 1", calls)
	}

	// A real change fires callbacks again.
	if err := w.applySettings(context.Background(), map[string]string{"server.log_level": "warn"}); err != nil {
		t.Fatalf("third apply: %v", err)
	}
	if calls != 2 {
		t.Fatalf("after changed apply: calls = %d, want 2", calls)
	}
}

func TestApplySettingsReappliesBootstrapOverrides(t *testing.T) {
	w := newTestWatcher(t, BootstrapOverrides{
		Listen:   ":9999",
		RedisURL: "redis://env-host:6379",
	})

	err := w.applySettings(context.Background(), map[string]string{
		"server.listen": ":8080",
		"redis.url":     "redis://db-host:6379",
	})
	if err != nil {
		t.Fatalf("apply: %v", err)
	}

	cfg := w.Config()
	if cfg.Server.Listen != ":9999" {
		t.Errorf("Listen = %q, want bootstrap override %q", cfg.Server.Listen, ":9999")
	}
	if cfg.Redis.URL != "redis://env-host:6379" {
		t.Errorf("Redis.URL = %q, want bootstrap override %q", cfg.Redis.URL, "redis://env-host:6379")
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

func TestOnChangeAfterFirstApplySeesLaterChanges(t *testing.T) {
	w := newTestWatcher(t, BootstrapOverrides{})

	if err := w.applySettings(context.Background(), map[string]string{"server.log_level": "info"}); err != nil {
		t.Fatalf("initial apply: %v", err)
	}

	var gotOld, gotNew string
	w.OnChange(func(old, updated *config.Config) {
		gotOld = old.Server.LogLevel
		gotNew = updated.Server.LogLevel
	})

	if err := w.applySettings(context.Background(), map[string]string{"server.log_level": "error"}); err != nil {
		t.Fatalf("second apply: %v", err)
	}
	if gotOld != "info" || gotNew != "error" {
		t.Errorf("callback saw old=%q new=%q, want old=info new=error", gotOld, gotNew)
	}
}

func TestOnLoadNormalizersApplyOnEveryLoad(t *testing.T) {
	w := &Watcher{}
	w.OnLoad(func(c *config.Config) {
		c.Playback.FFmpegPath = "/resolved/ffmpeg"
	})

	if err := w.applySettings(context.Background(), map[string]string{
		"playback.ffmpeg_path": "/usr/lib/jellyfin-ffmpeg/ffmpeg",
	}); err != nil {
		t.Fatalf("applySettings() error = %v", err)
	}
	if got := w.Config().Playback.FFmpegPath; got != "/resolved/ffmpeg" {
		t.Fatalf("Playback.FFmpegPath = %q, want normalized value", got)
	}

	// A reload constructs a fresh config; the normalizer must apply again.
	if err := w.applySettings(context.Background(), map[string]string{
		"playback.ffmpeg_path": "/usr/lib/jellyfin-ffmpeg/ffmpeg",
	}); err != nil {
		t.Fatalf("applySettings() reload error = %v", err)
	}
	if got := w.Config().Playback.FFmpegPath; got != "/resolved/ffmpeg" {
		t.Fatalf("after reload Playback.FFmpegPath = %q, want normalized value", got)
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
			return map[string]string{"server.log_level": "info"}, nil
		}
		record("fetch error")
		return map[string]string{"server.log_level": "error"}, nil
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
