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
		{name: "first-party Trakt keeps the built-in key", pluginID: "silo.watchprovider.trakt", capabilityID: "trakt", siloManaged: true, want: "trakt"},
		{name: "first-party Simkl keeps the built-in key", pluginID: "silo.watchprovider.simkl", capabilityID: "simkl", siloManaged: true, want: "simkl"},
		{name: "first-party MDBList keeps the built-in key", pluginID: "silo.watchprovider.mdblist", capabilityID: "mdblist", siloManaged: true, want: "mdblist"},
		{name: "first-party ID from another repository", pluginID: "silo.watchprovider.trakt", capabilityID: "trakt", want: "plugin:3:trakt"},
		{name: "unknown capability of a first-party plugin", pluginID: "silo.watchprovider.trakt", capabilityID: "other", siloManaged: true, want: "plugin:3:other"},
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
	store := &fakeFirstPartyStore{connections: map[string]bool{"trakt": true, "mdblist": true}}
	got, err := FirstPartyPluginsToInstall(t.Context(), store)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"silo.watchprovider.mdblist", "silo.watchprovider.trakt"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("plugins to install = %v, want %v", got, want)
	}

	for _, key := range []string{"trakt", "plugin:4:floppy"} {
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
		t.Fatalf("plugins to install after Trakt migrated = %v, want %v", got, want)
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
		"watchsync.trakt.client_id":     "trakt-id",
		"watchsync.trakt.client_secret": "trakt-secret",
	}}
	seeder := &fakeConfigSeeder{}
	seeded, err := SeedFirstPartyAppConfig(t.Context(), "trakt", 5, store, seeder)
	if err != nil || !seeded {
		t.Fatalf("SeedFirstPartyAppConfig() = %t, %v", seeded, err)
	}
	want := map[string]any{"client_id": "trakt-id", "client_secret": "trakt-secret"}
	if seeder.installationID != 5 || seeder.key != "app" || !reflect.DeepEqual(seeder.value, want) {
		t.Fatalf("seeded %d %q %v", seeder.installationID, seeder.key, seeder.value)
	}

	// Seeding runs once: app credentials an admin clears from the plugin later
	// are not restored from the old settings.
	again := &fakeConfigSeeder{}
	if seeded, err := SeedFirstPartyAppConfig(t.Context(), "trakt", 5, store, again); err != nil || seeded || again.value != nil {
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

func TestServiceAppClientID(t *testing.T) {
	registry := NewRegistry()
	provider, err := NewPluginProvider(PluginProviderOptions{
		InstallationID: 2,
		ProviderKey:    "trakt",
		CapabilityID:   "trakt",
		Descriptor: &pluginv1.WatchSyncProviderDescriptor{
			AuthMethods:   []pluginv1.WatchSyncAuthMethod{pluginv1.WatchSyncAuthMethod_WATCH_SYNC_AUTH_METHOD_DEVICE_CODE},
			ExportWatched: true,
		},
		ResolveClient: func(context.Context, int, string) (WatchSyncPluginClient, error) {
			return nil, errors.New("not used")
		},
		ResolveConfig: func(context.Context, int) (*pluginv1.WatchSyncProviderConfig, error) {
			return &pluginv1.WatchSyncProviderConfig{
				Values:       map[string]string{"app.client_id": " plugin-id "},
				SecretValues: map[string]string{"app.client_secret": "secret"},
			}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := registry.ReplacePluginProviders([]Provider{provider}); err != nil {
		t.Fatal(err)
	}
	service := NewService(nil, registry)
	if got, err := service.AppClientID(t.Context(), "trakt"); err != nil || got != "plugin-id" {
		t.Fatalf("AppClientID(trakt) = %q, %v", got, err)
	}
	if got, err := service.AppClientID(t.Context(), "simkl"); err != nil || got != "" {
		t.Fatalf("AppClientID(simkl) = %q, %v; want empty for an unregistered key", got, err)
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
				ProviderKey:    "trakt",
				CapabilityID:   "trakt",
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
			status, err := NewService(newServiceFakeRepo(), registry).GetConnectionStatus(t.Context(), 7, "profile-1", "trakt")
			if err != nil {
				t.Fatal(err)
			}
			if status.CredentialsConfigured != tc.want {
				t.Fatalf("credentials_configured = %t, want %t", status.CredentialsConfigured, tc.want)
			}
		})
	}
}
