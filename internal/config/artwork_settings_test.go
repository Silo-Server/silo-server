package config

import "testing"

func TestArtworkSettings(t *testing.T) {
	if got, err := NormalizeAdminSetting("artwork.storage_backend", "LOCAL"); err != nil || got != "local" {
		t.Fatalf("backend = %q, %v", got, err)
	}
	if _, err := NormalizeAdminSetting("artwork.storage_backend", "gcs"); err == nil {
		t.Fatal("invalid backend accepted")
	}
	if _, err := NormalizeAdminSetting("artwork.local_path", "relative"); err == nil {
		t.Fatal("relative path accepted")
	}
	if got, err := NormalizeAdminSetting("artwork.local_path", "/srv/silo/artwork/"); err != nil || got != "/srv/silo/artwork" {
		t.Fatalf("path = %q, %v", got, err)
	}
	if !RestartRequired("artwork.storage_backend") || !RestartRequired("artwork.local_path") {
		t.Fatal("artwork settings must require restart")
	}
}
