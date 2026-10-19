package watchsync

import (
	"context"
	"errors"
	"reflect"
	"testing"

	pluginv1 "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginproto/silo/plugin/v1"
)

func TestPluginProviderKey(t *testing.T) {
	for _, tc := range []struct {
		name         string
		pluginID     string
		capabilityID string
		siloManaged  bool
		want         string
	}{
		{name: "retired Trakt plugin no longer claims the built-in key", pluginID: "silo.watchprovider.trakt", capabilityID: "trakt", siloManaged: true, want: "plugin:3:trakt"},
		{name: "first-party Simkl keeps the built-in key", pluginID: "silo.watchprovider.simkl", capabilityID: "simkl", siloManaged: true, want: "simkl"},
		{name: "first-party MDBList keeps the built-in key", pluginID: "silo.watchprovider.mdblist", capabilityID: "mdblist", siloManaged: true, want: "mdblist"},
		{name: "first-party ID from another repository", pluginID: "silo.watchprovider.simkl", capabilityID: "simkl", want: "plugin:3:simkl"},
		{name: "unknown capability of a first-party plugin", pluginID: "silo.watchprovider.simkl", capabilityID: "other", siloManaged: true, want: "plugin:3:other"},
		{name: "third-party plugin", pluginID: "silo.watchprovider.floppy", capabilityID: "floppy", siloManaged: true, want: "plugin:3:floppy"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := PluginProviderKey(3, tc.pluginID, tc.capabilityID, tc.siloManaged); got != tc.want {
				t.Fatalf("PluginProviderKey() = %q, want %q", got, tc.want)
			}
		})
	}
}

type fakeFirstPartyStore struct {
	settings    map[string]string
	connections map[string]bool
	err         error
}

func (f *fakeFirstPartyStore) GetServerSetting(_ context.Context, key string) (string, error) {
	return f.settings[key], f.err
}

func (f *fakeFirstPartyStore) SetServerSetting(_ context.Context, key, value string) error {
	if f.settings == nil {
		f.settings = map[string]string{}
	}
	f.settings[key] = value
	return f.err
}

func (f *fakeFirstPartyStore) HasConnections(_ context.Context, providerKey string) (bool, error) {
	return f.connections[providerKey], f.err
}

func TestFirstPartyPluginsToInstallUntilMigrated(t *testing.T) {
	// Trakt connections no longer pull in the retired Trakt plugin.
	store := &fakeFirstPartyStore{connections: map[string]bool{"simkl": true, "mdblist": true, "trakt": true}}
	got, err := FirstPartyPluginsToInstall(t.Context(), store)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"silo.watchprovider.mdblist", "silo.watchprovider.simkl"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("plugins to install = %v, want %v", got, want)
	}

	for _, key := range []string{"simkl", "plugin:4:floppy"} {
		if err := MarkFirstPartyMigrated(t.Context(), store, key); err != nil {
			t.Fatal(err)
		}
	}
	if _, marked := store.settings[firstPartyMigrationSettingKey("plugin:4:floppy")]; marked {
		t.Fatal("a third-party provider was recorded as migrated")
	}
	got, err = FirstPartyPluginsToInstall(t.Context(), store)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"silo.watchprovider.mdblist"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("plugins to install after Simkl migrated = %v, want %v", got, want)
	}
}

func TestRetiredPlugin(t *testing.T) {
	if !RetiredPlugin("silo.watchprovider.trakt") {
		t.Fatal("the Trakt plugin is not retired")
	}
	for _, pluginID := range []string{"silo.watchprovider.simkl", "silo.watchprovider.mdblist", "silo.watchprovider.floppy"} {
		if RetiredPlugin(pluginID) {
			t.Fatalf("%s is retired", pluginID)
		}
	}
}

func TestFirstPartyPluginsToInstallReportsStoreErrors(t *testing.T) {
	if _, err := FirstPartyPluginsToInstall(t.Context(), &fakeFirstPartyStore{err: errors.New("down")}); err == nil {
		t.Fatal("expected a store error")
	}
}

type fakeConfigSeeder struct {
	installationID int
	key            string
	value          map[string]any
}

func (f *fakeConfigSeeder) SeedGlobalConfig(_ context.Context, installationID int, key string, value map[string]any) (bool, error) {
	f.installationID, f.key, f.value = installationID, key, value
	return true, nil
}

func TestSeedFirstPartyAppConfig(t *testing.T) {
	store := &fakeFirstPartyStore{settings: map[string]string{
		"watchsync.simkl.client_id":     "simkl-id",
		"watchsync.simkl.client_secret": "simkl-secret",
	}}
	seeder := &fakeConfigSeeder{}
	seeded, err := SeedFirstPartyAppConfig(t.Context(), "simkl", 5, store, seeder)
	if err != nil || !seeded {
		t.Fatalf("SeedFirstPartyAppConfig() = %t, %v", seeded, err)
	}
	want := map[string]any{"client_id": "simkl-id", "client_secret": "simkl-secret"}
	if seeder.installationID != 5 || seeder.key != "app" || !reflect.DeepEqual(seeder.value, want) {
		t.Fatalf("seeded %d %q %v", seeder.installationID, seeder.key, seeder.value)
	}

	// Seeding runs once: app credentials an admin clears from the plugin later
	// are not restored from the old settings.
	again := &fakeConfigSeeder{}
	if seeded, err := SeedFirstPartyAppConfig(t.Context(), "simkl", 5, store, again); err != nil || seeded || again.value != nil {
		t.Fatalf("second seed = %t, err = %v, value = %v; want nothing", seeded, err, again.value)
	}

	for _, key := range []string{"simkl", "plugin:4:floppy"} {
		seeder := &fakeConfigSeeder{}
		seeded, err := SeedFirstPartyAppConfig(t.Context(), key, 5, store, seeder)
		if err != nil || seeded || seeder.value != nil {
			t.Fatalf("%s: seeded = %t, err = %v, value = %v; want nothing without saved credentials", key, seeded, err, seeder.value)
		}
	}
}

// A plugin that needs provider app credentials reports whether its required
// config is saved, so clients can hold back Connect until an admin sets it up.
func TestPluginConnectionStatusReportsAppConfigReadiness(t *testing.T) {
	for _, tc := range []struct {
		name  string
		ready WatchSyncPluginConfigReady
		want  bool
	}{
		{name: "required config saved", ready: func(context.Context, int) (bool, error) { return true, nil }, want: true},
		{name: "required config missing", ready: func(context.Context, int) (bool, error) { return false, nil }, want: false},
		{name: "config check failed", ready: func(context.Context, int) (bool, error) { return false, errors.New("down") }, want: true},
		{name: "no config check", want: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			provider, err := NewPluginProvider(PluginProviderOptions{
				InstallationID: 4,
				ProviderKey:    "simkl",
				CapabilityID:   "simkl",
				Descriptor: &pluginv1.WatchSyncProviderDescriptor{
					AuthMethods:   []pluginv1.WatchSyncAuthMethod{pluginv1.WatchSyncAuthMethod_WATCH_SYNC_AUTH_METHOD_DEVICE_CODE},
					ExportWatched: true,
				},
				ResolveClient: func(context.Context, int, string) (WatchSyncPluginClient, error) {
					return nil, errors.New("not used")
				},
				ConfigReady: tc.ready,
			})
			if err != nil {
				t.Fatal(err)
			}
			registry := NewRegistry()
			if err := registry.ReplacePluginProviders([]Provider{provider}); err != nil {
				t.Fatal(err)
			}
			status, err := NewService(newServiceFakeRepo(), registry).GetConnectionStatus(t.Context(), 7, "profile-1", "simkl")
			if err != nil {
				t.Fatal(err)
			}
			if status.CredentialsConfigured != tc.want {
				t.Fatalf("credentials_configured = %t, want %t", status.CredentialsConfigured, tc.want)
			}
		})
	}
}
