package plugins

import (
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"
)

// An update check decides its offer against the installation it read. When
// another install replaced that release meanwhile, the offer is stale and is
// not recorded, so Update can't install it over the newer release (#4518).
func TestNotifyPluginUpdateDropsAnOfferForAReplacedReleaseDB(t *testing.T) {
	pool := builtinGuardTestPool(t)
	ctx := t.Context()
	store := NewInstallationStore(pool)
	var id int
	pluginID := "silo.test.update-offer" + time.Now().UTC().Format("-20060102150405.000000000")
	if err := pool.QueryRow(ctx,
		`INSERT INTO plugin_installations (plugin_id, version, install_path, enabled, update_policy, kind)
		 VALUES ($1, '1.0.0', '/nonexistent/update-offer/1.0.0/plugin', true, 'notify', 'plugin')
		 RETURNING id`, pluginID).Scan(&id); err != nil {
		t.Fatalf("seed installation: %v", err)
	}
	t.Cleanup(func() {
		// t.Context() is canceled by the time cleanup runs.
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if _, err := pool.Exec(cleanupCtx, `DELETE FROM plugin_installations WHERE id = $1`, id); err != nil {
			t.Errorf("delete seeded installation: %v", err)
		}
	})
	read, err := store.GetByID(ctx, id)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	// An admin installs 2.0.0 while the check that read 1.0.0 still runs.
	if _, err := pool.Exec(ctx, `UPDATE plugin_installations SET version = '2.0.0', install_path = '/nonexistent/update-offer/2.0.0/plugin' WHERE id = $1`, id); err != nil {
		t.Fatalf("replace installation: %v", err)
	}

	service := &AutoUpdateService{installations: store, logger: slog.Default()}
	outcome, err := service.handleExistingPlugin(ctx, read, CatalogEntry{Manifest: packageErrorTestManifest(pluginID, "1.1.0")})
	if err != nil {
		t.Fatalf("handleExistingPlugin: %v", err)
	}
	// A dropped offer is not an update the check found.
	if outcome != autoUpdateOutcomeNone {
		t.Fatalf("outcome = %v, want none for a dropped offer", outcome)
	}
	got, err := store.GetByID(ctx, id)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if got.AvailableVersion != nil && *got.AvailableVersion != "" {
		t.Fatalf("available_version = %q, want none recorded over the 2.0.0 install", *got.AvailableVersion)
	}
}

// An update check that listed a notify plugin before an admin uninstalled it
// finds no row to record the offer on. That is a race the check skips, not a
// failed check (#5237).
func TestNotifyPluginUpdateSkipsAPluginUninstalledMeanwhileDB(t *testing.T) {
	pool := builtinGuardTestPool(t)
	ctx := t.Context()
	store := NewInstallationStore(pool)
	var id int
	pluginID := "silo.test.update-offer-uninstalled" + time.Now().UTC().Format("-20060102150405.000000000")
	if err := pool.QueryRow(ctx,
		`INSERT INTO plugin_installations (plugin_id, version, install_path, enabled, update_policy, kind)
		 VALUES ($1, '1.0.0', '/nonexistent/update-offer-uninstalled/1.0.0/plugin', true, 'notify', 'plugin')
		 RETURNING id`, pluginID).Scan(&id); err != nil {
		t.Fatalf("seed installation: %v", err)
	}
	t.Cleanup(func() {
		// t.Context() is canceled by the time cleanup runs.
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if _, err := pool.Exec(cleanupCtx, `DELETE FROM plugin_installations WHERE id = $1`, id); err != nil {
			t.Errorf("delete seeded installation: %v", err)
		}
	})
	read, err := store.GetByID(ctx, id)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	// An admin uninstalls the plugin while the check that read it still runs.
	if _, err := pool.Exec(ctx, `DELETE FROM plugin_installations WHERE id = $1`, id); err != nil {
		t.Fatalf("uninstall: %v", err)
	}

	service := &AutoUpdateService{installations: store, logger: slog.Default()}
	outcome, err := service.handleExistingPlugin(ctx, read, CatalogEntry{Manifest: packageErrorTestManifest(pluginID, "1.1.0")})
	if err != nil {
		t.Fatalf("handleExistingPlugin: %v, want the uninstalled plugin skipped", err)
	}
	if outcome != autoUpdateOutcomeNone {
		t.Fatalf("outcome = %v, want none for an uninstalled plugin", outcome)
	}
}

// Applying a recorded update to a plugin whose repository was deleted after
// the caller's read has nowhere to install it from. It answers
// ErrNoUpdateAvailable, which /update maps to a conflict, before any download
// or stop (#5237).
func TestUpdateToAvailableVersionRefusesAnOfferWithNoRepository(t *testing.T) {
	for _, tc := range []struct {
		name       string
		repository *int
	}{
		{name: "repository deleted", repository: nil},
		{name: "zero repository", repository: new(0)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			events := []string{}
			store := newFakeServiceInstallationStore(&Installation{
				ID: 7, PluginID: "test.plugin", Version: "1.0.0", InstallPath: t.TempDir() + "/plugin", Enabled: true,
				AvailableVersion: new("2.0.0"), RepositoryID: tc.repository,
			})
			store.events = &events
			service := &Service{installations: store, host: &fakeServiceHost{events: &events}}

			_, err := service.UpdateToAvailableVersion(t.Context(), 7)
			if !errors.Is(err, ErrNoUpdateAvailable) {
				t.Fatalf("UpdateToAvailableVersion() = %v, want ErrNoUpdateAvailable", err)
			}
			if len(events) != 0 {
				t.Fatalf("events = %v, want none", events)
			}
		})
	}
}

// A repository deleted after UpdateToAvailableVersion read the installation,
// while the catalog resolves the release, also leaves nowhere to install
// from: ErrNoUpdateAvailable, before any download or stop (#5237).
func TestUpdateToAvailableVersionRefusesAnOfferWhoseRepositoryGoesMeanwhileDB(t *testing.T) {
	pool := builtinGuardTestPool(t)
	ctx := t.Context()
	repositories := NewRepositoryStore(pool)
	repository, err := repositories.Create(ctx, CreateRepositoryInput{
		URL:         "https://example.invalid/update-offer-repository-gone" + time.Now().UTC().Format("-20060102150405.000000000") + "/index.json",
		DisplayName: "Removed catalog",
	})
	if err != nil {
		t.Fatalf("create repository: %v", err)
	}
	if err := repositories.Delete(ctx, repository.ID); err != nil {
		t.Fatalf("delete repository: %v", err)
	}

	events := []string{}
	// The fake store still holds the repository_id the service read.
	store := newFakeServiceInstallationStore(&Installation{
		ID: 7, PluginID: "test.plugin", Version: "1.0.0", InstallPath: t.TempDir() + "/plugin", Enabled: true,
		AvailableVersion: new("2.0.0"), RepositoryID: &repository.ID,
	})
	store.events = &events
	service := &Service{
		installations: store,
		catalog:       NewCatalogService(repositories, CatalogServiceOptions{}),
		host:          &fakeServiceHost{events: &events},
	}

	_, err = service.UpdateToAvailableVersion(ctx, 7)
	if !errors.Is(err, ErrNoUpdateAvailable) {
		t.Fatalf("UpdateToAvailableVersion() = %v, want ErrNoUpdateAvailable", err)
	}
	if len(events) != 0 {
		t.Fatalf("events = %v, want none", events)
	}
}

// Applying a recorded update refuses a version that isn't newer than the
// installed one with ErrNoUpdateAvailable, before any download or stop, so a
// stale offer never downgrades the plugin (#4518).
func TestUpdateToAvailableVersionRefusesAnOfferNotNewerThanInstalled(t *testing.T) {
	repositoryID := 1
	for _, tc := range []struct {
		name      string
		available *string
	}{
		{name: "older", available: new("1.1.0")},
		{name: "same", available: new("2.0.0")},
		{name: "cleared meanwhile", available: new("")},
		{name: "none", available: nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			events := []string{}
			store := newFakeServiceInstallationStore(&Installation{
				ID: 7, PluginID: "test.plugin", Version: "2.0.0", InstallPath: t.TempDir() + "/plugin", Enabled: true,
				AvailableVersion: tc.available, RepositoryID: &repositoryID,
			})
			store.events = &events
			service := &Service{installations: store, host: &fakeServiceHost{events: &events}}

			_, err := service.UpdateToAvailableVersion(t.Context(), 7)
			if !errors.Is(err, ErrNoUpdateAvailable) {
				t.Fatalf("UpdateToAvailableVersion() = %v, want ErrNoUpdateAvailable", err)
			}
			if len(events) != 0 {
				t.Fatalf("events = %v, want none", events)
			}
		})
	}
}
