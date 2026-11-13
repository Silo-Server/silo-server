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

// Issue #3963: with semantic search on, the meaning half of a hybrid search
// ranked every item with a vector, so every search of two or more words
// returned the whole library. The search now runs as a keyword query plus a
// meaning-only query, and only the meaning-only query has a score floor, so
// weak keyword matches stay while unrelated meaning matches go.
func TestMeilisearchHybridSearchFloorsOnlyMeaningMatches(t *testing.T) {
	var mu sync.Mutex
	bodies := map[string][]byte{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "/settings") {
			_, _ = w.Write([]byte(`{"pagination":{"maxTotalHits":1000}}`))
			return
		}
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		bodies[r.URL.Path] = body
		mu.Unlock()
		_, _ = w.Write([]byte(`{"hits":[],"estimatedTotalHits":0}`))
	}))
	defer server.Close()
	// sentBody returns a request body the fake server recorded, and the paths
	// it saw for failure messages.
	sentBody := func(path string) ([]byte, []string) {
		mu.Lock()
		defer mu.Unlock()
		paths := make([]string, 0, len(bodies))
		for seen := range bodies {
			paths = append(paths, seen)
		}
		return bodies[path], paths
	}

	itemTypes := []string{"movie", "series"}
	provider, err := NewMeilisearchSearchProvider(nil, nil, nil, MeilisearchProviderConfig{
		URL:             server.URL,
		IndexTypes:      itemTypes,
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

	result, err := provider.Search(t.Context(), CatalogSearchRequest{
		CursorPaging: true, Query: "Model Citizen", ItemTypes: itemTypes, Limit: 60,
	})
	if err != nil {
		t.Fatalf("hybrid search: %v", err)
	}
	if result.Mode != searchModeHybrid || !result.SemanticUsed || result.FallbackReason != "" {
		t.Fatalf("mode/semantic/fallback = %q/%v/%q, want hybrid without fallback", result.Mode, result.SemanticUsed, result.FallbackReason)
	}
	federated, paths := sentBody("/multi-search")
	if federated == nil {
		t.Fatalf("hybrid search did not federate; requests = %v", paths)
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
	keyword, semantic := sent.Queries[0], sent.Queries[1]

	// The keyword query is the keyword-mode search: no vector and no floor.
	if string(keyword["q"]) != `"Model Citizen"` || string(keyword["matchingStrategy"]) != `"all"` {
		t.Fatalf("keyword query = %s", federated)
	}
	for _, field := range []string{"vector", "hybrid", "rankingScoreThreshold"} {
		if _, present := keyword[field]; present {
			t.Fatalf("keyword query carries %q: %s", field, federated)
		}
	}
	if weight := federationWeight(t, keyword); weight != 0.5 {
		t.Fatalf("keyword weight = %v, want 1 - semantic ratio", weight)
	}

	// The meaning-only query ranks by the vector alone, above the floor.
	var hybrid meilisearchHybridRequest
	if err := json.Unmarshal(semantic["hybrid"], &hybrid); err != nil || hybrid.SemanticRatio != 1 || hybrid.Embedder != provider.config.Embedder {
		t.Fatalf("meaning query hybrid = %s (%v)", semantic["hybrid"], err)
	}
	var vector []float32
	if err := json.Unmarshal(semantic["vector"], &vector); err != nil || len(vector) != 3072 {
		t.Fatalf("meaning query vector length = %d (%v)", len(vector), err)
	}
	if string(semantic["q"]) != `""` {
		t.Fatalf("meaning query q = %s, want empty so Meilisearch skips keyword matching", semantic["q"])
	}
	var threshold float64
	if err := json.Unmarshal(semantic["rankingScoreThreshold"], &threshold); err != nil || threshold != 0.65 {
		t.Fatalf("meaning query rankingScoreThreshold = %s, want 0.65", semantic["rankingScoreThreshold"])
	}
	if weight := federationWeight(t, semantic); weight != 0.5 {
		t.Fatalf("meaning weight = %v, want the semantic ratio", weight)
	}

	// Offset paging (jellycompat search) sends the same floored federation.
	mu.Lock()
	clear(bodies)
	mu.Unlock()
	result, err = provider.Search(t.Context(), CatalogSearchRequest{
		Query: "past lives", ItemTypes: itemTypes, Limit: 20,
	})
	if err != nil || result.Mode != searchModeHybrid {
		t.Fatalf("offset hybrid search = %+v, %v; want hybrid mode", result, err)
	}
	if offsetBody, paths := sentBody("/multi-search"); !strings.Contains(string(offsetBody), `"rankingScoreThreshold":0.65`) {
		t.Fatalf("offset hybrid search sent %s; requests = %v", offsetBody, paths)
	}

	// A one-word search stays a plain keyword search with no floor.
	result, err = provider.Search(t.Context(), CatalogSearchRequest{
		CursorPaging: true, Query: "Conclave", ItemTypes: itemTypes, Limit: 60,
	})
	if err != nil || result.Mode != searchModeKeyword {
		t.Fatalf("one-word search = %+v, %v; want keyword mode", result, err)
	}
	plain, paths := sentBody("/indexes/catalog_hybrid/search")
	if plain == nil {
		t.Fatalf("one-word search did not use /search; requests = %v", paths)
	}
	for _, field := range []string{`"vector"`, `"hybrid"`, `"rankingScoreThreshold"`} {
		if strings.Contains(string(plain), field) {
			t.Fatalf("keyword search carries %s: %s", field, plain)
		}
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
