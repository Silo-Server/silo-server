package access

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/Silo-Server/silo-server/internal/settingscontract"
	"github.com/Silo-Server/silo-server/internal/settingskeys"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

// settingStoreStub only answers the one method resolution reaches; the
// embedded nil interface panics on anything else, which is the point — this
// path must not touch the rest of the store.
type settingStoreStub struct {
	userstore.UserStore
	rows []userstore.SettingValue
	err  error
}

func (s settingStoreStub) ListSettingValuesForResolution(
	context.Context, userstore.SettingResolutionQuery,
) ([]userstore.SettingValue, error) {
	return s.rows, s.err
}

func TestResolveViewerPreferencesIncludesMetadataLanguageOverrides(t *testing.T) {
	store := settingStoreStub{rows: []userstore.SettingValue{
		{
			SettingIdentity: userstore.SettingIdentity{
				Key: settingskeys.UiDisabledLibraryIds, Scope: settingscontract.ScopeProfile,
				ProfileID: "profile-1",
			},
			Value: json.RawMessage(`[]`),
		},
		{
			SettingIdentity: userstore.SettingIdentity{
				Key: settingskeys.CatalogMetadataLanguage, Scope: settingscontract.ScopeProfile,
				ProfileID: "profile-1",
			},
			Value: json.RawMessage(`"en"`),
		},
		{
			SettingIdentity: userstore.SettingIdentity{
				Key: settingskeys.CatalogMetadataLanguageOverrides, Scope: settingscontract.ScopeProfile,
				ProfileID: "profile-1",
			},
			Value: json.RawMessage(`{"nor":"x-silo-original","JA":"de"}`),
		},
	}}

	preferences := ResolveViewerPreferences(context.Background(), store, nil, "profile-1")
	if preferences.PreferredMetadataLanguage != "en" {
		t.Fatalf("fallback language = %q, want en", preferences.PreferredMetadataLanguage)
	}
	want := map[string]string{"no": OriginalMetadataLanguage, "ja": "de"}
	if len(preferences.MetadataLanguageOverrides) != len(want) {
		t.Fatalf("metadata overrides = %#v, want %#v", preferences.MetadataLanguageOverrides, want)
	}
	for source, target := range want {
		if preferences.MetadataLanguageOverrides[source] != target {
			t.Errorf("metadata override %q = %q, want %q", source, preferences.MetadataLanguageOverrides[source], target)
		}
	}
}
