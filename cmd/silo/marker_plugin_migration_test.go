package main

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

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
