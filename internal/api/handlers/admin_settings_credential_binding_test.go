package handlers

import (
	"maps"
	"testing"

	"github.com/Silo-Server/silo-server/internal/catalog"
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
