package handlers

import (
	"context"
	"errors"
	"maps"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/config"
)

// These tests inspect only the merged configuration. They never create a client.
func TestMeilisearchCheckCredentialBinding(t *testing.T) {
	const (
		urlKey       = catalog.SearchSettingMeilisearchURL
		keyKey       = catalog.SearchSettingMeilisearchAPIKey
		savedURL     = "https://search.example.test/base"
		storedMarker = "SYNTHETIC_STORED"
		draftMarker  = "SYNTHETIC_DRAFT"
	)
	for _, tc := range []struct {
		name      string
		storedURL string
		values    map[string]string
		dirty     []string
		bootstrap map[string]string
		want      string
	}{
		{name: "unchanged", storedURL: savedURL, want: storedMarker},
		{name: "same authority path", storedURL: savedURL, values: map[string]string{urlKey: "https://SEARCH.example.test/other"}, dirty: []string{urlKey}, want: storedMarker},
		{name: "explicit default port", storedURL: savedURL, values: map[string]string{urlKey: "https://search.example.test:443/other"}, dirty: []string{urlKey}, want: storedMarker},
		{name: "implicit default port", storedURL: "http://search.example.test:80/base", values: map[string]string{urlKey: "http://search.example.test/other"}, dirty: []string{urlKey}, want: storedMarker},
		{name: "changed host", storedURL: savedURL, values: map[string]string{urlKey: "https://other.example.test"}, dirty: []string{urlKey}},
		{name: "changed scheme", storedURL: savedURL, values: map[string]string{urlKey: "http://search.example.test/base"}, dirty: []string{urlKey}},
		{name: "changed port", storedURL: savedURL, values: map[string]string{urlKey: "https://search.example.test:8443/base"}, dirty: []string{urlKey}},
		{name: "changed host replacement", storedURL: savedURL, values: map[string]string{urlKey: "https://other.example.test", keyKey: draftMarker}, dirty: []string{urlKey, keyKey}, want: draftMarker},
		{name: "changed host explicit clear", storedURL: savedURL, values: map[string]string{urlKey: "https://other.example.test", keyKey: ""}, dirty: []string{urlKey, keyKey}},
		{name: "changed host omitted dirty value", storedURL: savedURL, values: map[string]string{urlKey: "https://other.example.test"}, dirty: []string{urlKey, keyKey}},
		{name: "replacement not dirty", storedURL: savedURL, values: map[string]string{urlKey: "https://other.example.test", keyKey: draftMarker}, dirty: []string{urlKey}},
		{name: "url not dirty", storedURL: savedURL, values: map[string]string{urlKey: "https://other.example.test"}, want: storedMarker},
		{name: "same authority explicit clear", storedURL: savedURL, values: map[string]string{keyKey: ""}, dirty: []string{keyKey}},
		{name: "bootstrap withheld", storedURL: savedURL, bootstrap: map[string]string{keyKey: "SYNTHETIC_BOOTSTRAP"}, values: map[string]string{urlKey: "https://other.example.test"}, dirty: []string{urlKey}},
		{name: "bootstrap same authority", storedURL: savedURL, bootstrap: map[string]string{keyKey: "SYNTHETIC_BOOTSTRAP"}, want: "SYNTHETIC_BOOTSTRAP"},
		{name: "bootstrap replacement", storedURL: savedURL, bootstrap: map[string]string{keyKey: "SYNTHETIC_BOOTSTRAP"}, values: map[string]string{urlKey: "https://other.example.test", keyKey: draftMarker}, dirty: []string{urlKey, keyKey}, want: draftMarker},
		{name: "no saved destination", values: map[string]string{urlKey: "https://search.example.test"}, dirty: []string{urlKey}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stored := map[string]string{urlKey: tc.storedURL, keyKey: storedMarker}
			store := &fakeServerSettingsStore{values: maps.Clone(stored)}
			bootstrap := maps.Clone(tc.bootstrap)
			h := &AdminHandler{SettingsRepo: store, BootstrapSensitiveValues: bootstrap}
			got, err := h.effectiveSettingsForConnectionCheck(t.Context(), "meilisearch", adminSettingsConnectionCheckRequest{Values: tc.values, DirtyKeys: tc.dirty})
			if err != nil {
				t.Fatal(err)
			}
			if got[keyKey] != tc.want {
				t.Fatalf("merged credential = %q, want %q", got[keyKey], tc.want)
			}
			if !maps.Equal(store.values, stored) || !maps.Equal(bootstrap, tc.bootstrap) {
				t.Fatal("connection check modified saved settings")
			}
		})
	}
}

func TestConnectionCheckAuthorityEquivalence(t *testing.T) {
	for _, tc := range []struct {
		name, left, right string
		equal             bool
	}{
		{"case and path", "https://SEARCH.example.test/base", "https://search.example.test/other?query=1", true},
		{"HTTPS default port", "https://search.example.test", "https://search.example.test:443/base", true},
		{"HTTP default port", "http://search.example.test:80", "http://search.example.test/base", true},
		{"numeric default port", "https://search.example.test:00443", "https://search.example.test/base", true},
		{"IPv6 default port", "https://[2001:db8::1]:443/base", "https://[2001:db8::1]/other", true},
		{"other host", "https://search.example.test", "https://other.example.test", false},
		{"other port", "https://search.example.test", "https://search.example.test:8443", false},
		{"other scheme", "https://search.example.test:443", "http://search.example.test:443", false},
		{"trailing dot stays distinct", "https://search.example.test", "https://search.example.test.", false},
		{"malformed stays distinct", "https://search.example.test", "https://search.example.test:bad", false},
		{"empty port is the default", "https://search.example.test:", "https://search.example.test", true},
		{"IDNA twin stays distinct", "https://search.example.test", "https://search.example.t\u0130st", false},
		{"Unicode host equals its punycode", "https://b\u00fccher.example.test", "https://xn--bcher-kva.example.test", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := endpointAuthority(tc.left) == endpointAuthority(tc.right); got != tc.equal {
				t.Fatalf("authority equality = %v, want %v", got, tc.equal)
			}
		})
	}
}

func TestConnectionCheckDefaultPortsRetainSavedCredentials(t *testing.T) {
	for _, tc := range []struct{ kind, urlKey, keyKey string }{
		{"ai_chat", "ai.base_url", "ai.api_key"},
		{"ai_transcription", "ai.asr_base_url", "ai.asr_api_key"},
		{"recommendations_embedding", "recommendations.embedding_base_url", "recommendations.embedding_auth_token"},
	} {
		t.Run(tc.kind, func(t *testing.T) {
			h := &AdminHandler{SettingsRepo: &fakeServerSettingsStore{values: map[string]string{
				tc.urlKey: "https://provider.example.test/base", tc.keyKey: "SYNTHETIC_STORED",
			}}}
			got, err := h.effectiveSettingsForConnectionCheck(t.Context(), tc.kind, adminSettingsConnectionCheckRequest{
				Values: map[string]string{tc.urlKey: "https://PROVIDER.example.test:443/other"}, DirtyKeys: []string{tc.urlKey},
			})
			if err != nil || got[tc.keyKey] != "SYNTHETIC_STORED" {
				t.Fatalf("saved credential withheld for default port: %v", err)
			}
		})
	}
}

func TestEmbeddingCheckLegacyCredentialDraftSemantics(t *testing.T) {
	const baseKey, tokenKey, legacyKey = "recommendations.embedding_base_url", "recommendations.embedding_auth_token", "recommendations.openai_api_key"
	for _, tc := range []struct {
		name, replacementKey, replacement, want string
		dirty                                   bool
	}{
		{name: "withhold both saved keys"},
		{name: "replace primary", replacementKey: tokenKey, replacement: "SYNTHETIC_PRIMARY", dirty: true, want: "SYNTHETIC_PRIMARY"},
		{name: "replace legacy", replacementKey: legacyKey, replacement: "SYNTHETIC_LEGACY", dirty: true, want: "SYNTHETIC_LEGACY"},
		{name: "clear primary withholds saved fallback", replacementKey: tokenKey, dirty: true},
		{name: "clear fallback withholds saved primary", replacementKey: legacyKey, dirty: true},
		{name: "non-dirty replacement ignored", replacementKey: tokenKey, replacement: "SYNTHETIC_IGNORED"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := &AdminHandler{SettingsRepo: &fakeServerSettingsStore{values: map[string]string{
				baseKey: "https://provider.example.test/base", tokenKey: "SYNTHETIC_STORED_PRIMARY", legacyKey: "SYNTHETIC_STORED_LEGACY",
			}}}
			values := map[string]string{baseKey: "https://other.example.test/base"}
			dirty := []string{baseKey}
			if tc.replacementKey != "" {
				values[tc.replacementKey] = tc.replacement
				if tc.dirty {
					dirty = append(dirty, tc.replacementKey)
				}
			}
			got, err := h.effectiveSettingsForConnectionCheck(t.Context(), "recommendations_embedding", adminSettingsConnectionCheckRequest{Values: values, DirtyKeys: dirty})
			if err != nil {
				t.Fatal(err)
			}
			cfg, err := config.LoadFromDB(got)
			if err != nil || cfg.Recommendations.EmbeddingAuthToken != tc.want {
				t.Fatalf("fallback credential mismatch: %v", err)
			}
		})
	}
}

func TestMeilisearchCheckBridgesUseOnlyBoundDraftCredentials(t *testing.T) {
	originalCheck := checkAdminMeilisearchSettingsConnection
	t.Cleanup(func() { checkAdminMeilisearchSettingsConnection = originalCheck })
	for _, tc := range []struct {
		name    string
		draft   map[string]string
		wantKey string
	}{
		{name: "changed authority", draft: map[string]string{catalog.SearchSettingMeilisearchURL: "https://other.example.test/base"}},
		{name: "same authority path", draft: map[string]string{catalog.SearchSettingMeilisearchURL: "https://SEARCH.example.test:443/other"}, wantKey: "SYNTHETIC_STORED"},
		{name: "explicit replacement", draft: map[string]string{catalog.SearchSettingMeilisearchURL: "https://other.example.test/base", catalog.SearchSettingMeilisearchAPIKey: "SYNTHETIC_DRAFT"}, wantKey: "SYNTHETIC_DRAFT"},
		{name: "explicit clear", draft: map[string]string{catalog.SearchSettingMeilisearchURL: "https://other.example.test/base", catalog.SearchSettingMeilisearchAPIKey: ""}},
		{name: "no authentication", draft: map[string]string{catalog.SearchSettingMeilisearchAPIKey: ""}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := &AdminHandler{SettingsRepo: &fakeServerSettingsStore{values: map[string]string{
				catalog.SearchSettingMeilisearchURL:       "https://search.example.test/base",
				catalog.SearchSettingMeilisearchAPIKey:    "SYNTHETIC_STORED",
				catalog.SearchSettingMeilisearchTimeoutMS: "1234",
			}}}
			calls := 0
			checkAdminMeilisearchSettingsConnection = func(ctx context.Context, cfg catalog.CatalogSearchSettings) error {
				calls++
				if cfg.MeilisearchAPIKey != tc.wantKey || cfg.Timeout != 1234*time.Millisecond {
					t.Fatal("wrong credential or changed deadline")
				}
				if wantURL, changed := tc.draft[catalog.SearchSettingMeilisearchURL]; changed && cfg.MeilisearchURL != wantURL {
					t.Fatal("draft URL was changed")
				}
				return ctx.Err()
			}
			dirty := slices.Sorted(maps.Keys(tc.draft))
			result, err := h.CheckAdminSettingsConnection(t.Context(), "meilisearch", tc.draft, dirty)
			if err != nil || !result.Success {
				t.Fatalf("native service check failed: %v", err)
			}
			rec := performSettingsCheckRequest(t, h, "/admin/settings/check/meilisearch", map[string]any{"values": tc.draft, "dirty_keys": dirty})
			if rec.Code != http.StatusOK || calls != 2 {
				t.Fatalf("v1 status = %d, combined calls = %d", rec.Code, calls)
			}
		})
	}
	checkAdminMeilisearchSettingsConnection = func(context.Context, catalog.CatalogSearchSettings) error {
		return catalog.ErrMeilisearchCheckRedirected
	}
	redirected := &AdminHandler{SettingsRepo: &fakeServerSettingsStore{values: map[string]string{catalog.SearchSettingMeilisearchURL: "http://search.example.test"}}}
	if result, err := redirected.CheckAdminSettingsConnection(t.Context(), "meilisearch", nil, nil); err != nil || result.Success || result.Message != adminMeilisearchRedirectMessage {
		t.Fatalf("redirected check result = %+v, %v", result, err)
	}
	checkAdminMeilisearchSettingsConnection = func(context.Context, catalog.CatalogSearchSettings) error {
		return errors.New("SYNTHETIC_PROVIDER_DETAIL")
	}
	h := &AdminHandler{SettingsRepo: &fakeServerSettingsStore{values: map[string]string{catalog.SearchSettingMeilisearchURL: "https://search.example.test"}}}
	result, err := h.CheckAdminSettingsConnection(t.Context(), "meilisearch", nil, nil)
	if err != nil || result.Success || strings.Contains(result.Message, "SYNTHETIC_PROVIDER_DETAIL") {
		t.Fatal("native service exposed provider detail")
	}
}
