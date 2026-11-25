package plugins

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"io/fs"
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

// foreignELF returns an ELF header for another platform than the running
// test's: linux/arm64, or linux/amd64 on an arm64 host.
func foreignELF() []byte {
	binaryData := make([]byte, 64)
	copy(binaryData, []byte{0x7f, 'E', 'L', 'F', 2, 1, 1})
	binaryData[16], binaryData[18], binaryData[20], binaryData[52] = 2, 0xB7, 1, 64
	if runtime.GOOS == "linux" && runtime.GOARCH == "arm64" {
		binaryData[18] = 0x3E // EM_X86_64
	}
	return binaryData
}

func TestArchiveCacheRejectsForeignBinaryOnCacheHit(t *testing.T) {
	// The cache represents a volume moved from another proxy platform.
	binaryData := foreignELF()
	manifest, manifestBytes, _ := binaryRelease(t, "0.0.19", binaryData)
	installation := &Installation{ID: 42, PluginID: manifest.PluginId, Version: manifest.Version, InstallPath: "/api/plugins/install-cached/plugin"}
	cache := NewArchiveCacheAt(&legacyArchiveStore{}, t.TempDir())
	path := requireLocalInstallPath(t, cache, installation)
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
			path := requireLocalInstallPath(t, cache, installation)
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

// Only a failure of the stored archive's own contents is
// ErrArchiveUnreadable. A store failure is not, so a caller that tolerates one
// damaged plugin still fails on a database fault.
func TestArchiveCacheEnsureMarksOnlyDamagedArchivesUnreadable(t *testing.T) {
	// The manifest and binary check out, but an extra entry, which only
	// extraction reads, fails its CRC.
	binary := []byte("#!/bin/sh\nexit 0\n")
	manifest, manifestBytes, _ := binaryRelease(t, "0.0.19", binary)
	var buffer bytes.Buffer
	writer := zip.NewWriter(&buffer)
	if err := writeArchiveEntry(writer, "manifest.json", manifestBytes); err != nil {
		t.Fatal(err)
	}
	if err := writeArchiveEntry(writer, "plugin", binary); err != nil {
		t.Fatal(err)
	}
	extra, err := writer.CreateHeader(&zip.FileHeader{Name: "assets/extra.txt", Method: zip.Store})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := extra.Write([]byte("extra entry data")); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	corruptEntry := buffer.Bytes()
	corruptEntry[bytes.Index(corruptEntry, []byte("extra entry data"))] ^= 0xff

	// The binary entry is marked as a directory. The archive opens and its
	// checksums match, but extraction writes a directory where the binary goes.
	dirManifest, dirManifestBytes, _ := binaryRelease(t, "0.0.19", nil)
	var dirBuffer bytes.Buffer
	dirWriter := zip.NewWriter(&dirBuffer)
	if err := writeArchiveEntry(dirWriter, "manifest.json", dirManifestBytes); err != nil {
		t.Fatal(err)
	}
	dirHeader := &zip.FileHeader{Name: "plugin", Method: zip.Store}
	dirHeader.SetMode(fs.ModeDir | 0o755)
	if _, err := dirWriter.CreateHeader(dirHeader); err != nil {
		t.Fatal(err)
	}
	if err := dirWriter.Close(); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		name  string
		store *legacyArchiveStore
		want  bool
	}{
		{name: "archive is not a zip", want: true, store: &legacyArchiveStore{archive: &InstallationArchive{
			InstallationID: 42, ManifestJSON: []byte("{}"), Checksum: "0", Bytes: []byte("not a zip"),
		}}},
		{name: "entry fails its CRC", want: true, store: &legacyArchiveStore{archive: &InstallationArchive{
			InstallationID: 42, ManifestJSON: manifestBytes, Checksum: manifest.GetChecksum(), Bytes: corruptEntry,
		}}},
		{name: "binary is a directory entry", want: true, store: &legacyArchiveStore{archive: &InstallationArchive{
			InstallationID: 42, ManifestJSON: dirManifestBytes, Checksum: dirManifest.GetChecksum(), Bytes: dirBuffer.Bytes(),
		}}},
		{name: "store fails", store: &legacyArchiveStore{getErr: errors.New("db unavailable")}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			installation := &Installation{ID: 42, PluginID: "silo.metadb", Version: "0.0.19",
				InstallPath: filepath.Join(t.TempDir(), "plugin")}

			_, err := NewArchiveCache(tc.store).Ensure(t.Context(), installation)
			if err == nil {
				t.Fatal("Ensure() succeeded without a usable archive")
			}
			if got := errors.Is(err, ErrArchiveUnreadable); got != tc.want {
				t.Fatalf("errors.Is(%v, ErrArchiveUnreadable) = %v, want %v", err, got, tc.want)
			}
		})
	}
}

type legacyArchiveStore struct {
	archive             *InstallationArchive
	getErr              error
	saveErr             error
	savedInstallationID int
	savedManifestJSON   []byte
	savedChecksum       string
	savedArchiveBytes   []byte
}

func (s *legacyArchiveStore) GetArchive(context.Context, int) (*InstallationArchive, error) {
	return s.archive, s.getErr
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
// <release>/plugin, reuses that copy on the next Ensure, and removes only
// the observed previous release when the host reconciles a replacement.
func TestArchiveCacheAtOwnRootRehydratesAndRetiresObservedRelease(t *testing.T) {
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
	if got := requireLocalInstallPath(t, cache, first); got != wantFirst {
		t.Fatalf("LocalInstallPath = %q, want %q", got, wantFirst)
	}
	if got := requireLocalInstallPath(t, NewArchiveCache(firstStore), first); got != first.InstallPath {
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
	staged := filepath.Join(root, "silo.metadb", "0.0.20", "install-staged")
	if err := os.MkdirAll(staged, 0o755); err != nil {
		t.Fatal(err)
	}
	cache = NewArchiveCacheAt(secondStore, root)
	if _, err := cache.Ensure(ctx, second); err != nil {
		t.Fatalf("Ensure(second) returned error: %v", err)
	}
	wantSecond := filepath.Join(root, "silo.metadb", "0.0.20", "install-bbbb", "plugin")
	if _, err := os.Stat(wantSecond); err != nil {
		t.Fatalf("second release was not rehydrated: %v", err)
	}
	if err := cache.retire(first, second); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Dir(wantFirst)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("previous release still present after the replacement: %v", err)
	}
	if _, err := os.Stat(staged); err != nil {
		t.Fatalf("retiring a release removed a staged package: %v", err)
	}
	if _, err := cache.Ensure(ctx, first); !errors.Is(err, ErrInstallationChanged) {
		t.Fatalf("stale cache read returned %v, want ErrInstallationChanged", err)
	}
}

func TestArchiveCacheReusesRestoredPathAndRejectsEarlierRead(t *testing.T) {
	ctx := t.Context()
	oldManifest, oldJSON, oldZip := binaryRelease(t, "1.0.0", []byte("#!/bin/sh\nexit 1\n"))
	wantBinary := []byte("#!/bin/sh\nexit 0\n")
	restoredManifest, restoredJSON, restoredZip := binaryRelease(t, "1.0.0", wantBinary)
	oldArchive := &InstallationArchive{InstallationID: 42, ManifestJSON: oldJSON, Checksum: oldManifest.Checksum, Bytes: oldZip}
	restoredArchive := &InstallationArchive{InstallationID: 42, ManifestJSON: restoredJSON, Checksum: restoredManifest.Checksum, Bytes: restoredZip}
	installation := &Installation{ID: 42, PluginID: restoredManifest.PluginId, Version: restoredManifest.Version, InstallPath: "/api/plugins/install-old/plugin"}
	replacement := *installation
	replacement.InstallPath = "/api/plugins/install-new/plugin"

	// The first load has read the old archive but cannot put it on disk yet.
	// A later guarded read sees either the replacement or the restored row.
	entered := make(chan struct{})
	resume := make(chan struct{})
	release := sync.OnceFunc(func() { close(resume) })
	defer release()
	var calls atomic.Int32
	var current atomic.Pointer[Installation]
	current.Store(installation)
	store := &releaseArchiveFixture{initial: installation, read: func(_ context.Context, want *Installation) (*InstallationArchive, error) {
		if calls.Add(1) == 1 {
			close(entered)
			<-resume
			return oldArchive, nil
		}
		if row := current.Load(); row.ID != want.ID || row.Version != want.Version || row.InstallPath != want.InstallPath {
			return nil, ErrInstallationChanged
		}
		return restoredArchive, nil
	}}
	cache := NewArchiveCacheAt(store, t.TempDir())
	oldRead := make(chan error, 1)
	go func() {
		_, err := cache.Ensure(ctx, installation)
		oldRead <- err
	}()
	waitForLifecycleTestSignal(t, entered)
	current.Store(&replacement)
	if err := cache.retire(installation, &replacement); err != nil {
		t.Fatal(err)
	}
	// A poll that already read this row can arrive after retirement. Its
	// snapshot alone cannot make the retired archive usable.
	cache.retryRetirements(ctx, map[int]*Installation{installation.ID: installation})
	if _, err := cache.Manifest(ctx, installation); !errors.Is(err, ErrInstallationChanged) {
		t.Fatalf("lookup of a superseded path returned %v, want ErrInstallationChanged", err)
	}
	current.Store(installation)
	// The restore can become usable even before the next follower poll. A
	// fresh read must also be independent of the old read's singleflight.
	restoredRead := make(chan error, 1)
	go func() {
		_, err := cache.Ensure(ctx, installation)
		restoredRead <- err
	}()
	select {
	case err := <-restoredRead:
		if err != nil {
			t.Fatalf("valid restored path is unavailable: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("a restored path waited for an earlier read of the retired archive")
	}
	release()
	if err := <-oldRead; !errors.Is(err, ErrInstallationChanged) {
		t.Fatalf("read begun before retirement returned %v, want ErrInstallationChanged", err)
	}
	path := requireLocalInstallPath(t, cache, installation)
	if got, err := os.ReadFile(path); err != nil || !bytes.Equal(got, wantBinary) {
		t.Fatalf("cached restored binary = %q, err = %v", got, err)
	}
}

// The release check models the store's query that matches the archive to the
// requested installation ID, version and path in one database snapshot.
type releaseArchiveFixture struct {
	legacyArchiveStore
	initial *Installation
	read    func(context.Context, *Installation) (*InstallationArchive, error)
}

func (s *releaseArchiveFixture) GetArchive(ctx context.Context, _ int) (*InstallationArchive, error) {
	return s.read(ctx, s.initial)
}

func (s *releaseArchiveFixture) archiveForRelease(ctx context.Context, installation *Installation) (*InstallationArchive, error) {
	return s.read(ctx, installation)
}

func TestFollowerProtectsRestoredPathFromPendingCleanup(t *testing.T) {
	ctx := t.Context()
	root := t.TempDir()
	oldBinary := []byte("#!/bin/sh\nexit 1\n")
	wantBinary := []byte("#!/bin/sh\nexit 0\n")
	_, oldJSON, _ := binaryRelease(t, "1.0.0", oldBinary)
	manifest, raw, zip := binaryRelease(t, "1.0.0", wantBinary)
	installation := &Installation{ID: 1, PluginID: manifest.PluginId, Version: manifest.Version, Enabled: true,
		InstallPath: filepath.Join(root, manifest.PluginId, manifest.Version, "plugin"),
	}
	unblock := blockLegacyFileRemoval(t, installation)
	rows := newFakeServiceInstallationStore(installation)
	archive := &InstallationArchive{InstallationID: installation.ID, ManifestJSON: raw, Checksum: manifest.Checksum, Bytes: zip}
	store := &releaseArchiveFixture{initial: installation, read: func(ctx context.Context, want *Installation) (*InstallationArchive, error) {
		row, err := rows.GetByID(ctx, want.ID)
		if err != nil || row.InstallPath != want.InstallPath || row.Version != want.Version {
			return nil, ErrInstallationChanged
		}
		return archive, nil
	}}
	cache := NewArchiveCacheAt(store, root)
	follower := &Service{installations: rows, archiveCache: cache}
	follower.reconcileLifecycle(ctx, false)
	if err := rows.Delete(ctx, installation.ID); err != nil {
		t.Fatal(err)
	}
	follower.reconcileLifecycle(ctx, false)

	// The path returned as current. Leave a valid-looking old cache copy
	// there as if the prior cleanup had not finished removing the files.
	path := requireLocalInstallPath(t, cache, installation)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, oldBinary, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(InstalledManifestPath(path), oldJSON, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := rows.Create(ctx, CreateInstallationInput{
		PluginID: installation.PluginID, Version: installation.Version, InstallPath: installation.InstallPath, Enabled: installation.Enabled,
	}); err != nil {
		t.Fatal(err)
	}
	unblock()
	follower.reconcileLifecycle(ctx, false)
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("pending retirement removed the path observed as current: %v", err)
	}

	if _, err := cache.Ensure(ctx, installation); err != nil {
		t.Fatal(err)
	}
	// A delayed snapshot that still misses the restored row cannot replay
	// the obsolete cleanup after the guarded read restored the package.
	cache.retryRetirements(ctx, nil)
	if got, err := os.ReadFile(path); err != nil || !bytes.Equal(got, wantBinary) {
		t.Fatalf("cached restored binary = %q, err = %v", got, err)
	}
}

func TestArchiveCacheRejectsSupersededSameVersionDB(t *testing.T) {
	ctx := t.Context()
	store := NewInstallationStore(builtinGuardTestPool(t))
	archive := func(script string) ArchiveInput {
		t.Helper()
		binary := []byte(script)
		manifest := testPluginManifest(t, "silo.archive.release-race", "1.0.0")
		manifest.Checksum = sha256Hex(binary)
		raw, err := protojson.Marshal(manifest)
		if err != nil {
			t.Fatal(err)
		}
		data, err := buildBinaryPluginArchive(raw, binary)
		if err != nil {
			t.Fatal(err)
		}
		return ArchiveInput{ManifestJSON: raw, Checksum: manifest.Checksum, Bytes: data}
	}
	initial := archive("#!/bin/sh\nexit 0\n")
	replacement := archive("#!/bin/sh\nexit 1\n")
	writer := t.TempDir()
	old, err := store.Create(ctx, CreateInstallationInput{
		PluginID: "silo.archive.release-race", Version: "1.0.0",
		InstallPath: filepath.Join(writer, "install-old", "plugin"), Archive: initial,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := store.Delete(context.Background(), old.ID); err != nil {
			t.Error(err)
		}
	})
	original, err := store.GetArchive(ctx, old.ID)
	if err != nil {
		t.Fatal(err)
	}
	installer := NewInstaller(store, InstallerOptions{BaseDir: t.TempDir()})
	follower := NewService(nil, store, nil, nil, installer, nil)
	cache := follower.archiveCache
	follower.reconcileLifecycle(ctx, false)
	nextPath := filepath.Join(writer, "install-new", "plugin")
	if err := store.Update(ctx, old.ID, UpdateInstallationInput{InstallPath: &nextPath, Archive: &replacement}); err != nil {
		t.Fatal(err)
	}
	if _, err := cache.Ensure(ctx, old); !errors.Is(err, ErrInstallationChanged) {
		t.Fatalf("Ensure on a superseded package returned %v, want ErrInstallationChanged", err)
	}
	if err := store.repairArchive(ctx, original, initial.Bytes); err != nil {
		t.Fatal(err)
	}
	current, err := store.GetByID(ctx, old.ID)
	if err != nil {
		t.Fatal(err)
	}
	if manifest, err := cache.Ensure(ctx, current); err != nil || manifest.GetChecksum() != replacement.Checksum {
		t.Fatalf("rehydrate committed replacement after late repair: %v", err)
	}
	follower.reconcileLifecycle(ctx, false)

	// A restore can return the exact earlier installation path and archive.
	// The existing follower must read the restored package before its next poll.
	if err := store.Update(ctx, old.ID, UpdateInstallationInput{InstallPath: &old.InstallPath, Archive: &initial}); err != nil {
		t.Fatal(err)
	}
	if manifest, err := cache.Ensure(ctx, old); err != nil || manifest.GetChecksum() != initial.Checksum {
		t.Fatalf("rehydrate retired path from restored row: %v", err)
	}
}

// Plugin IDs the installer's mapping would merge get their own cache
// directories, so rehydrating one leaves the others' files in place (#2800).
func TestArchiveCacheKeepsPluginsWhoseIDsDifferOnlyInSeparators(t *testing.T) {
	ctx := t.Context()
	root := filepath.Join(t.TempDir(), "proxy-cache")
	ids := []string{"acme/x", "acme_x", "acme:x", "acme@x", "acme\\x", "acme%2Fx"}
	installations := make([]*Installation, len(ids))
	stores := make([]*legacyArchiveStore, len(ids))
	for i, id := range ids {
		binary := []byte("#!/bin/sh\nexit " + string(rune('0'+i)) + "\n")
		manifest := testPluginManifest(t, id, "1.0.0")
		manifest.Checksum = sha256Hex(binary)
		manifestBytes, err := protojson.Marshal(manifest)
		if err != nil {
			t.Fatal(err)
		}
		archiveBytes, err := buildBinaryPluginArchive(manifestBytes, binary)
		if err != nil {
			t.Fatal(err)
		}
		stores[i] = &legacyArchiveStore{archive: &InstallationArchive{
			InstallationID: 100 + i, ManifestJSON: manifestBytes, Checksum: manifest.GetChecksum(), Bytes: archiveBytes,
		}}
		installations[i] = &Installation{ID: 100 + i, PluginID: id, Version: "1.0.0",
			InstallPath: filepath.Join(t.TempDir(), "api", "1.0.0", "install-"+string(rune('a'+i)), "plugin")}
	}
	if got, want := requireLocalInstallPath(t, NewArchiveCacheAt(stores[0], root), installations[0]),
		filepath.Join(root, "acme%2Fx", "1.0.0", "install-a", "plugin"); got != want {
		t.Fatalf("LocalInstallPath(acme/x) = %q, want %q", got, want)
	}
	if got, want := requireLocalInstallPath(t, NewArchiveCacheAt(stores[1], root), installations[1]),
		filepath.Join(root, "acme_x", "1.0.0", "install-b", "plugin"); got != want {
		t.Fatalf("LocalInstallPath(acme_x) = %q, want %q (an ID without escaped characters keeps its directory)", got, want)
	}

	paths := make([]string, len(ids))
	for i := range ids {
		if _, err := NewArchiveCacheAt(stores[i], root).Ensure(ctx, installations[i]); err != nil {
			t.Fatalf("Ensure(%s): %v", ids[i], err)
		}
		paths[i] = requireLocalInstallPath(t, NewArchiveCacheAt(stores[i], root), installations[i])
	}
	for i, path := range paths {
		if _, err := os.Stat(path); err != nil {
			t.Errorf("%s binary after every rehydration: %v", ids[i], err)
		}
	}
}

// An identity whose escaped name would pass the component limit gets a
// bounded, distinct name, so a proxy node can still rehydrate it.
func TestCachePathSegmentStaysWithinTheComponentLimit(t *testing.T) {
	long := strings.Repeat("a/", 100)
	other := strings.Repeat("a:", 100)
	names := map[string]string{}
	for _, value := range []string{long, other, strings.Repeat("a", 255), "acme/x"} {
		got, err := cachePathSegment(value, "plugin id")
		if err != nil {
			t.Fatal(err)
		}
		if len(got) > maxCacheSegmentBytes {
			t.Errorf("segment for a %d-byte identity is %d bytes, want at most %d", len(value), len(got), maxCacheSegmentBytes)
		}
		if previous, ok := names[got]; ok {
			t.Errorf("%q and %q share segment %q", previous, value, got)
		}
		names[got] = value
	}
	if got, _ := cachePathSegment(strings.Repeat("a", 255), "plugin id"); got != strings.Repeat("a", 255) {
		t.Errorf("a 255-byte identity without escapes = %q, want it unchanged", got)
	}
	if got, _ := cachePathSegment(long, "plugin id"); !strings.HasPrefix(got, "%h") {
		t.Errorf("an over-long escaped identity = %q, want its hashed name", got)
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

	if _, err := cache.validateInstalledFiles(requireLocalInstallPath(t, cache, installation), manifest); err != nil {
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
	path := requireLocalInstallPath(t, cache, installation)

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

func requireLocalInstallPath(t *testing.T, cache *ArchiveCache, installation *Installation) string {
	t.Helper()
	path, err := cache.LocalInstallPath(installation)
	if err != nil {
		t.Fatal(err)
	}
	return path
}

// platformTestStore is an installation's stored archive, holding another
// platform's build, and the builds stored for other platforms.
type platformTestStore struct {
	mu     sync.Mutex
	stored *InstallationArchive
	builds map[string]*PlatformArchive
	// replaced makes every save find the installation replaced, and saveErr
	// makes every save fail.
	replaced bool
	saveErr  error
	// reads counts reads of the stored archive.
	reads atomic.Int32
}

func (s *platformTestStore) GetArchive(context.Context, int) (*InstallationArchive, error) {
	s.reads.Add(1)
	return s.stored, nil
}

func (s *platformTestStore) SaveArchive(context.Context, int, []byte, string, []byte) error {
	return nil
}

func (s *platformTestStore) GetPlatformArchive(_ context.Context, _ int, goos, goarch, baseChecksum string) (*PlatformArchive, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if archive, ok := s.builds[goos+"/"+goarch+"@"+baseChecksum]; ok {
		return archive, nil
	}
	return nil, ErrArchiveNotFound
}

func (s *platformTestStore) SavePlatformArchive(_ context.Context, in SavePlatformArchiveInput) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.saveErr != nil {
		return false, s.saveErr
	}
	if s.replaced {
		return false, nil
	}
	if s.builds == nil {
		s.builds = map[string]*PlatformArchive{}
	}
	s.builds[in.OS+"/"+in.Arch+"@"+in.BaseChecksum] = &PlatformArchive{Archive: in.Archive, Signature: in.Signature}
	return true, nil
}

// platformBuildFixture is an installation from a catalog whose stored
// archive holds another platform's build, on a cache without its own root as
// an API server has, and a catalog that serves this platform's build.
type platformBuildFixture struct {
	store        *platformTestStore
	installation *Installation
	// root, when set, is the cache's own root, as an API server can have.
	root       string
	hostBinary []byte
	// signature is the fetched build's release signature check.
	signature Signature
	fetches   atomic.Int32
	// mutate, when set, changes the fetched build's manifest.
	mutate func(*pluginv1.PluginManifest)
}

func newPlatformBuildFixture(t *testing.T) *platformBuildFixture {
	t.Helper()
	foreign := foreignELF()
	manifest, manifestBytes, archive := binaryRelease(t, "1.0.0", foreign)
	repositoryID := 1
	return &platformBuildFixture{
		store: &platformTestStore{stored: &InstallationArchive{
			InstallationID: 7, ManifestJSON: manifestBytes, Checksum: manifest.GetChecksum(), Bytes: archive,
		}},
		installation: &Installation{
			ID: 7, RepositoryID: &repositoryID, PluginID: manifest.GetPluginId(), Version: manifest.GetVersion(),
			InstallPath: filepath.Join(t.TempDir(), "install-1", "plugin"),
		},
		// Not an executable format binaryPlatform knows, so it passes as
		// this platform's.
		hostBinary: []byte("#!/bin/sh\n# this platform's build\n"),
	}
}

func (f *platformBuildFixture) cache(t *testing.T) *ArchiveCache {
	cache := NewArchiveCache(f.store)
	if f.root != "" {
		cache = NewArchiveCacheAt(f.store, f.root)
	}
	cache.platforms = &platformArchives{store: f.store, fetch: func(context.Context, *Installation) (*downloadedBinary, error) {
		f.fetches.Add(1)
		manifest := testPluginManifest(t, f.installation.PluginID, f.installation.Version)
		manifest.Checksum = sha256Hex(f.hostBinary)
		if f.mutate != nil {
			f.mutate(manifest)
		}
		manifestJSON, err := protojson.Marshal(manifest)
		if err != nil {
			return nil, err
		}
		return &downloadedBinary{data: f.hostBinary, checksum: manifest.Checksum, manifest: manifest, manifestJSON: manifestJSON, signature: f.signature}, nil
	}}
	return cache
}

// binaryPath is where the fixture's caches install the binary.
func (f *platformBuildFixture) binaryPath(t *testing.T) string {
	t.Helper()
	return requireLocalInstallPath(t, f.cache(t), f.installation)
}

func (f *platformBuildFixture) installedBinary(t *testing.T) []byte {
	t.Helper()
	data, err := os.ReadFile(f.binaryPath(t))
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// A server on another platform than the one that installed a plugin used to
// run the stored build and fail with exec format error. It fetches the same
// version's build for its own platform once, stores it, and every server on
// that platform then rehydrates from the stored one.
func TestArchiveCacheFetchesThisPlatformsBuildOfAForeignStoredArchive(t *testing.T) {
	// An API server's cache may also have its own root, as a proxy node's
	// does; it still fetches rather than refusing, and only before running.
	for _, withRoot := range []bool{false, true} {
		t.Run(map[bool]string{false: "recorded install path", true: "own cache root"}[withRoot], func(t *testing.T) {
			testFetchesThisPlatformsBuild(t, withRoot)
		})
	}
}

func testFetchesThisPlatformsBuild(t *testing.T, withRoot bool) {
	ctx := context.Background()
	f := newPlatformBuildFixture(t)
	if withRoot {
		f.root = t.TempDir()
	}
	// A build stored for an earlier release whose package had the same
	// checksum is not this release's and is fetched again.
	staleManifest, staleManifestJSON, staleArchive := binaryRelease(t, "0.9.0", []byte("an earlier release"))
	f.store.builds = map[string]*PlatformArchive{
		runtime.GOOS + "/" + runtime.GOARCH + "@" + f.store.stored.Checksum: {Archive: &InstallationArchive{
			ManifestJSON: staleManifestJSON, Checksum: staleManifest.GetChecksum(), Bytes: staleArchive,
		}},
	}

	// A manifest read runs nothing and takes the stored build as it is,
	// leaving it on disk as the reported servers had it.
	if _, err := f.cache(t).Manifest(ctx, f.installation); err != nil {
		t.Fatalf("Manifest() = %v", err)
	}
	if f.fetches.Load() != 0 || !bytes.Equal(f.installedBinary(t), foreignELF()) {
		t.Fatalf("a manifest read fetched %d builds; want the stored build on disk and no fetch", f.fetches.Load())
	}

	// Running it replaces that build with this platform's, fetched once.
	manifest, err := f.cache(t).Ensure(ctx, f.installation)
	if err != nil {
		t.Fatalf("Ensure() = %v", err)
	}
	if f.fetches.Load() != 1 || !bytes.Equal(f.installedBinary(t), f.hostBinary) || manifest.GetChecksum() != sha256Hex(f.hostBinary) {
		t.Fatalf("Ensure() fetched %d builds and installed checksum %s; want this platform's build, fetched once", f.fetches.Load(), manifest.GetChecksum())
	}

	// Another server on this platform, with an empty cache, uses the stored
	// build.
	if err := os.RemoveAll(filepath.Dir(f.binaryPath(t))); err != nil {
		t.Fatal(err)
	}
	if _, err := f.cache(t).Ensure(ctx, f.installation); err != nil {
		t.Fatalf("Ensure() on a second server = %v", err)
	}
	if f.fetches.Load() != 1 || !bytes.Equal(f.installedBinary(t), f.hostBinary) {
		t.Fatalf("a second server fetched again (%d fetches) or installed another build", f.fetches.Load())
	}
}

// A build Silo can't fetch, or one it can't trust as the installed
// release for this platform, fails before anything runs, nothing is stored,
// and starts within the retry delay neither fetch it again nor read and
// extract the stored build again.
func TestArchiveCacheRefusesAPlatformBuildItCannotTrust(t *testing.T) {
	for _, tc := range []struct {
		name        string
		setup       func(*platformBuildFixture)
		wantFetches int32
		// wantReads is how often the stored archive is read; 1 unless set.
		wantReads int32
	}{
		{name: "plugin without a catalog", setup: func(f *platformBuildFixture) { f.installation.RepositoryID = nil }},
		{name: "build declaring another manifest", wantFetches: 1, setup: func(f *platformBuildFixture) {
			f.mutate = func(m *pluginv1.PluginManifest) { m.Capabilities = m.Capabilities[:0] }
		}},
		{name: "build for another platform", wantFetches: 1, setup: func(f *platformBuildFixture) { f.hostBinary = foreignELF() }},
		{name: "build with a weaker signature than the installed one", wantFetches: 1, setup: func(f *platformBuildFixture) {
			repo := "https://github.com/Silo-Server/silo-plugin-example"
			f.installation.Signature = Signature{State: SignatureStateVerified, SourceRepo: &repo}
		}},
		// The fetched build is for a release no longer installed, so it
		// isn't run; the next start fetches for the current one.
		{name: "installation replaced during the fetch", wantFetches: 2, wantReads: 2, setup: func(f *platformBuildFixture) { f.store.replaced = true }},
		// Nothing shows the installation is still the one fetched for, and
		// the next start waits out the retry delay like a failed fetch.
		{name: "storing the build fails", wantFetches: 1, setup: func(f *platformBuildFixture) { f.store.saveErr = errors.New("database unavailable") }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newPlatformBuildFixture(t)
			tc.setup(f)
			cache := f.cache(t)
			for range 2 {
				if _, err := cache.Ensure(context.Background(), f.installation); err == nil {
					t.Fatal("Ensure() succeeded, want an error naming the platform")
				}
			}
			if f.fetches.Load() != tc.wantFetches || len(f.store.builds) != 0 {
				t.Fatalf("fetched %d builds and stored %d; want %d fetched and none stored", f.fetches.Load(), len(f.store.builds), tc.wantFetches)
			}
			if want := max(tc.wantReads, 1); f.store.reads.Load() != want {
				t.Fatalf("read the stored archive %d times; want %d", f.store.reads.Load(), want)
			}
			if _, err := os.Stat(f.binaryPath(t)); !errors.Is(err, fs.ErrNotExist) {
				t.Fatalf("a binary was left to run at the install path: %v", err)
			}
		})
	}
}

// A build stored with a weaker signature than the installation now has, as
// after a verified reinstall of the same package, is fetched again rather
// than run.
func TestArchiveCacheRefetchesAStoredBuildSignedWithLess(t *testing.T) {
	f := newPlatformBuildFixture(t)
	repo := "https://github.com/Silo-Server/silo-plugin-example"
	f.installation.Signature = Signature{State: SignatureStateVerified, SourceRepo: &repo}
	f.signature = f.installation.Signature
	unsigned := f.hostBinary
	manifest, manifestJSON, archive := binaryRelease(t, f.installation.Version, unsigned)
	f.store.builds = map[string]*PlatformArchive{
		runtime.GOOS + "/" + runtime.GOARCH + "@" + f.store.stored.Checksum: {
			Archive:   &InstallationArchive{ManifestJSON: manifestJSON, Checksum: manifest.GetChecksum(), Bytes: archive},
			Signature: Signature{State: SignatureStateUnsigned},
		},
	}
	if _, err := f.cache(t).Ensure(context.Background(), f.installation); err != nil {
		t.Fatalf("Ensure() = %v", err)
	}
	stored := f.store.builds[runtime.GOOS+"/"+runtime.GOARCH+"@"+f.store.stored.Checksum]
	if f.fetches.Load() != 1 || stored.Signature.State != SignatureStateVerified {
		t.Fatalf("fetched %d builds and stored one signed %q; want the unsigned build replaced by a verified fetch", f.fetches.Load(), stored.Signature.State)
	}
}

// Preload runs before the API listens, so it leaves fetching this platform's
// build of a foreign stored archive to the plugin's first launch.
func TestPreloadLeavesThePlatformBuildFetchToTheFirstLaunch(t *testing.T) {
	ctx := context.Background()
	f := newPlatformBuildFixture(t)
	f.installation.Enabled = true
	cache := f.cache(t)
	svc := &Service{installations: newFakeServiceInstallationStore(f.installation), archiveCache: cache}
	if err := svc.PreloadEnabled(ctx); err != nil {
		t.Fatalf("PreloadEnabled() = %v", err)
	}
	if f.fetches.Load() != 0 {
		t.Fatalf("preload fetched %d builds; want none before the server listens", f.fetches.Load())
	}
	if _, err := cache.Ensure(ctx, f.installation); err != nil || f.fetches.Load() != 1 {
		t.Fatalf("Ensure() after preload = %v with %d fetches; want this platform's build, fetched once", err, f.fetches.Load())
	}
}
