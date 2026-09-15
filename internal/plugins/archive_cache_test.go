package plugins

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"google.golang.org/protobuf/encoding/protojson"
)

func TestArchiveCacheRejectsForeignBinaryOnCacheHit(t *testing.T) {
	// Use an ELF architecture different from the running test so the cache
	// represents a volume moved from another proxy platform.
	binaryData := make([]byte, 64)
	copy(binaryData, []byte{0x7f, 'E', 'L', 'F', 2, 1, 1})
	binaryData[16], binaryData[18], binaryData[20], binaryData[52] = 2, 0xB7, 1, 64
	if runtime.GOARCH == "arm64" {
		binaryData[18] = 0x3E // EM_X86_64
	}
	manifest := testPluginManifest(t, "silo.metadb", "0.0.19")
	checksum := sha256.Sum256(binaryData)
	manifest.Checksum = hex.EncodeToString(checksum[:])
	manifestBytes, err := protojson.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	installation := &Installation{ID: 42, PluginID: manifest.PluginId, Version: manifest.Version, InstallPath: "/api/plugins/install-cached/plugin"}
	cache := NewArchiveCacheAt(&legacyArchiveStore{}, t.TempDir())
	path := cache.LocalInstallPath(installation)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, binaryData, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(InstalledManifestPath(path), manifestBytes, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := cache.Ensure(context.Background(), installation); err == nil || !strings.Contains(err.Error(), "plugin binary is built for") {
		t.Fatalf("cached foreign binary was not rejected: %v", err)
	}
}

func TestArchiveCacheRepairsCorruptedBinaryOnCacheHit(t *testing.T) {
	for _, proxy := range []bool{false, true} {
		name := "local"
		if proxy {
			name = "proxy"
		}
		t.Run(name, func(t *testing.T) {
			binaryData := []byte("#!/bin/sh\nexit 0\n")
			checksum := sha256.Sum256(binaryData)
			manifest := testPluginManifest(t, "silo.metadb", "0.0.19")
			manifest.Checksum = hex.EncodeToString(checksum[:])
			manifestBytes, err := protojson.Marshal(manifest)
			if err != nil {
				t.Fatal(err)
			}
			archiveBytes, err := buildBinaryPluginArchive(manifestBytes, binaryData)
			if err != nil {
				t.Fatal(err)
			}
			store := &legacyArchiveStore{archive: &InstallationArchive{
				InstallationID: 42, ManifestJSON: manifestBytes, Checksum: manifest.GetChecksum(), Bytes: archiveBytes,
			}}
			installation := &Installation{
				ID: 42, PluginID: manifest.GetPluginId(), Version: manifest.GetVersion(),
				InstallPath: filepath.Join(t.TempDir(), "install-cached", "plugin"),
			}
			cache := NewArchiveCache(store)
			if proxy {
				cache = NewArchiveCacheAt(store, t.TempDir())
			}
			if _, err := cache.Ensure(t.Context(), installation); err != nil {
				t.Fatalf("populate cache: %v", err)
			}
			path := cache.LocalInstallPath(installation)
			// Keep the file executable and the same size, but change its contents.
			if err := os.WriteFile(path, []byte("#!/bin/sh\nexit 1\n"), 0o755); err != nil {
				t.Fatal(err)
			}
			if _, err := cache.Ensure(t.Context(), installation); err != nil {
				t.Fatalf("repair cache: %v", err)
			}
			got, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(got, binaryData) {
				t.Fatalf("cached binary = %q, want stored binary %q", got, binaryData)
			}
		})
	}
}

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

// A cache with its own root (a proxy node's) never touches the install path
// the API server recorded: it rehydrates under <root>/<plugin>/<version>/
// <release>/plugin, reuses that copy on the next Ensure, and drops the
// previous release once a replacement is in place.
func TestArchiveCacheAtOwnRootRehydratesUnderItAndPrunesOldReleases(t *testing.T) {
	ctx := context.Background()
	root := filepath.Join(t.TempDir(), "proxy-cache")
	apiInstallRoot := filepath.Join(t.TempDir(), "api-machine", "plugins", "silo.metadb")

	release := func(version, dir, script string) (*Installation, *legacyArchiveStore) {
		t.Helper()
		binaryData := []byte(script)
		checksum := sha256.Sum256(binaryData)
		manifest := testPluginManifest(t, "silo.metadb", version)
		manifest.Checksum = hex.EncodeToString(checksum[:])
		manifestBytes, err := protojson.Marshal(manifest)
		if err != nil {
			t.Fatal(err)
		}
		archiveBytes, err := buildBinaryPluginArchive(manifestBytes, binaryData)
		if err != nil {
			t.Fatal(err)
		}
		store := &legacyArchiveStore{archive: &InstallationArchive{
			InstallationID: 42, ManifestJSON: manifestBytes, Checksum: manifest.GetChecksum(), Bytes: archiveBytes,
		}}
		return &Installation{
			ID:          42,
			PluginID:    "silo.metadb",
			Version:     version,
			InstallPath: filepath.Join(apiInstallRoot, version, dir, "plugin"),
		}, store
	}

	first, firstStore := release("0.0.19", "install-aaaa", "#!/bin/sh\nexit 0\n")
	cache := NewArchiveCacheAt(firstStore, root)
	wantFirst := filepath.Join(root, "silo.metadb", "0.0.19", "install-aaaa", "plugin")
	if got := cache.LocalInstallPath(first); got != wantFirst {
		t.Fatalf("LocalInstallPath = %q, want %q", got, wantFirst)
	}
	if got := NewArchiveCache(firstStore).LocalInstallPath(first); got != first.InstallPath {
		t.Fatalf("LocalInstallPath without a root = %q, want the recorded path %q", got, first.InstallPath)
	}

	manifest, err := cache.Ensure(ctx, first)
	if err != nil {
		t.Fatalf("Ensure(first) returned error: %v", err)
	}
	if manifest.GetVersion() != "0.0.19" {
		t.Fatalf("manifest version = %q", manifest.GetVersion())
	}
	if _, err := os.Stat(wantFirst); err != nil {
		t.Fatalf("binary was not rehydrated under the cache root: %v", err)
	}
	if _, err := os.Stat(apiInstallRoot); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the API server's install root was touched: %v", err)
	}

	// The second Ensure finds the copy and does not read the archive again.
	firstStore.archive = nil
	if _, err := cache.Ensure(ctx, first); err != nil {
		t.Fatalf("Ensure(first) again returned error: %v", err)
	}

	second, secondStore := release("0.0.20", "install-bbbb", "#!/bin/sh\nexit 1\n")
	cache = NewArchiveCacheAt(secondStore, root)
	if _, err := cache.Ensure(ctx, second); err != nil {
		t.Fatalf("Ensure(second) returned error: %v", err)
	}
	wantSecond := filepath.Join(root, "silo.metadb", "0.0.20", "install-bbbb", "plugin")
	if _, err := os.Stat(wantSecond); err != nil {
		t.Fatalf("second release was not rehydrated: %v", err)
	}
	if _, err := os.Stat(filepath.Dir(wantFirst)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("previous release still present after the replacement: %v", err)
	}
}

type blockedArchiveStore struct {
	*legacyArchiveStore
	calls   atomic.Int32
	release chan struct{}
}

func (s *blockedArchiveStore) GetArchive(ctx context.Context, id int) (*InstallationArchive, error) {
	s.calls.Add(1)
	select {
	case <-s.release:
		return s.legacyArchiveStore.GetArchive(ctx, id)
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func TestArchiveCacheSerializesRehydration(t *testing.T) {
	binary := []byte("#!/bin/sh\nexit 0\n")
	manifest := testPluginManifest(t, "silo.metadb", "0.0.19")
	sum := sha256.Sum256(binary)
	manifest.Checksum = hex.EncodeToString(sum[:])
	raw, err := protojson.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	archive, err := buildBinaryPluginArchive(raw, binary)
	if err != nil {
		t.Fatal(err)
	}
	store := &blockedArchiveStore{legacyArchiveStore: &legacyArchiveStore{archive: &InstallationArchive{InstallationID: 42, ManifestJSON: raw, Checksum: manifest.Checksum, Bytes: archive}}, release: make(chan struct{})}
	cache := NewArchiveCacheAt(store, t.TempDir())
	installation := &Installation{ID: 42, PluginID: manifest.PluginId, Version: manifest.Version, InstallPath: "/api/plugins/install-release/plugin"}
	var wg sync.WaitGroup
	ready := make(chan struct{}, 32)
	for range 32 {
		wg.Go(func() {
			ready <- struct{}{}
			if _, err := cache.Ensure(t.Context(), installation); err != nil {
				t.Error(err)
			}
		})
	}
	for range 32 {
		<-ready
	}
	close(store.release)
	wg.Wait()
	if got := store.calls.Load(); got != 1 {
		t.Errorf("archive loads=%d, want 1", got)
	}

	if err := validateInstalledFiles(cache.LocalInstallPath(installation), manifest); err != nil {
		t.Fatal(err)
	}
}
