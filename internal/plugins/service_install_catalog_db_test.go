package plugins

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"

	pluginv1 "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginproto/silo/plugin/v1"
)

// Updating an installed plugin from its catalog stops the running plugin
// only after the new package was downloaded and passed its checks; a refused
// package leaves the plugin and its row untouched. An admin who turns the
// plugin off or on while the update downloads keeps that choice, and the old
// process is stopped either way.
func TestServiceInstallCatalogStopsPluginOnlyForAnAcceptedPackageDB(t *testing.T) {
	pool := builtinGuardTestPool(t)
	for _, tc := range []struct {
		name        string
		wrongSum    bool
		withAsset   bool
		wantEvents  []string
		wantRefusal string
		wantVersion string
		// installedOff starts the plugin disabled; toggleTo switches it
		// while the package downloads, as an admin would.
		installedOff bool
		toggleTo     *bool
		wantEnabled  bool
	}{
		{name: "checksum mismatch", wrongSum: true, wantEvents: []string{}, wantRefusal: "SHA-256 checksum", wantVersion: "1.0.0", wantEnabled: true},
		// A binary has nowhere to carry assets, so its stored archive is
		// refused after the download passed its checks.
		{name: "manifest declares an asset", withAsset: true, wantEvents: []string{}, wantVersion: "1.0.0", wantEnabled: true},
		{name: "accepted package", wantEvents: []string{"stop", "save_archive", "update"}, wantVersion: "2.0.0", wantEnabled: true},
		{name: "disabled during download", toggleTo: new(false), wantEvents: []string{"stop", "save_archive", "update"}, wantVersion: "2.0.0"},
		{name: "enabled during download", installedOff: true, toggleTo: new(true), wantEvents: []string{"stop", "save_archive", "update"}, wantVersion: "2.0.0", wantEnabled: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			manifest := packageErrorTestManifest("test.plugin", "2.0.0")
			if tc.withAsset {
				manifest.Assets = []*pluginv1.PackagedAsset{{Path: "web/app.js"}}
			}
			binary := manifestScript(t, manifest, "")
			checksum := sha256Hex(binary)
			if tc.wrongSum {
				checksum = sha256Hex([]byte("another build"))
			}
			events := []string{}
			store := newFakeServiceInstallationStore(&Installation{
				ID: 7, PluginID: "test.plugin", Version: "1.0.0", InstallPath: t.TempDir() + "/plugin", Enabled: !tc.installedOff,
			})
			store.events = &events
			mux := http.NewServeMux()
			server := httptest.NewServer(mux)
			t.Cleanup(server.Close)
			mux.HandleFunc("/plugin", func(w http.ResponseWriter, _ *http.Request) {
				if tc.toggleTo != nil {
					store.mu.Lock()
					store.byID[7].Enabled = *tc.toggleTo
					store.mu.Unlock()
				}
				_, _ = w.Write(binary)
			})
			mux.HandleFunc("/index.json", func(w http.ResponseWriter, _ *http.Request) {
				_ = json.NewEncoder(w).Encode(RepositoryIndex{Plugins: []CatalogPackage{{
					Manifest: packageErrorTestManifest("test.plugin", "2.0.0"),
					Binaries: map[string]PlatformBinary{"linux/amd64": {URL: server.URL + "/plugin", Checksum: checksum}},
				}}})
			})

			repositories := NewRepositoryStore(pool)
			repository, err := repositories.Create(ctx, CreateRepositoryInput{
				URL:         server.URL + "/index.json?" + time.Now().UTC().Format("20060102150405.000000000"),
				DisplayName: "Install catalog test",
			})
			if err != nil {
				t.Fatalf("create repository: %v", err)
			}
			t.Cleanup(func() { _ = repositories.Delete(context.Background(), repository.ID) })

			store.byID[7].RepositoryID = &repository.ID
			service := &Service{
				installations: store,
				catalog:       NewCatalogService(repositories, CatalogServiceOptions{HTTPClient: server.Client(), CurrentOS: "linux", CurrentArch: "amd64"}),
				installer:     NewInstaller(store, InstallerOptions{BaseDir: t.TempDir(), HTTPClient: server.Client()}),
				host:          &fakeServiceHost{events: &events},
			}

			result, err := service.InstallCatalog(ctx, InstallCatalogRequest{RepositoryID: repository.ID, PluginID: "test.plugin", Version: "2.0.0"})
			switch {
			case tc.wantRefusal != "":
				requirePackageError(t, err, tc.wantRefusal)
			case tc.wantVersion == "1.0.0":
				if err == nil {
					t.Fatal("InstallCatalog() accepted a package it can't store")
				}
			case err != nil:
				t.Fatalf("InstallCatalog() returned error: %v", err)
			}
			if !reflect.DeepEqual(events, tc.wantEvents) {
				t.Fatalf("events = %v, want %v", events, tc.wantEvents)
			}
			if got := store.byID[7].Version; got != tc.wantVersion {
				t.Fatalf("installed version = %q, want %q", got, tc.wantVersion)
			}
			if got := store.byID[7].Enabled; got != tc.wantEnabled {
				t.Fatalf("enabled = %v, want %v", got, tc.wantEnabled)
			}
			if result != nil && result.Installation.Enabled != tc.wantEnabled {
				t.Fatalf("reported enabled = %v, want %v", result.Installation.Enabled, tc.wantEnabled)
			}
		})
	}
}
