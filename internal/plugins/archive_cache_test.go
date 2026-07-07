package plugins

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"google.golang.org/protobuf/encoding/protojson"
)

func TestArchiveCacheEnsureRecoversLegacyBinaryArchive(t *testing.T) {
	ctx := context.Background()

	binaryData := []byte("#!/bin/sh\nexit 0\n")
	checksum := sha256.Sum256(binaryData)

	manifest := testPluginManifest(t, "silo.metadb", "0.0.19")
	manifest.Checksum = hex.EncodeToString(checksum[:])
	manifestBytes, err := protojson.Marshal(manifest)
	if err != nil {
		t.Fatalf("protojson.Marshal() returned error: %v", err)
	}

	store := &legacyArchiveStore{
		archive: &InstallationArchive{
			InstallationID: 42,
			ManifestJSON:   manifestBytes,
			Checksum:       manifest.GetChecksum(),
			Bytes:          binaryData,
		},
	}

	installDir := filepath.Join(t.TempDir(), "plugins", "silo.metadb", "0.0.19")
	installation := &Installation{
		ID:          42,
		PluginID:    manifest.GetPluginId(),
		Version:     manifest.GetVersion(),
		InstallPath: filepath.Join(installDir, "plugin"),
	}

	got, err := NewArchiveCache(store).Ensure(ctx, installation)
	if err != nil {
		t.Fatalf("Ensure() returned error: %v", err)
	}
	if got.GetPluginId() != manifest.GetPluginId() {
		t.Fatalf("manifest plugin_id = %q, want %q", got.GetPluginId(), manifest.GetPluginId())
	}

	if store.savedInstallationID != installation.ID {
		t.Fatalf("repaired archive installation id = %d, want %d", store.savedInstallationID, installation.ID)
	}
	if !bytes.Equal(store.savedManifestJSON, manifestBytes) {
		t.Fatal("repaired archive manifest does not match stored manifest")
	}
	if _, _, savedManifest, err := openPluginArchive(store.savedArchiveBytes); err != nil {
		t.Fatalf("repaired archive is not a valid plugin archive: %v", err)
	} else if savedManifest.GetChecksum() != manifest.GetChecksum() {
		t.Fatalf("repaired archive checksum = %q, want %q", savedManifest.GetChecksum(), manifest.GetChecksum())
	}

	installedBinary, err := os.ReadFile(installation.InstallPath)
	if err != nil {
		t.Fatalf("read rehydrated plugin binary: %v", err)
	}
	if !bytes.Equal(installedBinary, binaryData) {
		t.Fatal("rehydrated plugin binary does not match legacy binary bytes")
	}
	if _, err := os.Stat(InstalledManifestPath(installation.InstallPath)); err != nil {
		t.Fatalf("expected rehydrated manifest file: %v", err)
	}
}

func TestArchiveCacheEnsureRecoveryToleratesPersistFailure(t *testing.T) {
	ctx := context.Background()

	binaryData := []byte("#!/bin/sh\nexit 0\n")
	checksum := sha256.Sum256(binaryData)

	manifest := testPluginManifest(t, "silo.metadb", "0.0.19")
	manifest.Checksum = hex.EncodeToString(checksum[:])
	manifestBytes, err := protojson.Marshal(manifest)
	if err != nil {
		t.Fatalf("protojson.Marshal() returned error: %v", err)
	}

	store := &legacyArchiveStore{
		archive: &InstallationArchive{
			InstallationID: 42,
			ManifestJSON:   manifestBytes,
			Checksum:       manifest.GetChecksum(),
			Bytes:          binaryData,
		},
		saveErr: errors.New("db unavailable"),
	}

	installDir := filepath.Join(t.TempDir(), "plugins", "silo.metadb", "0.0.19")
	installation := &Installation{
		ID:          42,
		PluginID:    manifest.GetPluginId(),
		Version:     manifest.GetVersion(),
		InstallPath: filepath.Join(installDir, "plugin"),
	}

	if _, err := NewArchiveCache(store).Ensure(ctx, installation); err != nil {
		t.Fatalf("Ensure() returned error despite in-memory recovery: %v", err)
	}

	installedBinary, err := os.ReadFile(installation.InstallPath)
	if err != nil {
		t.Fatalf("read rehydrated plugin binary: %v", err)
	}
	if !bytes.Equal(installedBinary, binaryData) {
		t.Fatal("rehydrated plugin binary does not match legacy binary bytes")
	}
}

type legacyArchiveStore struct {
	archive             *InstallationArchive
	saveErr             error
	savedInstallationID int
	savedManifestJSON   []byte
	savedChecksum       string
	savedArchiveBytes   []byte
}

func (s *legacyArchiveStore) GetArchive(context.Context, int) (*InstallationArchive, error) {
	return s.archive, nil
}

func (s *legacyArchiveStore) SaveArchive(
	_ context.Context,
	installationID int,
	manifestJSON []byte,
	checksum string,
	archiveBytes []byte,
) error {
	if s.saveErr != nil {
		return s.saveErr
	}
	s.savedInstallationID = installationID
	s.savedManifestJSON = append([]byte(nil), manifestJSON...)
	s.savedChecksum = checksum
	s.savedArchiveBytes = append([]byte(nil), archiveBytes...)
	return nil
}
