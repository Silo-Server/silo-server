package catalog

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"
)

// Payloads are cut by their encoded size: each stays within the cap, is as
// full as the next document allows, and together they carry every document
// once, in order. Zero-padded vectors encode much smaller than full ones, so
// an estimate per dimension would cut these payloads far too early or late.
func TestForEachCatalogSearchDocumentPayloadFillsCapInOrder(t *testing.T) {
	full := make([]float32, 256)
	padded := make([]float32, 256)
	for i := range full {
		full[i] = -0.012345678 * float32(i+1)
		if i < 32 {
			padded[i] = full[i]
		}
	}
	var docs []catalogSearchDocument
	for i := range 40 {
		doc := catalogSearchDocument{ContentID: fmt.Sprintf("item-%02d", i), Type: "movie", Title: strings.Repeat("t", i)}
		switch i % 4 {
		case 0:
			doc.Vectors = map[string][]float32{DefaultMeilisearchEmbedder: full}
		case 1:
			doc.Vectors = map[string][]float32{DefaultMeilisearchEmbedder: padded}
		case 2:
			doc.Type = "episode"
			doc.Vectors = map[string][]float32{DefaultMeilisearchEmbedder: nil}
		}
		docs = append(docs, doc)
	}
	encodedSize := func(doc catalogSearchDocument) int {
		data, err := json.Marshal(doc)
		if err != nil {
			t.Fatal(err)
		}
		return len(data)
	}
	const maxBytes = 12 * 1024
	// One document larger than the cap travels alone.
	huge := catalogSearchDocument{ContentID: "item-huge", Type: "movie", Vectors: map[string][]float32{DefaultMeilisearchEmbedder: make([]float32, 4096)}}
	for i := range huge.Vectors[DefaultMeilisearchEmbedder] {
		huge.Vectors[DefaultMeilisearchEmbedder][i] = 0.123456789
	}
	if encodedSize(huge) <= maxBytes {
		t.Fatal("test document must exceed the cap")
	}
	docs = slices.Insert(docs, 20, huge)

	var gotIDs []string
	var payloads []catalogSearchDocumentPayload
	err := forEachCatalogSearchDocumentPayload(docs, maxBytes, func(payload catalogSearchDocumentPayload) error {
		payloads = append(payloads, payload)
		return nil
	})
	if err != nil {
		t.Fatalf("forEachCatalogSearchDocumentPayload() error = %v", err)
	}
	next := 0
	for i, payload := range payloads {
		var decoded []struct {
			ContentID string `json:"content_id"`
		}
		if err := json.Unmarshal(payload.body, &decoded); err != nil {
			t.Fatalf("payload %d is not a JSON array: %v", i, err)
		}
		if len(decoded) != len(payload.docs) {
			t.Fatalf("payload %d carries %d documents in its body, %d in docs", i, len(decoded), len(payload.docs))
		}
		for j, doc := range decoded {
			if doc.ContentID != payload.docs[j].ContentID {
				t.Fatalf("payload %d body document %d = %q, docs has %q", i, j, doc.ContentID, payload.docs[j].ContentID)
			}
			gotIDs = append(gotIDs, doc.ContentID)
		}
		next += len(payload.docs)
		if len(payload.body) > maxBytes && len(payload.docs) != 1 {
			t.Fatalf("payload %d is %d bytes with %d documents, over the %d byte cap", i, len(payload.body), len(payload.docs), maxBytes)
		}
		if i < len(payloads)-1 && len(payload.body)+1+encodedSize(docs[next]) <= maxBytes {
			t.Fatalf("payload %d (%d bytes) was cut although %s fit", i, len(payload.body), docs[next].ContentID)
		}
	}
	var wantIDs []string
	for _, doc := range docs {
		wantIDs = append(wantIDs, doc.ContentID)
	}
	if !slices.Equal(gotIDs, wantIDs) {
		t.Fatalf("documents sent = %v, want %v", gotIDs, wantIDs)
	}
	if len(payloads) < 3 {
		t.Fatalf("payload count = %d, want the documents split", len(payloads))
	}
}

func TestMeilisearchMixedHybridSearchQueries(t *testing.T) {
	provider := &MeilisearchSearchProvider{config: MeilisearchProviderConfig{}}
	base := meilisearchSearchRequest{
		Query:                "Who Are You",
		AttributesToRetrieve: []string{"content_id"},
		MatchingStrategy:     "all",
		Vector:               []float32{0.5, 0.25},
		Hybrid:               &meilisearchHybridRequest{Embedder: DefaultMeilisearchEmbedder, SemanticRatio: 0.4},
	}
	built := provider.buildHybridSearchQueries("catalog-v3", CatalogSearchRequest{
		Access: AccessFilter{AllowedLibraryIDs: []int{4}},
	}, base, 125)
	if built.Ratio != 0.4 || len(built.Keyword) != 2 || len(built.Keyword[0]) != 1 || len(built.Keyword[1]) != 1 || built.Meaning == nil {
		t.Fatalf("queries = %#v", built)
	}
	media, episode, meaning := built.Keyword[0][0], built.Keyword[1][0], *built.Meaning
	if media.Hybrid != nil || media.Vector != nil || media.Query != base.Query || media.Limit != 125 || media.Offset != 0 ||
		!strings.Contains(media.Filter, `type != "episode"`) {
		t.Fatalf("media keyword query = %#v", media)
	}
	if episode.Hybrid != nil || episode.Vector != nil || episode.Limit != 125 || !strings.Contains(episode.Filter, `type = "episode"`) {
		t.Fatalf("episode keyword query = %#v", episode)
	}
	if meaning.Hybrid == nil || meaning.Hybrid.SemanticRatio != 1 || len(meaning.Vector) == 0 || meaning.Query != "" ||
		meaning.Limit != meilisearchSemanticCandidateLimit || meaning.Filter != media.Filter {
		t.Fatalf("meaning query = %#v", meaning)
	}
	queries := built.all()
	if len(queries) != 3 || queries[2].Hybrid == nil {
		t.Fatalf("flattened queries = %#v", queries)
	}
	for _, query := range queries {
		if query.IndexUID != "catalog-v3" || !query.ShowRankingScore {
			t.Fatalf("query index/score = %q/%v", query.IndexUID, query.ShowRankingScore)
		}
		if keyword := query.Hybrid == nil; query.ShowRankingScoreDetails != keyword {
			t.Fatalf("query details = %v, want %v for keyword %v", query.ShowRankingScoreDetails, keyword, keyword)
		}
	}
}

func TestMeilisearchMatchingStrategyByTermCount(t *testing.T) {
	for _, tc := range []struct{ configured, query, want string }{
		{"last", "dune", "last"},
		{"last", "tom hanks", "all"},
		{"last", "movies about time travel", "frequency"},
		{"all", "movies about time travel", "all"},
	} {
		provider := &MeilisearchSearchProvider{config: MeilisearchProviderConfig{MatchingStrategy: tc.configured}}
		if got := provider.matchingStrategyForRequest(CatalogSearchRequest{Query: tc.query}); got != tc.want {
			t.Errorf("strategy %q, query %q = %q, want %q", tc.configured, tc.query, got, tc.want)
		}
	}
}

func TestMeilisearchSearchRequestFallsBackWhenEmbeddingFails(t *testing.T) {
	provider := &MeilisearchSearchProvider{
		config: MeilisearchProviderConfig{
			MatchingStrategy: DefaultMeilisearchMatchingStrategy,
			SemanticEnabled:  true,
			SemanticRatio:    DefaultMeilisearchSemanticRatio,
			Embedder:         DefaultMeilisearchEmbedder,
			Vectorizer:       &fakeCatalogSearchVectorizer{err: errors.New(`Post "https://embed.example/v1?key=secret-key": dial tcp: connection refused`)},
		},
	}
	req, fallback := provider.buildMeilisearchSearchRequest(context.Background(), CatalogSearchRequest{
		Query: "something vague atmospheric",
	})
	if req.Vector != nil || req.Hybrid != nil {
		t.Fatalf("fallback request should be keyword-only: %#v", req)
	}
	// Every searching user sees the reason, so it must not quote the provider error.
	if fallback != "semantic query embedding failed" {
		t.Fatalf("fallback = %q, want the fixed semantic query failure reason", fallback)
	}
}

func TestMeilisearchCircuitTripsOnServerAndDecodeErrors(t *testing.T) {
	provider := &MeilisearchSearchProvider{}
	if !provider.shouldTripCircuit(&meilisearchHTTPError{StatusCode: 500}) {
		t.Fatal("HTTP 500 should trip circuit")
	}
	if !provider.shouldTripCircuit(&meilisearchDecodeError{Err: errors.New("bad json")}) {
		t.Fatal("decode failure should trip circuit")
	}
	if provider.shouldTripCircuit(context.Canceled) {
		t.Fatal("context.Canceled should not trip circuit")
	}
}

type fakeMeilisearchIndexStateStore struct {
	state   SearchIndexState
	pending int
}

func (f fakeMeilisearchIndexStateStore) GetState(context.Context, string) (SearchIndexState, error) {
	return f.state, nil
}

func (f fakeMeilisearchIndexStateStore) PendingCount(context.Context, string) (int, error) {
	return f.pending, nil
}

type countingMeilisearchIndexStateStore struct {
	state         SearchIndexState
	pending       int
	getStateCalls int
	pendingCalls  int
}

func (f *countingMeilisearchIndexStateStore) GetState(context.Context, string) (SearchIndexState, error) {
	f.getStateCalls++
	return f.state, nil
}

func (f *countingMeilisearchIndexStateStore) PendingCount(context.Context, string) (int, error) {
	f.pendingCalls++
	return f.pending, nil
}

func TestMeilisearchProviderCachesIndexStateAcrossRequests(t *testing.T) {
	client := newMeilisearchTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"hits":[],"estimatedTotalHits":0}`))
	})
	store := &countingMeilisearchIndexStateStore{
		state: SearchIndexState{
			ActiveIndexUID: "search-index",
			SchemaVersion:  catalogSearchMeilisearchSchemaVersion(DefaultMeilisearchEmbedder, nil, false, false),
		},
		pending: 3,
	}
	provider := &MeilisearchSearchProvider{
		stateRepo: store,
		fallback:  &PostgresSearchProvider{},
		client:    client,
		config: MeilisearchProviderConfig{
			MatchingStrategy: DefaultMeilisearchMatchingStrategy,
			Embedder:         DefaultMeilisearchEmbedder,
		},
	}

	for i := 0; i < 3; i++ {
		result, err := provider.Search(context.Background(), CatalogSearchRequest{Query: "sponge", Limit: 10})
		if err != nil {
			t.Fatalf("Search %d returned error: %v", i, err)
		}
		if result.IndexPendingEvents != 3 {
			t.Fatalf("Search %d IndexPendingEvents = %d, want cached 3", i, result.IndexPendingEvents)
		}
	}
	if store.getStateCalls != 1 || store.pendingCalls != 1 {
		t.Fatalf("state store calls = %d/%d, want 1/1 (state cached within TTL)", store.getStateCalls, store.pendingCalls)
	}
}

func TestMeilisearchProviderInvalidatesStateCacheOnSearchFailure(t *testing.T) {
	client := newMeilisearchTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		// 404 models the cached index having been deleted by a rebuild's
		// cleanup; it must not trip the circuit, only the state cache.
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"message":"Index search-index not found.","code":"index_not_found"}`))
	})
	store := &countingMeilisearchIndexStateStore{
		state: SearchIndexState{
			ActiveIndexUID: "search-index",
			SchemaVersion:  catalogSearchMeilisearchSchemaVersion(DefaultMeilisearchEmbedder, nil, false, false),
		},
	}
	provider := &MeilisearchSearchProvider{
		stateRepo: store,
		fallback:  &PostgresSearchProvider{},
		client:    client,
		config: MeilisearchProviderConfig{
			MatchingStrategy: DefaultMeilisearchMatchingStrategy,
			Embedder:         DefaultMeilisearchEmbedder,
		},
	}

	// Both searches fail (the nil-repo postgres fallback errors too); what
	// matters is that the failed first search dropped the cached state so the
	// second one refetched instead of reusing it for the rest of the TTL.
	for i := 0; i < 2; i++ {
		if _, err := provider.Search(context.Background(), CatalogSearchRequest{Query: "sponge", Limit: 10}); err == nil {
			t.Fatalf("Search %d should surface the fallback error in this setup", i)
		}
	}
	if store.getStateCalls != 3 {
		t.Fatalf("getStateCalls = %d, want 3 (each missing-index failure refreshes state immediately)", store.getStateCalls)
	}
	if reason, blocked := provider.circuitBlocked(time.Now()); blocked {
		t.Fatalf("HTTP 404 must not trip the circuit, got open circuit: %s", reason)
	}
}

func TestMeilisearchProviderRetriesNewActiveIndexAfterSwap(t *testing.T) {
	var paths []string
	client := newMeilisearchTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/indexes/old-index/search" {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"message":"Index old-index not found.","code":"index_not_found"}`))
			return
		}
		_, _ = w.Write([]byte(`{"hits":[],"estimatedTotalHits":0}`))
	})
	schemaVersion := catalogSearchMeilisearchSchemaVersion(DefaultMeilisearchEmbedder, nil, false, false)
	store := &countingMeilisearchIndexStateStore{state: SearchIndexState{
		ActiveIndexUID: "new-index",
		SchemaVersion:  schemaVersion,
	}}
	provider := &MeilisearchSearchProvider{
		stateRepo:     store,
		fallback:      &PostgresSearchProvider{},
		client:        client,
		config:        MeilisearchProviderConfig{MatchingStrategy: DefaultMeilisearchMatchingStrategy, Embedder: DefaultMeilisearchEmbedder},
		cachedState:   SearchIndexState{ActiveIndexUID: "old-index", SchemaVersion: schemaVersion},
		stateCachedAt: time.Now(),
	}

	result, err := provider.Search(t.Context(), CatalogSearchRequest{Query: "space opera", Limit: 10})
	if err != nil {
		t.Fatalf("Search returned error: %v", err)
	}
	wantPaths := []string{"/indexes/old-index/search", "/indexes/new-index/search"}
	if !reflect.DeepEqual(paths, wantPaths) {
		t.Fatalf("Meilisearch paths = %#v, want %#v", paths, wantPaths)
	}
	if store.getStateCalls != 1 {
		t.Fatalf("state refresh calls = %d, want 1", store.getStateCalls)
	}
	if result.Provider != SearchProviderMeilisearch {
		t.Fatalf("provider = %q, want Meilisearch", result.Provider)
	}
}

func TestMeilisearchProviderUsesActiveIndexWhenPendingUpdatesExist(t *testing.T) {
	requests := 0
	var gotMethod, gotPath string
	client := newMeilisearchTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		requests++
		gotMethod = r.Method
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"hits":[],"estimatedTotalHits":0}`))
	})

	provider := &MeilisearchSearchProvider{
		stateRepo: fakeMeilisearchIndexStateStore{
			state: SearchIndexState{
				ActiveIndexUID: "search-index",
				SchemaVersion:  catalogSearchMeilisearchSchemaVersion(DefaultMeilisearchEmbedder, nil, false, false),
			},
			pending: 7,
		},
		fallback: &PostgresSearchProvider{},
		client:   client,
		config: MeilisearchProviderConfig{
			MatchingStrategy: DefaultMeilisearchMatchingStrategy,
			Embedder:         DefaultMeilisearchEmbedder,
		},
	}

	result, err := provider.Search(context.Background(), CatalogSearchRequest{
		Query: "sponge in the sea",
		Limit: 10,
	})
	if err != nil {
		t.Fatalf("Search returned error: %v", err)
	}
	if requests != 1 {
		t.Fatalf("Meilisearch requests = %d, want 1", requests)
	}
	if gotMethod != http.MethodPost || gotPath != "/indexes/search-index/search" {
		t.Fatalf("unexpected Meilisearch request %s %s", gotMethod, gotPath)
	}
	if result.Provider != SearchProviderMeilisearch {
		t.Fatalf("provider = %q, want %q", result.Provider, SearchProviderMeilisearch)
	}
	if result.FallbackReason != "" {
		t.Fatalf("fallback reason = %q, want empty", result.FallbackReason)
	}
	if result.IndexPendingEvents != 7 {
		t.Fatalf("IndexPendingEvents = %d, want 7", result.IndexPendingEvents)
	}
}

func TestMeilisearchProviderFallsBackWhenScopedIndexCannotSatisfyRequest(t *testing.T) {
	provider := &MeilisearchSearchProvider{
		config: MeilisearchProviderConfig{
			IndexTypes: []string{"movie", "series"},
		},
		fallback: &PostgresSearchProvider{},
	}

	if !provider.indexCoversRequest([]string{"movie"}) {
		t.Fatal("movie request should be covered by movie/series index")
	}
	if provider.indexCoversRequest([]string{"audiobook"}) {
		t.Fatal("audiobook request should not be covered by movie/series index")
	}
	if provider.indexCoversRequest(nil) {
		t.Fatal("unscoped request should not be covered by a scoped index")
	}
}

type fakeCatalogSearchStatusStateStore struct {
	fakeMeilisearchIndexStateStore
}

func (fakeCatalogSearchStatusStateStore) DeadLetterCount(context.Context, string) (int, error) {
	return 0, nil
}

// An active index recorded in Postgres that Meilisearch no longer has (its
// data directory was emptied) must not be reported as a current, healthy
// index: searches fall back to Postgres until the sync task rebuilds it.
func TestCatalogSearchStatusReportsMissingActiveIndex(t *testing.T) {
	const uid = "silo_media_items_rebuild_1"
	settings := DefaultCatalogSearchSettings()
	settings.Provider = SearchProviderMeilisearch
	settings.MeilisearchURL = "http://meilisearch.invalid"
	settings.SemanticEnabled = true
	for name, indexSemantic := range map[string]bool{
		"current index": true,
		// An index built without vectors serves keyword search and already
		// needs a rebuild; it can be missing all the same.
		"keyword-only index": false,
	} {
		t.Run(name, func(t *testing.T) {
			client := newMeilisearchTestClient(t, func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusNotFound)
				_, _ = w.Write([]byte(`{"message":"Index ` + uid + ` not found.","code":"index_not_found"}`))
			})
			store := fakeCatalogSearchStatusStateStore{fakeMeilisearchIndexStateStore{state: SearchIndexState{
				ActiveIndexUID: uid,
				SchemaVersion:  catalogSearchMeilisearchSchemaVersion(settings.Embedder, settings.IndexTypes, indexSemantic, settings.BinaryQuantized),
				DocumentCount:  1345309,
			}}}
			meili := &MeilisearchSearchProvider{stateRepo: store, client: client, config: MeilisearchProviderConfig{Embedder: settings.Embedder}}
			service := &CatalogSearchService{settings: settings, provider: meili, meili: meili, state: store}

			status := service.Status(t.Context())
			if !status.Index.RebuildRequired || !status.Degraded || status.ActiveProvider != SearchProviderPostgres {
				t.Fatalf("status = rebuild_required %t, degraded %t, active_provider %q; want true, true, postgres",
					status.Index.RebuildRequired, status.Degraded, status.ActiveProvider)
			}
			if status.Index.DocumentCount != 0 {
				t.Fatalf("document_count = %d for a missing index, want 0", status.Index.DocumentCount)
			}
		})
	}
}

type fakeCatalogSearchVectorizer struct {
	vector    []float32
	err       error
	calls     int
	lastQuery string
}

func (f *fakeCatalogSearchVectorizer) EmbedSearchQuery(_ context.Context, query string) ([]float32, error) {
	f.calls++
	f.lastQuery = query
	if f.err != nil {
		return nil, f.err
	}
	return append([]float32(nil), f.vector...), nil
}

// fakeCoverageGate is a hot-path gate stub: it returns a fixed readiness verdict
// regardless of item types, so buildMeilisearchSearchRequest's gating can be
// exercised without a live coverage tracker.
type fakeCoverageGate struct {
	ready  bool
	reason string
}

func (g fakeCoverageGate) CoverageReady(_ []string) (bool, string) {
	return g.ready, g.reason
}
