package plugins

import (
	"reflect"
	"testing"

	pluginv1 "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginproto/silo/plugin/v1"
)

func seedTestService(t *testing.T, existing []*RuntimeConfig) (*Service, *fakeServiceConfigStore) {
	t.Helper()
	manifest := testPluginManifest(t, "silo.watchprovider.simkl", "0.1.0")
	manifest.GlobalConfigSchema = []*pluginv1.ConfigSchema{{
		Key:        "app",
		Title:      "Simkl app",
		JsonSchema: `{"type":"object","properties":{"client_id":{"type":"string"}},"additionalProperties":false}`,
	}}
	store := &fakeServiceConfigStore{configsByInstallation: map[int][]*RuntimeConfig{7: existing}}
	service := &Service{
		installations: newFakeServiceInstallationStore(&Installation{
			ID:          7,
			PluginID:    manifest.GetPluginId(),
			Version:     manifest.GetVersion(),
			InstallPath: writeInstalledPluginManifest(t, manifest),
			Enabled:     true,
		}),
		configs: store,
	}
	return service, store
}

func TestSeedGlobalConfigKeepsOnlyDeclaredFields(t *testing.T) {
	service, store := seedTestService(t, nil)
	seeded, err := service.SeedGlobalConfig(t.Context(), 7, "app", map[string]any{
		"client_id":     "simkl-id",
		"client_secret": "not declared by this plugin",
	})
	if err != nil || !seeded {
		t.Fatalf("SeedGlobalConfig() = %t, %v", seeded, err)
	}
	if len(store.puts) != 1 || !reflect.DeepEqual(store.puts[0].value, map[string]any{"client_id": "simkl-id"}) {
		t.Fatalf("puts = %#v, want only the declared client_id", store.puts)
	}
}

func TestSeedGlobalConfigNeverOverwritesSavedConfig(t *testing.T) {
	service, store := seedTestService(t, []*RuntimeConfig{{
		InstallationID: 7,
		Key:            "app",
		Value:          map[string]any{"client_id": "admin-entered"},
	}})
	seeded, err := service.SeedGlobalConfig(t.Context(), 7, "app", map[string]any{"client_id": "legacy"})
	if err != nil || seeded || len(store.puts) != 0 {
		t.Fatalf("SeedGlobalConfig() = %t, %v, puts = %d; want no write", seeded, err, len(store.puts))
	}
}

func TestSeedGlobalConfigSkipsEmptyAndUndeclaredValues(t *testing.T) {
	service, store := seedTestService(t, nil)
	for _, key := range []string{"app", "other"} {
		seeded, err := service.SeedGlobalConfig(t.Context(), 7, key, map[string]any{"client_id": " "})
		if err != nil || seeded || len(store.puts) != 0 {
			t.Fatalf("%s: SeedGlobalConfig() = %t, %v, puts = %d; want no write", key, seeded, err, len(store.puts))
		}
	}
}

func TestInstalledFromSiloRepositoryWithoutRepository(t *testing.T) {
	service := &Service{}
	for _, installation := range []*Installation{nil, {ID: 1}} {
		managed, err := service.InstalledFromSiloRepository(t.Context(), installation)
		if err != nil || managed {
			t.Fatalf("InstalledFromSiloRepository(%v) = %t, %v; want false", installation, managed, err)
		}
	}
}
