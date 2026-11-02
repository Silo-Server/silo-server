package config

import (
	"os"
	"strings"
	"testing"
	"time"
)

func TestLoadFromDBMetadataPresignExpiry(t *testing.T) {
	cfg, err := LoadFromDB(map[string]string{})
	if err != nil {
		t.Fatalf("LoadFromDB() returned error: %v", err)
	}
	if cfg.S3.MetadataPresignExpiry != 4*time.Hour {
		t.Fatalf("default metadata presign expiry = %s, want 4h", cfg.S3.MetadataPresignExpiry)
	}

	cfg, err = LoadFromDB(map[string]string{"s3.metadata_presign_expiry": "90m"})
	if err != nil {
		t.Fatalf("LoadFromDB() with metadata expiry returned error: %v", err)
	}
	if cfg.S3.MetadataPresignExpiry != 90*time.Minute {
		t.Fatalf("configured metadata presign expiry = %s, want 90m", cfg.S3.MetadataPresignExpiry)
	}
}

func TestLoadFromDBDownloadArtifactDirRequiresAbsolutePath(t *testing.T) {
	cfg, err := LoadFromDB(map[string]string{downloadArtifactDirSettingKey: "/mnt/silo-downloads"})
	if err != nil {
		t.Fatalf("LoadFromDB() with absolute artifact dir: %v", err)
	}
	if cfg.Download.ArtifactDir != "/mnt/silo-downloads" {
		t.Fatalf("artifact dir = %q", cfg.Download.ArtifactDir)
	}
}

func TestLoadFromDBScannerRealtimeMonitoringDefaultsTrue(t *testing.T) {
	cfg, err := LoadFromDB(map[string]string{})
	if err != nil {
		t.Fatalf("LoadFromDB() returned error: %v", err)
	}
	if !cfg.Scanner.RealtimeMonitoring {
		t.Fatal("Scanner.RealtimeMonitoring = false, want default true")
	}

	cfg, err = LoadFromDB(map[string]string{"scanner.realtime_monitoring": "false"})
	if err != nil {
		t.Fatalf("LoadFromDB() returned error: %v", err)
	}
	if cfg.Scanner.RealtimeMonitoring {
		t.Fatal("Scanner.RealtimeMonitoring = true, want configured false")
	}
}

func TestLoadFromDBAudiobookshelfCompatFlagGatesCompatListener(t *testing.T) {
	cfg, err := LoadFromDB(map[string]string{})
	if err != nil {
		t.Fatalf("LoadFromDB() returned error: %v", err)
	}
	if cfg.AudiobookshelfCompat.Listen != ":13378" {
		t.Fatalf("default audiobooks listener = %q, want default :13378", cfg.AudiobookshelfCompat.Listen)
	}

	cfg, err = LoadFromDB(map[string]string{"audiobookshelf_compat.enabled": "false"})
	if err != nil {
		t.Fatalf("LoadFromDB() returned error: %v", err)
	}
	if cfg.AudiobookshelfCompat.Listen != "" {
		t.Fatalf("disabled audiobooks listener = %q, want empty", cfg.AudiobookshelfCompat.Listen)
	}

	cfg, err = LoadFromDB(map[string]string{"audiobookshelf_compat.enabled": "true"})
	if err != nil {
		t.Fatalf("LoadFromDB() returned error: %v", err)
	}
	if cfg.AudiobookshelfCompat.Listen != ":13378" {
		t.Fatalf("enabled audiobooks listener = %q, want default :13378", cfg.AudiobookshelfCompat.Listen)
	}
}

func TestLoadFromDBJellyfinCompatEnabledPreservesLegacyListenerDefault(t *testing.T) {
	cfg, err := LoadFromDB(map[string]string{})
	if err != nil {
		t.Fatalf("LoadFromDB() returned error: %v", err)
	}
	if !cfg.JellyfinCompat.Enabled {
		t.Fatal("JellyfinCompat.Enabled = false, want legacy default true when listener would bind")
	}
	if cfg.JellyfinCompat.Listen == "" {
		t.Fatal("JellyfinCompat.Listen is empty, want default listener")
	}

	cfg, err = LoadFromDB(map[string]string{"jellyfin_compat.listen": ":19096"})
	if err != nil {
		t.Fatalf("LoadFromDB() returned error: %v", err)
	}
	if !cfg.JellyfinCompat.Enabled {
		t.Fatal("JellyfinCompat.Enabled = false, want true when legacy listener is configured")
	}
	if cfg.JellyfinCompat.Listen != ":19096" {
		t.Fatalf("JellyfinCompat.Listen = %q, want configured listener", cfg.JellyfinCompat.Listen)
	}
}

func TestLoadFromDBJellyfinCompatEnabledRespectsExplicitDisable(t *testing.T) {
	cfg, err := LoadFromDB(map[string]string{
		"jellyfin_compat.enabled": "false",
		"jellyfin_compat.listen":  ":19096",
	})
	if err != nil {
		t.Fatalf("LoadFromDB() returned error: %v", err)
	}
	if cfg.JellyfinCompat.Enabled {
		t.Fatal("JellyfinCompat.Enabled = true, want configured false")
	}
}

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
