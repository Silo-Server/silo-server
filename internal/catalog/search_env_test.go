package catalog

import (
	"context"
	"maps"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/secret"
)

func TestSearchSettingsFromEnv(t *testing.T) {
	cases := []struct {
		name    string
		env     map[string]string
		want    map[string]string
		wantErr string
	}{
		{name: "unset and blank variables set nothing", env: map[string]string{EnvMeilisearchURL: "  "}, want: map[string]string{}},
		{
			name: "values are normalized like an admin save",
			env: map[string]string{
				EnvSearchProvider:    " Meilisearch ",
				EnvMeilisearchURL:    "http://meilisearch:7700/",
				EnvMeilisearchAPIKey: " master-key ",
			},
			want: map[string]string{
				SearchSettingProvider:          "meilisearch",
				SearchSettingMeilisearchURL:    "http://meilisearch:7700",
				SearchSettingMeilisearchAPIKey: "master-key",
			},
		},
		{name: "unknown provider", env: map[string]string{EnvSearchProvider: "elasticsearch"}, wantErr: EnvSearchProvider},
		{name: "URL without a host", env: map[string]string{EnvMeilisearchURL: "meilisearch:7700"}, wantErr: EnvMeilisearchURL},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := SearchSettingsFromEnv(func(name string) string { return tc.env[name] })
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want one naming %s", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("SearchSettingsFromEnv: %v", err)
			}
			if !maps.Equal(got, tc.want) {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
		})
	}
}

func TestStoreSearchSettingsEncryptsTheKeyAndLeavesUnsetKeysAlone(t *testing.T) {
	ctx := context.Background()
	raw := newMemSettings()
	store := NewEncryptedSettingsRepo(raw, newCipher(t))
	raw.m[SearchSettingProvider] = SearchProviderPostgres
	raw.m[SearchSettingMeilisearchURL] = "http://saved:7700"
	stored := func() map[string]string {
		t.Helper()
		all, err := store.GetAll(ctx)
		if err != nil {
			t.Fatalf("GetAll: %v", err)
		}
		return all
	}

	values := map[string]string{
		SearchSettingProvider:          SearchProviderMeilisearch,
		SearchSettingMeilisearchAPIKey: "master-key",
	}
	if err := StoreSearchSettings(ctx, store, stored(), values); err != nil {
		t.Fatalf("StoreSearchSettings: %v", err)
	}
	if got := raw.m[SearchSettingProvider]; got != SearchProviderMeilisearch {
		t.Fatalf("provider = %q, want %q", got, SearchProviderMeilisearch)
	}
	if got := raw.m[SearchSettingMeilisearchURL]; got != "http://saved:7700" {
		t.Fatalf("URL without a variable = %q, want the saved value", got)
	}
	ciphertext := raw.m[SearchSettingMeilisearchAPIKey]
	if !secret.IsEncrypted(ciphertext) {
		t.Fatalf("stored API key = %q, want ciphertext", ciphertext)
	}
	if got, err := store.Get(ctx, SearchSettingMeilisearchAPIKey); err != nil || got != "master-key" {
		t.Fatalf("API key reads back %q, %v", got, err)
	}

	// The next start with the same environment rewrites nothing: a fresh
	// encryption would change the ciphertext.
	if err := StoreSearchSettings(ctx, store, stored(), values); err != nil {
		t.Fatalf("second StoreSearchSettings: %v", err)
	}
	if got := raw.m[SearchSettingMeilisearchAPIKey]; got != ciphertext {
		t.Fatal("an unchanged API key was written again")
	}
}
