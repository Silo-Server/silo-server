package catalog

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestCatalogSearchSettingsFromMapParsesMeilisearchTuning(t *testing.T) {
	settings, err := CatalogSearchSettingsFromMap(map[string]string{
		SearchSettingProvider:                    SearchProviderMeilisearch,
		SearchSettingMeilisearchSyncBatchSize:    "750",
		SearchSettingMeilisearchRebuildBatchSize: "5000",
		SearchSettingMeilisearchRebuildQueue:     "6",
		SearchSettingMeilisearchIndexTypes:       "video,audiobook",
		SearchSettingMeilisearchSemanticEnabled:  "true",
		SearchSettingMeilisearchSemanticRatio:    "0.42",
		SearchSettingMeilisearchEmbedder:         "custom_embedder",
	})
	if err != nil {
		t.Fatalf("CatalogSearchSettingsFromMap returned error: %v", err)
	}
	if settings.SyncBatchSize != 750 {
		t.Fatalf("SyncBatchSize = %d, want 750", settings.SyncBatchSize)
	}
	if settings.RebuildBatchSize != 5000 {
		t.Fatalf("RebuildBatchSize = %d, want 5000", settings.RebuildBatchSize)
	}
	if settings.RebuildQueueDepth != 6 {
		t.Fatalf("RebuildQueueDepth = %d, want 6", settings.RebuildQueueDepth)
	}
	wantTypes := []string{"movie", "series", "audiobook"}
	if !reflect.DeepEqual(settings.IndexTypes, wantTypes) {
		t.Fatalf("IndexTypes = %#v, want %#v", settings.IndexTypes, wantTypes)
	}
	if !settings.SemanticEnabled {
		t.Fatal("SemanticEnabled = false, want true")
	}
	if settings.SemanticRatio != 0.42 {
		t.Fatalf("SemanticRatio = %v, want 0.42", settings.SemanticRatio)
	}
	if settings.Embedder != "custom_embedder" {
		t.Fatalf("Embedder = %q, want custom_embedder", settings.Embedder)
	}
}

func TestCatalogSearchSettingsFromMapRejectsOutOfRangeTuning(t *testing.T) {
	tests := []map[string]string{
		{SearchSettingMeilisearchSyncBatchSize: "0"},
		{SearchSettingMeilisearchRebuildBatchSize: "25001"},
		{SearchSettingMeilisearchRebuildQueue: "0"},
		{SearchSettingMeilisearchIndexTypes: "movie,unknown"},
		{SearchSettingMeilisearchSemanticEnabled: "sometimes"},
		{SearchSettingMeilisearchSemanticRatio: "-0.1"},
		{SearchSettingMeilisearchSemanticRatio: "1.1"},
		{SearchSettingMeilisearchSemanticRatio: "NaN"},
		{SearchSettingMeilisearchEmbedder: "bad.name"},
	}

	for _, values := range tests {
		if _, err := CatalogSearchSettingsFromMap(values); err == nil {
			t.Fatalf("CatalogSearchSettingsFromMap(%v) succeeded, want error", values)
		}
	}
}

func TestActiveCatalogSearchProviderRequiresConfiguredMeilisearch(t *testing.T) {
	if got := ActiveCatalogSearchProvider(DefaultCatalogSearchSettings()); got != SearchProviderPostgres {
		t.Fatalf("default active provider = %q, want postgres", got)
	}
	if got := ActiveCatalogSearchProvider(CatalogSearchSettings{
		Provider: SearchProviderMeilisearch,
	}); got != SearchProviderPostgres {
		t.Fatalf("meilisearch without URL active provider = %q, want postgres", got)
	}
	if got := ActiveCatalogSearchProvider(CatalogSearchSettings{
		Provider:         SearchProviderMeilisearch,
		MeilisearchURL:   "http://localhost:7700",
		MeilisearchIndex: DefaultMeilisearchIndex,
	}); got != SearchProviderMeilisearch {
		t.Fatalf("configured meilisearch active provider = %q, want meilisearch", got)
	}
}

func TestCatalogSearchDocumentVectorsUseEmbedderAndOptOutMissing(t *testing.T) {
	docs := []catalogSearchDocument{
		{ContentID: "movie-1", Title: "One"},
		{ContentID: "movie-2", Title: "Two"},
	}
	count := setCatalogSearchDocumentVectors(docs, map[string][]float32{
		"movie-1": {0.1, 0.2},
	}, "silo_recommendations")
	if count != 1 {
		t.Fatalf("vectorized docs = %d, want 1", count)
	}
	if got := docs[0].Vectors["silo_recommendations"]; !reflect.DeepEqual(got, []float32{0.1, 0.2}) {
		t.Fatalf("doc vector = %#v", got)
	}
	if docs[1].Vectors == nil {
		t.Fatal("missing vector doc should include explicit _vectors opt-out")
	}
	if got, ok := docs[1].Vectors["silo_recommendations"]; !ok || got != nil {
		t.Fatalf("missing vector opt-out = %#v, present=%v; want nil value", got, ok)
	}

	data, err := json.Marshal(docs)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"_vectors"`) {
		t.Fatalf("marshaled docs missing _vectors: %s", data)
	}
	if strings.Count(string(data), `"_vectors"`) != 2 {
		t.Fatalf("marshaled docs should include _vectors for both docs: %s", data)
	}
	if !strings.Contains(string(data), `"silo_recommendations":null`) {
		t.Fatalf("marshaled docs missing null vector opt-out: %s", data)
	}
	if got := catalogSearchVectorDocumentCount(docs); got != 1 {
		t.Fatalf("vector document count = %d, want 1", got)
	}
}

func TestCatalogSearchMeilisearchSettingsOmitEmbeddersWhenSemanticDisabled(t *testing.T) {
	settings := catalogSearchMeilisearchSettings("silo_recommendations", false, false)

	if _, ok := settings["embedders"]; ok {
		t.Fatalf("semantic-disabled settings should not include embedders: %#v", settings["embedders"])
	}
	if _, ok := settings["searchableAttributes"]; !ok {
		t.Fatal("semantic-disabled settings should still configure keyword searchable attributes")
	}
}

func TestCatalogSearchMeilisearchSettingsIncludeEmbeddersWhenSemanticEnabled(t *testing.T) {
	settings := catalogSearchMeilisearchSettings("custom_embedder", true, false)

	embedders, ok := settings["embedders"].(map[string]any)
	if !ok {
		t.Fatalf("embedders = %#v, want map[string]any", settings["embedders"])
	}
	if _, ok := embedders["custom_embedder"]; !ok {
		t.Fatalf("embedders = %#v, want custom_embedder", embedders)
	}
}

func TestCatalogSearchMeilisearchSettingsFilterLibraryIDs(t *testing.T) {
	settings := catalogSearchMeilisearchSettings(DefaultMeilisearchEmbedder, false, false)
	filterable, ok := settings["filterableAttributes"].([]string)
	if !ok || !reflect.DeepEqual(filterable, []string{"type", "library_ids"}) {
		t.Fatalf("filterable attributes = %#v", settings["filterableAttributes"])
	}
}

func TestCatalogSearchDocumentPayloadBatchesSplitVectorDocs(t *testing.T) {
	vector := make([]float32, 128)
	docs := []catalogSearchDocument{
		{ContentID: "movie-1", Vectors: map[string][]float32{DefaultMeilisearchEmbedder: vector}},
		{ContentID: "movie-2", Vectors: map[string][]float32{DefaultMeilisearchEmbedder: vector}},
		{ContentID: "movie-3", Vectors: map[string][]float32{DefaultMeilisearchEmbedder: vector}},
	}
	maxBytes := estimateCatalogSearchDocumentJSONBytes(docs[0]) + 10
	batches := catalogSearchDocumentPayloadBatches(docs, maxBytes)
	if len(batches) != len(docs) {
		t.Fatalf("batch count = %d, want %d", len(batches), len(docs))
	}
	for idx, batch := range batches {
		if len(batch) != 1 || batch[0].ContentID != docs[idx].ContentID {
			t.Fatalf("batch %d = %#v", idx, batch)
		}
	}
}

func TestMeilisearchSearchRequestBuildsKeywordOnlyByDefault(t *testing.T) {
	provider := &MeilisearchSearchProvider{
		config: MeilisearchProviderConfig{
			MatchingStrategy: DefaultMeilisearchMatchingStrategy,
			SemanticRatio:    DefaultMeilisearchSemanticRatio,
			Embedder:         DefaultMeilisearchEmbedder,
		},
	}
	req, fallback := provider.buildMeilisearchSearchRequest(context.Background(), CatalogSearchRequest{
		Query:     "dune",
		ItemTypes: []string{"movie"},
	})
	if fallback != "" {
		t.Fatalf("fallback = %q, want empty", fallback)
	}
	if req.Vector != nil || req.Hybrid != nil {
		t.Fatalf("keyword request should not include vector or hybrid: %#v", req)
	}
	if req.MatchingStrategy != DefaultMeilisearchMatchingStrategy {
		t.Fatalf("matching strategy = %q, want %q", req.MatchingStrategy, DefaultMeilisearchMatchingStrategy)
	}
	if req.Filter != `type = "movie"` {
		t.Fatalf("filter = %q, want movie filter", req.Filter)
	}
}

func TestMeilisearchSearchRequestBuildsHybridWhenSemanticEnabled(t *testing.T) {
	vectorizer := &fakeCatalogSearchVectorizer{vector: []float32{0.5, 0.25}}
	provider := &MeilisearchSearchProvider{
		config: MeilisearchProviderConfig{
			MatchingStrategy: DefaultMeilisearchMatchingStrategy,
			SemanticEnabled:  true,
			SemanticRatio:    0.4,
			Embedder:         "silo_recommendations",
			Vectorizer:       vectorizer,
		},
	}
	req, fallback := provider.buildMeilisearchSearchRequest(context.Background(), CatalogSearchRequest{
		Query: "  found family space opera  ",
	})
	if fallback != "" {
		t.Fatalf("fallback = %q, want empty", fallback)
	}
	if req.Hybrid == nil {
		t.Fatal("hybrid request missing")
	}
	if req.Hybrid.Embedder != "silo_recommendations" {
		t.Fatalf("embedder = %q", req.Hybrid.Embedder)
	}
	if req.Hybrid.SemanticRatio != 0.4 {
		t.Fatalf("semantic ratio = %v, want 0.4", req.Hybrid.SemanticRatio)
	}
	if req.MatchingStrategy != DefaultMeilisearchMatchingStrategy {
		t.Fatalf("long semantic query matching strategy = %q, want %q", req.MatchingStrategy, DefaultMeilisearchMatchingStrategy)
	}
	if req.AttributesToSearchOn != nil {
		t.Fatalf("long semantic query should search all attributes, got %#v", req.AttributesToSearchOn)
	}
	if len(req.Vector) != 3072 {
		t.Fatalf("vector len = %d, want 3072", len(req.Vector))
	}
	if vectorizer.calls != 1 || vectorizer.lastQuery != "found family space opera" {
		t.Fatalf("vectorizer calls/query = %d/%q", vectorizer.calls, vectorizer.lastQuery)
	}

	req, fallback = provider.buildMeilisearchSearchRequest(context.Background(), CatalogSearchRequest{
		Query: "found   family space opera",
	})
	if fallback != "" || req.Hybrid == nil {
		t.Fatalf("cached hybrid fallback/request = %q/%#v", fallback, req.Hybrid)
	}
	if vectorizer.calls != 1 {
		t.Fatalf("cached query should not call vectorizer again, calls = %d", vectorizer.calls)
	}
}

func TestMeilisearchSearchRequestStaysKeywordWhenCoverageNotReady(t *testing.T) {
	vectorizer := &fakeCatalogSearchVectorizer{vector: []float32{0.5, 0.25}}
	provider := &MeilisearchSearchProvider{
		config: MeilisearchProviderConfig{
			MatchingStrategy: DefaultMeilisearchMatchingStrategy,
			SemanticEnabled:  true,
			SemanticRatio:    0.4,
			Embedder:         "silo_recommendations",
			Vectorizer:       vectorizer,
			Coverage:         fakeCoverageGate{ready: false, reason: `type "movie" coverage 40% below threshold`},
		},
	}
	req, fallback := provider.buildMeilisearchSearchRequest(context.Background(), CatalogSearchRequest{
		Query:     "found family space opera",
		ItemTypes: []string{"movie"},
	})
	if req.Vector != nil || req.Hybrid != nil {
		t.Fatalf("not-ready coverage should stay keyword-only: %#v", req)
	}
	if fallback != `semantic_not_ready: type "movie" coverage 40% below threshold` {
		t.Fatalf("fallback = %q, want semantic_not_ready diagnostic", fallback)
	}
	if vectorizer.calls != 0 {
		t.Fatalf("not-ready coverage should not call vectorizer, calls = %d", vectorizer.calls)
	}
}

func TestMeilisearchSearchRequestBuildsHybridWhenCoverageReady(t *testing.T) {
	vectorizer := &fakeCatalogSearchVectorizer{vector: []float32{0.5, 0.25}}
	provider := &MeilisearchSearchProvider{
		config: MeilisearchProviderConfig{
			MatchingStrategy: DefaultMeilisearchMatchingStrategy,
			SemanticEnabled:  true,
			SemanticRatio:    0.4,
			Embedder:         "silo_recommendations",
			Vectorizer:       vectorizer,
			Coverage:         fakeCoverageGate{ready: true},
		},
	}
	req, fallback := provider.buildMeilisearchSearchRequest(context.Background(), CatalogSearchRequest{
		Query:     "found family space opera",
		ItemTypes: []string{"movie"},
	})
	if fallback != "" {
		t.Fatalf("fallback = %q, want empty", fallback)
	}
	if req.Hybrid == nil || req.Vector == nil {
		t.Fatalf("ready coverage should emit hybrid request: %#v", req)
	}
}

func TestMeilisearchEpisodeSearchRemainsKeywordOnly(t *testing.T) {
	vectorizer := &fakeCatalogSearchVectorizer{vector: []float32{0.5, 0.25}}
	provider := &MeilisearchSearchProvider{config: MeilisearchProviderConfig{
		SemanticEnabled: true,
		SemanticRatio:   0.4,
		Embedder:        DefaultMeilisearchEmbedder,
		Vectorizer:      vectorizer,
		Coverage:        fakeCoverageGate{ready: true},
	}}
	req, fallback := provider.buildMeilisearchSearchRequest(context.Background(), CatalogSearchRequest{
		Query:     "Who Are You",
		ItemTypes: []string{"episode"},
		Access:    AccessFilter{AllowedLibraryIDs: []int{4, 9}, DisabledLibraryIDs: []int{12}},
	})
	if fallback != "" || req.Hybrid != nil || req.Vector != nil {
		t.Fatalf("episode request should be keyword-only, fallback/request = %q/%#v", fallback, req)
	}
	for _, want := range []string{`type = "episode"`, "library_ids IN [4, 9]", "library_ids NOT IN [12]"} {
		if !strings.Contains(req.Filter, want) {
			t.Fatalf("episode filter missing %q: %s", want, req.Filter)
		}
	}
	if vectorizer.calls != 0 {
		t.Fatalf("episode request generated a vector, calls=%d", vectorizer.calls)
	}
}

func TestMeilisearchMixedSemanticSearchBuildsFederation(t *testing.T) {
	provider := &MeilisearchSearchProvider{config: MeilisearchProviderConfig{}}
	base := meilisearchSearchRequest{
		Query:                "Who Are You",
		AttributesToRetrieve: []string{"content_id"},
		MatchingStrategy:     "all",
		Vector:               []float32{0.5, 0.25},
		Hybrid:               &meilisearchHybridRequest{Embedder: DefaultMeilisearchEmbedder, SemanticRatio: 0.4},
	}
	federated := provider.buildFederatedSearchRequest("catalog-v3", CatalogSearchRequest{
		Access: AccessFilter{AllowedLibraryIDs: []int{4}},
	}, base, 100, 25)
	if federated.Federation.Offset != 100 || federated.Federation.Limit != 25 {
		t.Fatalf("federation pagination = %#v", federated.Federation)
	}
	if len(federated.Queries) != 2 {
		t.Fatalf("federated queries = %#v", federated.Queries)
	}
	media, episode := federated.Queries[0], federated.Queries[1]
	if media.Hybrid == nil || len(media.Vector) == 0 || !strings.Contains(media.Filter, `type != "episode"`) {
		t.Fatalf("media federation branch = %#v", media)
	}
	if episode.Hybrid != nil || episode.Vector != nil || !strings.Contains(episode.Filter, `type = "episode"`) {
		t.Fatalf("episode federation branch = %#v", episode)
	}
	if media.FederationOptions.Weight != 1 || episode.FederationOptions.Weight != 1 {
		t.Fatalf("federation weights = %v/%v", media.FederationOptions.Weight, episode.FederationOptions.Weight)
	}
}

func TestMeilisearchFederationUnsupportedClassification(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{
			name: "missing index during rebuild swap is transient",
			err: &meilisearchHTTPError{
				StatusCode: http.StatusNotFound,
				Code:       "index_not_found",
				Message:    "Index catalog_rebuild_old not found.",
			},
			want: false,
		},
		{
			name: "missing multi-search route is unsupported",
			err: &meilisearchHTTPError{
				StatusCode: http.StatusNotFound,
				Code:       "not_found",
				Message:    "The multi-search route does not exist.",
			},
			want: true,
		},
		{
			name: "older server rejects federation field",
			err: &meilisearchHTTPError{
				StatusCode: http.StatusBadRequest,
				Code:       "bad_request",
				Message:    "Unknown field federation: expected queries.",
			},
			want: true,
		},
		{
			name: "invalid federated request is not a capability result",
			err: &meilisearchHTTPError{
				StatusCode: http.StatusBadRequest,
				Code:       "invalid_multi_search_query_federated",
				Message:    "A query has federationOptions but federation is missing.",
			},
			want: false,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := isMeilisearchFederationUnsupported(test.err); got != test.want {
				t.Fatalf("isMeilisearchFederationUnsupported(%v) = %t, want %t", test.err, got, test.want)
			}
		})
	}
}

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

func TestCatalogSearchEpisodeDocumentsNeverCarryVectors(t *testing.T) {
	docs := []catalogSearchDocument{
		{ContentID: "movie-1", Type: "movie"},
		{ContentID: "episode-1", Type: "episode"},
	}
	count := setCatalogSearchDocumentVectors(docs, map[string][]float32{
		"movie-1":   {0.1, 0.2},
		"episode-1": {0.3, 0.4},
	}, DefaultMeilisearchEmbedder)
	if count != 1 || docs[0].Vectors == nil {
		t.Fatalf("media vector assignment = count %d, vectors %#v", count, docs[0].Vectors)
	}
	// Episodes must carry the explicit `_vectors.<embedder>: null` opt-out:
	// omitting _vectors entirely fails indexing under a userProvided embedder.
	episodeVector, ok := docs[1].Vectors[DefaultMeilisearchEmbedder]
	if !ok || episodeVector != nil {
		t.Fatalf("episode vectors = %#v, want explicit nil opt-out", docs[1].Vectors)
	}
}

func TestSemanticModelProviderUsesCommaOkSafety(t *testing.T) {
	if got := semanticModelProvider(nil); got != nil {
		t.Fatalf("nil vectorizer should yield nil model provider, got %#v", got)
	}
	// A vectorizer that does NOT implement CatalogSemanticModelProvider must not
	// be asserted into the interface (a nil-interface assertion would panic).
	if got := semanticModelProvider(&fakeCatalogSearchVectorizer{}); got != nil {
		t.Fatalf("non-implementing vectorizer should yield nil, got %#v", got)
	}
	impl := &fakeModelVectorizer{}
	if got := semanticModelProvider(impl); got != CatalogSemanticModelProvider(impl) {
		t.Fatalf("implementing vectorizer should yield itself, got %#v", got)
	}
}

func TestMeilisearchSearchRequestBuildsHybridForApproximateInteractiveSearch(t *testing.T) {
	vectorizer := &fakeCatalogSearchVectorizer{vector: []float32{0.5, 0.25}}
	provider := &MeilisearchSearchProvider{
		config: MeilisearchProviderConfig{
			MatchingStrategy: DefaultMeilisearchMatchingStrategy,
			SemanticEnabled:  true,
			SemanticRatio:    0.4,
			Embedder:         "silo_recommendations",
			Vectorizer:       vectorizer,
		},
	}
	req, fallback := provider.buildMeilisearchSearchRequest(context.Background(), CatalogSearchRequest{
		Query:     "found family space opera",
		SkipTotal: true,
	})
	if fallback != "" {
		t.Fatalf("fallback = %q, want empty", fallback)
	}
	if req.Vector == nil || req.Hybrid == nil {
		t.Fatalf("approximate interactive search should include hybrid search: %#v", req)
	}
	if vectorizer.calls != 1 {
		t.Fatalf("interactive search should call vectorizer once, calls = %d", vectorizer.calls)
	}
}

func TestMeilisearchSearchRequestBuildsHybridAndStrictMatchingForTwoTermSearch(t *testing.T) {
	vectorizer := &fakeCatalogSearchVectorizer{vector: []float32{0.5, 0.25}}
	provider := &MeilisearchSearchProvider{
		config: MeilisearchProviderConfig{
			MatchingStrategy: DefaultMeilisearchMatchingStrategy,
			SemanticEnabled:  true,
			SemanticRatio:    0.3,
			Embedder:         "silo_recommendations",
			Vectorizer:       vectorizer,
		},
	}
	req, fallback := provider.buildMeilisearchSearchRequest(context.Background(), CatalogSearchRequest{
		Query: "spnge bob",
	})
	if fallback != "" {
		t.Fatalf("fallback = %q, want empty", fallback)
	}
	if req.Vector == nil || req.Hybrid == nil {
		t.Fatalf("two-term title search should include hybrid search: %#v", req)
	}
	if req.MatchingStrategy != "all" {
		t.Fatalf("two-term title search matching strategy = %q, want all", req.MatchingStrategy)
	}
	if !reflect.DeepEqual(req.AttributesToSearchOn, meilisearchTitleSearchAttributes) {
		t.Fatalf("two-term title search attributes = %#v, want %#v", req.AttributesToSearchOn, meilisearchTitleSearchAttributes)
	}
	if vectorizer.calls != 1 || vectorizer.lastQuery != "spnge bob" {
		t.Fatalf("vectorizer calls/query = %d/%q", vectorizer.calls, vectorizer.lastQuery)
	}
}

func TestMeilisearchSearchRequestUsesStrictMatchingWhenTwoTermHybridNotReady(t *testing.T) {
	vectorizer := &fakeCatalogSearchVectorizer{vector: []float32{0.5, 0.25}}
	provider := &MeilisearchSearchProvider{
		config: MeilisearchProviderConfig{
			MatchingStrategy: DefaultMeilisearchMatchingStrategy,
			SemanticEnabled:  true,
			SemanticRatio:    0.3,
			Embedder:         "silo_recommendations",
			Vectorizer:       vectorizer,
			Coverage:         fakeCoverageGate{ready: false, reason: `type "movie" coverage 40% below threshold`},
		},
	}
	req, fallback := provider.buildMeilisearchSearchRequest(context.Background(), CatalogSearchRequest{
		Query:     "spnge bob",
		ItemTypes: []string{"movie"},
	})
	if req.Vector != nil || req.Hybrid != nil {
		t.Fatalf("not-ready coverage should stay keyword-only: %#v", req)
	}
	if req.MatchingStrategy != "all" {
		t.Fatalf("not-ready two-term search matching strategy = %q, want all", req.MatchingStrategy)
	}
	if !reflect.DeepEqual(req.AttributesToSearchOn, meilisearchTitleSearchAttributes) {
		t.Fatalf("not-ready two-term search attributes = %#v, want %#v", req.AttributesToSearchOn, meilisearchTitleSearchAttributes)
	}
	if fallback != `semantic_not_ready: type "movie" coverage 40% below threshold` {
		t.Fatalf("fallback = %q, want semantic_not_ready diagnostic", fallback)
	}
	if vectorizer.calls != 0 {
		t.Fatalf("not-ready coverage should not call vectorizer, calls = %d", vectorizer.calls)
	}
}

func TestMeilisearchSearchRequestSkipsHybridForSingleTermTitleSearch(t *testing.T) {
	vectorizer := &fakeCatalogSearchVectorizer{vector: []float32{0.5, 0.25}}
	provider := &MeilisearchSearchProvider{
		config: MeilisearchProviderConfig{
			MatchingStrategy: DefaultMeilisearchMatchingStrategy,
			SemanticEnabled:  true,
			SemanticRatio:    0.4,
			Embedder:         "silo_recommendations",
			Vectorizer:       vectorizer,
		},
	}
	for _, query := range []string{"sponge", "spongebob"} {
		req, fallback := provider.buildMeilisearchSearchRequest(context.Background(), CatalogSearchRequest{
			Query: query,
		})
		if fallback != "" {
			t.Fatalf("fallback for %q = %q, want empty", query, fallback)
		}
		if req.Vector != nil || req.Hybrid != nil {
			t.Fatalf("short title search %q should stay keyword-only: %#v", query, req)
		}
		if req.MatchingStrategy != DefaultMeilisearchMatchingStrategy {
			t.Fatalf("single-term search %q matching strategy = %q, want %q", query, req.MatchingStrategy, DefaultMeilisearchMatchingStrategy)
		}
		if req.AttributesToSearchOn != nil {
			t.Fatalf("single-term search %q should search all attributes, got %#v", query, req.AttributesToSearchOn)
		}
	}
	if vectorizer.calls != 0 {
		t.Fatalf("short title searches should not call vectorizer, calls = %d", vectorizer.calls)
	}
}

func TestMeilisearchSearchRequestFallsBackWhenEmbeddingFails(t *testing.T) {
	provider := &MeilisearchSearchProvider{
		config: MeilisearchProviderConfig{
			MatchingStrategy: DefaultMeilisearchMatchingStrategy,
			SemanticEnabled:  true,
			SemanticRatio:    DefaultMeilisearchSemanticRatio,
			Embedder:         DefaultMeilisearchEmbedder,
			Vectorizer:       &fakeCatalogSearchVectorizer{err: errors.New("embedding offline")},
		},
	}
	req, fallback := provider.buildMeilisearchSearchRequest(context.Background(), CatalogSearchRequest{
		Query: "something vague atmospheric",
	})
	if req.Vector != nil || req.Hybrid != nil {
		t.Fatalf("fallback request should be keyword-only: %#v", req)
	}
	if !strings.Contains(fallback, "semantic query embedding failed") {
		t.Fatalf("fallback = %q, want semantic query failure", fallback)
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
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"hits":[],"estimatedTotalHits":0}`))
	}))
	defer server.Close()

	client, err := newMeilisearchClient(server.URL, "", time.Second)
	if err != nil {
		t.Fatalf("newMeilisearchClient: %v", err)
	}
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
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		// 404 models the cached index having been deleted by a rebuild's
		// cleanup; it must not trip the circuit, only the state cache.
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"message":"Index search-index not found.","code":"index_not_found"}`))
	}))
	defer server.Close()

	client, err := newMeilisearchClient(server.URL, "", time.Second)
	if err != nil {
		t.Fatalf("newMeilisearchClient: %v", err)
	}
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
	if store.getStateCalls != 2 {
		t.Fatalf("getStateCalls = %d, want 2 (cache invalidated after failed search)", store.getStateCalls)
	}
	if reason, blocked := provider.circuitBlocked(time.Now()); blocked {
		t.Fatalf("HTTP 404 must not trip the circuit, got open circuit: %s", reason)
	}
}

func TestMeilisearchProviderUsesActiveIndexWhenPendingUpdatesExist(t *testing.T) {
	requests := 0
	var gotMethod, gotPath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		gotMethod = r.Method
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"hits":[],"estimatedTotalHits":0}`))
	}))
	defer server.Close()

	client, err := newMeilisearchClient(server.URL, "", time.Second)
	if err != nil {
		t.Fatalf("newMeilisearchClient: %v", err)
	}

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

func TestMeilisearchProviderAllowsUnscopedIndexForAnyRequest(t *testing.T) {
	provider := &MeilisearchSearchProvider{}
	for _, itemTypes := range [][]string{nil, []string{"movie"}, []string{"audiobook", "ebook"}} {
		if !provider.indexCoversRequest(itemTypes) {
			t.Fatalf("unscoped index should cover request %#v", itemTypes)
		}
	}
}

func TestNormalizeCatalogSearchIndexTypesValueFormatsCanonicalList(t *testing.T) {
	itemTypes, err := NormalizeCatalogSearchIndexTypesValue("video, movie, ebook")
	if err != nil {
		t.Fatalf("NormalizeCatalogSearchIndexTypesValue returned error: %v", err)
	}
	want := "movie,series,ebook"
	if got := FormatCatalogSearchIndexTypesValue(itemTypes); got != want {
		t.Fatalf("formatted index types = %q, want %q", got, want)
	}
}

func TestMeilisearchSchemaVersionChangesWithEmbedder(t *testing.T) {
	defaultVersion := catalogSearchMeilisearchSchemaVersion(DefaultMeilisearchEmbedder, nil, false, false)
	customVersion := catalogSearchMeilisearchSchemaVersion("custom_embedder", nil, false, false)
	if defaultVersion == customVersion {
		t.Fatal("schema version should change when embedder changes")
	}
	if defaultVersion/1_000_000 != SearchMeilisearchSchemaVersion {
		t.Fatalf("base schema version = %d, want %d", defaultVersion/1_000_000, SearchMeilisearchSchemaVersion)
	}
}

func TestMeilisearchSchemaVersionChangesWithIndexTypes(t *testing.T) {
	allTypesVersion := catalogSearchMeilisearchSchemaVersion(DefaultMeilisearchEmbedder, nil, false, false)
	videoOnlyVersion := catalogSearchMeilisearchSchemaVersion(DefaultMeilisearchEmbedder, []string{"movie", "series"}, false, false)
	if allTypesVersion == videoOnlyVersion {
		t.Fatal("schema version should change when indexed media scope changes")
	}
}

func TestMeilisearchSchemaVersionChangesWithSemanticEnabled(t *testing.T) {
	// Toggling semantic search must change the expected schema version so a
	// previously built (vector-less) index is treated as stale and forced to
	// rebuild. Without this, enabling semantic without a rebuild leaves indexed
	// documents missing _vectors while the Postgres coverage gate reports ready,
	// silently degrading hybrid ranking.
	disabledVersion := catalogSearchMeilisearchSchemaVersion(DefaultMeilisearchEmbedder, nil, false, false)
	enabledVersion := catalogSearchMeilisearchSchemaVersion(DefaultMeilisearchEmbedder, nil, true, false)
	if disabledVersion == enabledVersion {
		t.Fatal("schema version should change when semantic search is toggled")
	}
}

func TestPostgresSearchProviderNilRepoStillErrors(t *testing.T) {
	provider := NewPostgresSearchProvider(nil)
	if _, err := provider.Search(context.Background(), CatalogSearchRequest{}); err == nil {
		t.Fatal("nil item repo should still return an error")
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

// fakeModelVectorizer implements both CatalogSearchQueryVectorizer and
// CatalogSemanticModelProvider so semanticModelProvider's comma-ok assertion can
// be verified against a type that does satisfy the model interface.
type fakeModelVectorizer struct {
	fakeCatalogSearchVectorizer
	model string
}

func (f *fakeModelVectorizer) ActiveEmbeddingModel(_ context.Context) (string, error) {
	return f.model, nil
}

func TestCatalogSearchMeilisearchSchemaVersionBinaryQuantized(t *testing.T) {
	plain := catalogSearchMeilisearchSchemaVersion(DefaultMeilisearchEmbedder, nil, true, false)
	quantized := catalogSearchMeilisearchSchemaVersion(DefaultMeilisearchEmbedder, nil, true, true)
	if plain == quantized {
		t.Fatal("binary quantization must change the schema version")
	}
}

func TestCatalogSearchMeilisearchSchemaVersionBinaryQuantizedIgnoredWhenSemanticDisabled(t *testing.T) {
	// With semantic search off, the index carries no embedders (see
	// catalogSearchMeilisearchSettings), so binary quantization has no on-index
	// effect and must not shift the schema version — otherwise toggling it would
	// force a pointless full rebuild of a vector-less index. It must also stay
	// byte-identical to a pre-flag index so upgrades don't spuriously invalidate.
	off := catalogSearchMeilisearchSchemaVersion(DefaultMeilisearchEmbedder, nil, false, false)
	on := catalogSearchMeilisearchSchemaVersion(DefaultMeilisearchEmbedder, nil, false, true)
	if off != on {
		t.Fatal("binary quantization must not change the schema version when semantic search is disabled")
	}
}

func TestCatalogSearchMeilisearchEmbedderSettingsBinaryQuantized(t *testing.T) {
	off := catalogSearchMeilisearchEmbedderSettings("e", false)["e"].(map[string]any)
	if _, ok := off["binaryQuantized"]; ok {
		t.Fatal("binaryQuantized key must be absent when off (preserves legacy index settings shape)")
	}
	on := catalogSearchMeilisearchEmbedderSettings("e", true)["e"].(map[string]any)
	if on["binaryQuantized"] != true {
		t.Fatal("binaryQuantized must be set when enabled")
	}
}
