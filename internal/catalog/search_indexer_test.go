package catalog

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"sort"
	"strings"
	"sync"
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

// Meilisearch rejects a search whose attributesToSearchOn names an attribute
// the index does not search, so a two-term attribute missing from the index
// settings would send every two-term search to the Postgres fallback.
func TestMeilisearchTwoTermAttributesAreSearchable(t *testing.T) {
	searchable, _ := catalogSearchMeilisearchSettings(DefaultMeilisearchEmbedder, false, false)["searchableAttributes"].([]string)
	for _, attribute := range meilisearchTwoTermSearchAttributes {
		if !slices.Contains(searchable, attribute) {
			t.Errorf("two-term search attribute %q is not in searchableAttributes %v", attribute, searchable)
		}
	}
}

// meilisearchKeywordMatchCount cuts a keyword ranking at its first weaker hit,
// which drops only weaker hits while the index ranks by words, typo and
// proximity before anything else.
func TestMeilisearchRankingRulesLeadWithWordsTypoProximity(t *testing.T) {
	rules, _ := catalogSearchMeilisearchSettings(DefaultMeilisearchEmbedder, false, false)["rankingRules"].([]string)
	if want := []string{"words", "typo", "proximity"}; len(rules) < len(want) || !slices.Equal(rules[:len(want)], want) {
		t.Fatalf("rankingRules = %v, want them to begin with %v", rules, want)
	}
}

// A failed rebuild must not leave its partial index behind, even when the
// failure is its own context being canceled, and a new rebuild clears the
// leftovers of runs that died before they could clean up.
func TestCatalogSearchRebuildRemovesFailedBuildIndexes(t *testing.T) {
	const leftover = "silo_media_items_rebuild_1"
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	var (
		mu      sync.Mutex
		created string
		deleted []string
	)
	client := newMeilisearchTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		mu.Lock()
		defer mu.Unlock()
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/indexes":
			_, _ = w.Write([]byte(`{"results":[{"uid":"silo_media_items"},{"uid":"` + leftover + `"},{"uid":"other_app"}],"offset":0,"limit":100,"total":3}`))
		case r.Method == http.MethodPost && r.URL.Path == "/indexes":
			var body struct {
				UID string `json:"uid"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			created = body.UID
			_, _ = w.Write([]byte(`{"taskUid":1}`))
		case r.Method == http.MethodDelete && strings.HasPrefix(r.URL.Path, "/indexes/"):
			deleted = append(deleted, strings.TrimPrefix(r.URL.Path, "/indexes/"))
			_, _ = w.Write([]byte(`{"taskUid":2}`))
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/tasks/"):
			_, _ = w.Write([]byte(`{"status":"succeeded"}`))
		case r.Method == http.MethodPatch && strings.HasSuffix(r.URL.Path, "/settings"):
			// The task running the rebuild is canceled mid-request.
			cancel()
			w.WriteHeader(http.StatusServiceUnavailable)
		default:
			t.Errorf("unexpected Meilisearch request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusInternalServerError)
		}
	})
	settings := DefaultCatalogSearchSettings()
	settings.Provider = SearchProviderMeilisearch

	if _, err := NewCatalogSearchIndexer(nil, nil).rebuildLocked(ctx, nil, settings, client); err == nil {
		t.Fatal("rebuildLocked succeeded, want the settings failure")
	}
	mu.Lock()
	defer mu.Unlock()
	if created == "" || created == leftover {
		t.Fatalf("created build index %q", created)
	}
	if want := []string{leftover, created}; !slices.Equal(deleted, want) {
		t.Fatalf("deleted indexes = %v, want %v", deleted, want)
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

// The sync task rebuilds an index that is missing or has another schema,
// applies changed index settings in place to one that has the current schema,
// and otherwise leaves the index alone. The in-place update never sends the
// embedder, which only a rebuild may change, and fails when Meilisearch fails
// the settings task, so the recorded fingerprint stays and the next sync
// retries.
func TestCatalogSearchIndexMaintenanceChoosesRebuildSettingsOrNothing(t *testing.T) {
	settings := DefaultCatalogSearchSettings()
	settings.Provider = SearchProviderMeilisearch
	settings.SemanticEnabled = true
	schema := catalogSearchMeilisearchSchemaVersion(settings.Embedder, settings.IndexTypes, settings.SemanticEnabled, settings.BinaryQuantized)
	const (
		active  = "silo_media_items_rebuild_100"
		missing = "silo_media_items_rebuild_200"
	)
	var (
		mu         sync.Mutex
		patched    []map[string]any
		taskStatus = "succeeded"
	)
	client := newMeilisearchTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		mu.Lock()
		defer mu.Unlock()
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/indexes/"+active+"/stats":
			_, _ = w.Write([]byte(`{"numberOfDocuments":42}`))
		case r.Method == http.MethodGet && r.URL.Path == "/indexes/"+missing+"/stats":
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"message":"Index not found.","code":"index_not_found"}`))
		case r.Method == http.MethodPatch && r.URL.Path == "/indexes/"+active+"/settings":
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Errorf("decode settings: %v", err)
			}
			patched = append(patched, body)
			_, _ = w.Write([]byte(`{"taskUid":7}`))
		case r.Method == http.MethodGet && r.URL.Path == "/tasks/7":
			_, _ = w.Write([]byte(`{"status":"` + taskStatus + `","error":{"message":"bad setting"}}`))
		default:
			t.Errorf("unexpected Meilisearch request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusInternalServerError)
		}
	})

	tests := []struct {
		name  string
		state SearchIndexState
		want  catalogSearchIndexMaintenance
	}{
		{"current", SearchIndexState{ActiveIndexUID: active, SchemaVersion: schema, SettingsFingerprint: catalogSearchMeilisearchSettingsFingerprint}, catalogSearchIndexCurrentSettings},
		{"settings changed", SearchIndexState{ActiveIndexUID: active, SchemaVersion: schema, SettingsFingerprint: "older"}, catalogSearchIndexApplySettings},
		{"settings unknown after upgrade", SearchIndexState{ActiveIndexUID: active, SchemaVersion: schema}, catalogSearchIndexApplySettings},
		{"schema changed", SearchIndexState{ActiveIndexUID: active, SchemaVersion: schema + 1}, catalogSearchIndexRebuild},
		{"index missing from Meilisearch", SearchIndexState{ActiveIndexUID: missing, SchemaVersion: schema}, catalogSearchIndexRebuild},
		{"never built", SearchIndexState{}, catalogSearchIndexRebuild},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := planCatalogSearchIndexMaintenance(t.Context(), client, tc.state, settings)
			if err != nil {
				t.Fatalf("planCatalogSearchIndexMaintenance() error = %v", err)
			}
			if got != tc.want {
				t.Fatalf("planCatalogSearchIndexMaintenance() = %d, want %d", got, tc.want)
			}
		})
	}

	indexer := NewCatalogSearchIndexer(nil, nil)
	if err := indexer.applyIndexSettingsLocked(t.Context(), client, active); err != nil {
		t.Fatalf("applyIndexSettingsLocked() error = %v", err)
	}
	mu.Lock()
	if len(patched) != 1 {
		t.Fatalf("settings requests = %d, want 1", len(patched))
	}
	if _, ok := patched[0]["embedders"]; ok {
		t.Fatal("in-place settings update sent the embedder")
	}
	want, _ := json.Marshal(catalogSearchMeilisearchIndexSettings())
	got, _ := json.Marshal(patched[0])
	if string(got) != string(want) {
		t.Fatalf("settings sent = %s, want %s", got, want)
	}
	taskStatus = "failed"
	mu.Unlock()
	if err := indexer.applyIndexSettingsLocked(t.Context(), client, active); err == nil {
		t.Fatal("applyIndexSettingsLocked() succeeded although Meilisearch failed the settings task")
	}
}
