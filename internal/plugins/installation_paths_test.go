package plugins

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"google.golang.org/protobuf/encoding/protojson"
)

func TestPluginPathIdentityComponents(t *testing.T) {
	for _, invalid := range []string{"", ".", ".."} {
		for _, idField := range []bool{true, false} {
			id, version := "silo.example", "1.2.3"
			if idField {
				id = invalid
			} else {
				version = invalid
			}
			if _, _, err := pluginIdentitySegments(id, version); err == nil {
				t.Errorf("accepted identity %q@%q", id, version)
			}
			manifest := testPluginManifest(t, id, version)
			if err := ValidateManifest(manifest); err == nil {
				t.Errorf("admitted identity %q@%q", id, version)
			}
			if _, err := pluginVersionRoot("cache", id, version); err == nil {
				t.Errorf("derived install root for %q@%q", id, version)
			}
		}
	}
	for _, tc := range []struct{ value, want string }{
		{"silo.example", "silo.example"}, {"1.2.3+build.4", "1.2.3+build.4"},
		{"a/b\\c:d@e", "a_b_c_d_e"}, {"日本語 plugin", "日本語 plugin"},
		{"...", "..."}, {"NUL", "NUL"}, {"COM1", "COM1"}, {"name.", "name."},
	} {
		got, err := pluginPathSegment(tc.value, "identity")
		if err != nil || got != tc.want {
			t.Errorf("segment(%q) = %q, %v; want %q", tc.value, got, err, tc.want)
		}
		if err := ValidateManifest(testPluginManifest(t, tc.value, tc.value)); err != nil {
			t.Errorf("changed valid identity %q: %v", tc.value, err)
		}
	}
}

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
	_, binaryErr := installer.replaceBinary(t.Context(), existing, data, manifest.Checksum, manifest, nil, nil)
	_, archiveErr := installer.replaceArchive(t.Context(), existing, archive, InstallArchiveRequest{})
	for _, err := range []error{binaryErr, archiveErr} {
		if _, ok := AsPackageError(err); !ok || !strings.Contains(err.Error(), "does not match installation") {
			t.Fatalf("replacement mismatch = %v, want a package error", err)
		}
	}
	if len(store.createInputs)+len(store.updateInputs)+len(store.deleteIDs)+len(store.saveArchiveIDs) != 0 {
		t.Fatalf("mismatched replacement mutated store: %+v", store)
	}
}

// Uninstall trusts a valid row's host-written path, legacy layouts included,
// and leaves a pre-check row's files alone: with plugin id and version ".." a
// legacy layout's directory is the grandparent of the plugin root. Either way
// the row goes and Delete succeeds, so the handler finishes the uninstall.
func TestInstallationStoreDeleteRemovesOnlyTrustedFiles(t *testing.T) {
	pool := builtinGuardTestPool(t)
	store := NewInstallationStore(pool)
	parent := t.TempDir()
	base := filepath.Join(parent, "data", "plugins")
	sentinel := filepath.Join(parent, "data", "keep")
	legacyRelease := filepath.Join(base, "silo.example", "1.2.3")
	for _, dir := range []string{sentinel, legacyRelease} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for _, tc := range []struct {
		pluginID, version string
		removed, kept     string
	}{
		{"..", "..", "", sentinel},
		{"silo.example", "1.2.3", legacyRelease, sentinel},
	} {
		installation, err := store.Create(t.Context(), CreateInstallationInput{
			PluginID: tc.pluginID, Version: tc.version,
			InstallPath: filepath.Join(base, sanitizeFilesystemSegment(tc.pluginID), sanitizeFilesystemSegment(tc.version), "plugin"),
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
