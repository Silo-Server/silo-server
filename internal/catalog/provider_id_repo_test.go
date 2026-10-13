package catalog

import (
	"testing"
)

func TestNormalizeDurableProviderIDsFiltersEphemeralKeys(t *testing.T) {
	entries := normalizeDurableProviderIDs(map[string]string{
		"tmdb":      "27205",
		"metadb":    "existing-content",
		"_filepath": "/media/movie.mkv",
		"oshash":    "deadbeef",
		"custom":    "provider-1",
	})

	if got, want := len(entries), 2; got != want {
		t.Fatalf("len(entries) = %d, want %d", got, want)
	}
	if entries[0].Provider != "tmdb" || entries[0].ProviderID != "27205" {
		t.Fatalf("first entry = (%q, %q), want tmdb/27205", entries[0].Provider, entries[0].ProviderID)
	}
	if entries[1].Provider != "custom" || entries[1].ProviderID != "provider-1" {
		t.Fatalf("second entry = (%q, %q), want custom/provider-1", entries[1].Provider, entries[1].ProviderID)
	}
}

func TestNormalizeDurableProviderIDsOrdersCanonicalKeysFirst(t *testing.T) {
	entries := normalizeDurableProviderIDs(map[string]string{
		"custom": "c-1",
		"imdb":   "tt0133093",
		"tvdb":   "12345",
		"tmdb":   "603",
	})

	got := []string{entries[0].Provider, entries[1].Provider, entries[2].Provider, entries[3].Provider}
	want := []string{"tmdb", "tvdb", "imdb", "custom"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("provider order = %v, want %v", got, want)
		}
	}
}
