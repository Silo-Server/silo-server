package catalog

import (
	"io"

	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// hybridSearchFixture is a provider with semantic search on, backed by a fake
// Meilisearch that answers every search with no hits, so no database is needed.
type hybridSearchFixture struct {
	provider  *MeilisearchSearchProvider
	itemTypes []string
	mu        sync.Mutex
	bodies    map[string][]byte
}

func newHybridSearchFixture(t *testing.T) *hybridSearchFixture {
	t.Helper()
	f := &hybridSearchFixture{itemTypes: []string{"movie", "series"}, bodies: map[string][]byte{}}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "/settings") {
			_, _ = w.Write([]byte(`{"pagination":{"maxTotalHits":1000}}`))
			return
		}
		body, _ := io.ReadAll(r.Body)
		f.mu.Lock()
		f.bodies[r.URL.Path] = body
		f.mu.Unlock()
		_, _ = w.Write([]byte(`{"hits":[],"estimatedTotalHits":0}`))
	}))
	t.Cleanup(server.Close)
	provider, err := NewMeilisearchSearchProvider(nil, nil, nil, MeilisearchProviderConfig{
		URL:             server.URL,
		IndexTypes:      f.itemTypes,
		SemanticEnabled: true,
		SemanticRatio:   0.5,
		Vectorizer:      &fakeCatalogSearchVectorizer{vector: []float32{0.5, 0.25}},
		Coverage:        fakeCoverageGate{ready: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	provider.stateRepo = fakeMeilisearchIndexStateStore{state: SearchIndexState{
		ActiveIndexUID: "catalog_hybrid",
		SchemaVersion:  catalogSearchMeilisearchSchemaVersion(provider.config.Embedder, provider.config.IndexTypes, true, false),
	}}
	f.provider = provider
	return f
}

// search runs one search and returns its result and the body sent to path.
func (f *hybridSearchFixture) search(t *testing.T, req CatalogSearchRequest, path string) (*CatalogSearchResult, []byte) {
	t.Helper()
	req.ItemTypes = f.itemTypes
	result, err := f.provider.Search(t.Context(), req)
	if err != nil {
		t.Fatalf("search %q: %v", req.Query, err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	body, ok := f.bodies[path]
	if !ok {
		t.Fatalf("search %q sent nothing to %s; sent %d requests", req.Query, path, len(f.bodies))
	}
	clear(f.bodies)
	return result, body
}

// Offset paging (jellycompat search) sends the same floored federation.
func TestMeilisearchOffsetHybridSearchFloorsMeaningMatches(t *testing.T) {
	f := newHybridSearchFixture(t)
	result, body := f.search(t, CatalogSearchRequest{Query: "past lives", Limit: 20}, "/multi-search")
	if result.Mode != searchModeHybrid || !strings.Contains(string(body), `"rankingScoreThreshold":0.65`) {
		t.Fatalf("offset hybrid search mode %q sent %s", result.Mode, body)
	}
}
