package catalog

import (
	"context"

	"errors"
	"net/http"

	"reflect"

	"testing"
	"time"
)

func TestMeilisearchFederationUnsupportedCacheExpiresAndRecovers(t *testing.T) {
	provider := &MeilisearchSearchProvider{}
	provider.markFederationUnsupported()
	if !provider.isFederationUnsupported() {
		t.Fatal("fresh unsupported result should be cached")
	}

	provider.federationMu.Lock()
	provider.federationUnsupportedAt = time.Now().Add(-meilisearchFederationCapabilityCacheTTL)
	provider.federationMu.Unlock()
	if provider.isFederationUnsupported() {
		t.Fatal("expired unsupported result should permit a federation re-probe")
	}

	provider.markFederationSupported()
	if provider.isFederationUnsupported() {
		t.Fatal("successful federation probe should clear the unsupported result")
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
