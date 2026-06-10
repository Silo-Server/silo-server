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

func TestLoadFromDBMetadataPresignExpiryRejectsInvalidDuration(t *testing.T) {
	_, err := LoadFromDB(map[string]string{"s3.metadata_presign_expiry": "soon"})
	if err == nil {
		t.Fatal("LoadFromDB() error = nil, want invalid duration error")
	}
	if !strings.Contains(err.Error(), "s3.metadata_presign_expiry") {
		t.Fatalf("LoadFromDB() error = %v, want key name", err)
	}
}

func TestLoadFromDBJellyfinWebEnabledDefaultsToTrue(t *testing.T) {
	cfg, err := LoadFromDB(map[string]string{})
	if err != nil {
		t.Fatalf("LoadFromDB() returned error: %v", err)
	}
	if !cfg.JellyfinCompat.WebEnabled {
		t.Fatal("JellyfinCompat.WebEnabled = false, want default true")
	}

	cfg, err = LoadFromDB(map[string]string{"jellyfin_compat.web_enabled": "false"})
	if err != nil {
		t.Fatalf("LoadFromDB() returned error: %v", err)
	}
	if cfg.JellyfinCompat.WebEnabled {
		t.Fatal("JellyfinCompat.WebEnabled = true, want configured false")
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

	_, err = LoadFromDB(map[string]string{"jellyfin_compat.enabled": "maybe"})
	if err == nil {
		t.Fatal("LoadFromDB() error = nil, want invalid bool error")
	}
	if !strings.Contains(err.Error(), "jellyfin_compat.enabled") {
		t.Fatalf("LoadFromDB() error = %v, want key name", err)
	}
}

func TestYAMLToSettingsMapJellyfinCompatEnabledPreservesLegacyListener(t *testing.T) {
	m := yamlSettingsMapFromString(t, `
jellyfin_compat:
  listen: ":19096"
`)
	if got := m["jellyfin_compat.enabled"]; got != "true" {
		t.Fatalf("jellyfin_compat.enabled = %q, want true", got)
	}
	if got := m["jellyfin_compat.listen"]; got != ":19096" {
		t.Fatalf("jellyfin_compat.listen = %q, want configured listener", got)
	}
}

func TestYAMLToSettingsMapJellyfinCompatEnabledRespectsExplicitDisable(t *testing.T) {
	m := yamlSettingsMapFromString(t, `
jellyfin_compat:
  enabled: false
  listen: ":19096"
`)
	if got := m["jellyfin_compat.enabled"]; got != "false" {
		t.Fatalf("jellyfin_compat.enabled = %q, want false", got)
	}
}

func TestYAMLToSettingsMapJellyfinCompatEnabledDefaultsToLegacyListener(t *testing.T) {
	m := yamlSettingsMapFromString(t, `server:
  mode: integrated
`)
	if got := m["jellyfin_compat.enabled"]; got != "true" {
		t.Fatalf("jellyfin_compat.enabled = %q, want true for default listener", got)
	}
	if got := m["jellyfin_compat.listen"]; got == "" {
		t.Fatal("jellyfin_compat.listen is empty, want default listener")
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
