package plugins

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"google.golang.org/protobuf/encoding/protojson"

	pluginv1 "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginproto/silo/plugin/v1"
)

// A proxy node's cache derives its paths from the stored identity. Before the
// identity check, plugin id ".." rehydrated beside the cache root and pruned
// directories under the root's parent, and "." pruned other plugins' copies.
func TestArchiveCacheKeepsDotIdentitiesInsideItsRoot(t *testing.T) {
	for _, pluginID := range []string{"..", "."} {
		t.Run(pluginID, func(t *testing.T) {
			parent := t.TempDir()
			root := filepath.Join(parent, "cache")
			outside := filepath.Join(parent, "victim", "data")
			otherPlugin := filepath.Join(root, "silo.other", "1.0.0", "install-other")
			for _, dir := range []string{outside, otherPlugin} {
				if err := os.MkdirAll(dir, 0o755); err != nil {
					t.Fatal(err)
				}
			}
			binaryData := []byte("#!/bin/sh\nexit 0\n")
			sum := sha256.Sum256(binaryData)
			manifest := testPluginManifest(t, pluginID, "1.0.0")
			manifest.Checksum = hex.EncodeToString(sum[:])
			manifestBytes, err := protojson.Marshal(manifest)
			if err != nil {
				t.Fatal(err)
			}
			// Built by hand: the stored archive predates admission's identity check.
			var buffer bytes.Buffer
			writer := zip.NewWriter(&buffer)
			for _, entry := range []struct {
				name string
				data []byte
			}{{"manifest.json", manifestBytes}, {"plugin", binaryData}} {
				if err := writeArchiveEntry(writer, entry.name, entry.data); err != nil {
					t.Fatal(err)
				}
			}
			if err := writer.Close(); err != nil {
				t.Fatal(err)
			}
			archive := buffer.Bytes()
			store := &legacyArchiveStore{archive: &InstallationArchive{
				InstallationID: 7, ManifestJSON: manifestBytes, Checksum: manifest.GetChecksum(), Bytes: archive,
			}}
			installation := &Installation{ID: 7, PluginID: pluginID, Version: "1.0.0",
				InstallPath: filepath.Join(parent, "api", "1.0.0", "install-abc", "plugin")}

			if _, err := NewArchiveCacheAt(store, root).Ensure(t.Context(), installation); err == nil {
				t.Fatal("Ensure accepted a dot plugin id")
			}
			for _, dir := range []string{outside, otherPlugin} {
				if _, err := os.Stat(dir); err != nil {
					t.Errorf("%s was removed: %v", dir, err)
				}
			}
			if _, err := os.Stat(filepath.Join(parent, "1.0.0")); !errors.Is(err, os.ErrNotExist) {
				t.Errorf("cache wrote beside its root: %v", err)
			}
		})
	}
}

func TestPluginPathReplacementRejectsDifferentPlugin(t *testing.T) {
	// Both identities are valid. Replacement refuses the mismatch as a package
	// error before writing files or touching the store.
	data := []byte("inert payload, never executed")
	sum := sha256.Sum256(data)
	manifest := testPluginManifest(t, "silo.other", "1.2.3")
	manifest.Checksum = hex.EncodeToString(sum[:])
	manifestBytes, err := protojson.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	archive, err := buildBinaryPluginArchive(manifestBytes, data)
	if err != nil {
		t.Fatal(err)
	}
	store := newRecordingInstallationStore()
	installer := NewInstaller(store, InstallerOptions{BaseDir: t.TempDir()})
	existing := &Installation{ID: 7, PluginID: "silo.example", Version: "1.2.2", InstallPath: "unused/plugin"}
	_, binaryErr := installer.prepareBinary(existing, data, manifest.Checksum, manifest, nil, nil, unsignedSignature, false)
	_, archiveErr := installer.prepareArchive(existing, archive, InstallArchiveRequest{})
	// The identity is checked before any asset is decompressed, so an archive
	// for another plugin is refused without reading its assets.
	assetArchivePath := filepath.Join(t.TempDir(), "plugin.zip")
	writeTestPluginZip(t, assetArchivePath, []*pluginv1.PackagedAsset{{Path: "web/app.js"}}, testBinaryEntry,
		testZipEntry{name: "web/app.js", data: []byte("not lzma"), lzma: true})
	assetArchive, err := os.ReadFile(assetArchivePath)
	if err != nil {
		t.Fatal(err)
	}
	_, assetArchiveErr := installer.prepareArchive(existing, assetArchive, InstallArchiveRequest{})
	for _, err := range []error{binaryErr, archiveErr, assetArchiveErr} {
		if _, ok := AsPackageError(err); !ok || !strings.Contains(err.Error(), "does not match installation") {
			t.Fatalf("replacement mismatch = %v, want a package error", err)
		}
	}
	if len(store.createInputs)+len(store.updateInputs)+len(store.deleteIDs)+len(store.saveArchiveIDs) != 0 {
		t.Fatalf("mismatched replacement mutated store: %+v", store)
	}
}

// Uninstall deletes the local legacy executable while retaining the version
// directory, which may also contain a staged replacement.
func TestInstallationStoreDeleteRemovesOnlyTrustedFiles(t *testing.T) {
	pool := builtinGuardTestPool(t)
	store := NewInstallationStore(pool)
	parent := t.TempDir()
	base := filepath.Join(parent, "data", "plugins")
	store.archiveCache = NewArchiveCacheAt(store, base)
	sentinel := filepath.Join(parent, "data", "plugin")
	legacyRelease := filepath.Join(base, "silo.example", "1.2.3")
	staged := filepath.Join(legacyRelease, "install-staged")
	for _, dir := range []string{filepath.Dir(sentinel), staged} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	legacyBinary := filepath.Join(legacyRelease, "plugin")
	for _, file := range []string{sentinel, legacyBinary} {
		if err := os.WriteFile(file, []byte("binary"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	raw, err := protojson.Marshal(testPluginManifest(t, "silo.example", "1.2.3"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(InstalledManifestPath(legacyBinary), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		pluginID, version string
		removed, kept     string
	}{
		{"..", "..", "", sentinel},
		{"silo.example", "1.2.3", legacyBinary, staged},
	} {
		installation, err := store.Create(t.Context(), CreateInstallationInput{
			PluginID: tc.pluginID, Version: tc.version,
			InstallPath: filepath.Join(base, sanitizeFilesystemSegment(tc.pluginID), sanitizeFilesystemSegment(tc.version), "plugin"),
			Archive:     ArchiveInput{ManifestJSON: []byte("{}"), Checksum: "test", Bytes: []byte("archive")},
		})
		if err != nil {
			t.Fatal(err)
		}
		if err := store.Delete(t.Context(), installation.ID); err != nil {
			t.Fatalf("Delete(%s@%s) = %v, want nil", tc.pluginID, tc.version, err)
		}
		if _, err := store.GetByID(t.Context(), installation.ID); !errors.Is(err, ErrInstallationNotFound) {
			t.Errorf("row for %s@%s still present: %v", tc.pluginID, tc.version, err)
		}
		if _, err := os.Stat(tc.kept); err != nil {
			t.Errorf("Delete(%s@%s) removed %s: %v", tc.pluginID, tc.version, tc.kept, err)
		}
		if tc.removed != "" {
			if _, err := os.Stat(tc.removed); !errors.Is(err, os.ErrNotExist) {
				t.Errorf("Delete(%s@%s) left %s: %v", tc.pluginID, tc.version, tc.removed, err)
			}
		}
	}
}

func TestLifecyclePollRetriesFailedArchiveRetirement(t *testing.T) {
	root := t.TempDir()
	ctx := t.Context()
	old := &Installation{ID: 7, PluginID: "silo.example", Version: "1.0.0", Enabled: true}
	old.InstallPath = filepath.Join(root, old.PluginID, old.Version, pluginBinaryName)
	unblock := blockLegacyFileRemoval(t, old)
	store := newFakeServiceInstallationStore(old)
	service := &Service{installations: store, archiveCache: NewArchiveCacheAt(store, root)}
	service.reconcileLifecycle(ctx, false)

	next := filepath.Join(root, old.PluginID, "2.0.0", "install-new", pluginBinaryName)
	if err := os.MkdirAll(filepath.Dir(next), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := store.Update(ctx, 7, UpdateInstallationInput{Version: new("2.0.0"), InstallPath: &next}); err != nil {
		t.Fatal(err)
	}
	if !service.reconcileInstallations(ctx) {
		t.Fatal("failed file removal blocked the newer installation")
	}
	unblock()
	if service.reconcileInstallations(ctx) {
		t.Fatal("retrying file removal reported a new runtime change")
	}
	if _, err := os.Stat(old.InstallPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("retired installation still on disk after retry: %v", err)
	}
	if _, err := os.Stat(filepath.Dir(next)); err != nil {
		t.Fatalf("replacement directory was removed: %v", err)
	}
}

func TestInstallationStoreRetiresFilesAfterCommittedDeleteDB(t *testing.T) {
	ctx := t.Context()
	pool := builtinGuardTestPool(t)
	store := NewInstallationStore(pool)
	root := t.TempDir()
	store.archiveCache = NewArchiveCacheAt(store, root)
	installation, err := store.Create(ctx, CreateInstallationInput{
		PluginID: "silo.test.delete-retirement", Version: "1.0.0",
		InstallPath: filepath.Join(root, "silo.test.delete-retirement", "1.0.0", pluginBinaryName),
		Archive:     ArchiveInput{ManifestJSON: []byte("{}"), Checksum: "test", Bytes: []byte("archive")},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Delete(context.Background(), installation.ID) })
	unblock := blockLegacyFileRemoval(t, installation)

	if err := store.Delete(ctx, installation.ID); err != nil {
		t.Fatalf("file error made the committed delete fail: %v", err)
	}
	if _, err := store.GetByID(ctx, installation.ID); !errors.Is(err, ErrInstallationNotFound) {
		t.Fatalf("uninstalled row still present: %v", err)
	}
	unblock()
	// There is no previous snapshot when the first poll follows uninstall.
	service := &Service{installations: store, archiveCache: store.archiveCache}
	service.reconcileLifecycle(ctx, false)
	if _, err := os.Stat(installation.InstallPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("uninstalled files still on disk after retry: %v", err)
	}
}

// Keep the legacy path nonempty so removing it fails even when run as root.
func blockLegacyFileRemoval(t *testing.T, installation *Installation) func() {
	t.Helper()
	if err := os.MkdirAll(installation.InstallPath, 0o755); err != nil {
		t.Fatal(err)
	}
	blocker := filepath.Join(installation.InstallPath, "blocker")
	if err := os.WriteFile(blocker, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	raw, err := protojson.Marshal(testPluginManifest(t, installation.PluginID, installation.Version))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(InstalledManifestPath(installation.InstallPath), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	return func() {
		if err := os.Remove(blocker); err != nil {
			t.Fatal(err)
		}
	}
}
