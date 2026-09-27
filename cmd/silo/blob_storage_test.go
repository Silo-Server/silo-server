package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/api"
	"github.com/Silo-Server/silo-server/internal/config"
)

type artworkBackendSettings struct {
	active string
	reads  int
}

func (s *artworkBackendSettings) Get(context.Context, string) (string, error) {
	s.reads++
	return s.active, nil
}

func (s *artworkBackendSettings) Set(_ context.Context, _, value string) error {
	s.active = value
	return nil
}

func (s *artworkBackendSettings) SetIfAbsent(context.Context, string, string) (bool, error) {
	return false, nil
}

func TestWorkerModesDoNotOpenBlobStorage(t *testing.T) {
	for _, mode := range []string{"proxy", "transcode", "worker"} {
		t.Run(mode, func(t *testing.T) {
			cfg := &config.Config{}
			cfg.Artwork.StorageBackend = "s3"
			cfg.Artwork.LocalPath = filepath.Join(t.TempDir(), "unused")
			settings := &artworkBackendSettings{active: "s3|https://s3.example|artwork|"}
			deps := &api.Dependencies{}
			if err := configureBlobStorage(t.Context(), mode, cfg, deps, settings); err != nil {
				t.Fatal(err)
			}
			if settings.reads != 0 || deps.Blobs.Assets != nil || deps.ArtworkSigner != nil || deps.ArtworkResolver != nil {
				t.Fatal("worker initialized catalog artwork dependencies")
			}
			if _, err := os.Stat(cfg.Artwork.LocalPath); !os.IsNotExist(err) {
				t.Fatalf("worker touched artwork filesystem: %v", err)
			}
		})
	}
}

func TestCatalogModesEnforceBlobBackend(t *testing.T) {
	for _, mode := range []string{"api", "integrated"} {
		t.Run(mode, func(t *testing.T) {
			cfg := &config.Config{}
			cfg.Artwork.StorageBackend = "local"
			cfg.Artwork.LocalPath = t.TempDir()
			deps := &api.Dependencies{}
			settings := &artworkBackendSettings{active: "s3|https://s3.example|artwork|"}
			if err := configureBlobStorage(t.Context(), mode, cfg, deps, settings); err == nil || !strings.Contains(err.Error(), "recorded as") {
				t.Fatalf("expected storage mismatch, got %v", err)
			}
			settings.active = ""
			if err := configureBlobStorage(t.Context(), mode, cfg, deps, settings); err != nil {
				t.Fatal(err)
			}
			if deps.Blobs.Assets == nil || deps.ArtworkBackend != "local" || deps.ArtworkSigner == nil || deps.ArtworkResolver == nil {
				t.Fatal("catalog blob dependencies missing")
			}
			// A local backend has one root, so diagnostics, job artifacts, and
			// avatars must resolve to the same recorded store as artwork.
			if !deps.Blobs.Local() || deps.Blobs.Operational != deps.Blobs.Assets {
				t.Fatal("local backend did not share one store")
			}
		})
	}
}
