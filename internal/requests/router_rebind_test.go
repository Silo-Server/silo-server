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

func insertRouterServer(t *testing.T, pool *pgxpool.Pool, id, capabilityID string, installationID int) {
	t.Helper()
	if _, err := pool.Exec(t.Context(), `
		INSERT INTO request_integrations (id, name, enabled, base_url, capability_id, installation_id)
		VALUES ($1, $1, true, 'http://router.invalid', $2, $3)`, id, capabilityID, installationID); err != nil {
		t.Fatal(err)
	}
}

// Reinstalling a request router plugin gives it a new installation id; its
// servers follow it unless two installations offer the same capability.
func TestRebindOrphanedRouterServersDatabase(t *testing.T) {
	repo, pool := routerRebindRepository(t)
	ctx := t.Context()
	installRouterPlugin(t, pool, 20, "silo.requests.seerr", "seerr")
	installRouterPlugin(t, pool, 30, "silo.requests.arr", "arr")
	installRouterPlugin(t, pool, 31, "example.requests.arr", "arr")
	insertRouterServer(t, pool, "orphaned", "seerr", 10)
	insertRouterServer(t, pool, "ambiguous", "arr", 11)
	insertRouterServer(t, pool, "bound", "arr", 30)

	rebound, err := repo.RebindOrphanedIntegrations(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(rebound, []string{"orphaned"}) {
		t.Fatalf("rebound = %v, want [orphaned]", rebound)
	}
	for id, want := range map[string]int{"orphaned": 20, "ambiguous": 11, "bound": 30} {
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
	insertRouterServer(t, pool, "unbound", "seerr", 10)
	insertRouterServer(t, pool, "bound", "seerr", 20)
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
