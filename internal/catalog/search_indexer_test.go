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

func TestStaleCatalogSearchIndexUIDs(t *testing.T) {
	uids := []string{
		"vio_media_items_rebuild_100", // superseded rebuild
		"vio_media_items_rebuild_200", // newly active
		"vio_media_items",             // legacy previously-active index
		"other_app_index",             // unrelated index on a shared instance
		"vio_media_items_rebuild_50",  // failed-run leftover
		"",
	}
	stale := staleCatalogSearchIndexUIDs(uids, "vio_media_items", "vio_media_items_rebuild_200", "vio_media_items")
	sort.Strings(stale)
	want := []string{"vio_media_items", "vio_media_items_rebuild_100", "vio_media_items_rebuild_50"}
	if !slices.Equal(stale, want) {
		t.Fatalf("stale = %v, want %v", stale, want)
	}

	// The newly active index must survive even when it is also the previous
	// active uid (re-running a rebuild that already swapped).
	if got := staleCatalogSearchIndexUIDs([]string{"idx_rebuild_1"}, "idx", "idx_rebuild_1", "idx_rebuild_1"); len(got) != 0 {
		t.Fatalf("active index must never be deleted, got %v", got)
	}
}

func TestCatalogSearchIndexStateRequiresRebuild(t *testing.T) {
	const expected = 123
	tests := []struct {
		name        string
		state       SearchIndexState
		indexPrefix string
		want        bool
	}{
		{name: "missing index", state: SearchIndexState{SchemaVersion: expected}, indexPrefix: "index", want: true},
		{name: "stale schema", state: SearchIndexState{ActiveIndexUID: "index", SchemaVersion: expected - 1}, indexPrefix: "index", want: true},
		{name: "changed prefix", state: SearchIndexState{ActiveIndexUID: "old_rebuild_123", SchemaVersion: expected}, indexPrefix: "new", want: true},
		{name: "current legacy uid", state: SearchIndexState{ActiveIndexUID: "index", SchemaVersion: expected}, indexPrefix: "index", want: false},
		{name: "current rebuild uid", state: SearchIndexState{ActiveIndexUID: "index_rebuild_123", SchemaVersion: expected}, indexPrefix: "index", want: false},
		{name: "similar prefix", state: SearchIndexState{ActiveIndexUID: "index_rebuild_rebuild_123", SchemaVersion: expected}, indexPrefix: "index", want: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := catalogSearchIndexStateRequiresRebuild(tc.state, expected, tc.indexPrefix); got != tc.want {
				t.Fatalf("catalogSearchIndexStateRequiresRebuild() = %t, want %t", got, tc.want)
			}
		})
	}
}

func TestCatalogSearchIndexRequiresRebuildWhenActiveUIDIsMissingFromTarget(t *testing.T) {
	const (
		expectedSchema = 123
		indexPrefix    = "index"
	)
	client := newMeilisearchTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/indexes/index_rebuild_123/stats":
			_, _ = w.Write([]byte(`{"numberOfDocuments":42}`))
		case "/indexes/index_rebuild_456/stats":
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"message":"Index not found.","code":"index_not_found"}`))
		case "/indexes/index_rebuild_789/stats":
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`{"message":"Unavailable.","code":"internal"}`))
		default:
			t.Errorf("unexpected Meilisearch request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusInternalServerError)
		}
	})
	tests := []struct {
		name    string
		uid     string
		want    bool
		wantErr bool
	}{
		{name: "active uid exists", uid: "index_rebuild_123", want: false},
		{name: "active uid missing after target move", uid: "index_rebuild_456", want: true},
		{name: "target temporarily unavailable", uid: "index_rebuild_789", wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := catalogSearchIndexRequiresRebuildOnTarget(
				t.Context(),
				client,
				SearchIndexState{ActiveIndexUID: tc.uid, SchemaVersion: expectedSchema},
				expectedSchema,
				indexPrefix,
			)
			if (err != nil) != tc.wantErr {
				t.Fatalf("catalogSearchIndexRequiresRebuildOnTarget() error = %v", err)
			}
			if got != tc.want {
				t.Fatalf("catalogSearchIndexRequiresRebuildOnTarget() = %t, want %t", got, tc.want)
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

// memSettings is an in-memory settings store for indexer runtime tests.
type memSettings struct{ m map[string]string }

func (s *memSettings) GetAll(context.Context) (map[string]string, error) { return s.m, nil }

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

func TestCatalogSearchMaintenanceTargetEqual(t *testing.T) {
	startup := DefaultCatalogSearchSettings()
	startup.Provider = SearchProviderMeilisearch
	startup.MeilisearchURL = "http://meilisearch:7700"
	startup.IndexTypes = []string{"movie", "series"}

	tuningOnly := startup
	tuningOnly.SyncBatchSize++
	tuningOnly.RebuildBatchSize++
	tuningOnly.RebuildQueueDepth++
	if !catalogSearchMaintenanceTargetEqual(startup, tuningOnly) {
		t.Fatal("hot-reloadable maintenance tuning must not require a restart fence")
	}

	desired := startup
	desired.SemanticEnabled = true
	if catalogSearchMaintenanceTargetEqual(startup, desired) {
		t.Fatal("semantic setting change must wait for the startup configuration")
	}

	desired = startup
	desired.IndexTypes = []string{"movie"}
	if catalogSearchMaintenanceTargetEqual(startup, desired) {
		t.Fatal("index scope change must wait for the startup configuration")
	}
}

func TestCatalogSearchIndexHasNewerSchema(t *testing.T) {
	newer := SearchIndexState{
		ActiveIndexUID: "new-index",
		SchemaVersion:  (SearchMeilisearchSchemaVersion + 1) * 1_000_000,
	}
	if !catalogSearchIndexHasNewerSchema(newer) {
		t.Fatal("newer schema should fence an older server from rebuilding")
	}
	if catalogSearchIndexHasNewerSchema(SearchIndexState{ActiveIndexUID: "index", SchemaVersion: SearchMeilisearchSchemaVersion * 1_000_000}) {
		t.Fatal("current schema generation should not be treated as newer")
	}
}

func TestRebuildIndexingPercent(t *testing.T) {
	if got := rebuildIndexingPercent(0, 0); got != 50 {
		t.Fatalf("unknown total should report midpoint, got %v", got)
	}
	if got := rebuildIndexingPercent(0, 10); got != 5 {
		t.Fatalf("start of band = %v, want 5", got)
	}
	if got := rebuildIndexingPercent(10, 10); got != 90 {
		t.Fatalf("end of band = %v, want 90", got)
	}
	// Documents created after the total was counted must not push past the band.
	if got := rebuildIndexingPercent(15, 10); got != 90 {
		t.Fatalf("overshoot should clamp to 90, got %v", got)
	}
}

func TestMeilisearchTwoTermAttributesAreSearchable(t *testing.T) {
	searchable, _ := catalogSearchMeilisearchSettings(DefaultMeilisearchEmbedder, false, false)["searchableAttributes"].([]string)
	for _, attribute := range meilisearchTwoTermSearchAttributes {
		if !slices.Contains(searchable, attribute) {
			t.Errorf("two-term search attribute %q is not in searchableAttributes %v", attribute, searchable)
		}
	}
}

func TestMeilisearchRankingRulesLeadWithWordsTypoProximity(t *testing.T) {
	rules, _ := catalogSearchMeilisearchSettings(DefaultMeilisearchEmbedder, false, false)["rankingRules"].([]string)
	if want := []string{"words", "typo", "proximity"}; len(rules) < len(want) || !slices.Equal(rules[:len(want)], want) {
		t.Fatalf("rankingRules = %v, want them to begin with %v", rules, want)
	}
}

func TestMeilisearchRankingRulesRankEpisodesAfterMediaInOneAttribute(t *testing.T) {
	rules, _ := catalogSearchMeilisearchSettings(DefaultMeilisearchEmbedder, false, false)["rankingRules"].([]string)
	want := []string{"attributeRank", "sort", "type_rank:asc", "wordPosition", "exactness", "popularity:desc"}
	if len(rules) < len(want) || !slices.Equal(rules[len(rules)-len(want):], want) {
		t.Fatalf("rankingRules = %v, want them to end with %v", rules, want)
	}
}

func TestCatalogSearchRebuildRemovesFailedBuildIndexes(t *testing.T) {
	const leftover = "vio_media_items_rebuild_1"
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
			_, _ = w.Write([]byte(`{"results":[{"uid":"vio_media_items"},{"uid":"` + leftover + `"},{"uid":"other_app"}],"offset":0,"limit":100,"total":3}`))
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
