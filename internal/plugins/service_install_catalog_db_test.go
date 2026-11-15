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
// package leaves the plugin and its row untouched.
func TestServiceInstallCatalogStopsPluginOnlyForAnAcceptedPackageDB(t *testing.T) {
	pool := builtinGuardTestPool(t)
	for _, tc := range []struct {
		name        string
		wrongSum    bool
		withAsset   bool
		wantEvents  []string
		wantRefusal string
		wantVersion string
	}{
		{name: "checksum mismatch", wrongSum: true, wantEvents: []string{}, wantRefusal: "SHA-256 checksum", wantVersion: "1.0.0"},
		// A binary has nowhere to carry assets, so its stored archive is
		// refused after the download passed its checks.
		{name: "manifest declares an asset", withAsset: true, wantEvents: []string{}, wantVersion: "1.0.0"},
		{name: "accepted package", wantEvents: []string{"stop", "save_archive", "update"}, wantVersion: "2.0.0"},
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
			mux := http.NewServeMux()
			server := httptest.NewServer(mux)
			t.Cleanup(server.Close)
			mux.HandleFunc("/plugin", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(binary) })
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

			events := []string{}
			store := newFakeServiceInstallationStore(&Installation{
				ID: 7, RepositoryID: &repository.ID, PluginID: "test.plugin", Version: "1.0.0", InstallPath: t.TempDir() + "/plugin", Enabled: true,
			})
			store.events = &events
			service := &Service{
				installations: store,
				catalog:       NewCatalogService(repositories, CatalogServiceOptions{HTTPClient: server.Client(), CurrentOS: "linux", CurrentArch: "amd64"}),
				installer:     NewInstaller(store, InstallerOptions{BaseDir: t.TempDir(), HTTPClient: server.Client()}),
				host:          &fakeServiceHost{events: &events},
			}

			_, err = service.InstallCatalog(ctx, InstallCatalogRequest{RepositoryID: repository.ID, PluginID: "test.plugin", Version: "2.0.0"})
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
		})
	}
}
