package catalog

import (
	"encoding/json"
	"io"
	"math"
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

// Issue #3963: with semantic search on, the meaning half of a hybrid search
// ranked every item with a vector, so every search of two or more words
// returned the whole library. The search now runs as a keyword query plus a
// meaning-only query, and only the meaning-only query has a score floor, so
// weak keyword matches stay while unrelated meaning matches go.
func TestMeilisearchHybridSearchFloorsOnlyMeaningMatches(t *testing.T) {
	f := newHybridSearchFixture(t)
	result, federated := f.search(t, CatalogSearchRequest{CursorPaging: true, Query: "Model Citizen", Limit: 60}, "/multi-search")
	if result.Mode != searchModeHybrid || !result.SemanticUsed || result.FallbackReason != "" {
		t.Fatalf("mode/semantic/fallback = %q/%v/%q, want hybrid without fallback", result.Mode, result.SemanticUsed, result.FallbackReason)
	}
	var sent struct {
		Federation struct{ Offset, Limit int }
		Queries    []map[string]json.RawMessage
	}
	if err := json.Unmarshal(federated, &sent); err != nil {
		t.Fatal(err)
	}
	if sent.Federation.Offset != 0 || sent.Federation.Limit != 1000 || len(sent.Queries) != 2 {
		t.Fatalf("federated request = %s", federated)
	}
	assertKeywordQuery(t, sent.Queries[0])
	assertMeaningQuery(t, sent.Queries[1], f.provider.config.Embedder)
}

// Offset paging (jellycompat search) sends the same floored federation.
func TestMeilisearchOffsetHybridSearchFloorsMeaningMatches(t *testing.T) {
	f := newHybridSearchFixture(t)
	result, body := f.search(t, CatalogSearchRequest{Query: "past lives", Limit: 20}, "/multi-search")
	if result.Mode != searchModeHybrid || !strings.Contains(string(body), `"rankingScoreThreshold":0.65`) {
		t.Fatalf("offset hybrid search mode %q sent %s", result.Mode, body)
	}
}

// A one-word search stays a plain keyword search with no vector or floor.
func TestMeilisearchOneWordSearchStaysKeyword(t *testing.T) {
	f := newHybridSearchFixture(t)
	result, body := f.search(t, CatalogSearchRequest{CursorPaging: true, Query: "Conclave", Limit: 60}, "/indexes/catalog_hybrid/search")
	if result.Mode != searchModeKeyword {
		t.Fatalf("one-word search mode = %q, want keyword", result.Mode)
	}
	for _, field := range []string{`"vector"`, `"hybrid"`, `"rankingScoreThreshold"`} {
		if strings.Contains(string(body), field) {
			t.Fatalf("keyword search carries %s: %s", field, body)
		}
	}
}

// assertKeywordQuery checks the keyword query is the keyword-mode search:
// no vector and no floor.
func assertKeywordQuery(t *testing.T, query map[string]json.RawMessage) {
	t.Helper()
	if string(query["q"]) != `"Model Citizen"` || string(query["matchingStrategy"]) != `"all"` {
		t.Fatalf("keyword query q/matchingStrategy = %s/%s", query["q"], query["matchingStrategy"])
	}
	for _, field := range []string{"vector", "hybrid", "rankingScoreThreshold"} {
		if _, present := query[field]; present {
			t.Fatalf("keyword query carries %q", field)
		}
	}
	if weight := federationWeight(t, query); weight != 0.5 {
		t.Fatalf("keyword weight = %v, want 1 - semantic ratio", weight)
	}
}

// assertMeaningQuery checks the meaning-only query ranks by the vector alone,
// above the floor.
func assertMeaningQuery(t *testing.T, query map[string]json.RawMessage, embedder string) {
	t.Helper()
	var hybrid meilisearchHybridRequest
	if err := json.Unmarshal(query["hybrid"], &hybrid); err != nil || hybrid.SemanticRatio != 1 || hybrid.Embedder != embedder {
		t.Fatalf("meaning query hybrid = %s (%v)", query["hybrid"], err)
	}
	var vector []float32
	if err := json.Unmarshal(query["vector"], &vector); err != nil || len(vector) != 3072 {
		t.Fatalf("meaning query vector length = %d (%v)", len(vector), err)
	}
	if string(query["q"]) != `""` {
		t.Fatalf("meaning query q = %s, want empty so Meilisearch skips keyword matching", query["q"])
	}
	if string(query["rankingScoreThreshold"]) != "0.65" {
		t.Fatalf("meaning query rankingScoreThreshold = %s, want 0.65", query["rankingScoreThreshold"])
	}
	if weight := federationWeight(t, query); weight != 0.5 {
		t.Fatalf("meaning weight = %v, want the semantic ratio", weight)
	}
}

func TestMeilisearchSemanticScoreFloor(t *testing.T) {
	full := make([]float32, 3072)
	for i := range full {
		full[i] = 0.01
	}
	// A 1536-dimension model padded with zeros to the canonical 3072.
	half := make([]float32, 3072)
	copy(half, full[:1536])

	for _, tc := range []struct {
		name      string
		vector    []float32
		quantized bool
		want      float64
	}{
		// (1 + 0.3) / 2; zero padding does not change a cosine.
		{name: "full precision", vector: half, want: 0.65},
		// 1 - acos(0.3)/π: the share of sign bits that agree at cosine 0.3.
		{name: "quantized, model fills every dimension", vector: full, quantized: true, want: 0.5970},
		// Padded bits always agree, so only half the bits can disagree.
		{name: "quantized, model fills half", vector: half, quantized: true, want: 0.7985},
		{name: "quantized, empty vector", vector: make([]float32, 3072), quantized: true, want: 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := meilisearchSemanticScoreFloor(tc.vector, tc.quantized); math.Abs(got-tc.want) > 0.0001 {
				t.Fatalf("floor = %.4f, want %.4f", got, tc.want)
			}
		})
	}
}

func federationWeight(t *testing.T, query map[string]json.RawMessage) float64 {
	t.Helper()
	var options struct{ Weight float64 }
	if err := json.Unmarshal(query["federationOptions"], &options); err != nil {
		t.Fatalf("federationOptions = %s: %v", query["federationOptions"], err)
	}
	return options.Weight
}
