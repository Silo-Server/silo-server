package nodeconfig

import (
	"context"
	"testing"
)

// clusterSettings is what every node in a QSV deployment loads before its own
// row is consulted.
func clusterSettings() map[string]string {
	return map[string]string{
		"auth.jwt_secret":    testJWTSecret,
		"playback.hw_accel":  "qsv",
		"playback.hw_device": "/dev/dri/renderD128",
	}
}

func newOverrideWatcher(t *testing.T, nodeURL string, load loadNodeHWOverrides) *Watcher {
	t.Helper()
	w := NewWatcher(nil, nil, nil, BootstrapOverrides{NodeURL: nodeURL})
	w.loadOverrides = load
	return w
}

// An unregistered node keeps the cluster settings, and says so once rather
// than on every 60-second reload.
func TestApplySettingsMissingRowInheritsAndLogsOnce(t *testing.T) {
	calls := 0
	w := newOverrideWatcher(t, "http://node-unregistered", func(context.Context, string, string) (nodeHWOverrides, bool, error) {
		calls++
		return nodeHWOverrides{}, false, nil
	})
	for range 3 {
		if err := w.applySettings(context.Background(), clusterSettings()); err != nil {
			t.Fatalf("apply: %v", err)
		}
	}
	cfg := w.Config()
	if cfg.Playback.HWAccel != "qsv" || cfg.Playback.HWDevice != "/dev/dri/renderD128" {
		t.Fatalf("effective policy = %q / %q, want the cluster values", cfg.Playback.HWAccel, cfg.Playback.HWDevice)
	}
	if calls != 3 {
		t.Fatalf("lookups = %d, want one per reload", calls)
	}
	w.mu.RLock()
	logged := w.missingRowLogged
	w.mu.RUnlock()
	if !logged {
		t.Fatal("missing row was never recorded, so it would be logged on every reload")
	}
}

// The overlay is the last word: a bootstrap re-apply happens before it, and
// nothing may put the cluster value back afterwards.
func TestApplySettingsOverlayOutlivesBootstrapReapply(t *testing.T) {
	accel := "none"
	w := NewWatcher(nil, nil, nil, BootstrapOverrides{
		NodeURL: "http://node-1",
		Listen:  ":9999",
		Mode:    "transcode",
	})
	w.loadOverrides = func(context.Context, string, string) (nodeHWOverrides, bool, error) {
		return nodeHWOverrides{HWAccel: &accel}, true, nil
	}
	settings := clusterSettings()
	settings["server.listen"] = ":8080"
	if err := w.applySettings(context.Background(), settings); err != nil {
		t.Fatalf("apply: %v", err)
	}
	cfg := w.Config()
	if cfg.Server.Listen != ":9999" || cfg.Server.Mode != "transcode" {
		t.Fatalf("bootstrap overrides lost: listen %q mode %q", cfg.Server.Listen, cfg.Server.Mode)
	}
	if cfg.Playback.HWAccel != "none" {
		t.Fatalf("HWAccel = %q, want the node override", cfg.Playback.HWAccel)
	}
}
