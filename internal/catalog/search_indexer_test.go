package catalog

import (
	"slices"
	"sort"
	"testing"
)

func TestCoalesceSearchIndexEvents(t *testing.T) {
	sortedCopy := func(values []string) []string {
		out := append([]string(nil), values...)
		sort.Strings(out)
		return out
	}
	cases := []struct {
		name        string
		events      []SearchIndexEvent
		wantUpserts []string
		wantDeletes []string
	}{
		{
			name: "later delete wins over earlier upsert",
			events: []SearchIndexEvent{
				{Action: SearchIndexEventUpsert, ContentID: "x"},
				{Action: SearchIndexEventDelete, ContentID: "x"},
			},
			wantDeletes: []string{"x"},
		},
		{
			name: "later upsert resurrects earlier delete",
			events: []SearchIndexEvent{
				{Action: SearchIndexEventDelete, ContentID: "x"},
				{Action: SearchIndexEventUpsert, ContentID: "x"},
			},
			wantUpserts: []string{"x"},
		},
		{
			name: "rename deletes previous id and upserts new id",
			events: []SearchIndexEvent{
				{Action: SearchIndexEventRename, ContentID: "new", PreviousContentID: "old"},
			},
			wantUpserts: []string{"new"},
			wantDeletes: []string{"old"},
		},
		{
			name: "delete after rename removes the renamed id",
			events: []SearchIndexEvent{
				{Action: SearchIndexEventRename, ContentID: "new", PreviousContentID: "old"},
				{Action: SearchIndexEventDelete, ContentID: "new"},
			},
			wantDeletes: []string{"new", "old"},
		},
		{
			name: "upsert after rename resurrects the previous id",
			events: []SearchIndexEvent{
				{Action: SearchIndexEventRename, ContentID: "new", PreviousContentID: "old"},
				{Action: SearchIndexEventUpsert, ContentID: "old"},
			},
			wantUpserts: []string{"new", "old"},
		},
		{
			name: "blank ids are dropped",
			events: []SearchIndexEvent{
				{Action: SearchIndexEventUpsert, ContentID: "  "},
				{Action: SearchIndexEventRename, ContentID: "new", PreviousContentID: ""},
			},
			wantUpserts: []string{"new"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			upserts, deletes := coalesceSearchIndexEvents(tc.events)
			if got, want := sortedCopy(upserts), sortedCopy(tc.wantUpserts); !slices.Equal(got, want) {
				t.Fatalf("upserts = %v, want %v", got, want)
			}
			if got, want := sortedCopy(deletes), sortedCopy(tc.wantDeletes); !slices.Equal(got, want) {
				t.Fatalf("deletes = %v, want %v", got, want)
			}
		})
	}
}

func TestCatalogSearchIndexerFreezesRuntimeSettings(t *testing.T) {
	settings := DefaultCatalogSearchSettings()
	settings.Provider = SearchProviderMeilisearch
	settings.MeilisearchURL = "http://meilisearch-at-startup:7700"
	settings.IndexTypes = []string{"movie"}
	indexer := NewCatalogSearchIndexerFromSettings(nil, nil, settings)

	settings.MeilisearchURL = "http://changed-after-startup:7700"
	settings.IndexTypes[0] = "series"
	got, configured, err := indexer.loadMeilisearchRuntime(t.Context())
	if err != nil {
		t.Fatalf("loadMeilisearchRuntime() error = %v", err)
	}
	if !configured {
		t.Fatal("loadMeilisearchRuntime() configured = false, want true")
	}
	if got.MeilisearchURL != "http://meilisearch-at-startup:7700" {
		t.Fatalf("MeilisearchURL = %q, want startup value", got.MeilisearchURL)
	}
	if len(got.IndexTypes) != 1 || got.IndexTypes[0] != "movie" {
		t.Fatalf("IndexTypes = %#v, want startup copy", got.IndexTypes)
	}
}

func TestCatalogSearchIndexerWaitsForRestartBeforeUsingSavedSchemaSettings(t *testing.T) {
	startup := DefaultCatalogSearchSettings()
	startup.Provider = SearchProviderMeilisearch
	startup.MeilisearchURL = "http://meilisearch:7700"
	store := &memSettings{m: map[string]string{
		SearchSettingProvider:                    SearchProviderMeilisearch,
		SearchSettingMeilisearchURL:              startup.MeilisearchURL,
		SearchSettingMeilisearchSemanticEnabled:  "true",
		SearchSettingMeilisearchRebuildBatchSize: "17",
	}}
	indexer := NewCatalogSearchIndexerFromSettings(nil, store, startup)

	runtime, configured, err := indexer.loadMeilisearchRuntime(t.Context())
	if err != nil {
		t.Fatalf("loadMeilisearchRuntime() error = %v", err)
	}
	if !configured {
		t.Fatal("loadMeilisearchRuntime() configured = false, want true")
	}
	if runtime.SemanticEnabled {
		t.Fatal("saved semantic enablement became active before restart")
	}
	if runtime.RebuildBatchSize != 17 {
		t.Fatalf("RebuildBatchSize = %d, want hot-reloaded 17", runtime.RebuildBatchSize)
	}
	matches, err := indexer.runtimeMatchesDesiredSettings(t.Context(), runtime)
	if err != nil {
		t.Fatalf("runtimeMatchesDesiredSettings() error = %v", err)
	}
	if matches {
		t.Fatal("saved schema settings should fence rebuilds until restart")
	}
}
