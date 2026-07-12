package metadata

import (
	"context"
	"fmt"
	"os"
	"reflect"
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

// The migration must seed the reserved builtin installation and the NFO
// capability exactly as specified.
func TestMigrationSeedsReservedBuiltinInstallation(t *testing.T) {
	pool := chainBuiltinTestPool(t)
	ctx := context.Background()

	var (
		id           int
		kind         string
		enabled      bool
		installPath  string
		updatePolicy string
	)
	err := pool.QueryRow(ctx,
		`SELECT id, kind, enabled, install_path, update_policy
		 FROM plugin_installations WHERE plugin_id = 'silo.builtin'`).
		Scan(&id, &kind, &enabled, &installPath, &updatePolicy)
	if err != nil {
		t.Fatalf("reserved silo.builtin installation missing: %v", err)
	}
	if kind != "builtin" || !enabled || installPath != "/nonexistent/silo-builtin" || updatePolicy != "manual" {
		t.Errorf("reserved row = kind=%q enabled=%v install_path=%q update_policy=%q", kind, enabled, installPath, updatePolicy)
	}

	md := lookupCapabilityMetadata(ctx, pool, id, "nfo")
	if md == nil {
		t.Fatal("silo.builtin nfo capability missing")
	}
	if extractDefaultEnabled(md) {
		t.Error("nfo capability default_enabled must be false")
	}
	if got := extractDefaultPriority(md, "movie"); got != 1 {
		t.Errorf("nfo movie default_priority = %d, want 1", got)
	}
	if got := extractDefaultPriority(md, "series"); got != 1 {
		t.Errorf("nfo series default_priority = %d, want 1", got)
	}
	// Phase D declares season/episode support so the builtin chain sync
	// appends the NFO provider to those chains.
	if got := extractDefaultPriority(md, "season"); got != 1 {
		t.Errorf("nfo season default_priority = %d, want 1 (Phase D migration)", got)
	}
	if got := extractDefaultPriority(md, "episode"); got != 1 {
		t.Errorf("nfo episode default_priority = %d, want 1 (Phase D migration)", got)
	}
}

// An enabled chain entry pointing at a builtin capability must resolve to the
// in-process provider through the real enabled-check (generic installation
// reads must NOT filter builtins).
func TestResolveChainWithChecker_BuiltinEntryResolvesInProcessProvider(t *testing.T) {
	pool := chainBuiltinTestPool(t)
	ctx := context.Background()

	capID := fmt.Sprintf("test-builtin-%d", time.Now().UnixNano())
	RegisterBuiltinProvider(capID, func() Provider { return &builtinStubProvider{slug: capID} })

	installationID := insertTestInstallation(t, pool, "builtin", true)
	insertTestCapability(t, pool, installationID, capID,
		`{"display_name":"Test Builtin","default_priority":{"movie":1},"default_enabled":false}`)
	folderID := insertTestFolder(t, pool, "movies")

	chainRepo := NewChainRepository(pool)
	if err := chainRepo.SetChain(ctx, folderID, []ChainEntry{
		{PluginInstallationID: installationID, CapabilityID: capID, ContentLevel: "movie", Priority: 0, Enabled: true},
	}); err != nil {
		t.Fatalf("set chain: %v", err)
	}

	providers, err := ResolveChainWithChecker(ctx, folderID, "movie", chainRepo, nil, nil)
	if err != nil {
		t.Fatalf("resolve chain: %v", err)
	}
	if len(providers) != 1 {
		t.Fatalf("providers = %d, want 1", len(providers))
	}
	if _, ok := providers[0].(*builtinStubProvider); !ok {
		t.Fatalf("provider type = %T, want in-process *builtinStubProvider", providers[0])
	}

	// A disabled builtin installation must be skipped like any plugin.
	if _, err := pool.Exec(ctx, `UPDATE plugin_installations SET enabled = false WHERE id = $1`, installationID); err != nil {
		t.Fatalf("disable installation: %v", err)
	}
	providers, err = ResolveChainWithChecker(ctx, folderID, "movie", chainRepo, nil, nil)
	if err != nil {
		t.Fatalf("resolve chain: %v", err)
	}
	// The chain entry is still enabled, so the fallback does not run; the
	// disabled installation is filtered by buildProviders.
	if len(providers) != 0 {
		t.Fatalf("providers after disabling installation = %d, want 0", len(providers))
	}
}

// The chain-less fallback must skip default_enabled=false capabilities: a
// chain-less library, and a library whose only chain row is the disabled
// seeded NFO entry, must not activate the provider.
func TestChainlessFallback_SkipsDefaultDisabledCapability(t *testing.T) {
	pool := chainBuiltinTestPool(t)
	ctx := context.Background()

	capID := fmt.Sprintf("test-fallback-%d", time.Now().UnixNano())
	RegisterBuiltinProvider(capID, func() Provider { return &builtinStubProvider{slug: capID} })

	installationID := insertTestInstallation(t, pool, "builtin", true)
	insertTestCapability(t, pool, installationID, capID,
		`{"display_name":"Test Fallback","default_priority":{"movie":1},"default_enabled":false}`)
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

// Startup sync: a library whose chain rows are only legacy content_level=”
// must be materialized per level (same order/enabled, ” rows left in place),
// then the builtin appended disabled at MAX(priority)+1. The resolved
// (enabled-filtered) chain must be identical before and after.
func TestSyncBuiltinProviderChains_LegacyMaterializationFixture(t *testing.T) {
	pool := chainBuiltinTestPool(t)
	ctx := context.Background()

	pluginInstallID := insertTestInstallation(t, pool, "plugin", true)
	capID := fmt.Sprintf("test-legacy-%d", time.Now().UnixNano())
	insertTestCapability(t, pool, pluginInstallID, capID,
		`{"display_name":"Legacy Plugin","metadata":{"default_priority":{"movie":2}}}`)

	builtinInstallID := insertTestInstallation(t, pool, "builtin", true)
	builtinCapID := fmt.Sprintf("test-legacy-builtin-%d", time.Now().UnixNano())
	insertTestCapability(t, pool, builtinInstallID, builtinCapID,
		`{"display_name":"Legacy Builtin","default_priority":{"movie":1,"series":1},"default_enabled":false}`)

	folderID := insertTestFolder(t, pool, "movies")
	chainRepo := NewChainRepository(pool)
	if err := chainRepo.SetChain(ctx, folderID, []ChainEntry{
		{PluginInstallationID: pluginInstallID, CapabilityID: capID, ContentLevel: "", Priority: 0, Enabled: true},
	}); err != nil {
		t.Fatalf("set legacy chain: %v", err)
	}

	resolvedBefore, err := chainRepo.GetChain(ctx, folderID, "movie")
	if err != nil {
		t.Fatalf("get chain before sync: %v", err)
	}

	if err := SyncBuiltinProviderChains(ctx, chainRepo); err != nil {
		t.Fatalf("sync builtin provider chains: %v", err)
	}

	after, err := chainRepo.GetChain(ctx, folderID, "movie")
	if err != nil {
		t.Fatalf("get chain after sync: %v", err)
	}

	// Enabled-filtered resolution must be identical (modulo the now-real level).
	var enabledAfter []ChainEntry
	for _, e := range after {
		if e.Enabled {
			e.ContentLevel = ""
			enabledAfter = append(enabledAfter, e)
		}
	}
	var enabledBefore []ChainEntry
	for _, e := range resolvedBefore {
		if e.Enabled {
			e.ContentLevel = ""
			enabledBefore = append(enabledBefore, e)
		}
	}
	if !reflect.DeepEqual(enabledBefore, enabledAfter) {
		t.Errorf("resolved chain changed across sync:\nbefore=%+v\nafter=%+v", enabledBefore, enabledAfter)
	}

	// The editor now sees real per-level rows: the materialized plugin row plus
	// the appended disabled builtin.
	foundPlugin, foundBuiltin := false, false
	maxPluginPriority := -1
	for _, e := range after {
		switch {
		case e.PluginInstallationID == pluginInstallID && e.CapabilityID == capID:
			foundPlugin = true
			if !e.Enabled {
				t.Error("materialized plugin row lost enabled state")
			}
			if e.Priority > maxPluginPriority {
				maxPluginPriority = e.Priority
			}
		case e.PluginInstallationID == builtinInstallID && e.CapabilityID == builtinCapID:
			foundBuiltin = true
			if e.Enabled {
				t.Error("builtin must be appended disabled")
			}
		}
	}
	if !foundPlugin {
		t.Error("legacy '' plugin row was not materialized at the movie level")
	}
	if !foundBuiltin {
		t.Error("builtin capability was not appended to the materialized movie level")
	}

	// The legacy '' rows stay in place for old binaries.
	legacy, err := chainRepo.GetChain(ctx, folderID, "")
	if err != nil {
		t.Fatalf("get legacy chain: %v", err)
	}
	if len(legacy) != 1 {
		t.Errorf("legacy '' rows = %d, want 1 (left in place)", len(legacy))
	}

	// Idempotency: running the sync again changes nothing.
	if err := SyncBuiltinProviderChains(ctx, chainRepo); err != nil {
		t.Fatalf("second sync: %v", err)
	}
	again, err := chainRepo.GetChain(ctx, folderID, "movie")
	if err != nil {
		t.Fatalf("get chain after second sync: %v", err)
	}
	if !reflect.DeepEqual(after, again) {
		t.Errorf("sync is not idempotent:\nfirst=%+v\nsecond=%+v", after, again)
	}
}
