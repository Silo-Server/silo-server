package metadata

import (
	"context"
	"fmt"
	"reflect"
	"slices"
	"testing"
	"time"
)

func TestPinLocalTier(t *testing.T) {
	t.Parallel()

	entries := []ChainEntry{
		{PluginInstallationID: 2, CapabilityID: "tmdb", ContentLevel: "series", Priority: 1, Enabled: true},
		{PluginInstallationID: 3, CapabilityID: "tvdb", ContentLevel: "series", Priority: 0, Enabled: true},
		{PluginInstallationID: 1, CapabilityID: "nfo", ContentLevel: "series", Priority: 7, Enabled: false},
		{PluginInstallationID: 2, CapabilityID: "tmdb", ContentLevel: "movie", Priority: 0, Enabled: true},
		{PluginInstallationID: 1, CapabilityID: "nfo", ContentLevel: "movie", Priority: 1, Enabled: true},
	}
	got := pinLocalTier(entries, func(e ChainEntry) bool { return e.PluginInstallationID == 1 })
	want := []ChainEntry{
		{PluginInstallationID: 1, CapabilityID: "nfo", ContentLevel: "movie", Priority: 0, Enabled: true, Local: true},
		{PluginInstallationID: 2, CapabilityID: "tmdb", ContentLevel: "movie", Priority: 1, Enabled: true},
		{PluginInstallationID: 1, CapabilityID: "nfo", ContentLevel: "series", Priority: 0, Enabled: false, Local: true},
		{PluginInstallationID: 3, CapabilityID: "tvdb", ContentLevel: "series", Priority: 1, Enabled: true},
		{PluginInstallationID: 2, CapabilityID: "tmdb", ContentLevel: "series", Priority: 2, Enabled: true},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("pinLocalTier =\n%+v\nwant\n%+v", got, want)
	}
	if entries[2].Priority != 7 {
		t.Error("pinLocalTier modified its input")
	}
}

// SetChain stores the local tier first whatever order the caller sends, and
// resolution runs it first even when a stored chain lists it last (a row an
// older binary wrote).
func TestLocalTierIsStoredAndResolvedFirst(t *testing.T) {
	pool := chainBuiltinTestPool(t)
	ctx := context.Background()

	localCap := fmt.Sprintf("test-local-%d", time.Now().UnixNano())
	RegisterBuiltinProvider(localCap, func() Provider { return &builtinStubProvider{slug: localCap} })
	builtinID := insertTestInstallation(t, pool, "builtin", true)
	// Disabled by default, so the startup sync below appends it to other
	// tests' chains switched off, as the legacy sync test's builtin is.
	insertTestCapability(t, pool, builtinID, localCap, `{"default_priority":{"movie":1},"default_enabled":false}`)
	pluginID := insertTestInstallation(t, pool, "plugin", true)
	remoteCap := fmt.Sprintf("test-remote-%d", time.Now().UnixNano())
	insertTestCapability(t, pool, pluginID, remoteCap, `{"metadata":{"default_priority":{"movie":1}}}`)
	chainRepo := NewChainRepository(pool)

	folderID := insertTestFolder(t, pool, "movies")
	if err := chainRepo.SetChain(ctx, folderID, []ChainEntry{
		{PluginInstallationID: pluginID, CapabilityID: remoteCap, ContentLevel: "movie", Priority: 0, Enabled: true},
		{PluginInstallationID: builtinID, CapabilityID: localCap, ContentLevel: "movie", Priority: 1, Enabled: true},
	}); err != nil {
		t.Fatalf("set chain: %v", err)
	}
	stored, err := chainRepo.GetChain(ctx, folderID, "movie")
	if err != nil {
		t.Fatalf("get chain: %v", err)
	}
	if len(stored) != 2 || stored[0].CapabilityID != localCap || !stored[0].Local || stored[0].Priority != 0 ||
		stored[1].CapabilityID != remoteCap || stored[1].Local || stored[1].Priority != 1 {
		t.Fatalf("stored chain = %+v, want the local entry first", stored)
	}

	// A row written out of order, bypassing SetChain.
	if _, err := pool.Exec(ctx,
		`UPDATE library_provider_chains SET priority = CASE WHEN plugin_installation_id = $2 THEN 5 ELSE 0 END
		 WHERE media_folder_id = $1`, folderID, builtinID); err != nil {
		t.Fatalf("reorder rows: %v", err)
	}
	providers, err := ResolveChainWithChecker(ctx, folderID, "movie", chainRepo, stubMetadataResolver{}, nil)
	if err != nil {
		t.Fatalf("resolve chain: %v", err)
	}
	if len(providers) != 2 || providers[0].Slug() != localCap || providers[1].Slug() != remoteCap {
		t.Fatalf("resolved = %v, want the local provider first", providerSlugs(providers))
	}

	// The startup sync moves stored rows back into place.
	if err := SyncBuiltinProviderChains(ctx, chainRepo); err != nil {
		t.Fatalf("sync: %v", err)
	}
	stored, err = chainRepo.GetChain(ctx, folderID, "movie")
	if err != nil {
		t.Fatalf("get chain after sync: %v", err)
	}
	for i, e := range stored {
		if e.Local != (i < len(stored)-1) {
			t.Fatalf("stored chain after sync = %+v, want the builtin rows first", stored)
		}
	}
}

func providerSlugs(providers []Provider) []string {
	out := make([]string, len(providers))
	for i, p := range providers {
		out[i] = p.Slug()
	}
	return out
}

// Switching local metadata off holds even when it leaves a level with no
// enabled provider: the chain-less fallback, which includes the
// default-enabled local tier, must not bring it back.
func TestLocalTierSwitchedOffStaysOffInFallback(t *testing.T) {
	pool := chainBuiltinTestPool(t)
	ctx := context.Background()

	localCap := fmt.Sprintf("test-local-off-%d", time.Now().UnixNano())
	RegisterBuiltinProvider(localCap, func() Provider { return &builtinStubProvider{slug: localCap} })
	builtinID := insertTestInstallation(t, pool, "builtin", true)
	insertTestCapability(t, pool, builtinID, localCap, `{"default_priority":{"movie":1},"default_enabled":true}`)
	chainRepo := NewChainRepository(pool)

	resolvesLocal := func(folderID int) bool {
		t.Helper()
		providers, err := ResolveChainWithChecker(ctx, folderID, "movie", chainRepo, stubMetadataResolver{}, nil)
		if err != nil {
			t.Fatalf("resolve chain: %v", err)
		}
		return slices.Contains(providerSlugs(providers), localCap)
	}

	if !resolvesLocal(insertTestFolder(t, pool, "movies")) {
		t.Error("chain-less library: local tier missing from the fallback, want it on by default")
	}
	folderID := insertTestFolder(t, pool, "movies")
	if err := chainRepo.SetChain(ctx, folderID, []ChainEntry{
		{PluginInstallationID: builtinID, CapabilityID: localCap, ContentLevel: "movie", Enabled: false},
	}); err != nil {
		t.Fatalf("set chain: %v", err)
	}
	if resolvesLocal(folderID) {
		t.Error("local tier switched off but resolved through the fallback")
	}
}
