package watchsync

import (
	"context"
	"testing"
)

func TestPluginProviderKey(t *testing.T) {
	for _, tc := range []struct {
		name         string
		pluginID     string
		capabilityID string
		siloManaged  bool
		want         string
	}{
		{name: "first-party Simkl keeps the built-in key", pluginID: "silo.watchprovider.simkl", capabilityID: "simkl", siloManaged: true, want: "simkl"},
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

type fakeConfigSeeder struct {
	installationID int
	key            string
	value          map[string]any
}

func (f *fakeConfigSeeder) SeedGlobalConfig(_ context.Context, installationID int, key string, value map[string]any) (bool, error) {
	f.installationID, f.key, f.value = installationID, key, value
	return true, nil
}
