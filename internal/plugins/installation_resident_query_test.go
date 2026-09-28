package plugins

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-plugin-sdk/pkg/pluginsdk/capability"
)

// ListEnabledWithCapabilityTypes backs the resident supervisor's reconcile:
// it must return exactly the enabled installations declaring one of the
// requested capability types, once each, and skip disabled rows and rows
// whose capabilities are all of other types.
func TestInstallationStoreListEnabledWithCapabilityTypes(t *testing.T) {
	pool := builtinGuardTestPool(t)
	store := NewInstallationStore(pool)
	ctx := context.Background()

	resident := seedResidentQueryInstallation(t, pool, true)
	seedResidentQueryCapability(t, pool, resident, capability.NetworkAccessProvider, "a")
	seedResidentQueryCapability(t, pool, resident, capability.NetworkAccessProvider, "b")
	seedResidentQueryCapability(t, pool, resident, capability.MetadataProvider, "meta")

	disabled := seedResidentQueryInstallation(t, pool, false)
	seedResidentQueryCapability(t, pool, disabled, capability.NetworkAccessProvider, "off")

	lazy := seedResidentQueryInstallation(t, pool, true)
	seedResidentQueryCapability(t, pool, lazy, capability.MetadataProvider, "meta")

	got, err := store.ListEnabledWithCapabilityTypes(ctx, []string{capability.NetworkAccessProvider})
	if err != nil {
		t.Fatalf("ListEnabledWithCapabilityTypes: %v", err)
	}
	seen := map[int]int{}
	for _, installation := range got {
		seen[installation.ID]++
	}
	if seen[resident] != 1 {
		t.Fatalf("resident installation %d returned %d times, want 1 (got %+v)", resident, seen[resident], seen)
	}
	if seen[disabled] != 0 || seen[lazy] != 0 {
		t.Fatalf("disabled (%d) or lazy (%d) installation returned: %+v", disabled, lazy, seen)
	}

	none, err := store.ListEnabledWithCapabilityTypes(ctx, nil)
	if err != nil {
		t.Fatalf("ListEnabledWithCapabilityTypes(nil): %v", err)
	}
	if len(none) != 0 {
		t.Fatalf("empty type list returned %d installations, want 0", len(none))
	}
}

func seedResidentQueryInstallation(t *testing.T, pool *pgxpool.Pool, enabled bool) int {
	t.Helper()
	var id int
	pluginID := "silo.test.resident-query" + time.Now().UTC().Format("-20060102150405.000000000")
	err := pool.QueryRow(context.Background(),
		`INSERT INTO plugin_installations (plugin_id, version, install_path, enabled, update_policy, kind)
		 VALUES ($1, '0', '/nonexistent/resident-query-test', $2, 'manual', 'plugin')
		 RETURNING id`, pluginID, enabled).Scan(&id)
	if err != nil {
		t.Fatalf("seed installation: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM plugin_installations WHERE id = $1`, id)
	})
	return id
}

func seedResidentQueryCapability(t *testing.T, pool *pgxpool.Pool, installationID int, capabilityType, capabilityID string) {
	t.Helper()
	_, err := pool.Exec(context.Background(),
		`INSERT INTO plugin_capabilities (plugin_installation_id, capability_type, capability_id, metadata)
		 VALUES ($1, $2, $3, '{}'::jsonb)`, installationID, capabilityType, capabilityID)
	if err != nil {
		t.Fatalf("seed capability: %v", err)
	}
}
