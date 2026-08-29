package config

import (
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLoadFromDBArtworkDefaults(t *testing.T) {
	cfg, err := LoadFromDB(map[string]string{})
	if err != nil {
		t.Fatalf("LoadFromDB: %v", err)
	}
	if cfg.Artwork.StorageBackend != ArtworkBackendAuto {
		t.Errorf("StorageBackend = %q, want %q", cfg.Artwork.StorageBackend, ArtworkBackendAuto)
	}
	if cfg.Artwork.LocalPath != DefaultArtworkLocalPath {
		t.Errorf("LocalPath = %q, want %q", cfg.Artwork.LocalPath, DefaultArtworkLocalPath)
	}
	if cfg.Artwork.URLTTL != 4*time.Hour {
		t.Errorf("URLTTL = %s, want 4h", cfg.Artwork.URLTTL)
	}
}

// The artwork URL lifetime mirrors the presign lifetime so an install that
// tuned one does not end up running two different windows.
func TestLoadFromDBArtworkURLTTLMirrorsPresignExpiry(t *testing.T) {
	cfg, err := LoadFromDB(map[string]string{"s3.metadata_presign_expiry": "90m"})
	if err != nil {
		t.Fatalf("LoadFromDB: %v", err)
	}
	if cfg.Artwork.URLTTL != 90*time.Minute {
		t.Fatalf("URLTTL = %s, want the presign expiry 90m", cfg.Artwork.URLTTL)
	}

	cfg, err = LoadFromDB(map[string]string{
		"s3.metadata_presign_expiry": "90m",
		ArtworkURLTTLKey:             "15m",
	})
	if err != nil {
		t.Fatalf("LoadFromDB: %v", err)
	}
	if cfg.Artwork.URLTTL != 15*time.Minute {
		t.Fatalf("URLTTL = %s, want the explicit 15m", cfg.Artwork.URLTTL)
	}
}

func TestLoadFromDBArtworkRejectsBadValues(t *testing.T) {
	for name, values := range map[string]map[string]string{
		"backend":         {ArtworkStorageBackendKey: "gcs"},
		"materialization": {ArtworkRemoteMaterializationKey: "sometimes"},
		"relative path":   {ArtworkLocalPathKey: "relative/artwork"},
		"url ttl":         {ArtworkURLTTLKey: "soon"},
		"zero url ttl":    {ArtworkURLTTLKey: "0s"},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := LoadFromDB(values); err == nil {
				t.Fatalf("LoadFromDB(%v) succeeded, want a validation error", values)
			}
		})
	}
}

// An explicitly stored metadata.cache_images=false is an administrator's
// decision not to fetch remote artwork, so the upgrade must not convert it into
// remote caching. An absent row is the old shipped default, not a decision.
func TestArtworkMaterializationUpgradeMapping(t *testing.T) {
	tests := []struct {
		name   string
		stored map[string]string
		want   string
	}{
		{"absent row adopts the new default", map[string]string{}, ArtworkMaterializationSelected},
		{"explicit false maps to passthrough", map[string]string{"metadata.cache_images": "false"}, ArtworkMaterializationPassthrough},
		{"explicit true maps to selected", map[string]string{"metadata.cache_images": "true"}, ArtworkMaterializationSelected},
		{"empty row is not a decision", map[string]string{"metadata.cache_images": ""}, ArtworkMaterializationSelected},
		{
			"an explicit artwork setting always wins",
			map[string]string{
				"metadata.cache_images":         "false",
				ArtworkRemoteMaterializationKey: ArtworkMaterializationSelected,
			},
			ArtworkMaterializationSelected,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg, err := LoadFromDB(tc.stored)
			if err != nil {
				t.Fatalf("LoadFromDB: %v", err)
			}
			if cfg.Artwork.RemoteMaterialization != tc.want {
				t.Errorf("RemoteMaterialization = %q, want %q", cfg.Artwork.RemoteMaterialization, tc.want)
			}
			// The admin form must describe the behavior the server runs.
			if got := EffectiveAdminSettings(tc.stored)[ArtworkRemoteMaterializationKey]; got != tc.want {
				t.Errorf("effective setting = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestNormalizeArtworkSettings(t *testing.T) {
	tests := []struct {
		key     string
		value   string
		want    string
		wantErr bool
	}{
		{ArtworkStorageBackendKey, "Local", ArtworkBackendLocal, false},
		{ArtworkStorageBackendKey, "", "", false},
		{ArtworkStorageBackendKey, "gcs", "", true},
		{ArtworkRemoteMaterializationKey, "PASSTHROUGH", ArtworkMaterializationPassthrough, false},
		{ArtworkRemoteMaterializationKey, "maybe", "", true},
		{ArtworkURLTTLKey, "30m", "30m", false},
		{ArtworkURLTTLKey, "", "", false},
		{ArtworkURLTTLKey, "-1h", "", true},
		{ArtworkLocalPathKey, "/srv/silo/artwork/", "/srv/silo/artwork", false},
		{ArtworkLocalPathKey, "", "", false},
		{ArtworkLocalPathKey, "artwork", "", true},
		{ArtworkLocalPathKey, filepath.VolumeName(t.TempDir()) + string(filepath.Separator), "", true},
	}
	for _, tc := range tests {
		got, err := NormalizeAdminSetting(tc.key, tc.value)
		if tc.wantErr {
			if err == nil {
				t.Errorf("NormalizeAdminSetting(%q, %q) = %q, want an error", tc.key, tc.value, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("NormalizeAdminSetting(%q, %q): %v", tc.key, tc.value, err)
			continue
		}
		if got != tc.want {
			t.Errorf("NormalizeAdminSetting(%q, %q) = %q, want %q", tc.key, tc.value, got, tc.want)
		}
	}
}

// The store handle and the confined root are captured at startup; the URL
// lifetime and materialization policy are read live.
func TestArtworkRestartClassification(t *testing.T) {
	restartRequired := map[string]bool{
		ArtworkStorageBackendKey:        true,
		ArtworkLocalPathKey:             true,
		ArtworkURLTTLKey:                false,
		ArtworkRemoteMaterializationKey: false,
	}
	for key, want := range restartRequired {
		if got := RestartRequired(key); got != want {
			t.Errorf("RestartRequired(%q) = %v, want %v", key, got, want)
		}
	}
}

func TestValidateAdminSettingsAcceptsArtworkSnapshot(t *testing.T) {
	err := ValidateAdminSettings(map[string]string{
		ArtworkStorageBackendKey:        ArtworkBackendLocal,
		ArtworkLocalPathKey:             "/srv/silo/artwork",
		ArtworkRemoteMaterializationKey: ArtworkMaterializationSelected,
		ArtworkURLTTLKey:                "2h",
	})
	if err != nil {
		t.Fatalf("ValidateAdminSettings: %v", err)
	}

	err = ValidateAdminSettings(map[string]string{ArtworkLocalPathKey: "not/absolute"})
	if err == nil || !strings.Contains(err.Error(), ArtworkLocalPathKey) {
		t.Fatalf("ValidateAdminSettings = %v, want a rejected local path", err)
	}
}
