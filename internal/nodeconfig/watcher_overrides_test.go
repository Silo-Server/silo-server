package nodeconfig

import (
	"context"
	"errors"
	"testing"
)

// clusterSettings is what every node in a QSV deployment loads before its own
// row is consulted.
func clusterSettings() map[string]string {
	return map[string]string{
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

// A node's own row wins over the cluster-wide settings: this is the whole
// mechanism behind a CPU-only box living in a hardware-accelerated cluster.
func TestApplySettingsOverlaysNodeHWOverrides(t *testing.T) {
	accel, device := "none", "/dev/dri/renderD129"
	tests := []struct {
		name       string
		overrides  nodeHWOverrides
		wantAccel  string
		wantDevice string
	}{
		{
			name:       "both overridden",
			overrides:  nodeHWOverrides{HWAccel: &accel, HWDevice: &device},
			wantAccel:  "none",
			wantDevice: "/dev/dri/renderD129",
		},
		{
			name:       "backend only, device still inherited",
			overrides:  nodeHWOverrides{HWAccel: &accel},
			wantAccel:  "none",
			wantDevice: "/dev/dri/renderD128",
		},
		{
			name:       "device only, backend still inherited",
			overrides:  nodeHWOverrides{HWDevice: &device},
			wantAccel:  "qsv",
			wantDevice: "/dev/dri/renderD129",
		},
		{
			name:       "row with no overrides inherits both",
			wantAccel:  "qsv",
			wantDevice: "/dev/dri/renderD128",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			w := newOverrideWatcher(t, "http://node-1", func(context.Context, string, string) (nodeHWOverrides, bool, error) {
				return test.overrides, true, nil
			})
			if err := w.applySettings(context.Background(), clusterSettings()); err != nil {
				t.Fatalf("apply: %v", err)
			}
			cfg := w.Config()
			if cfg.Playback.HWAccel != test.wantAccel || cfg.Playback.HWDevice != test.wantDevice {
				t.Fatalf("effective policy = %q / %q, want %q / %q",
					cfg.Playback.HWAccel, cfg.Playback.HWDevice, test.wantAccel, test.wantDevice)
			}
		})
	}
}

// The API host has no stream_nodes row and must never pay for a lookup.
func TestApplySettingsSkipsOverlayWithoutNodeIdentity(t *testing.T) {
	looked := false
	w := newOverrideWatcher(t, "", func(context.Context, string, string) (nodeHWOverrides, bool, error) {
		looked = true
		return nodeHWOverrides{}, true, nil
	})
	if err := w.applySettings(context.Background(), clusterSettings()); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if looked {
		t.Fatal("a host with no NodeURL or NodeName queried stream_nodes")
	}
	if got := w.Config().Playback.HWAccel; got != "qsv" {
		t.Fatalf("HWAccel = %q, want the cluster value", got)
	}
}

// A split-horizon node whose registered url differs from its own NODE_URL
// still finds its row by NODE_NAME — the fallback identity an operator
// controls on both sides. The fake models the DB-level fallback behavior:
// it only reports a hit when the name matches, and this test asserts the
// overlay guard passes both bootstrap values through to the loader.
func TestApplySettingsOverlaysNodeHWOverridesByName(t *testing.T) {
	accel := "vaapi"
	var gotURL, gotName string
	w := NewWatcher(nil, nil, nil, BootstrapOverrides{
		NodeURL:  "http://10.0.4.7:8082",
		NodeName: "silo-dev-transcode-ns17",
	})
	w.loadOverrides = func(_ context.Context, nodeURL, nodeName string) (nodeHWOverrides, bool, error) {
		gotURL, gotName = nodeURL, nodeName
		if nodeName == "silo-dev-transcode-ns17" {
			return nodeHWOverrides{HWAccel: &accel}, true, nil
		}
		return nodeHWOverrides{}, false, nil
	}
	if err := w.applySettings(context.Background(), clusterSettings()); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if gotURL != "http://10.0.4.7:8082" || gotName != "silo-dev-transcode-ns17" {
		t.Fatalf("loader received url %q name %q, want both bootstrap values", gotURL, gotName)
	}
	if got := w.Config().Playback.HWAccel; got != "vaapi" {
		t.Fatalf("HWAccel = %q, want the name-matched override", got)
	}
}

// A node with only NODE_NAME set (no NODE_URL at all) still runs the
// overlay: the guard fires when either identity is present, not just the url.
func TestApplySettingsOverlayRunsWithNodeNameOnly(t *testing.T) {
	accel := "none"
	looked := false
	w := NewWatcher(nil, nil, nil, BootstrapOverrides{NodeName: "silo-dev-transcode-ns17"})
	w.loadOverrides = func(_ context.Context, nodeURL, nodeName string) (nodeHWOverrides, bool, error) {
		looked = true
		if nodeURL != "" {
			t.Fatalf("nodeURL = %q, want empty", nodeURL)
		}
		if nodeName != "silo-dev-transcode-ns17" {
			t.Fatalf("nodeName = %q, want the bootstrap value", nodeName)
		}
		return nodeHWOverrides{HWAccel: &accel}, true, nil
	}
	if err := w.applySettings(context.Background(), clusterSettings()); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if !looked {
		t.Fatal("a host with NodeName set skipped the overlay lookup")
	}
	if got := w.Config().Playback.HWAccel; got != "none" {
		t.Fatalf("HWAccel = %q, want the overlay applied", got)
	}
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

// A database hiccup must not flip a node back onto the cluster-wide backend:
// an unreadable row is not evidence that an operator cleared the override.
func TestApplySettingsKeepsPreviousOverrideWhenTheLookupFails(t *testing.T) {
	accel := "nvenc"
	fail := false
	w := newOverrideWatcher(t, "http://node-1", func(context.Context, string, string) (nodeHWOverrides, bool, error) {
		if fail {
			return nodeHWOverrides{}, false, errors.New("connection refused")
		}
		return nodeHWOverrides{HWAccel: &accel}, true, nil
	})
	if err := w.applySettings(context.Background(), clusterSettings()); err != nil {
		t.Fatalf("first apply: %v", err)
	}
	if got := w.Config().Playback.HWAccel; got != "nvenc" {
		t.Fatalf("HWAccel = %q, want the stored override", got)
	}

	fail = true
	if err := w.applySettings(context.Background(), clusterSettings()); err != nil {
		t.Fatalf("apply during outage: %v", err)
	}
	if got := w.Config().Playback.HWAccel; got != "nvenc" {
		t.Fatalf("HWAccel = %q after a failed lookup, want the last known override", got)
	}
}

// Before any successful read there is nothing to keep, so the cluster settings
// stand rather than an invented value.
func TestApplySettingsFallsBackToClusterWhenNoOverrideWasEverRead(t *testing.T) {
	w := newOverrideWatcher(t, "http://node-1", func(context.Context, string, string) (nodeHWOverrides, bool, error) {
		return nodeHWOverrides{}, false, errors.New("connection refused")
	})
	if err := w.applySettings(context.Background(), clusterSettings()); err != nil {
		t.Fatalf("apply: %v", err)
	}
	cfg := w.Config()
	if cfg.Playback.HWAccel != "qsv" || cfg.Playback.HWDevice != "/dev/dri/renderD128" {
		t.Fatalf("effective policy = %q / %q, want the cluster values", cfg.Playback.HWAccel, cfg.Playback.HWDevice)
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

// On a split-horizon deployment the row is matched by NODE_NAME, and renaming
// the node through the admin form leaves this worker's environment pointing at a
// name nothing carries. "No row" then arrives while the API is still dispatching
// that row's overridden backend, so reverting to the cluster device here would
// pair the two wrongly for as long as the names disagree. A row that has gone is
// not evidence an operator cleared the override.
func TestApplySettingsKeepsOverrideWhenTheRowStopsMatching(t *testing.T) {
	accel, device := "nvenc", "0"
	found := true
	w := newOverrideWatcher(t, "http://node-1", func(context.Context, string, string) (nodeHWOverrides, bool, error) {
		if !found {
			return nodeHWOverrides{}, false, nil
		}
		return nodeHWOverrides{HWAccel: &accel, HWDevice: &device}, true, nil
	})
	if err := w.applySettings(context.Background(), clusterSettings()); err != nil {
		t.Fatalf("first apply: %v", err)
	}

	found = false
	if err := w.applySettings(context.Background(), clusterSettings()); err != nil {
		t.Fatalf("apply after the rename: %v", err)
	}
	cfg := w.Config()
	if cfg.Playback.HWAccel != "nvenc" || cfg.Playback.HWDevice != "0" {
		t.Fatalf("effective policy = %q / %q, want the last override read from the row",
			cfg.Playback.HWAccel, cfg.Playback.HWDevice)
	}
}

// A node that never had a row is a different case: there is nothing to keep, so
// the cluster settings stand rather than an invented value.
func TestApplySettingsInheritsClusterWhenNoRowWasEverFound(t *testing.T) {
	w := newOverrideWatcher(t, "http://node-1", func(context.Context, string, string) (nodeHWOverrides, bool, error) {
		return nodeHWOverrides{}, false, nil
	})
	if err := w.applySettings(context.Background(), clusterSettings()); err != nil {
		t.Fatalf("apply: %v", err)
	}
	cfg := w.Config()
	if cfg.Playback.HWAccel != "qsv" || cfg.Playback.HWDevice != "/dev/dri/renderD128" {
		t.Fatalf("effective policy = %q / %q, want the cluster values", cfg.Playback.HWAccel, cfg.Playback.HWDevice)
	}
}
