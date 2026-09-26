package metadata

import (
	"slices"
	"testing"
)

// Capability metadata as stored in plugin_capabilities.metadata: plugin-declared
// fields are wrapped in a "metadata" envelope.
const (
	audiobookCapMetadata = `{"metadata":{"default_priority":{"audiobook":2}},"display_name":"Audiobook Metadata"}`
	tmdbCapMetadata      = `{"metadata":{"default_priority":{"movie":2,"season":3,"series":3,"episode":3}},"display_name":"TMDB"}`
	tvdbCapMetadata      = `{"metadata":{"default_priority":{"season":2,"series":2,"episode":2}},"display_name":"TVDB"}`
)

func TestProviderSupportsLevel(t *testing.T) {
	cases := []struct {
		name         string
		metadataJSON string
		contentLevel string
		want         bool
	}{
		// An audiobook-only provider must NOT be pulled into video content
		// levels by the chain-less global fallback. This is the regression
		// that let silo.audiobook-metadata hammer audiobook APIs with
		// anime/movie/series titles.
		{"audiobook provider excluded from episode", audiobookCapMetadata, "episode", false},
		{"audiobook provider excluded from series", audiobookCapMetadata, "series", false},
		{"audiobook provider excluded from season", audiobookCapMetadata, "season", false},
		{"audiobook provider excluded from movie", audiobookCapMetadata, "movie", false},
		{"audiobook provider included for audiobook", audiobookCapMetadata, "audiobook", true},

		// Providers that declare a level stay eligible for it.
		{"tmdb included for movie", tmdbCapMetadata, "movie", true},
		{"tmdb included for season", tmdbCapMetadata, "season", true},
		{"tmdb included for episode", tmdbCapMetadata, "episode", true},

		// tvdb declares no movie level -> excluded from the movie fallback.
		{"tvdb excluded from movie", tvdbCapMetadata, "movie", false},
		{"tvdb included for series", tvdbCapMetadata, "series", true},

		// A provider that declares no default_priority makes no claim and stays
		// eligible everywhere (legacy behavior), ranked last by priority 0.
		{"no metadata is eligible", ``, "episode", true},
		{"empty object is eligible", `{}`, "episode", true},
		{"empty priority map is eligible", `{"metadata":{"default_priority":{}}}`, "episode", true},
		{"malformed json is eligible (fail-open)", `{not json`, "episode", true},

		// A declared level with non-positive priority is not supported.
		{"zero priority level not supported", `{"metadata":{"default_priority":{"episode":0}}}`, "episode", false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := providerSupportsLevel([]byte(tc.metadataJSON), tc.contentLevel)
			if got != tc.want {
				t.Fatalf("providerSupportsLevel(%q, %q) = %v, want %v",
					tc.metadataJSON, tc.contentLevel, got, tc.want)
			}
		})
	}
}

func TestExtractDefaultEnabled(t *testing.T) {
	cases := []struct {
		name         string
		metadataJSON string
		want         bool
	}{
		// Absent flag defaults to true so every existing plugin is seeded
		// enabled exactly as before this flag existed.
		{"no metadata defaults enabled", ``, true},
		{"empty object defaults enabled", `{}`, true},
		{"tmdb without flag defaults enabled", tmdbCapMetadata, true},
		{"malformed json defaults enabled (fail-open)", `{not json`, true},

		// A specialist provider opts out of being auto-enabled. The flag lives
		// in the same "metadata" envelope as default_priority.
		{"opt-out inside envelope", `{"metadata":{"default_priority":{"series":50},"default_enabled":false}}`, false},
		{"opt-out at top level", `{"default_enabled":false}`, false},

		// An explicit true is honored (and is the default anyway).
		{"explicit true inside envelope", `{"metadata":{"default_enabled":true}}`, true},

		// A non-boolean value is ignored, falling back to the enabled default.
		{"non-boolean value defaults enabled", `{"metadata":{"default_enabled":"nope"}}`, true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := extractDefaultEnabled([]byte(tc.metadataJSON))
			if got != tc.want {
				t.Fatalf("extractDefaultEnabled(%q) = %v, want %v",
					tc.metadataJSON, got, tc.want)
			}
		})
	}
}

func TestExtractLookupProviderIDs(t *testing.T) {
	cases := []struct {
		name         string
		metadataJSON string
		want         []string
	}{
		{"inside envelope", `{"metadata":{"lookup_provider_ids":["imdb","tmdb"]}}`, []string{"imdb", "tmdb"}},
		{"top level", `{"lookup_provider_ids":["tmdb"]}`, []string{"tmdb"}},
		{"top level wins over envelope", `{"lookup_provider_ids":["tvdb"],"metadata":{"lookup_provider_ids":["imdb"]}}`, []string{"tvdb"}},
		{"normalized and deduplicated", `{"metadata":{"lookup_provider_ids":[" IMDb ","imdb","","TMDB"]}}`, []string{"imdb", "tmdb"}},

		// Anything that is not a list of strings declares nothing, so the
		// provider stays gated on its own ID.
		{"absent", tmdbCapMetadata, nil},
		{"no metadata", ``, nil},
		{"malformed json", `{not json`, nil},
		{"comma string is not a list", `{"metadata":{"lookup_provider_ids":"imdb,tmdb"}}`, nil},
		{"mixed types", `{"metadata":{"lookup_provider_ids":["imdb",7]}}`, nil},
		{"only blanks", `{"metadata":{"lookup_provider_ids":[" ",""]}}`, nil},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := extractLookupProviderIDs([]byte(tc.metadataJSON))
			if !slices.Equal(got, tc.want) {
				t.Fatalf("extractLookupProviderIDs(%q) = %q, want %q", tc.metadataJSON, got, tc.want)
			}
		})
	}
}

func TestExtractBulkLookupLimit(t *testing.T) {
	cases := []struct {
		name         string
		metadataJSON string
		want         int
	}{
		{"inside envelope", `{"metadata":{"bulk_lookup_limit":100}}`, 100},
		{"top level", `{"bulk_lookup_limit":10}`, 10},
		{"capped", `{"metadata":{"bulk_lookup_limit":100000}}`, maxBulkLookupLimit},

		// Anything but a positive integer leaves the provider out of the pass.
		{"absent", tmdbCapMetadata, 0},
		{"zero", `{"metadata":{"bulk_lookup_limit":0}}`, 0},
		{"negative", `{"metadata":{"bulk_lookup_limit":-5}}`, 0},
		{"fractional", `{"metadata":{"bulk_lookup_limit":2.5}}`, 0},
		{"string", `{"metadata":{"bulk_lookup_limit":"100"}}`, 0},
		{"malformed json", `{not json`, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := extractBulkLookupLimit([]byte(tc.metadataJSON)); got != tc.want {
				t.Fatalf("extractBulkLookupLimit(%q) = %d, want %d", tc.metadataJSON, got, tc.want)
			}
		})
	}
}
