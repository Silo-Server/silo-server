package handlers

import (
	"net/url"
	"testing"
)

func TestBuildLegacySearchCatalogValuesAcceptsEpisode(t *testing.T) {
	values := url.Values{
		"q":      {"Who Are You?"},
		"type":   {"episode"},
		"limit":  {"25"},
		"offset": {"50"},
	}

	translated, ok := buildLegacySearchCatalogValues(values)
	if !ok {
		t.Fatal("episode search was not routed through the catalog resolver")
	}
	for key, want := range map[string]string{
		"source": "query",
		"q":      "Who Are You?",
		"type":   "episode",
		"sort":   "relevance",
		"order":  "desc",
		"limit":  "25",
		"offset": "50",
	} {
		if got := translated.Get(key); got != want {
			t.Fatalf("translated %s = %q, want %q", key, got, want)
		}
	}
}
