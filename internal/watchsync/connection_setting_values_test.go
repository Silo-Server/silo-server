package watchsync

import (
	"context"
	"errors"
	"maps"
	"testing"

	pluginv1 "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginproto/silo/plugin/v1"
)

var testConnectionSettings = []ConnectionSetting{
	{Key: "track_rewatches", Label: "Log rewatches", Type: ConnectionSettingTypeBoolean, Default: false},
	{Key: "on_by_default", Label: "On by default", Type: ConnectionSettingTypeBoolean, Default: true},
}

func TestConnectionSettingValuesApplyDefaults(t *testing.T) {
	for _, tc := range []struct {
		name   string
		stored map[string]string
		want   map[string]any
	}{
		{name: "nothing stored", stored: nil, want: map[string]any{"track_rewatches": false, "on_by_default": true}},
		{name: "changed settings", stored: map[string]string{"track_rewatches": "true", "on_by_default": "false"},
			want: map[string]any{"track_rewatches": true, "on_by_default": false}},
		{name: "undeclared and unreadable values", stored: map[string]string{"removed": "true", "on_by_default": "maybe"},
			want: map[string]any{"track_rewatches": false, "on_by_default": true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := connectionSettingValues(testConnectionSettings, tc.stored); !maps.Equal(got, tc.want) {
				t.Fatalf("values = %v, want %v", got, tc.want)
			}
		})
	}
	encoded := encodedConnectionSettings(testConnectionSettings, map[string]string{"track_rewatches": "true"})
	if want := map[string]string{"track_rewatches": "true", "on_by_default": "true"}; !maps.Equal(encoded, want) {
		t.Fatalf("encoded = %v, want %v", encoded, want)
	}
}

func TestServiceStoresAndValidatesConnectionSettings(t *testing.T) {
	provider := testPluginProviderWithDescriptor(t, &fakeWatchSyncPluginClient{}, &pluginv1.WatchSyncProviderDescriptor{
		AuthMethods:   []pluginv1.WatchSyncAuthMethod{pluginv1.WatchSyncAuthMethod_WATCH_SYNC_AUTH_METHOD_DEVICE_CODE},
		ExportWatched: true,
		MaxBatchSize:  25,
		ConnectionSettings: []*pluginv1.WatchSyncConnectionSetting{{
			Key: "track_rewatches", Label: "Log rewatches",
			Type: pluginv1.WatchSyncConnectionSettingType_WATCH_SYNC_CONNECTION_SETTING_TYPE_BOOLEAN,
		}},
	})
	reg := registryWith(t, provider)
	repo := newServiceFakeRepo()
	if _, err := repo.UpsertConnection(context.Background(), Connection{
		Provider: testPluginProviderKey, UserID: 7, ProfileID: "profile-1", AccessToken: "token",
	}); err != nil {
		t.Fatal(err)
	}
	service := NewService(repo, reg)
	ctx := context.Background()

	status, err := service.GetConnectionStatus(ctx, 7, "profile-1", testPluginProviderKey)
	if err != nil {
		t.Fatal(err)
	}
	if len(status.ConnectionSettings) != 1 || status.ConnectionSettingValues["track_rewatches"] != false {
		t.Fatalf("status settings = %#v values = %v, want the default", status.ConnectionSettings, status.ConnectionSettingValues)
	}
	if summaries := service.ListProviders(); len(summaries) != 1 || len(summaries[0].ConnectionSettings) != 1 {
		t.Fatalf("provider summaries = %#v", summaries)
	}

	status, err = service.UpdateConnection(ctx, 7, "profile-1", testPluginProviderKey, ConnectionUpdate{
		ConnectionSettings: map[string]any{"track_rewatches": true},
	})
	if err != nil {
		t.Fatal(err)
	}
	if status.ConnectionSettingValues["track_rewatches"] != true {
		t.Fatalf("values after update = %v", status.ConnectionSettingValues)
	}

	for name, values := range map[string]map[string]any{
		"undeclared key":   {"other": true},
		"wrong value type": {"track_rewatches": "yes"},
	} {
		_, err := service.UpdateConnection(ctx, 7, "profile-1", testPluginProviderKey, ConnectionUpdate{ConnectionSettings: values})
		if _, ok := errors.AsType[InvalidConnectionSettingError](err); !ok {
			t.Fatalf("%s: error = %v, want InvalidConnectionSettingError", name, err)
		}
	}
	conn, _, _ := repo.GetConnection(ctx, testPluginProviderKey, 7, "profile-1")
	if want := map[string]string{"track_rewatches": "true"}; !maps.Equal(conn.ConnectionSettings, want) {
		t.Fatalf("stored settings = %v, want %v", conn.ConnectionSettings, want)
	}
}
