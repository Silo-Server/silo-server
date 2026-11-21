package requests

import (
	"errors"
	"slices"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

// routerRebindRepository extends the lifecycle schema with copies of the
// plugin tables the rebind reads, so the shared database's plugins stay out.
func routerRebindRepository(t *testing.T) (*Repository, *pgxpool.Pool) {
	t.Helper()
	repo, pool := lifecycleTestRepository(t)
	for _, table := range []string{"plugin_installations", "plugin_capabilities"} {
		if _, err := pool.Exec(t.Context(), `CREATE TABLE `+table+` (LIKE public.`+table+` INCLUDING ALL)`); err != nil {
			t.Fatalf("copy %s: %v", table, err)
		}
	}
	return repo, pool
}

func installRouterPlugin(t *testing.T, pool *pgxpool.Pool, id int, pluginID, capabilityID string) {
	t.Helper()
	if _, err := pool.Exec(t.Context(), `
		INSERT INTO plugin_installations (id, plugin_id, version, install_path) VALUES ($1, $2, '1.0.0', '/plugins/x')`,
		id, pluginID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(t.Context(), `
		INSERT INTO plugin_capabilities (plugin_installation_id, capability_type, capability_id)
		VALUES ($1, 'request_router.v1', $2)`, id, capabilityID); err != nil {
		t.Fatal(err)
	}
}

// uninstallRouterPlugin deletes the installation as the plugin store does; the
// copied tables have no foreign keys, so its capabilities go explicitly.
func uninstallRouterPlugin(t *testing.T, pool *pgxpool.Pool, id int) {
	t.Helper()
	if _, err := pool.Exec(t.Context(), `DELETE FROM plugin_capabilities WHERE plugin_installation_id = $1`, id); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(t.Context(), `DELETE FROM plugin_installations WHERE id = $1`, id); err != nil {
		t.Fatal(err)
	}
}

// insertRouterServer saves a server through the repository, which records the
// plugin of the installation it names.
func insertRouterServer(t *testing.T, repo *Repository, id, capabilityID string, installationID int) {
	t.Helper()
	if _, err := repo.CreateIntegration(t.Context(), Integration{
		ID: id, Name: id, Enabled: true, BaseURL: "http://router.invalid",
		CapabilityID: capabilityID, InstallationID: new(installationID),
	}); err != nil {
		t.Fatalf("create %s: %v", id, err)
	}
}

// Reinstalling a request router plugin gives it a new installation id; its
// servers follow it. Another plugin offering the same capability never gets
// them, and a server with no recorded plugin is left for the admin.
func TestRebindOrphanedRouterServersDatabase(t *testing.T) {
	repo, pool := routerRebindRepository(t)
	ctx := t.Context()
	installRouterPlugin(t, pool, 10, "silo.requests.seerr", "seerr")
	installRouterPlugin(t, pool, 30, "silo.requests.arr", "arr")
	installRouterPlugin(t, pool, 31, "example.requests.arr", "arr")
	insertRouterServer(t, repo, "seerr", "seerr", 10)
	insertRouterServer(t, repo, "arr", "arr", 30)
	insertRouterServer(t, repo, "unrecorded", "seerr", 11)

	uninstallRouterPlugin(t, pool, 10)
	uninstallRouterPlugin(t, pool, 30)
	installRouterPlugin(t, pool, 20, "silo.requests.seerr", "seerr")

	rebound, err := repo.RebindOrphanedIntegrations(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(rebound, []string{"seerr"}) {
		t.Fatalf("rebound = %v, want [seerr]", rebound)
	}
	for id, want := range map[string]int{"seerr": 20, "arr": 30, "unrecorded": 11} {
		got, err := repo.GetIntegration(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if got.InstallationID == nil || *got.InstallationID != want {
			t.Fatalf("%s installation = %v, want %d", id, got.InstallationID, want)
		}
	}
	if again, err := repo.RebindOrphanedIntegrations(ctx); err != nil || len(again) != 0 {
		t.Fatalf("second rebind = %v, %v, want nothing", again, err)
	}
}

// A server no installation backs can be deleted; its live targets fail
// instead of waiting forever on a plugin that is gone. A bound server with a
// live target still refuses.
func TestDeleteUnboundRouterServerFailsItsTargetsDatabase(t *testing.T) {
	repo, pool := routerRebindRepository(t)
	ctx := t.Context()
	installRouterPlugin(t, pool, 20, "silo.requests.seerr", "seerr")
	insertRouterServer(t, repo, "unbound", "seerr", 10)
	insertRouterServer(t, repo, "bound", "seerr", 20)
	insertLifecycleRequest(t, repo, "stuck", 1, 101, StatusQueued)
	insertLifecycleRequest(t, repo, "tracked", 1, 102, StatusQueued)
	if _, err := pool.Exec(ctx, `INSERT INTO media_request_targets (request_id, integration_id, quality, status, download_phase)
		VALUES ('stuck', 'unbound', '1080p', 'downloading', 'downloading'), ('tracked', 'bound', '1080p', 'queued', NULL)`); err != nil {
		t.Fatal(err)
	}

	if err := repo.DeleteIntegration(ctx, "bound"); !errors.Is(err, ErrInvalidState) {
		t.Fatalf("delete bound server: err = %v, want ErrInvalidState", err)
	}
	if err := repo.DeleteIntegration(ctx, "unbound"); err != nil {
		t.Fatalf("delete unbound server: %v", err)
	}
	if _, err := repo.GetIntegration(ctx, "unbound"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unbound server after delete: err = %v, want ErrNotFound", err)
	}
	var status, lastError string
	var phase *string
	if err := pool.QueryRow(ctx, `SELECT status, last_error, download_phase FROM media_request_targets WHERE request_id = 'stuck'`).
		Scan(&status, &lastError, &phase); err != nil {
		t.Fatal(err)
	}
	if status != string(StatusFailed) || lastError != unboundTargetError || phase != nil {
		t.Fatalf("stuck target = %s %q phase %v, want failed with the unbound error and no progress", status, lastError, phase)
	}
	req, err := repo.GetRequest(ctx, "stuck")
	if err != nil {
		t.Fatal(err)
	}
	if req.Outcome != OutcomeFailed {
		t.Fatalf("stuck request outcome = %s, want failed", req.Outcome)
	}
}

// A delete that runs after the plugin is installed again but before the
// lifecycle hook rebinds the server must not fail requests the reinstalled
// plugin still tracks: the delete rebinds the server and refuses as for any
// bound server with live requests.
func TestDeleteReinstalledRouterServerRebindsFirstDatabase(t *testing.T) {
	repo, pool := routerRebindRepository(t)
	ctx := t.Context()
	installRouterPlugin(t, pool, 10, "silo.requests.seerr", "seerr")
	insertRouterServer(t, repo, "seerr", "seerr", 10)
	insertLifecycleRequest(t, repo, "tracked", 1, 101, StatusQueued)
	if _, err := pool.Exec(ctx, `INSERT INTO media_request_targets (request_id, integration_id, quality, status, download_phase)
		VALUES ('tracked', 'seerr', '1080p', 'downloading', 'downloading')`); err != nil {
		t.Fatal(err)
	}
	uninstallRouterPlugin(t, pool, 10)
	installRouterPlugin(t, pool, 20, "silo.requests.seerr", "seerr")

	if err := repo.DeleteIntegration(ctx, "seerr"); !errors.Is(err, ErrInvalidState) {
		t.Fatalf("delete reinstalled server: err = %v, want ErrInvalidState", err)
	}
	var status string
	if err := pool.QueryRow(ctx, `SELECT status FROM media_request_targets WHERE request_id = 'tracked'`).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "downloading" {
		t.Fatalf("tracked target = %s, want downloading", status)
	}
}

// A server saved onto an installation that no longer exists has no known
// plugin: it must not keep its previous plugin and later follow that plugin's
// reinstall.
func TestRouterServerMovedToMissingInstallationForgetsItsPluginDatabase(t *testing.T) {
	repo, pool := routerRebindRepository(t)
	ctx := t.Context()
	installRouterPlugin(t, pool, 10, "silo.requests.seerr", "seerr")
	insertRouterServer(t, repo, "moved", "seerr", 10)
	server, err := repo.GetIntegration(ctx, "moved")
	if err != nil {
		t.Fatal(err)
	}
	server.InstallationID = new(99)
	if _, err := repo.UpdateIntegration(ctx, *server); err != nil {
		t.Fatalf("update: %v", err)
	}
	uninstallRouterPlugin(t, pool, 10)
	installRouterPlugin(t, pool, 20, "silo.requests.seerr", "seerr")

	if rebound, err := repo.RebindOrphanedIntegrations(ctx); err != nil || len(rebound) != 0 {
		t.Fatalf("rebind = %v, %v, want nothing", rebound, err)
	}
}
