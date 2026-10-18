package watchsync

import (
	"context"
	"errors"
	"maps"
	"testing"

	pluginv1 "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginproto/silo/plugin/v1"
	"google.golang.org/protobuf/types/known/structpb"
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

func TestValidateConnectionSettingsUpdate(t *testing.T) {
	for _, tc := range []struct {
		name    string
		values  map[string]any
		wantErr bool
	}{
		{name: "declared boolean", values: map[string]any{"track_rewatches": true}},
		{name: "undeclared key", values: map[string]any{"other": true}, wantErr: true},
		{name: "string for a boolean", values: map[string]any{"track_rewatches": "true"}, wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := validateConnectionSettingsUpdate(testConnectionSettings, tc.values)
			if _, invalid := errors.AsType[InvalidConnectionSettingError](err); invalid != tc.wantErr || (err != nil) != tc.wantErr {
				t.Fatalf("error = %v, wantErr %t", err, tc.wantErr)
			}
		})
	}
	if patch := string(encodeConnectionSettingsPatch(map[string]any{"track_rewatches": true})); patch != `{"track_rewatches":"true"}` {
		t.Fatalf("patch = %s", patch)
	}
}

func TestPluginProviderSendsConnectionSettingsOnEveryCall(t *testing.T) {
	client := &fakeWatchSyncPluginClient{}
	provider := testPluginProviderWithDescriptor(t, client, &pluginv1.WatchSyncProviderDescriptor{
		AuthMethods:   []pluginv1.WatchSyncAuthMethod{pluginv1.WatchSyncAuthMethod_WATCH_SYNC_AUTH_METHOD_DEVICE_CODE},
		ExportWatched: true,
		MaxBatchSize:  25,
		ConnectionSettings: []*pluginv1.WatchSyncConnectionSetting{
			{Key: "track_rewatches", Label: "Log rewatches", Description: "Send repeat plays as rewatches.",
				Type: pluginv1.WatchSyncConnectionSettingType_WATCH_SYNC_CONNECTION_SETTING_TYPE_BOOLEAN},
			{Key: "on_by_default", Label: "On by default", DefaultValue: structpb.NewBoolValue(true),
				Type: pluginv1.WatchSyncConnectionSettingType_WATCH_SYNC_CONNECTION_SETTING_TYPE_BOOLEAN},
			// A type this server does not know is not shown or sent.
			{Key: "future", Label: "Future", Type: pluginv1.WatchSyncConnectionSettingType(99)},
		},
	})
	declared := provider.ConnectionSettings()
	if len(declared) != 2 || declared[0].Key != "track_rewatches" || declared[0].Label != "Log rewatches" ||
		declared[0].Description != "Send repeat plays as rewatches." || declared[0].Default != false ||
		declared[1].Default != true {
		t.Fatalf("declared settings = %#v", declared)
	}
	conn := Connection{AccessToken: "token", ConnectionSettings: map[string]string{"track_rewatches": "true"}}
	if _, err := provider.LookupAccount(context.Background(), ServerConfig{}, conn); err != nil {
		t.Fatal(err)
	}
	got := client.accountRequest.GetContext().GetConnectionSettings()
	if want := map[string]string{"track_rewatches": "true", "on_by_default": "true"}; !maps.Equal(got, want) {
		t.Fatalf("connection settings = %v, want %v", got, want)
	}
}

func TestPluginProviderWithoutConnectionSettingsSendsNone(t *testing.T) {
	client := &fakeWatchSyncPluginClient{}
	provider := testPluginProvider(t, client)
	if _, err := provider.LookupAccount(context.Background(), ServerConfig{}, Connection{AccessToken: "token"}); err != nil {
		t.Fatal(err)
	}
	if got := client.accountRequest.GetContext().GetConnectionSettings(); len(got) != 0 {
		t.Fatalf("connection settings = %v, want none", got)
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
	reg := NewRegistry()
	if err := reg.Register(provider); err != nil {
		t.Fatal(err)
	}
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
