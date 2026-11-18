package config

import (
	"os"
	"strings"
	"testing"
	"time"
)

// TestLoadFromDBRejectsInvalidValues checks that an unreadable stored value
// fails the load and names its key.
func TestLoadFromDBRejectsInvalidValues(t *testing.T) {
	for _, tc := range []struct{ key, value string }{
		{"s3.metadata_presign_expiry", "soon"},
		{playbackSegmentRetentionSettingKey, "-1"},
		{playbackSegmentRetentionSettingKey, "119"},
		{playbackSegmentRetentionSettingKey, "86401"},
		{downloadArtifactDirSettingKey, "relative/downloads"},
		{"scanner.realtime_monitoring", "maybe"},
		{"jellyfin_compat.enabled", "maybe"},
		{PlaybackRoutingRemuxExecutionSettingKey, "sometimes_worker"},
		{PlaybackRoutingDirectPlayEgressSettingKey, "worker"},
	} {
		t.Run(tc.key+"="+tc.value, func(t *testing.T) {
			_, err := LoadFromDB(map[string]string{tc.key: tc.value})
			if err == nil || !strings.Contains(err.Error(), tc.key) {
				t.Fatalf("LoadFromDB() error = %v, want an error naming %s", err, tc.key)
			}
		})
	}
}

func TestYAMLToSettingsMap(t *testing.T) {
	for _, tc := range []struct {
		name     string
		yaml     string
		want     map[string]string
		nonEmpty string
	}{
		{
			name: "jellyfin listener alone enables compat",
			yaml: "jellyfin_compat:\n  listen: \":19096\"\n",
			want: map[string]string{"jellyfin_compat.enabled": "true", "jellyfin_compat.listen": ":19096"},
		},
		{
			name: "jellyfin explicit disable wins over a listener",
			yaml: "jellyfin_compat:\n  enabled: false\n  listen: \":19096\"\n",
			want: map[string]string{"jellyfin_compat.enabled": "false"},
		},
		{
			name:     "jellyfin defaults to the legacy listener",
			yaml:     "server:\n  mode: integrated\n",
			want:     map[string]string{"jellyfin_compat.enabled": "true"},
			nonEmpty: "jellyfin_compat.listen",
		},
		{
			name: "explicitly disabled segment retention is preserved",
			yaml: "playback:\n  segment_retention_seconds: 0\n",
			want: map[string]string{playbackSegmentRetentionSettingKey: "0"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := yamlSettingsMapFromString(t, tc.yaml)
			for key, want := range tc.want {
				if got := m[key]; got != want {
					t.Errorf("%s = %q, want %q", key, got, want)
				}
			}
			if tc.nonEmpty != "" && m[tc.nonEmpty] == "" {
				t.Errorf("%s is empty, want a default", tc.nonEmpty)
			}
		})
	}
}

func yamlSettingsMapFromString(t *testing.T, body string) map[string]string {
	t.Helper()
	path := t.TempDir() + "/silo.yaml"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("WriteFile() returned error: %v", err)
	}
	m, err := YAMLToSettingsMap(path)
	if err != nil {
		t.Fatalf("YAMLToSettingsMap() returned error: %v", err)
	}
	return m
}

func TestLoadFromDBBandwidthProbeSchedule(t *testing.T) {
	cfg, err := LoadFromDB(map[string]string{PlaybackBandwidthProbeAutomaticSettingKey: "false", PlaybackBandwidthProbeMaxAgeDaysSettingKey: "30"})
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.Playback.AutomaticBandwidthProbeOff || cfg.Playback.BandwidthProbeMaxAge() != 30*24*time.Hour {
		t.Fatalf("configured: off=%v max age=%s", cfg.Playback.AutomaticBandwidthProbeOff, cfg.Playback.BandwidthProbeMaxAge())
	}
	for _, value := range []string{"0", "31"} {
		if _, err := LoadFromDB(map[string]string{PlaybackBandwidthProbeMaxAgeDaysSettingKey: value}); err == nil || !strings.Contains(err.Error(), PlaybackBandwidthProbeMaxAgeDaysSettingKey) {
			t.Fatalf("max age %s: error = %v, want a bounds error", value, err)
		}
		if _, err := NormalizeAdminSetting(PlaybackBandwidthProbeMaxAgeDaysSettingKey, value); err == nil {
			t.Fatalf("admin write of max age %s accepted", value)
		}
	}
}
