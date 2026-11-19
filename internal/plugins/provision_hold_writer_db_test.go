package plugins

import (
	"context"
	"errors"
	"testing"
)

func TestProvisionWriterRestartsAfterRestoreHoldDB(t *testing.T) {
	pool := builtinGuardTestPool(t)
	ctx := t.Context()
	repositories := NewRepositoryStore(pool)
	if _, err := repositories.ReconcileManaged(ctx); err != nil {
		t.Fatal(err)
	}
	catalog := NewCatalogService(repositories, CatalogServiceOptions{})
	repo, err := catalog.OfficialRepository(ctx)
	if err != nil {
		t.Fatal(err)
	}
	cipher := instanceStateTestCipher(t)
	installations := NewInstallationStore(pool, cipher)
	configs := NewRuntimeConfigStore(pool, cipher)
	installation := seedTrustedProvisionInstallation(t, pool, installations, repo.ID, tmdbID)
	newWriter := func(allowCreation bool) *ProvisionWriter {
		doc := &ProvisionDocument{APIVersion: 1, Plugins: map[string]ProvisionPlugin{
			tmdbID: {AuthBinding: &ProvisionAuthBinding{CapabilityID: "login", Enabled: new(true), AutoProvision: &allowCreation}},
		}}
		w := NewProvisionWriter(pool, repositories, installations, NewInstaller(installations, InstallerOptions{BaseDir: t.TempDir()}), catalog, configs, nil, doc, nil)
		t.Cleanup(w.Close)
		return w
	}
	const holdID = "test-running-writer-restore"
	if _, err := pool.Exec(ctx, "INSERT INTO server_settings (key, value) VALUES ($1, $2)", ProvisionRestoreHoldKey, holdID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, err := pool.Exec(context.Background(), "DELETE FROM server_settings WHERE key=$1 AND value=$2", ProvisionRestoreHoldKey, holdID)
		if err != nil {
			t.Error(err)
		}
		if _, err := pool.Exec(context.Background(), "DELETE FROM plugin_provision_status WHERE plugin_id IN ($1,$2)", tmdbID, pluginIDTVDB); err != nil {
			t.Error(err)
		}
	})
	old := newWriter(true)
	if err := old.StartOffline(ctx); err != nil {
		t.Fatal(err)
	}
	if err := AcknowledgeProvisionRestoreHold(ctx, pool, holdID); err != nil {
		t.Fatal(err)
	}
	// Reconnecting schedules another apply even if no retry was pending.
	old.loseAuthority()
	if acquired, err := old.tryWriter(ctx); err != nil || !acquired {
		t.Fatalf("writer did not reacquire authority: %v", err)
	}
	old.applyOnline(ctx)
	if _, err := configs.GetAuthBinding(ctx, installation.ID, "login"); !errors.Is(err, ErrAuthBindingNotFound) {
		t.Fatalf("previous startup declaration applied after acknowledgement: %v", err)
	}
	old.Close()
	restarted := newWriter(false)
	if err := restarted.StartOffline(ctx); err != nil {
		t.Fatal(err)
	}
	binding, err := configs.GetAuthBinding(ctx, installation.ID, "login")
	if err != nil || !binding.Enabled || binding.AutoProvision {
		t.Fatalf("reviewed binding on restart: %+v, %v", binding, err)
	}
	restarted.Close()
	release := newWriter(false)
	release.document.Plugins = map[string]ProvisionPlugin{}
	checkHeldProvisionRelease(t, release, installation, holdID)
	release.Close()
	partial := newWriter(false)
	partial.document.Plugins = map[string]ProvisionPlugin{pluginIDTVDB: {}}
	if err := partial.StartOffline(ctx); err != nil {
		t.Fatal(err)
	}
	statuses, err := ListProvisionStatus(ctx, pool)
	if err != nil || len(statuses) != 2 || statuses[0].State != "held" || statuses[1].State != "held" {
		t.Fatalf("partial release did not retain both statuses: %v, %v", statuses, err)
	}
}

func checkHeldProvisionRelease(t *testing.T, w *ProvisionWriter, installation *Installation, holdID string) {
	t.Helper()
	ctx := t.Context()
	if _, err := w.pool.Exec(ctx, "INSERT INTO server_settings (key, value) VALUES ($1, $2)", ProvisionRestoreHoldKey, holdID); err != nil {
		t.Fatal(err)
	}
	if err := w.StartOffline(ctx); err != nil {
		t.Fatal(err)
	}
	statuses, err := ListProvisionStatus(ctx, w.pool)
	if err != nil || len(statuses) != 1 || statuses[0].PluginID != installation.PluginID || statuses[0].State != "held" {
		t.Fatalf("follower cannot see held release: %v, %v", statuses, err)
	}
	ownership, err := w.installations.GetProvisionOwnership(ctx, installation.ID)
	if err != nil || !ownership.Managed {
		t.Fatalf("held release lost ownership: %+v, %v", ownership, err)
	}
}

func TestProvisionWriterOrphanedSessionDB(t *testing.T) {
	ctx := t.Context()
	pool := builtinGuardTestPool(t)
	writer := NewProvisionWriter(pool, nil, nil, nil, nil, nil, nil, nil, nil)
	t.Cleanup(writer.Close)
	if acquired, err := writer.tryWriter(ctx); err != nil || !acquired {
		t.Fatalf("initial lock: %v, %v", acquired, err)
	}
	// Keep the old PostgreSQL backend alive while the writer reconnects.
	orphan := writer.conn
	t.Cleanup(func() { _ = orphan.Close(context.Background()) })
	writer.conn = nil
	if acquired, err := writer.tryWriter(ctx); err == nil || acquired {
		t.Fatalf("expected a retry while the old session holds the lock, got %v, %v", acquired, err)
	}
	if err := orphan.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if acquired, err := writer.tryWriter(ctx); err != nil || !acquired {
		t.Fatalf("reacquiring the released lock: %v, %v", acquired, err)
	}
	writer.Close()
	other := NewProvisionWriter(pool, nil, nil, nil, nil, nil, nil, nil, nil)
	t.Cleanup(other.Close)
	if acquired, err := other.tryWriter(ctx); err != nil || !acquired {
		t.Fatalf("replacement writer: %v, %v", acquired, err)
	}
	if acquired, err := writer.tryWriter(ctx); err != nil || acquired {
		t.Fatalf("expected a permanent refusal with a different writer, got %v, %v", acquired, err)
	}
}
