package main

import (
	"context"
	"testing"

	"github.com/Silo-Server/silo-server/internal/plugins"
)

type fakeMarkerRuntimeConfigs struct {
	configs map[int][]*plugins.RuntimeConfig
	puts    []plugins.RuntimeConfig
}

func (f *fakeMarkerRuntimeConfigs) ListGlobalConfigs(_ context.Context, installationID int) ([]*plugins.RuntimeConfig, error) {
	return append([]*plugins.RuntimeConfig(nil), f.configs[installationID]...), nil
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
