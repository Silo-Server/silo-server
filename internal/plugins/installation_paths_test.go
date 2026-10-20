package plugins

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
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

func TestPluginPathDerivedContainment(t *testing.T) {
	for _, root := range []string{"cache", ".", filepath.Join(string(filepath.Separator), "cache"), filepath.Join("replica", "..", "cache")} {
		versionRoot, err := pluginVersionRoot(root, "silo/example", "1.2.3+build")
		if err != nil {
			t.Fatal(err)
		}
		binaryPath, err := pluginPathUnder(versionRoot, "install-benign", "plugin")
		if err != nil {
			t.Fatal(err)
		}
		for _, path := range []string{versionRoot, binaryPath} {
			rel, err := filepath.Rel(filepath.Clean(root), path)
			if err != nil || rel == "." || rel == ".." || filepath.IsAbs(rel) || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
				t.Fatalf("derived path %q outside %q: %q, %v", path, root, rel, err)
			}
		}
	}
	// Pure path checks only: no operation is attempted at any of these paths.
	for _, parts := range [][]string{nil, {"."}, {".."}, {"..", "sibling"}} {
		if _, err := pluginPathUnder("cache", parts...); err == nil {
			t.Errorf("accepted non-descendant parts %q", parts)
		}
	}
}

func TestPluginPathStoredPathsPreserved(t *testing.T) {
	for _, stored := range []string{
		filepath.Join("plugins", "silo.example", "1.2.3", "plugin"), // legacy, no release directory
		filepath.Join("old-root", "install-benign", "plugin"),
		filepath.Join(string(filepath.Separator), "other-replica", "install-benign", "plugin"),
	} {
		installation := &Installation{PluginID: "silo.example", Version: "1.2.3", InstallPath: stored}
		path, err := (*ArchiveCache)(nil).LocalInstallPath(installation)
		if err != nil || path != stored {
			t.Fatalf("stored path changed: %q, %v; want %q", path, err, stored)
		}
		var removed []string
		if err := removeInstallationFiles(installation, func(path string) error {
			removed = append(removed, path)
			return nil
		}); err != nil || !slices.Equal(removed, []string{filepath.Dir(stored)}) {
			t.Fatalf("legacy cleanup target = %v, %v", removed, err)
		}
	}
}

type pathArchiveSpy struct{ reads, writes int }

func (s *pathArchiveSpy) GetArchive(context.Context, int) (*InstallationArchive, error) {
	s.reads++
	return nil, errors.New("unexpected archive read")
}
func (s *pathArchiveSpy) SaveArchive(context.Context, int, []byte, string, []byte) error {
	s.writes++
	return errors.New("unexpected archive write")
}

func TestPluginPathCacheRejectsInvalidStoredIdentity(t *testing.T) {
	for _, root := range []string{"", "cache"} {
		for _, invalid := range []string{"", ".", ".."} {
			for _, idField := range []bool{true, false} {
				installation := &Installation{ID: 7, PluginID: "silo.example", Version: "1.2.3", InstallPath: "unused/plugin"}
				if idField {
					installation.PluginID = invalid
				} else {
					installation.Version = invalid
				}
				store := &pathArchiveSpy{}
				cache := NewArchiveCacheAt(store, root)
				if path, err := cache.LocalInstallPath(installation); err == nil || path != "" {
					t.Fatalf("invalid stored identity resolved: %q, %v", path, err)
				}
				if _, err := cache.Ensure(t.Context(), installation); err == nil {
					t.Fatal("invalid stored identity rehydrated")
				}
				if _, err := cache.Manifest(t.Context(), installation); err == nil {
					t.Fatal("invalid stored identity read")
				}
				if store.reads != 0 || store.writes != 0 {
					t.Fatalf("invalid identity reached archive store: %+v", store)
				}
				service := &Service{}
				if _, err := service.ensureLoadedInstallation(t.Context(), installation); err == nil {
					t.Fatal("cache-less service accepted invalid identity")
				}
				if _, err := service.readInstalledManifest(t.Context(), installation); err == nil {
					t.Fatal("cache-less manifest read accepted invalid identity")
				}
			}
		}
	}
}

func TestPluginPathProxyReleaseFallback(t *testing.T) {
	cache := NewArchiveCacheAt(&pathArchiveSpy{}, "cache")
	for _, stored := range []string{"plugin", filepath.Join("..", "plugin"), filepath.Join(string(filepath.Separator), "plugin")} {
		path, err := cache.LocalInstallPath(&Installation{PluginID: "silo.example", Version: "1.2.3", InstallPath: stored})
		want := filepath.Join("cache", "silo.example", "1.2.3", "install", "plugin")
		if err != nil || path != want {
			t.Fatalf("release fallback for %q = %q, %v; want %q", stored, path, err, want)
		}
	}
}

func TestPluginPathCleanupOutcomes(t *testing.T) {
	invalid := &Installation{ID: 7, PluginID: "silo.example", Version: ".", InstallPath: "unused/plugin"}
	calls := 0
	remove := func(string) error { calls++; return errors.New("inert cleanup failure") }
	if err := removeInstallationFiles(invalid, remove); err == nil || calls != 0 {
		t.Fatalf("invalid cleanup = %v, calls=%d", err, calls)
	}
	// Replacement cleanup has no error result, even when cleanup is unsafe or
	// its removal spy fails. It cannot unwind a committed replacement.
	cleanupReplacedInstallation(t.Context(), invalid, "new-release", remove)
	valid := &Installation{ID: 7, PluginID: "silo.example", Version: "1.2.3", InstallPath: "old-release/plugin"}
	cleanupReplacedInstallation(t.Context(), valid, "new-release", remove)
	if calls != 1 {
		t.Fatalf("replacement cleanup calls=%d, want 1", calls)
	}
}

// The database is inert. Invalid identities stop Delete before any filesystem
// removal, while this spy observes the existing row-before-cleanup ordering.
type pathDeleteDB struct {
	installation *Installation
	deleted      bool
}

func (d *pathDeleteDB) Begin(context.Context) (pgx.Tx, error) { panic("unexpected transaction") }
func (d *pathDeleteDB) Query(context.Context, string, ...any) (pgx.Rows, error) {
	panic("unexpected query")
}
func (d *pathDeleteDB) Exec(_ context.Context, query string, _ ...any) (pgconn.CommandTag, error) {
	if query != "DELETE FROM plugin_installations WHERE id = $1" {
		panic("unexpected mutation")
	}
	d.deleted = true
	return pgconn.NewCommandTag("DELETE 1"), nil
}
func (d *pathDeleteDB) QueryRow(context.Context, string, ...any) pgx.Row {
	return pathInstallationRow{d.installation}
}

type pathInstallationRow struct{ installation *Installation }

func (r pathInstallationRow) Scan(dest ...any) error {
	i := r.installation
	values := []any{i.ID, i.RepositoryID, i.PluginID, i.Version, i.InstallPath, i.Enabled, i.Kind,
		i.UpdatePolicy, i.AvailableVersion, i.CreatedAt, i.UpdatedAt, i.RuntimeGeneration}
	for n, value := range values {
		reflect.ValueOf(dest[n]).Elem().Set(reflect.ValueOf(value))
	}
	return nil
}

func TestPluginPathDeleteKeepsRowGoneCleanupError(t *testing.T) {
	db := &pathDeleteDB{installation: &Installation{ID: 7, PluginID: "silo.example", Version: "..", InstallPath: "unused/plugin"}}
	store := &InstallationStore{pool: db}
	err := store.Delete(t.Context(), 7)
	if !db.deleted || err == nil || !strings.HasPrefix(err.Error(), "removing plugin installation files:") {
		t.Fatalf("Delete = %v, row deleted=%v", err, db.deleted)
	}
}

type pathDirectoryEntry struct {
	name string
	mode fs.FileMode
}

func (e pathDirectoryEntry) Name() string               { return e.name }
func (e pathDirectoryEntry) IsDir() bool                { return e.mode.IsDir() }
func (e pathDirectoryEntry) Type() fs.FileMode          { return e.mode.Type() }
func (e pathDirectoryEntry) Info() (fs.FileInfo, error) { panic("unexpected stat") }

type pathPruneSpy struct {
	entries                map[string][]os.DirEntry
	read, removed, emptied []string
}

func (s *pathPruneSpy) ReadDir(path string) ([]os.DirEntry, error) {
	s.read = append(s.read, path)
	return s.entries[path], nil
}
func (s *pathPruneSpy) RemoveAll(path string) error {
	s.removed = append(s.removed, path)
	return nil
}
func (s *pathPruneSpy) Remove(path string) error {
	s.emptied = append(s.emptied, path)
	return nil
}

func TestPluginPathPruneConfinesTargets(t *testing.T) {
	for _, root := range []string{"cache", filepath.Join(string(filepath.Separator), "cache")} {
		cache := NewArchiveCacheAt(&pathArchiveSpy{}, root)
		installation := &Installation{PluginID: "silo.example", Version: "1.2.3", InstallPath: "source/install-current/plugin"}
		binaryPath := requireLocalInstallPath(t, cache, installation)
		keep := filepath.Dir(binaryPath)
		pluginRoot := filepath.Join(root, "silo.example")
		oldVersion := filepath.Join(pluginRoot, "1.2.2")
		newVersion := filepath.Dir(keep)
		spy := &pathPruneSpy{entries: map[string][]os.DirEntry{
			pluginRoot: {pathDirectoryEntry{"1.2.2", fs.ModeDir}, pathDirectoryEntry{"1.2.3", fs.ModeDir}, pathDirectoryEntry{"link", fs.ModeSymlink}},
			oldVersion: {pathDirectoryEntry{"install-old", fs.ModeDir}},
			newVersion: {pathDirectoryEntry{"install-current", fs.ModeDir}, pathDirectoryEntry{"install-other", fs.ModeDir}, pathDirectoryEntry{"file", 0}},
		}}
		cache.pruneStaleReleasesFrom(t.Context(), installation, keep, spy)
		want := []string{filepath.Join(oldVersion, "install-old"), filepath.Join(newVersion, "install-other")}
		if !slices.Equal(spy.removed, want) {
			t.Fatalf("prune targets = %v, want %v", spy.removed, want)
		}
		for _, target := range append(slices.Clone(spy.removed), spy.emptied...) {
			if _, err := pluginRelativeDescendant(pluginRoot, target); err != nil {
				t.Fatal(err)
			}
		}
		for _, invalidKeep := range []string{root, pluginRoot, newVersion, filepath.Join(root, "sibling", "1.2.3", "install-current")} {
			spy := &pathPruneSpy{}
			cache.pruneStaleReleasesFrom(t.Context(), installation, invalidKeep, spy)
			if len(spy.read)+len(spy.removed)+len(spy.emptied) != 0 {
				t.Fatalf("invalid keep %q reached filesystem: %+v", invalidKeep, spy)
			}
		}
		installation.Version = "."
		spy = &pathPruneSpy{}
		cache.pruneStaleReleasesFrom(t.Context(), installation, keep, spy)
		if len(spy.read)+len(spy.removed)+len(spy.emptied) != 0 {
			t.Fatalf("invalid stored identity reached prune filesystem: %+v", spy)
		}
	}
}

func TestPluginPathReplacementRejectsDifferentPlugin(t *testing.T) {
	// Both identities are ordinary valid identities; the payload remains inert
	// in memory. Corrected replacement rejects the mismatch before writing files.
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
	installer := NewInstaller(store, InstallerOptions{BaseDir: "unused-c48-root"})
	existing := &Installation{ID: 7, PluginID: "silo.example", Version: "1.2.2", InstallPath: "unused/plugin"}
	_, binaryErr := installer.replaceBinary(t.Context(), existing, data, manifest.Checksum, manifest, nil)
	_, archiveErr := installer.replaceArchive(t.Context(), existing, archive, InstallArchiveRequest{})
	for _, err := range []error{binaryErr, archiveErr} {
		if err == nil || !strings.Contains(err.Error(), "does not match installation") {
			t.Fatalf("replacement mismatch = %v", err)
		}
	}
	if len(store.createInputs)+len(store.updateInputs)+len(store.deleteIDs)+len(store.saveArchiveIDs) != 0 {
		t.Fatalf("mismatched replacement mutated store: %+v", store)
	}
}
