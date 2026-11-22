package plugins

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

func TestAutomaticInstallHonorsRestoreHoldDB(t *testing.T) {
	pool := builtinGuardTestPool(t)
	ctx := t.Context()
	repos := NewRepositoryStore(pool)
	if _, err := repos.ReconcileManaged(ctx); err != nil {
		t.Fatal(err)
	}
	repo, err := NewCatalogService(repos, CatalogServiceOptions{}).OfficialRepository(ctx)
	if err != nil {
		t.Fatal(err)
	}
	setHold := func(ctx context.Context) error {
		_, err := pool.Exec(ctx, `INSERT INTO server_settings (key, value) VALUES ($1, 'auto-install-restore-hold')
			ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value`, ProvisionRestoreHoldKey)
		return err
	}
	if err := setHold(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := AcknowledgeProvisionRestoreHold(context.Background(), pool, "auto-install-restore-hold"); err != nil {
			t.Error(err)
		}
	})
	store := NewInstallationStore(pool)
	manifest := packageErrorTestManifest(pluginIDTheIntroDB, "1.2.3")
	binary := manifestScript(t, manifest, "")
	var downloads atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		downloads.Add(1)
		// The hold becomes visible after the follower has admitted the work
		// in memory, while the package is still downloading.
		if err := setHold(r.Context()); err != nil {
			t.Error(err)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		_, _ = w.Write(binary)
	}))
	t.Cleanup(server.Close)
	target := &ResolvedCatalogInstall{
		RepositoryID: repo.ID, PluginID: manifest.PluginId, Version: manifest.Version,
		ArchiveURL: server.URL, Checksum: sha256Hex(binary),
	}
	catalog := &fakeAutoUpdateCatalog{
		entries:  []CatalogEntry{{RepositoryID: repo.ID, SourceKind: RepositorySourceSilo, Manifest: manifest}},
		resolved: target,
	}
	installer := NewInstaller(store, InstallerOptions{BaseDir: t.TempDir(), HTTPClient: server.Client()})
	service := NewAutoUpdateService(repos, store, catalog, installer, nil, nil)
	opts := AutoUpdateOptions{AutoInstallDefaults: true}
	checkSkipped := func() {
		t.Helper()
		summary, err := service.Check(ctx, opts)
		if err != nil || summary.DefaultPluginsInstalled != 0 || len(summary.Failures) != 0 {
			t.Fatalf("held automatic install should skip cleanly: %+v, %v", summary, err)
		}
		rows, err := store.ListByPluginID(ctx, manifest.PluginId)
		if err != nil || len(rows) != 0 {
			t.Fatalf("held automatic install wrote an installation: %+v, %v", rows, err)
		}
	}
	checkSkipped()
	if downloads.Load() != 0 {
		t.Fatal("automatic install started a download under the restore hold")
	}
	if err := AcknowledgeProvisionRestoreHold(ctx, pool, "auto-install-restore-hold"); err != nil {
		t.Fatal(err)
	}
	checkSkipped()
	if downloads.Load() != 1 {
		t.Fatalf("want one in-flight download, got %d", downloads.Load())
	}

	result, err := installer.InstallBinary(ctx, target.BinaryRequest())
	if err != nil {
		t.Fatalf("explicit admin install during a restore hold: %v", err)
	}
	t.Cleanup(func() {
		if err := store.Delete(context.Background(), result.Installation.ID); err != nil {
			t.Error(err)
		}
	})
	prepared, err := installer.PrepareCatalogReplacement(ctx, result.Installation, target.forAutomaticInstall())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := prepared.Apply(ctx); !errors.Is(err, errProvisionRestoreHeld) {
		t.Fatalf("automatic replacement during a restore hold: %v", err)
	}
	current, err := store.GetByID(ctx, result.Installation.ID)
	if err != nil || current.InstallPath != result.Installation.InstallPath {
		t.Fatalf("automatic replacement changed the installed release: %v", err)
	}
}
