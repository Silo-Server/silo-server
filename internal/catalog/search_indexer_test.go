package catalog

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"sort"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
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
		"silo_media_items_rebuild_100", // superseded rebuild
		"silo_media_items_rebuild_200", // newly active
		"silo_media_items",             // legacy previously-active index
		"other_app_index",              // unrelated index on a shared instance
		"silo_media_items_rebuild_50",  // failed-run leftover
		"",
	}
	stale := staleCatalogSearchIndexUIDs(uids, "silo_media_items", "silo_media_items_rebuild_200", "silo_media_items")
	sort.Strings(stale)
	want := []string{"silo_media_items", "silo_media_items_rebuild_100", "silo_media_items_rebuild_50"}
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
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
	}))
	defer server.Close()

	client, err := newMeilisearchClient(server.URL, "", time.Second)
	if err != nil {
		t.Fatalf("newMeilisearchClient() error = %v", err)
	}
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

func TestAttachDocumentVectorsSkipsWhenSemanticDisabled(t *testing.T) {
	docs := []catalogSearchDocument{{ContentID: "movie-1", Title: "Movie"}}
	indexer := &CatalogSearchIndexer{pool: new(pgxpool.Pool)}

	if err := indexer.attachDocumentVectors(context.Background(), docs, "invalid.embedder", false); err != nil {
		t.Fatalf("attachDocumentVectors returned error with semantic disabled: %v", err)
	}
	if docs[0].Vectors != nil {
		t.Fatalf("semantic-disabled document should not carry vectors: %#v", docs[0].Vectors)
	}
}

func TestCatalogSearchDocumentIncludesPersistedAliases(t *testing.T) {
	pool := newSemanticCoverageTestPool(t)
	ctx := context.Background()
	var aliasTable *string
	if err := pool.QueryRow(ctx, `SELECT to_regclass('public.media_item_aliases')::text`).Scan(&aliasTable); err != nil {
		t.Fatalf("check media_item_aliases table: %v", err)
	}
	if aliasTable == nil || *aliasTable == "" {
		t.Skip("test database has not applied media item aliases migration")
	}
	contentID := fmt.Sprintf("alias-doc-%d", time.Now().UnixNano())
	seedSemanticCoverageMediaItem(t, pool, contentID, "series", "matched")
	t.Cleanup(func() { _, _ = pool.Exec(ctx, `DELETE FROM media_items WHERE content_id = $1`, contentID) })
	if _, err := pool.Exec(ctx, `UPDATE media_items SET title = '倒凶十将伝', original_title = '倒凶十将伝' WHERE content_id = $1`, contentID); err != nil {
		t.Fatalf("seed native title: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO media_item_aliases (content_id, title, language, kind, provider)
		VALUES ($1, '10 Tokyo Warriors', 'en', 'alternate', 'tvdb')`, contentID); err != nil {
		t.Fatalf("seed alias: %v", err)
	}

	docs, err := NewCatalogSearchIndexer(pool, nil).LoadDocumentsByIDs(ctx, []string{contentID}, []string{catalogTestContentTypeSeries}, "", false, false)
	if err != nil {
		t.Fatalf("LoadDocumentsByIDs(): %v", err)
	}
	if len(docs) != 1 || !slices.Contains(docs[0].TitleVariants, "10 Tokyo Warriors") {
		t.Fatalf("documents = %#v, want English alias in title variants", docs)
	}
}

// TestCountCatalogSearchVectorDocumentsModelCoverage verifies case (a): an
// embedding stored under a different model is excluded when a model is
// requested, but counted when the model filter is empty.
func TestCountCatalogSearchVectorDocumentsModelCoverage(t *testing.T) {
	ctx := context.Background()
	pool := newSemanticCoverageTestPool(t)
	prefix := fmt.Sprintf("cov-model-%d", time.Now().UnixNano())
	current := prefix + "-current"
	other := prefix + "-other"
	cleanupSemanticCoverageItems(t, pool, prefix)

	seedSemanticCoverageMediaItem(t, pool, current, "movie", "matched")
	seedSemanticCoverageMediaItem(t, pool, other, "movie", "matched")
	seedSemanticCoverageEmbedding(t, pool, current, "text-embedding-3-large")
	seedSemanticCoverageEmbedding(t, pool, other, "stale-model")

	types := []string{"movie"}

	allModels, err := countCatalogSearchVectorDocuments(ctx, pool, types, "")
	if err != nil {
		t.Fatalf("count all models: %v", err)
	}
	if allModels < 2 {
		t.Fatalf("count with empty model = %d, want >= 2 (both rows)", allModels)
	}

	currentModel, err := countCatalogSearchVectorDocuments(ctx, pool, types, "text-embedding-3-large")
	if err != nil {
		t.Fatalf("count current model: %v", err)
	}
	if got := allModels - currentModel; got < 1 {
		t.Fatalf("model filter did not exclude the stale-model row (all=%d current=%d)", allModels, currentModel)
	}

	// The nil-itemTypes branch omits the "AND mi.type = ANY($1)" clause yet still
	// passes a NULL first arg; exercise it through pgx to prove the unreferenced
	// placeholder does not error.
	if _, err := countCatalogSearchVectorDocuments(ctx, pool, nil, "text-embedding-3-large"); err != nil {
		t.Fatalf("count with nil item types: %v", err)
	}
}

// TestCatalogSemanticCoverageByTypeC1Guard verifies case (b) (the C1 regression
// guard) and case (c): an unmatched, non-book item inflates neither numerator
// nor denominator, while a matched item with no embedding row counts toward the
// denominator only.
func TestCatalogSemanticCoverageByTypeC1Guard(t *testing.T) {
	ctx := context.Background()
	pool := newSemanticCoverageTestPool(t)
	prefix := fmt.Sprintf("cov-c1-%d", time.Now().UnixNano())
	matchedVectorized := prefix + "-matched-vec"
	matchedNoVector := prefix + "-matched-novec"
	unmatchedNoVector := prefix + "-unmatched-novec"
	cleanupSemanticCoverageItems(t, pool, prefix)

	seedSemanticCoverageMediaItem(t, pool, matchedVectorized, "movie", "matched")
	seedSemanticCoverageMediaItem(t, pool, matchedNoVector, "movie", "matched")
	seedSemanticCoverageMediaItem(t, pool, unmatchedNoVector, "movie", "unmatched")
	seedSemanticCoverageEmbedding(t, pool, matchedVectorized, "text-embedding-3-large")

	rows, err := catalogSemanticCoverageByType(ctx, pool, []string{"movie"}, "text-embedding-3-large")
	if err != nil {
		t.Fatalf("catalogSemanticCoverageByType: %v", err)
	}
	cov := findCoverageRow(t, rows, "movie")

	// Denominator: matchedVectorized + matchedNoVector are eligible; the
	// unmatched movie must NOT be counted.
	if cov.Eligible < 2 {
		t.Fatalf("eligible = %d, want >= 2 (two matched movies)", cov.Eligible)
	}
	// Numerator: only matchedVectorized has a current-model embedding.
	if cov.Vectorized < 1 {
		t.Fatalf("vectorized = %d, want >= 1", cov.Vectorized)
	}
	// The C1 guard: the unmatched movie inflated neither side, so vectorized
	// must trail eligible by at least the matched-no-vector item, and never
	// exceed eligible.
	if cov.Vectorized > cov.Eligible {
		t.Fatalf("numerator %d exceeds denominator %d", cov.Vectorized, cov.Eligible)
	}
	if cov.Eligible-cov.Vectorized < 1 {
		t.Fatalf("matched-no-embedding item not reflected in denominator (eligible=%d vectorized=%d)", cov.Eligible, cov.Vectorized)
	}
}

// TestCatalogSemanticCoverageByTypeStaleEmbeddingRatio verifies case (d): a
// stale embedding row left on a now-unmatched item must not let the numerator
// exceed the denominator, keeping the per-type ratio within [0,1].
func TestCatalogSemanticCoverageByTypeStaleEmbeddingRatio(t *testing.T) {
	ctx := context.Background()
	pool := newSemanticCoverageTestPool(t)
	prefix := fmt.Sprintf("cov-stale-%d", time.Now().UnixNano())
	staleUnmatched := prefix + "-stale-unmatched"
	cleanupSemanticCoverageItems(t, pool, prefix)

	// An item that lost its match but still carries an embedding row.
	seedSemanticCoverageMediaItem(t, pool, staleUnmatched, "movie", "unmatched")
	seedSemanticCoverageEmbedding(t, pool, staleUnmatched, "text-embedding-3-large")

	rows, err := catalogSemanticCoverageByType(ctx, pool, []string{"movie"}, "text-embedding-3-large")
	if err != nil {
		t.Fatalf("catalogSemanticCoverageByType: %v", err)
	}
	for _, cov := range rows {
		if cov.Eligible < 0 || cov.Vectorized < 0 {
			t.Fatalf("negative coverage counts: %+v", cov)
		}
		if cov.Vectorized > cov.Eligible {
			t.Fatalf("type %s ratio out of range: vectorized=%d eligible=%d", cov.Type, cov.Vectorized, cov.Eligible)
		}
	}
}

func newSemanticCoverageTestPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect test database: %v", err)
	}
	t.Cleanup(pool.Close)
	var tableName *string
	if err := pool.QueryRow(ctx, `SELECT to_regclass('public.media_item_embeddings')::text`).Scan(&tableName); err != nil {
		t.Fatalf("check media_item_embeddings table: %v", err)
	}
	if tableName == nil || *tableName == "" {
		t.Skip("test database has not applied base schema")
	}
	return pool
}

func seedSemanticCoverageMediaItem(t *testing.T, pool *pgxpool.Pool, contentID, mediaType, status string) {
	t.Helper()
	if _, err := pool.Exec(context.Background(), `
		INSERT INTO media_items (content_id, type, title, status, genres)
		VALUES ($1, $2, $3, $4, '{}'::text[])
	`, contentID, mediaType, contentID, status); err != nil {
		t.Fatalf("seed media item %s: %v", contentID, err)
	}
}

func seedSemanticCoverageEmbedding(t *testing.T, pool *pgxpool.Pool, contentID, model string) {
	t.Helper()
	if _, err := pool.Exec(context.Background(), `
		INSERT INTO media_item_embeddings (media_item_id, embedding, model, canonical_text)
		VALUES (
			$1,
			(SELECT array_agg(0.0::real) FROM generate_series(1, 3072))::vector,
			$2,
			$1
		)
	`, contentID, model); err != nil {
		t.Fatalf("seed embedding %s: %v", contentID, err)
	}
}

func cleanupSemanticCoverageItems(t *testing.T, pool *pgxpool.Pool, prefix string) {
	t.Helper()
	t.Cleanup(func() {
		// media_item_embeddings rows cascade on the media_items delete.
		_, _ = pool.Exec(context.Background(), `DELETE FROM media_items WHERE content_id LIKE $1`, prefix+"%")
	})
}

func findCoverageRow(t *testing.T, rows []catalogTypeCoverage, mediaType string) catalogTypeCoverage {
	t.Helper()
	for _, row := range rows {
		if row.Type == mediaType {
			return row
		}
	}
	t.Fatalf("no coverage row for type %q in %+v", mediaType, rows)
	return catalogTypeCoverage{}
}
