package settingsresolve

import (
	"context"
	"errors"
	"testing"

	"github.com/Silo-Server/silo-server/internal/settingscontract"
	"github.com/Silo-Server/silo-server/internal/settingskeys"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

// fakeServerSettings replays fixed server settings and records each read, so a
// test can assert that keys without admin_default never cost one.
type fakeServerSettings struct {
	values map[string]string
	err    error
	reads  []string
}

func (f *fakeServerSettings) Get(_ context.Context, key string) (string, error) {
	f.reads = append(f.reads, key)
	if f.err != nil {
		return "", f.err
	}
	return f.values[key], nil
}

func titleArtAdminDefault(value string) *fakeServerSettings {
	return &fakeServerSettings{values: map[string]string{
		settingscontract.AdminDefaultSettingKey(settingskeys.UiTitleArt): value,
	}}
}

func resolveTitleArt(t *testing.T, settings ServerSettings, rows []userstore.SettingValue) Effective {
	t.Helper()
	got, err := New(mustContract(t)).WithAdminDefaults(settings).Resolve(context.Background(),
		&fakeStore{rows: rows}, Context{ProfileID: "p1", DeviceID: "phone"},
		[]string{settingskeys.UiTitleArt}, nil)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("Resolve returned %d results, want 1", len(got))
	}
	return got[0]
}

func TestAdminDefaultIsInheritedWithoutAStoredChoice(t *testing.T) {
	got := resolveTitleArt(t, titleArtAdminDefault("false"), nil)
	if string(got.Value) != "false" || got.Source != settingscontract.ScopeDefault {
		t.Fatalf("got %s from %q, want the administrator's false from default", got.Value, got.Source)
	}
	if got.Identity != nil {
		t.Fatalf("an inherited value has no stored row to reset, got identity %+v", got.Identity)
	}
}

func TestStoredChoicesOverrideTheAdminDefault(t *testing.T) {
	device := userstore.SettingIdentity{ProfileID: "p1", DeviceID: "phone"}
	profile := userstore.SettingIdentity{ProfileID: "p1"}
	cases := map[string]struct {
		admin      string
		rows       []userstore.SettingValue
		wantValue  string
		wantSource settingscontract.Scope
	}{
		"device true over admin false": {
			admin:      "false",
			rows:       []userstore.SettingValue{row(settingskeys.UiTitleArt, settingscontract.ScopeProfileDevice, "true", device)},
			wantValue:  "true",
			wantSource: settingscontract.ScopeProfileDevice,
		},
		"device false over admin true": {
			admin:      "true",
			rows:       []userstore.SettingValue{row(settingskeys.UiTitleArt, settingscontract.ScopeProfileDevice, "false", device)},
			wantValue:  "false",
			wantSource: settingscontract.ScopeProfileDevice,
		},
		"profile false over admin true": {
			admin:      "true",
			rows:       []userstore.SettingValue{row(settingskeys.UiTitleArt, settingscontract.ScopeProfile, "false", profile)},
			wantValue:  "false",
			wantSource: settingscontract.ScopeProfile,
		},
		"apply-to-all profile value still beats the device value": {
			admin: "false",
			rows: []userstore.SettingValue{
				row(settingskeys.UiTitleArt, settingscontract.ScopeProfileDevice, "false", device),
				row(settingskeys.UiTitleArt, settingscontract.ScopeProfile, "true", profile),
			},
			wantValue:  "true",
			wantSource: settingscontract.ScopeProfile,
		},
		"another device's value is not this device's choice": {
			admin: "false",
			rows: []userstore.SettingValue{row(settingskeys.UiTitleArt, settingscontract.ScopeProfileDevice, "true",
				userstore.SettingIdentity{ProfileID: "p1", DeviceID: "living-room"})},
			wantValue:  "false",
			wantSource: settingscontract.ScopeDefault,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got := resolveTitleArt(t, titleArtAdminDefault(tc.admin), tc.rows)
			if string(got.Value) != tc.wantValue || got.Source != tc.wantSource {
				t.Fatalf("got %s from %q, want %s from %q", got.Value, got.Source, tc.wantValue, tc.wantSource)
			}
		})
	}
}

func TestContractDefaultAppliesWithoutAUsableAdminDefault(t *testing.T) {
	cases := map[string]ServerSettings{
		"no reader":                nil,
		"no admin value":           &fakeServerSettings{},
		"empty admin value":        titleArtAdminDefault(""),
		"value the schema rejects": titleArtAdminDefault(`"off"`),
		"failed read":              &fakeServerSettings{err: errors.New("database unavailable")},
	}
	for name, settings := range cases {
		t.Run(name, func(t *testing.T) {
			got := resolveTitleArt(t, settings, nil)
			if string(got.Value) != "true" || got.Source != settingscontract.ScopeDefault {
				t.Fatalf("got %s from %q, want the contract default true", got.Value, got.Source)
			}
		})
	}
}

func TestOnlyAdminDefaultKeysReadServerSettings(t *testing.T) {
	settings := titleArtAdminDefault("false")
	resolver := New(mustContract(t)).WithAdminDefaults(settings)

	if _, err := resolver.Resolve(context.Background(), &fakeStore{}, Context{ProfileID: "p1"},
		[]string{settingskeys.UiTheme}, nil); err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if len(settings.reads) != 0 {
		t.Fatalf("a key without admin_default read server settings: %v", settings.reads)
	}

	if _, err := resolver.Resolve(context.Background(), &fakeStore{}, Context{ProfileID: "p1", DeviceID: "phone"},
		[]string{settingskeys.UiTheme, settingskeys.UiTitleArt}, nil); err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if len(settings.reads) != 1 || settings.reads[0] != settingscontract.AdminDefaultSettingKey(settingskeys.UiTitleArt) {
		t.Fatalf("reads = %v, want one read of the title-art default", settings.reads)
	}
}

func TestBatchedResolutionsShareTheAdminDefault(t *testing.T) {
	resolver := New(mustContract(t)).WithAdminDefaults(titleArtAdminDefault("false"))
	store := &fakeStore{rows: []userstore.SettingValue{
		row(settingskeys.UiTitleArt, settingscontract.ScopeProfile, "true", userstore.SettingIdentity{ProfileID: "p2"}),
	}}

	profiles, err := resolver.ResolveProfiles(context.Background(), store, []string{"p1", "p2"},
		[]string{settingskeys.UiTitleArt}, nil)
	if err != nil {
		t.Fatalf("ResolveProfiles: %v", err)
	}
	if got := profiles["p1"][0]; string(got.Value) != "false" || got.Source != settingscontract.ScopeDefault {
		t.Fatalf("p1 got %s from %q, want the administrator's false", got.Value, got.Source)
	}
	if got := profiles["p2"][0]; string(got.Value) != "true" || got.Source != settingscontract.ScopeProfile {
		t.Fatalf("p2 got %s from %q, want its own true", got.Value, got.Source)
	}

	contexts, err := resolver.ResolveContexts(context.Background(), store, []Context{
		{ProfileID: "p1", DeviceID: "phone", LibraryIDs: []int{1}},
		{ProfileID: "p1", DeviceID: "phone", LibraryIDs: []int{2}},
	}, []string{settingskeys.UiTitleArt}, nil)
	if err != nil {
		t.Fatalf("ResolveContexts: %v", err)
	}
	for i, values := range contexts {
		if got := values[0]; string(got.Value) != "false" || got.Source != settingscontract.ScopeDefault {
			t.Fatalf("context %d got %s from %q, want the administrator's false", i, got.Value, got.Source)
		}
	}
}
