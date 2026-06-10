package nodeconfig

import (
	"testing"

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
	if err := w.applySettings(settings); err != nil {
		t.Fatalf("first apply: %v", err)
	}
	if calls != 1 {
		t.Fatalf("after initial apply: calls = %d, want 1", calls)
	}

	// Same settings again — pointer swaps, but callbacks must not fire.
	if err := w.applySettings(settings); err != nil {
		t.Fatalf("second apply: %v", err)
	}
	if calls != 1 {
		t.Fatalf("after no-op apply: calls = %d, want 1", calls)
	}

	// A real change fires callbacks again.
	if err := w.applySettings(map[string]string{"server.log_level": "warn"}); err != nil {
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

	err := w.applySettings(map[string]string{
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

	if err := w.applySettings(map[string]string{"server.log_level": "info"}); err != nil {
		t.Fatalf("initial apply: %v", err)
	}

	var gotOld, gotNew string
	w.OnChange(func(old, updated *config.Config) {
		gotOld = old.Server.LogLevel
		gotNew = updated.Server.LogLevel
	})

	if err := w.applySettings(map[string]string{"server.log_level": "error"}); err != nil {
		t.Fatalf("second apply: %v", err)
	}
	if gotOld != "info" || gotNew != "error" {
		t.Errorf("callback saw old=%q new=%q, want old=info new=error", gotOld, gotNew)
	}
}
