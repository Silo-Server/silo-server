package config

import (
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

func TestLoadFromDBAudiobookshelfCompatFlagGatesCompatListener(t *testing.T) {
	cfg, err := LoadFromDB(map[string]string{"audiobookshelf_compat.enabled": "false"})
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
