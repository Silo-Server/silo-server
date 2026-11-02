package metadata

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func chainBuiltinTestPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatalf("connect test database: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

func insertTestInstallation(t *testing.T, pool *pgxpool.Pool, kind string, enabled bool) int {
	t.Helper()
	var id int
	pluginID := fmt.Sprintf("test.chain-%s-%d", kind, time.Now().UnixNano())
	err := pool.QueryRow(context.Background(),
		`INSERT INTO plugin_installations (plugin_id, version, install_path, enabled, update_policy, kind)
		 VALUES ($1, '0', '/nonexistent/test-chain-builtin', $2, 'manual', $3)
		 RETURNING id`, pluginID, enabled, kind).Scan(&id)
	if err != nil {
		t.Fatalf("seed %s installation: %v", kind, err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM plugin_installations WHERE id = $1`, id)
	})
	return id
}

func insertTestCapability(t *testing.T, pool *pgxpool.Pool, installationID int, capabilityID, metadataJSON string) {
	t.Helper()
	_, err := pool.Exec(context.Background(),
		`INSERT INTO plugin_capabilities (plugin_installation_id, capability_type, capability_id, metadata)
		 VALUES ($1, 'metadata_provider.v1', $2, $3::jsonb)`,
		installationID, capabilityID, metadataJSON)
	if err != nil {
		t.Fatalf("seed capability %s: %v", capabilityID, err)
	}
}

func insertTestFolder(t *testing.T, pool *pgxpool.Pool, folderType string) int {
	t.Helper()
	var id int
	err := pool.QueryRow(context.Background(),
		`INSERT INTO media_folders (type, name, enabled) VALUES ($1, $2, true) RETURNING id`,
		folderType, fmt.Sprintf("chain-builtin-test-%d", time.Now().UnixNano())).Scan(&id)
	if err != nil {
		t.Fatalf("seed folder: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM media_folders WHERE id = $1`, id)
	})
	return id
}

// The chain-less fallback must skip default_enabled=false capabilities: a
// chain-less library, and a library whose only chain row is the disabled
// seeded NFO entry, must not activate the provider. Default-enabled providers
// must also declare support for the requested content level.
func TestChainlessFallback_SkipsDefaultDisabledCapability(t *testing.T) {
	pool := chainBuiltinTestPool(t)
	ctx := context.Background()

	capID := fmt.Sprintf("test-fallback-%d", time.Now().UnixNano())
	RegisterBuiltinProvider(capID, func() Provider { return &builtinStubProvider{slug: capID} })

	installationID := insertTestInstallation(t, pool, "builtin", true)
	insertTestCapability(t, pool, installationID, capID,
		`{"display_name":"Test Fallback","default_priority":{"movie":1},"default_enabled":false}`)
	seriesCapID := capID + "-series"
	RegisterBuiltinProvider(seriesCapID, func() Provider { return &builtinStubProvider{slug: seriesCapID} })
	insertTestCapability(t, pool, installationID, seriesCapID,
		`{"display_name":"Test Series","default_priority":{"series":1},"default_enabled":true}`)
	chainRepo := NewChainRepository(pool)

	assertNotResolved := func(folderID int, scenario string) {
		t.Helper()
		providers, err := ResolveChainWithChecker(ctx, folderID, "movie", chainRepo, nil, nil)
		if err != nil {
			t.Fatalf("%s: resolve chain: %v", scenario, err)
		}
		for _, p := range providers {
			if p.Slug() == capID {
				t.Errorf("%s: default_enabled=false capability leaked into the fallback", scenario)
			}
			if p.Slug() == seriesCapID {
				t.Errorf("%s: series-only capability leaked into the movie fallback", scenario)
			}
		}
	}

	// Chain-less library.
	assertNotResolved(insertTestFolder(t, pool, "movies"), "chain-less library")

	// Library whose only row is the disabled seeded entry (all-disabled chain).
	folderID := insertTestFolder(t, pool, "movies")
	if err := chainRepo.SetChain(ctx, folderID, []ChainEntry{
		{PluginInstallationID: installationID, CapabilityID: capID, ContentLevel: "movie", Priority: 0, Enabled: false},
	}); err != nil {
		t.Fatalf("set chain: %v", err)
	}
	assertNotResolved(folderID, "all-disabled chain")
}

type stubMetadataResolver struct{}

func (stubMetadataResolver) MetadataProviderClient(context.Context, int, string) (pluginMetadataClient, error) {
	return nil, fmt.Errorf("stub resolver: no client")
}
