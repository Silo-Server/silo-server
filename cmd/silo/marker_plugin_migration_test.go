package main

import (
	"context"
	"errors"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	sdkcapability "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginsdk/capability"
	"github.com/Silo-Server/silo-server/internal/markers"
	"github.com/Silo-Server/silo-server/internal/pluginhost"
	"github.com/Silo-Server/silo-server/internal/plugins"
)

type fakeMarkerRuntimeConfigs struct {
	configs map[int][]*plugins.RuntimeConfig
	puts    []plugins.RuntimeConfig
	errors  map[int]error
}

func (f *fakeMarkerRuntimeConfigs) ListGlobalConfigs(_ context.Context, installationID int) ([]*plugins.RuntimeConfig, error) {
	if err := f.errors[installationID]; err != nil {
		return nil, err
	}
	return append([]*plugins.RuntimeConfig(nil), f.configs[installationID]...), nil
}

type fakeMarkerCapabilities struct {
	installations []*plugins.Installation
	capabilities  map[int][]*plugins.Capability
}

func (f *fakeMarkerCapabilities) ListEnabled(context.Context) ([]*plugins.Installation, error) {
	return append([]*plugins.Installation(nil), f.installations...), nil
}

func (f *fakeMarkerCapabilities) ListCapabilities(_ context.Context, installationID int) ([]*plugins.Capability, error) {
	return append([]*plugins.Capability(nil), f.capabilities[installationID]...), nil
}

type unusedMarkerPluginResolver struct{ refreshed []int }

func (*unusedMarkerPluginResolver) MarkerProviderClient(context.Context, int, string) (*pluginhost.MarkerProviderClient, error) {
	return nil, errors.New("unexpected marker provider request")
}

func (r *unusedMarkerPluginResolver) RefreshMarkerRuntime(id int) error {
	r.refreshed = append(r.refreshed, id)
	return nil
}

func (f *fakeMarkerRuntimeConfigs) PutGlobalConfig(_ context.Context, installationID int, key string, value map[string]any) error {
	f.puts = append(f.puts, plugins.RuntimeConfig{InstallationID: installationID, Key: key, Value: value})
	f.configs[installationID] = append(f.configs[installationID], &plugins.RuntimeConfig{
		InstallationID: installationID,
		Key:            key,
		Value:          value,
	})
	return nil
}

type fakeMarkerLegacySettings map[string]string

func (f fakeMarkerLegacySettings) Get(_ context.Context, key string) (string, error) {
	return f[key], nil
}

type fakeMarkerProviderConfigs struct {
	rows map[string]markers.ProviderConfig
	// sharingSeeds lists the rows seeded through EnsureDefaultSharing; the
	// store's DB test covers how markers.share_by_default shapes them.
	sharingSeeds []string
}

func (f *fakeMarkerProviderConfigs) Get(provider string) (markers.ProviderConfig, bool) {
	c, ok := f.rows[provider]
	return c, ok
}

func (f *fakeMarkerProviderConfigs) Ensure(_ context.Context, c markers.ProviderConfig) error {
	if f.rows == nil {
		f.rows = map[string]markers.ProviderConfig{}
	}
	if _, ok := f.rows[c.Provider]; !ok {
		f.rows[c.Provider] = c
	}
	return nil
}

func (f *fakeMarkerProviderConfigs) EnsureDefaultSharing(ctx context.Context, c markers.ProviderConfig) error {
	f.sharingSeeds = append(f.sharingSeeds, c.Provider)
	return f.Ensure(ctx, c)
}

func TestCopyLegacyIntroDBPluginConfigCopiesAPIKeyOnce(t *testing.T) {
	runtimeConfigs := &fakeMarkerRuntimeConfigs{configs: map[int][]*plugins.RuntimeConfig{}}
	installation := &plugins.Installation{ID: 42, PluginID: "silo.theintrodb"}
	capability := &plugins.Capability{ID: "introdb"}

	if err := copyLegacyIntroDBPluginConfig(
		context.Background(),
		runtimeConfigs,
		fakeMarkerLegacySettings{"introdb.api_key": " legacy-key "},
		installation,
		capability,
	); err != nil {
		t.Fatalf("copyLegacyIntroDBPluginConfig: %v", err)
	}
	if len(runtimeConfigs.puts) != 1 {
		t.Fatalf("puts = %d, want 1", len(runtimeConfigs.puts))
	}
	got := runtimeConfigs.puts[0]
	if got.InstallationID != 42 || got.Key != "account" || got.Value["api_key"] != "legacy-key" {
		t.Fatalf("put = %+v, want account api_key copy", got)
	}

	if err := copyLegacyIntroDBPluginConfig(
		context.Background(),
		runtimeConfigs,
		fakeMarkerLegacySettings{"introdb.api_key": "new-key"},
		installation,
		capability,
	); err != nil {
		t.Fatalf("second copyLegacyIntroDBPluginConfig: %v", err)
	}
	if len(runtimeConfigs.puts) != 1 {
		t.Fatalf("second copy overwrote config; puts = %d, want 1", len(runtimeConfigs.puts))
	}
}

func TestCopyLegacyIntroDBPluginConfigIgnoresOtherPlugins(t *testing.T) {
	runtimeConfigs := &fakeMarkerRuntimeConfigs{configs: map[int][]*plugins.RuntimeConfig{}}
	if err := copyLegacyIntroDBPluginConfig(
		context.Background(),
		runtimeConfigs,
		fakeMarkerLegacySettings{"introdb.api_key": "legacy-key"},
		&plugins.Installation{ID: 7, PluginID: "silo.other"},
		&plugins.Capability{ID: "introdb"},
	); err != nil {
		t.Fatalf("copyLegacyIntroDBPluginConfig: %v", err)
	}
	if len(runtimeConfigs.puts) != 0 {
		t.Fatalf("puts = %d, want 0 for non-TheIntroDB plugin", len(runtimeConfigs.puts))
	}
}

func TestReloadMarkerPluginProvidersCacheRevision(t *testing.T) {
	installation := &plugins.Installation{ID: 42, PluginID: "silo.theintrodb", Version: "1.0.0"}
	store := &fakeMarkerCapabilities{
		installations: []*plugins.Installation{installation},
		capabilities: map[int][]*plugins.Capability{
			42: {{InstallationID: 42, Type: sdkcapability.MarkerProvider, ID: "introdb"}},
		},
	}
	updatedAt := time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)
	account := &plugins.RuntimeConfig{Key: "account", Value: map[string]any{"api_key": "test-api-key"}, UpdatedAt: updatedAt}
	preferences := &plugins.RuntimeConfig{Key: "preferences", Value: map[string]any{"language": "en"}, UpdatedAt: updatedAt}
	runtimeConfigs := &fakeMarkerRuntimeConfigs{configs: map[int][]*plugins.RuntimeConfig{42: {preferences, account}}}
	registry := markers.NewRegistry(nil)
	runtime := &unusedMarkerPluginResolver{}
	resolver := markers.NewPluginResolverAdapter(runtime)
	reloadRevision := func() string {
		t.Helper()
		if err := reloadMarkerPluginProviders(context.Background(), registry, nil, store, runtimeConfigs, nil, resolver); err != nil {
			t.Fatalf("reloadMarkerPluginProviders: %v", err)
		}
		providers := registry.Providers()
		if len(providers) != 1 {
			t.Fatalf("providers = %d, want 1", len(providers))
		}
		provider, ok := providers[0].(*markers.PluginProvider)
		if !ok {
			t.Fatalf("provider = %T, want *markers.PluginProvider", providers[0])
		}
		revision := provider.CacheRevision()
		if revision == "" {
			t.Fatal("cache revision is empty")
		}
		for _, secret := range []string{"test-api-key", "changed-api-key"} {
			if strings.Contains(revision, secret) {
				t.Fatal("cache revision contains an API key")
			}
		}
		return revision
	}

	original := reloadRevision()
	runtimeConfigs.configs[42] = []*plugins.RuntimeConfig{account, preferences}
	if got := reloadRevision(); got != original {
		t.Fatal("reordering config rows changed the cache revision")
	}
	account.Value["api_key"] = "changed-api-key"
	if got := reloadRevision(); got != original {
		t.Fatal("config values changed the revision without a persisted timestamp change")
	}
	account.UpdatedAt = updatedAt.Add(time.Second)
	reconfigured := reloadRevision()
	if reconfigured == original {
		t.Fatal("updated config timestamp did not change the cache revision")
	}
	installation.Version = "1.1.0"
	if got := reloadRevision(); got == reconfigured {
		t.Fatal("plugin upgrade did not change the cache revision")
	}
	if len(runtime.refreshed) != 3 {
		t.Fatalf("runtime refreshes = %v, want initial load, configuration change and upgrade", runtime.refreshed)
	}
}

func TestReloadMarkerPluginProvidersRemovesProviderOnConfigReadFailure(t *testing.T) {
	store := &fakeMarkerCapabilities{
		installations: []*plugins.Installation{
			{ID: 42, PluginID: "silo.theintrodb", Version: "1.0.0"},
			{ID: 43, PluginID: "silo.other", Version: "1.0.0"},
		},
		capabilities: map[int][]*plugins.Capability{
			42: {{InstallationID: 42, Type: sdkcapability.MarkerProvider, ID: "introdb"}},
			43: {{InstallationID: 43, Type: sdkcapability.MarkerProvider, ID: "other"}},
		},
	}
	runtimeConfigs := &fakeMarkerRuntimeConfigs{configs: map[int][]*plugins.RuntimeConfig{}}
	registry := markers.NewRegistry(nil)
	resolver := markers.NewPluginResolverAdapter(&unusedMarkerPluginResolver{})
	if err := reloadMarkerPluginProviders(context.Background(), registry, nil, store, runtimeConfigs, nil, resolver); err != nil {
		t.Fatalf("initial reloadMarkerPluginProviders: %v", err)
	}
	if got := len(registry.Providers()); got != 2 {
		t.Fatalf("initial provider count = %d, want 2", got)
	}

	configErr := errors.New("config unavailable")
	runtimeConfigs.errors = map[int]error{42: configErr}
	err := reloadMarkerPluginProviders(context.Background(), registry, nil, store, runtimeConfigs, nil, resolver)
	if !errors.Is(err, configErr) {
		t.Fatalf("reload error = %v, want %v", err, configErr)
	}
	providers := registry.Providers()
	if len(providers) != 1 || providers[0].ID() != markers.PluginProviderID(43, "other") {
		t.Fatalf("providers = %+v, want only the healthy provider", providers)
	}
}

func TestReloadMarkerPluginProvidersHonorsSupportsContribution(t *testing.T) {
	sharingDefaults := map[string]any{
		"default_contribute_enabled":        true,
		"default_contribute_auto_local":     "true",
		"default_contribute_min_confidence": 0.9,
	}
	for _, test := range []struct {
		name       string
		metadata   map[string]any
		wantSubmit bool
		wantAmend  bool
		wantAbsent bool
		// wantConfig is the row created for the provider; Provider,
		// FetchEnabled and FetchPriority are not compared.
		wantConfig       markers.ProviderConfig
		wantShareDefault bool
		// wantSharingSeed means the row is seeded through EnsureDefaultSharing,
		// so markers.share_by_default=false keeps it off.
		wantSharingSeed bool
	}{
		{
			name: "manifest without the key keeps submissions", metadata: nil, wantSubmit: true,
			wantConfig: markers.ProviderConfig{ContributeMinConfidence: 0.95},
		},
		{
			name:       "fetch-only manifest ignores sharing defaults",
			metadata:   map[string]any{"metadata": map[string]any{"supports_contribution": false, "default_contribute_enabled": true}},
			wantSubmit: false,
			wantConfig: markers.ProviderConfig{ContributeMinConfidence: 0.95},
		},
		{
			name:       "amend and absent declared",
			metadata:   map[string]any{"metadata": map[string]any{"supports_amend": true, "supports_absent": true}},
			wantSubmit: true, wantAmend: true, wantAbsent: true,
			wantConfig: markers.ProviderConfig{ContributeMinConfidence: 0.95},
		},
		{
			name:             "sharing defaults start contribution on",
			metadata:         map[string]any{"metadata": sharingDefaults},
			wantSubmit:       true,
			wantConfig:       markers.ProviderConfig{ContributeEnabled: true, ContributeAutoLocal: true, ContributeMinConfidence: 0.9},
			wantShareDefault: true,
			wantSharingSeed:  true,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := &fakeMarkerCapabilities{
				installations: []*plugins.Installation{{ID: 42, PluginID: "silo.public-markers", Version: "1.0.0"}},
				capabilities: map[int][]*plugins.Capability{
					42: {{InstallationID: 42, Type: sdkcapability.MarkerProvider, ID: "markers", Metadata: test.metadata}},
				},
			}
			runtimeConfigs := &fakeMarkerRuntimeConfigs{configs: map[int][]*plugins.RuntimeConfig{}}
			registry := markers.NewRegistry(nil)
			resolver := markers.NewPluginResolverAdapter(&unusedMarkerPluginResolver{})
			configs := &fakeMarkerProviderConfigs{}
			if err := reloadMarkerPluginProviders(context.Background(), registry, configs, store, runtimeConfigs, nil, resolver); err != nil {
				t.Fatalf("reloadMarkerPluginProviders: %v", err)
			}
			providers := registry.Providers()
			if len(providers) != 1 {
				t.Fatalf("providers = %d, want 1", len(providers))
			}
			got := configs.rows[providers[0].ID()]
			if seeded := len(configs.sharingSeeds) == 1; seeded != test.wantSharingSeed {
				t.Fatalf("sharing seeds = %v, want sharing seed %v", configs.sharingSeeds, test.wantSharingSeed)
			}
			if got.ContributeEnabled != test.wantConfig.ContributeEnabled ||
				got.ContributeAutoLocal != test.wantConfig.ContributeAutoLocal ||
				got.ContributeMinConfidence != test.wantConfig.ContributeMinConfidence {
				t.Fatalf("config = %+v, want contribution like %+v", got, test.wantConfig)
			}
			if share := providers[0].(markers.DescribedProvider).ProviderDescription().DefaultContributeEnabled; share != test.wantShareDefault {
				t.Fatalf("DefaultContributeEnabled = %v, want %v", share, test.wantShareDefault)
			}
			if _, ok := providers[0].(markers.Submitter); ok != test.wantSubmit {
				t.Fatalf("provider %T implements Submitter = %v, want %v", providers[0], ok, test.wantSubmit)
			}
			if !test.wantSubmit {
				return
			}
			reqs := providers[0].(markers.SubmissionRequirementProvider).SubmissionRequirements()
			if reqs.SupportsAmend != test.wantAmend || reqs.SupportsAbsent != test.wantAbsent {
				t.Fatalf("submission requirements = %+v, want amend=%v absent=%v", reqs, test.wantAmend, test.wantAbsent)
			}
		})
	}
}

// Saving markers.share_by_default=false stops sharing on every installed
// provider whose manifest shares by default, disabled and currently fetch-only
// ones included, using only the save's own connection:
// database.max_connections may be 1.
func TestStopDefaultMarkerSharingInTxDB(t *testing.T) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("parse dsn: %v", err)
	}
	cfg.MaxConns = 1
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatalf("connect test database: %v", err)
	}
	t.Cleanup(pool.Close)

	installations := plugins.NewInstallationStore(pool)
	configs := markers.NewProviderConfigStore(pool)
	suffix := strconv.FormatInt(time.Now().UnixNano(), 36)
	install := func(name string, enabled bool, metadata map[string]any) string {
		t.Helper()
		installation, err := installations.Create(ctx, plugins.CreateInstallationInput{
			PluginID: "test.markers." + name + "." + suffix, Version: "1.0.0", InstallPath: t.TempDir(), Enabled: enabled,
			Capabilities: []plugins.Capability{{Type: sdkcapability.MarkerProvider, ID: "markers", Metadata: metadata}},
		})
		if err != nil {
			t.Fatalf("install %s: %v", name, err)
		}
		t.Cleanup(func() { _ = installations.Delete(context.Background(), installation.ID) })
		provider := markers.PluginProviderID(installation.ID, "markers")
		t.Cleanup(func() {
			_, _ = pool.Exec(context.Background(), `DELETE FROM marker_provider_config WHERE provider = $1`, provider)
		})
		if err := configs.Ensure(ctx, markers.ProviderConfig{
			Provider: provider, FetchEnabled: true, ContributeEnabled: true, ContributeAutoLocal: true, ContributeMinConfidence: 0.9,
		}); err != nil {
			t.Fatalf("seed %s config: %v", name, err)
		}
		return provider
	}
	sharingDisabled := install("sharing", false, map[string]any{"metadata": map[string]any{"default_contribute_enabled": true}})
	optedIn := install("opted-in", true, nil)
	fetchOnly := install("fetch-only", true, map[string]any{"metadata": map[string]any{"default_contribute_enabled": true, "supports_contribution": false}})

	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	if err := stopDefaultMarkerSharingInTx(ctx, tx); err != nil {
		_ = tx.Rollback(ctx)
		t.Fatalf("stopDefaultMarkerSharingInTx: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit: %v", err)
	}
	if err := configs.Reload(ctx); err != nil {
		t.Fatalf("reload: %v", err)
	}
	for provider, wantSharing := range map[string]bool{sharingDisabled: false, optedIn: true, fetchOnly: false} {
		if got, _ := configs.Get(provider); got.ContributeEnabled != wantSharing || got.ContributeAutoLocal != wantSharing {
			t.Errorf("%s = %+v, want sharing %v", provider, got, wantSharing)
		}
	}
}
