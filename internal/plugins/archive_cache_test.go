package plugins

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/protobuf/encoding/protojson"

	pluginv1 "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginproto/silo/plugin/v1"
)

// binaryRelease returns a silo.metadb manifest checksummed for binary, its
// JSON, and a binary plugin archive holding both.
func binaryRelease(t *testing.T, version string, binary []byte) (*pluginv1.PluginManifest, []byte, []byte) {
	t.Helper()
	manifest := testPluginManifest(t, "silo.metadb", version)
	manifest.Checksum = sha256Hex(binary)
	manifestBytes, err := protojson.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	archiveBytes, err := buildBinaryPluginArchive(manifestBytes, binary)
	if err != nil {
		t.Fatal(err)
	}
	return manifest, manifestBytes, archiveBytes
}

func TestArchiveCacheRejectsForeignBinaryOnCacheHit(t *testing.T) {
	// Use an ELF architecture different from the running test so the cache
	// represents a volume moved from another proxy platform.
	binaryData := make([]byte, 64)
	copy(binaryData, []byte{0x7f, 'E', 'L', 'F', 2, 1, 1})
	binaryData[16], binaryData[18], binaryData[20], binaryData[52] = 2, 0xB7, 1, 64
	if runtime.GOARCH == "arm64" {
		binaryData[18] = 0x3E // EM_X86_64
	}
	manifest, manifestBytes, _ := binaryRelease(t, "0.0.19", binaryData)
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
			manifest, manifestBytes, archiveBytes := binaryRelease(t, "0.0.19", binaryData)
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

// A legacy archive holding only the binary is rehydrated and repaired into a
// plugin archive; a failure to persist the repair does not fail the launch.
func TestArchiveCacheEnsureRecoversLegacyBinaryArchive(t *testing.T) {
	for _, tc := range []struct {
		name    string
		saveErr error
	}{
		{name: "repair persisted"},
		{name: "persist failure tolerated", saveErr: errors.New("db unavailable")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()

			binaryData := []byte("#!/bin/sh\nexit 0\n")
			manifest, manifestBytes, _ := binaryRelease(t, "0.0.19", binaryData)

			store := &legacyArchiveStore{
				archive: &InstallationArchive{
					InstallationID: 42,
					ManifestJSON:   manifestBytes,
					Checksum:       manifest.GetChecksum(),
					Bytes:          binaryData,
				},
				saveErr: tc.saveErr,
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

			installedBinary, err := os.ReadFile(installation.InstallPath)
			if err != nil {
				t.Fatalf("read rehydrated plugin binary: %v", err)
			}
			if !bytes.Equal(installedBinary, binaryData) {
				t.Fatal("rehydrated plugin binary does not match legacy binary bytes")
			}
			if tc.saveErr != nil {
				return
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
			if _, err := os.Stat(InstalledManifestPath(installation.InstallPath)); err != nil {
				t.Fatalf("expected rehydrated manifest file: %v", err)
			}
		})
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
		manifest, manifestBytes, archiveBytes := binaryRelease(t, version, []byte(script))
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
	manifest, raw, archive := binaryRelease(t, "0.0.19", []byte("#!/bin/sh\nexit 0\n"))
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

	if _, err := cache.validateInstalledFiles(cache.LocalInstallPath(installation), manifest); err != nil {
		t.Fatal(err)
	}
}

// Manifest reuses a verification only while the binary is the same file with
// the same size and mtime. Ensure, which every launch goes through, hashes the
// binary every time and so catches what Manifest's file check cannot.
func TestArchiveCacheManifestTrustsOnlyAnUnchangedVerifiedBinary(t *testing.T) {
	ctx := t.Context()
	binaryData := []byte("#!/bin/sh\nexit 0\n")
	size := int64(len(binaryData))
	manifest, manifestBytes, archiveBytes := binaryRelease(t, "0.0.19", binaryData)
	archive := &InstallationArchive{InstallationID: 42, ManifestJSON: manifestBytes, Checksum: manifest.GetChecksum(), Bytes: archiveBytes}
	installation := &Installation{
		ID: 42, PluginID: manifest.GetPluginId(), Version: manifest.GetVersion(),
		InstallPath: filepath.Join(t.TempDir(), "install-aaaa", "plugin"),
	}
	store := newFakeServiceInstallationStore(installation)
	store.archives = map[int]*InstallationArchive{42: archive}
	cache := NewArchiveCache(store)
	hashed := countHashedBytes(cache)
	path := cache.LocalInstallPath(installation)

	expectHashed := func(step string, want int64) {
		t.Helper()
		if got := hashed.Swap(0); got != want {
			t.Fatalf("%s hashed %d bytes, want %d", step, got, want)
		}
	}
	expectRestored := func(step string) {
		t.Helper()
		got, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, binaryData) {
			t.Fatalf("%s: binary = %q, want the stored binary", step, got)
		}
	}
	// rewrite changes the binary in place without changing its size, then
	// sets its mtime: the verified one when keepMtime, a later one otherwise.
	rewrite := func(keepMtime bool) {
		t.Helper()
		before, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("#!/bin/sh\nexit 1\n"), 0o755); err != nil {
			t.Fatal(err)
		}
		mtime := before.ModTime().Add(time.Second)
		if keepMtime {
			mtime = before.ModTime()
		}
		if err := os.Chtimes(path, mtime, mtime); err != nil {
			t.Fatal(err)
		}
	}

	if _, err := cache.Manifest(ctx, installation); err != nil {
		t.Fatalf("rehydrate: %v", err)
	}
	expectHashed("rehydrating Manifest", size)
	if _, err := cache.Manifest(ctx, installation); err != nil {
		t.Fatal(err)
	}
	expectHashed("warm Manifest", 0)
	if _, err := cache.Ensure(ctx, installation); err != nil {
		t.Fatal(err)
	}
	expectHashed("warm Ensure", size)

	rewrite(false)
	if _, err := cache.Manifest(ctx, installation); err != nil {
		t.Fatalf("repair after a visible change: %v", err)
	}
	expectRestored("Manifest after a visible change")

	rewrite(true)
	hashed.Store(0)
	if _, err := cache.Manifest(ctx, installation); err != nil {
		t.Fatal(err)
	}
	expectHashed("Manifest after a stat-preserving rewrite", 0)
	if _, err := cache.Ensure(ctx, installation); err != nil {
		t.Fatalf("repair before launch: %v", err)
	}
	expectRestored("Ensure after a stat-preserving rewrite")

	// A different file renamed over the binary is a new file, even with the
	// verified size and mtime, so Manifest hashes it and restores the binary.
	verified, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	replacement := filepath.Join(filepath.Dir(path), "plugin.replacement")
	if err := os.WriteFile(replacement, []byte("#!/bin/sh\nexit 2\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(replacement, verified.ModTime(), verified.ModTime()); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(replacement, path); err != nil {
		t.Fatal(err)
	}
	hashed.Store(0)
	if _, err := cache.Manifest(ctx, installation); err != nil {
		t.Fatalf("repair after a replaced file: %v", err)
	}
	// Once to reject the replacement, once to check the restored binary.
	expectHashed("Manifest after a replaced file", 2*size)
	expectRestored("Manifest after a replaced file")

	// A failed check forgets the verification, even when repair fails too.
	rewrite(true)
	delete(store.archives, 42)
	if _, err := cache.Ensure(ctx, installation); err == nil {
		t.Fatal("Ensure accepted a rewritten binary with no archive to restore")
	}
	if _, err := cache.Manifest(ctx, installation); err == nil {
		t.Fatal("Manifest served a binary that failed its last check")
	}
	store.archives[42] = archive

	// A new release lands at a new path and is hashed before it is trusted.
	upgraded := *installation
	upgraded.InstallPath = filepath.Join(t.TempDir(), "install-bbbb", "plugin")
	hashed.Store(0)
	if _, err := cache.Manifest(ctx, &upgraded); err != nil {
		t.Fatal(err)
	}
	expectHashed("Manifest of a new release", size)
}
