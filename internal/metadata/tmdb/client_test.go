package tmdb

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestNewClientUsesProjectAPIKeyWhenEmpty(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("api_key"); got != projectAPIKey {
			t.Fatalf("api_key query = %q, want project API key", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"page":1,"total_pages":1,"total_results":0,"results":[]}`))
	}))
	defer server.Close()

	client := NewClient("", 1000)
	client.SetBaseURL(server.URL)

	if _, err := client.GetCollectionPreset(context.Background(), "trending", "all", "day", 10); err != nil {
		t.Fatalf("GetCollectionPreset returned error: %v", err)
	}
}

func TestGetCollectionPresetTrending(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/trending/all/day" {
			http.NotFound(w, r)
			return
		}
		if got := r.URL.Query().Get("page"); got != "1" {
			t.Fatalf("page query = %q, want 1", got)
		}
		if got := r.URL.Query().Get("api_key"); got != "test-key" {
			t.Fatalf("api_key query = %q, want test-key", got)
		}

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"page": 1,
			"total_pages": 1,
			"total_results": 2,
			"results": [
				{"id": 10, "media_type": "movie", "title": "Movie Title"},
				{"id": 20, "media_type": "tv", "name": "Series Title"}
			]
		}`))
	}))
	defer server.Close()

	client := NewClient("test-key", 1000)
	client.SetBaseURL(server.URL)

	results, err := client.GetCollectionPreset(context.Background(), "trending", "all", "day", 10)
	if err != nil {
		t.Fatalf("GetCollectionPreset returned error: %v", err)
	}

	if len(results) != 2 {
		t.Fatalf("len(results) = %d, want 2", len(results))
	}
	if results[0] != (CollectionResult{ID: 10, MediaType: "movie", Title: "Movie Title"}) {
		t.Fatalf("results[0] = %+v", results[0])
	}
	if results[1] != (CollectionResult{ID: 20, MediaType: "tv", Title: "Series Title"}) {
		t.Fatalf("results[1] = %+v", results[1])
	}
}

func TestDiscoverSectionCachesSuccess(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.Path != "/movie/popular" {
			http.NotFound(w, r)
			return
		}
		if got := r.URL.Query().Get("page"); got != "1" {
			t.Fatalf("page query = %q, want 1", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"page": 1,
			"total_pages": 1,
			"total_results": 1,
			"results": [
				{"id": 11, "title": "Cached Movie", "overview": "from tmdb"}
			]
		}`))
	}))
	defer server.Close()

	client := NewClient("test-key", 1000)
	client.SetBaseURL(server.URL)

	first, err := client.DiscoverSection(context.Background(), "popular_movies", 1)
	if err != nil {
		t.Fatalf("first DiscoverSection returned error: %v", err)
	}
	second, err := client.DiscoverSection(context.Background(), "popular_movies", 1)
	if err != nil {
		t.Fatalf("second DiscoverSection returned error: %v", err)
	}

	if got := calls.Load(); got != 1 {
		t.Fatalf("upstream calls = %d, want 1", got)
	}
	for name, page := range map[string]*MediaPage{"first": first, "second": second} {
		if page == nil || len(page.Results) != 1 {
			t.Fatalf("%s page results = %#v, want one result", name, page)
		}
		if page.Results[0].ID != 11 || page.Results[0].Title != "Cached Movie" {
			t.Fatalf("%s result = %+v", name, page.Results[0])
		}
	}
}

func TestDiscoverSectionCacheKeyIncludesPage(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.Path != "/movie/popular" {
			http.NotFound(w, r)
			return
		}
		page := r.URL.Query().Get("page")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"page": ` + page + `,
			"total_pages": 2,
			"total_results": 2,
			"results": [
				{"id": ` + page + `, "title": "Movie ` + page + `"}
			]
		}`))
	}))
	defer server.Close()

	client := NewClient("test-key", 1000)
	client.SetBaseURL(server.URL)

	first, err := client.DiscoverSection(context.Background(), "popular_movies", 1)
	if err != nil {
		t.Fatalf("page 1 DiscoverSection returned error: %v", err)
	}
	second, err := client.DiscoverSection(context.Background(), "popular_movies", 2)
	if err != nil {
		t.Fatalf("page 2 DiscoverSection returned error: %v", err)
	}

	if got := calls.Load(); got != 2 {
		t.Fatalf("upstream calls = %d, want 2", got)
	}
	if first.Results[0].ID != 1 || second.Results[0].ID != 2 {
		t.Fatalf("cached pages collapsed unexpectedly: first=%+v second=%+v", first.Results[0], second.Results[0])
	}
}

func TestDiscoverSectionDoesNotCacheClientErrors(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		call := calls.Add(1)
		if r.URL.Path != "/movie/popular" {
			http.NotFound(w, r)
			return
		}
		if call == 1 {
			http.Error(w, `{"status_message":"bad section"}`, http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"page": 1,
			"total_pages": 1,
			"total_results": 1,
			"results": [
				{"id": 22, "title": "Recovered Movie"}
			]
		}`))
	}))
	defer server.Close()

	client := NewClient("test-key", 1000)
	client.SetBaseURL(server.URL)

	if _, err := client.DiscoverSection(context.Background(), "popular_movies", 1); err == nil {
		t.Fatal("first DiscoverSection expected error")
	}
	page, err := client.DiscoverSection(context.Background(), "popular_movies", 1)
	if err != nil {
		t.Fatalf("second DiscoverSection returned error: %v", err)
	}
	if got := calls.Load(); got != 2 {
		t.Fatalf("upstream calls = %d, want 2", got)
	}
	if page == nil || len(page.Results) != 1 || page.Results[0].ID != 22 {
		t.Fatalf("second page = %#v, want recovered movie", page)
	}
}

func TestDiscoverSectionReturnsClonedCachedPage(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.Path != "/movie/popular" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"page": 1,
			"total_pages": 1,
			"total_results": 1,
			"results": [
				{"id": 33, "title": "Immutable Movie"}
			]
		}`))
	}))
	defer server.Close()

	client := NewClient("test-key", 1000)
	client.SetBaseURL(server.URL)

	first, err := client.DiscoverSection(context.Background(), "popular_movies", 1)
	if err != nil {
		t.Fatalf("first DiscoverSection returned error: %v", err)
	}
	first.Results[0].Title = "mutated by caller"

	second, err := client.DiscoverSection(context.Background(), "popular_movies", 1)
	if err != nil {
		t.Fatalf("second DiscoverSection returned error: %v", err)
	}

	if got := calls.Load(); got != 1 {
		t.Fatalf("upstream calls = %d, want 1", got)
	}
	if second.Results[0].Title != "Immutable Movie" {
		t.Fatalf("cached title = %q, want Immutable Movie", second.Results[0].Title)
	}
}

func TestGetExternalIDsCachesSuccess(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.Path != "/movie/123/external_ids" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"imdb_id":"tt123","tvdb_id":456}`))
	}))
	defer server.Close()

	client := NewClient("test-key", 1000)
	client.SetBaseURL(server.URL)

	first, err := client.GetExternalIDs(context.Background(), "movie", 123)
	if err != nil {
		t.Fatalf("first GetExternalIDs returned error: %v", err)
	}
	second, err := client.GetExternalIDs(context.Background(), "movie", 123)
	if err != nil {
		t.Fatalf("second GetExternalIDs returned error: %v", err)
	}

	if got := calls.Load(); got != 1 {
		t.Fatalf("upstream calls = %d, want 1", got)
	}
	if first.IMDbID != "tt123" || first.TVDBID != 456 || second.IMDbID != "tt123" || second.TVDBID != 456 {
		t.Fatalf("external IDs = first %+v second %+v", first, second)
	}
}

func TestRefreshExternalIDsBypassesCache(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := calls.Add(1)
		if r.URL.Path != "/tv/77/external_ids" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if n == 1 {
			_, _ = w.Write([]byte(`{"imdb_id":"tt77"}`))
			return
		}
		_, _ = w.Write([]byte(`{"imdb_id":"tt77","tvdb_id":456}`))
	}))
	defer server.Close()

	client := NewClient("test-key", 1000)
	client.SetBaseURL(server.URL)

	if _, err := client.GetExternalIDs(context.Background(), "tv", 77); err != nil {
		t.Fatalf("GetExternalIDs returned error: %v", err)
	}
	refreshed, err := client.RefreshExternalIDs(context.Background(), "tv", 77)
	if err != nil {
		t.Fatalf("RefreshExternalIDs returned error: %v", err)
	}
	if calls.Load() != 2 || refreshed.TVDBID != 456 {
		t.Fatalf("upstream calls = %d, refreshed = %+v; want a second fetch with the new TVDB ID", calls.Load(), refreshed)
	}
	cached, err := client.GetExternalIDs(context.Background(), "tv", 77)
	if err != nil || cached.TVDBID != 456 || calls.Load() != 2 {
		t.Fatalf("GetExternalIDs after refresh = %+v, %v (calls %d); want the refreshed value from cache", cached, err, calls.Load())
	}
}

func TestDiscoverMovieAppliesFilters(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/discover/movie" {
			http.NotFound(w, r)
			return
		}
		calls++
		q := r.URL.Query()
		if got := q.Get("sort_by"); got != "popularity.desc" {
			t.Errorf("sort_by = %q, want popularity.desc", got)
		}
		if got := q.Get("with_genres"); got != "28,12" {
			t.Errorf("with_genres = %q, want 28,12", got)
		}
		if got := q.Get("without_genres"); got != "99" {
			t.Errorf("without_genres = %q, want 99", got)
		}
		if got := q.Get("vote_count.gte"); got != "300" {
			t.Errorf("vote_count.gte = %q, want 300", got)
		}
		if got := q.Get("vote_average.gte"); got != "6.5" {
			t.Errorf("vote_average.gte = %q, want 6.5", got)
		}
		if got := q.Get("primary_release_date.gte"); got != "2020-01-01" {
			t.Errorf("primary_release_date.gte = %q, want 2020-01-01", got)
		}
		if got := q.Get("primary_release_date.lte"); got != "2025-12-31" {
			t.Errorf("primary_release_date.lte = %q, want 2025-12-31", got)
		}
		if got := q.Get("certification_country"); got != "US" {
			t.Errorf("certification_country = %q, want US", got)
		}
		if got := q.Get("certification"); got != "PG|PG-13" {
			t.Errorf("certification = %q, want PG|PG-13", got)
		}
		if got := q.Get("with_runtime.gte"); got != "90" {
			t.Errorf("with_runtime.gte = %q, want 90", got)
		}
		if got := q.Get("with_runtime.lte"); got != "180" {
			t.Errorf("with_runtime.lte = %q, want 180", got)
		}
		if got := q.Get("with_original_language"); got != "en" {
			t.Errorf("with_original_language = %q, want en", got)
		}
		if got := q.Get("api_key"); got != "test-key" {
			t.Errorf("api_key = %q, want test-key", got)
		}
		if got := q.Get("page"); got != "1" {
			t.Errorf("page = %q, want 1", got)
		}

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"page": 1,
			"total_pages": 1,
			"total_results": 2,
			"results": [
				{"id": 11, "title": "First Movie"},
				{"id": 22, "title": "Second Movie"}
			]
		}`))
	}))
	defer server.Close()

	client := NewClient("test-key", 1000)
	client.SetBaseURL(server.URL)

	results, err := client.Discover(context.Background(), "movie", DiscoverParams{
		SortBy:           "popularity.desc",
		WithGenres:       []int{28, 12},
		WithoutGenres:    []int{99},
		VoteCountGte:     300,
		VoteAverageGte:   6.5,
		ReleaseDateGte:   "2020-01-01",
		ReleaseDateLte:   "2025-12-31",
		Certifications:   []string{"PG", "PG-13"},
		WithRuntimeGte:   90,
		WithRuntimeLte:   180,
		OriginalLanguage: "en",
		Limit:            10,
	})
	if err != nil {
		t.Fatalf("Discover returned error: %v", err)
	}
	if calls != 1 {
		t.Fatalf("expected 1 server call, got %d", calls)
	}
	if len(results) != 2 {
		t.Fatalf("len(results) = %d, want 2", len(results))
	}
	if results[0] != (CollectionResult{ID: 11, MediaType: "movie", Title: "First Movie"}) {
		t.Errorf("results[0] = %+v", results[0])
	}
	if results[1] != (CollectionResult{ID: 22, MediaType: "movie", Title: "Second Movie"}) {
		t.Errorf("results[1] = %+v", results[1])
	}
}

func TestDiscoverTVUsesFirstAirDate(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/discover/tv" {
			http.NotFound(w, r)
			return
		}
		q := r.URL.Query()
		if got := q.Get("sort_by"); got != "vote_average.desc" {
			t.Errorf("sort_by = %q, want vote_average.desc", got)
		}
		if got := q.Get("first_air_date.gte"); got != "2010-01-01" {
			t.Errorf("first_air_date.gte = %q, want 2010-01-01", got)
		}
		if got := q.Get("first_air_date.lte"); got != "2020-01-01" {
			t.Errorf("first_air_date.lte = %q, want 2020-01-01", got)
		}
		// TV requests must NOT carry primary_release_date.* params.
		if got := q.Get("primary_release_date.gte"); got != "" {
			t.Errorf("primary_release_date.gte should be empty for tv, got %q", got)
		}

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"page": 1,
			"total_pages": 1,
			"total_results": 1,
			"results": [
				{"id": 99, "name": "Some Show"}
			]
		}`))
	}))
	defer server.Close()

	client := NewClient("test-key", 1000)
	client.SetBaseURL(server.URL)

	results, err := client.Discover(context.Background(), "tv", DiscoverParams{
		SortBy:         "vote_average.desc",
		ReleaseDateGte: "2010-01-01",
		ReleaseDateLte: "2020-01-01",
		Limit:          5,
	})
	if err != nil {
		t.Fatalf("Discover tv: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("len(results) = %d, want 1", len(results))
	}
	if results[0] != (CollectionResult{ID: 99, MediaType: "tv", Title: "Some Show"}) {
		t.Errorf("results[0] = %+v", results[0])
	}
}

func TestDiscoverIncludesCompaniesAndNetworks(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if got := q.Get("with_companies"); got != "420,2" {
			t.Errorf("with_companies = %q, want 420,2", got)
		}
		if got := q.Get("with_networks"); got != "213,49" {
			t.Errorf("with_networks = %q, want 213,49", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"page":1,"total_pages":1,"total_results":0,"results":[]}`))
	}))
	defer server.Close()

	client := NewClient("test-key", 1000)
	client.SetBaseURL(server.URL)

	_, err := client.Discover(context.Background(), "movie", DiscoverParams{
		SortBy:        "popularity.desc",
		WithCompanies: []int{420, 2},
		WithNetworks:  []int{213, 49},
		Limit:         5,
	})
	if err != nil {
		t.Fatalf("Discover returned error: %v", err)
	}
}

func TestDiscoverRejectsInvalidMediaType(t *testing.T) {
	client := NewClient("test-key", 1000)
	_, err := client.Discover(context.Background(), "all", DiscoverParams{SortBy: "popularity.desc"})
	if err == nil {
		t.Fatal("expected error for invalid media type")
	}
}

func TestDiscoverRequiresSortBy(t *testing.T) {
	client := NewClient("test-key", 1000)
	_, err := client.Discover(context.Background(), "movie", DiscoverParams{})
	if err == nil {
		t.Fatal("expected error when sort_by is empty")
	}
}

func TestDiscoverPageMovieReturnsFullResults(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/discover/movie" {
			http.NotFound(w, r)
			return
		}
		q := r.URL.Query()
		if got := q.Get("sort_by"); got != "popularity.desc" {
			t.Errorf("sort_by = %q, want popularity.desc", got)
		}
		if got := q.Get("with_companies"); got != "420" {
			t.Errorf("with_companies = %q, want 420", got)
		}
		if got := q.Get("page"); got != "2" {
			t.Errorf("page = %q, want 2", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"page": 2,
			"total_pages": 8,
			"total_results": 160,
			"results": [
				{"id": 24428, "title": "The Avengers", "release_date": "2012-04-25", "poster_path": "/p.jpg", "overview": "earth's mightiest", "popularity": 100.5, "vote_average": 7.7}
			]
		}`))
	}))
	defer server.Close()

	client := NewClient("test-key", 1000)
	client.SetBaseURL(server.URL)

	page, err := client.DiscoverPage(context.Background(), "movie", DiscoverParams{
		SortBy:        "popularity.desc",
		WithCompanies: []int{420},
	}, 2)
	if err != nil {
		t.Fatalf("DiscoverPage: %v", err)
	}
	if page.Page != 2 || page.TotalPages != 8 || page.TotalResults != 160 {
		t.Fatalf("page = %+v", page)
	}
	if len(page.Results) != 1 {
		t.Fatalf("results = %d, want 1", len(page.Results))
	}
	got := page.Results[0]
	if got.ID != 24428 || got.MediaType != "movie" || got.Title != "The Avengers" || got.Year != 2012 {
		t.Errorf("result = %+v", got)
	}
	if got.PosterPath != "/p.jpg" || got.Overview != "earth's mightiest" {
		t.Errorf("result detail mismatch: %+v", got)
	}
}

func TestDiscoverPageTVUsesFirstAirDate(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/discover/tv" {
			http.NotFound(w, r)
			return
		}
		q := r.URL.Query()
		if got := q.Get("with_networks"); got != "213" {
			t.Errorf("with_networks = %q, want 213", got)
		}
		if got := q.Get("first_air_date.gte"); got != "" {
			t.Errorf("first_air_date.gte = %q, want empty", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"page": 1,
			"total_pages": 1,
			"total_results": 1,
			"results": [
				{"id": 1399, "name": "Game of Thrones", "first_air_date": "2011-04-17", "poster_path": "/g.jpg"}
			]
		}`))
	}))
	defer server.Close()

	client := NewClient("test-key", 1000)
	client.SetBaseURL(server.URL)

	page, err := client.DiscoverPage(context.Background(), "tv", DiscoverParams{
		SortBy:       "vote_average.desc",
		WithNetworks: []int{213},
	}, 1)
	if err != nil {
		t.Fatalf("DiscoverPage tv: %v", err)
	}
	if len(page.Results) != 1 {
		t.Fatalf("results = %d, want 1", len(page.Results))
	}
	got := page.Results[0]
	if got.MediaType != "series" || got.Title != "Game of Thrones" || got.Year != 2011 {
		t.Errorf("result = %+v", got)
	}
}

func TestDiscoverPageRejectsInvalidMediaType(t *testing.T) {
	client := NewClient("test-key", 1000)
	_, err := client.DiscoverPage(context.Background(), "all", DiscoverParams{SortBy: "popularity.desc"}, 1)
	if err == nil {
		t.Fatal("expected error for invalid media type")
	}
}

func TestDiscoverPageRequiresSortBy(t *testing.T) {
	client := NewClient("test-key", 1000)
	_, err := client.DiscoverPage(context.Background(), "movie", DiscoverParams{}, 1)
	if err == nil {
		t.Fatal("expected error when sort_by is empty")
	}
}

func TestDiscoverPageDefaultsToPage1(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("page"); got != "1" {
			t.Errorf("page = %q, want 1", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"page":1,"total_pages":1,"total_results":0,"results":[]}`))
	}))
	defer server.Close()

	client := NewClient("test-key", 1000)
	client.SetBaseURL(server.URL)

	if _, err := client.DiscoverPage(context.Background(), "movie", DiscoverParams{SortBy: "popularity.desc"}, 0); err != nil {
		t.Fatalf("DiscoverPage: %v", err)
	}
}

func TestSearchMediaMovie(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/search/movie" {
			http.NotFound(w, r)
			return
		}
		q := r.URL.Query()
		if got := q.Get("query"); got != "fight club" {
			t.Fatalf("query = %q, want fight club", got)
		}
		if got := q.Get("include_adult"); got != "false" {
			t.Fatalf("include_adult = %q, want false", got)
		}
		if got := q.Get("page"); got != "2" {
			t.Fatalf("page = %q, want 2", got)
		}
		if got := q.Get("api_key"); got != "test-key" {
			t.Fatalf("api_key query = %q, want test-key", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"page": 2,
			"total_pages": 5,
			"total_results": 50,
			"results": [
				{
					"id": 550,
					"title": "Fight Club",
					"overview": "overview",
					"poster_path": "/poster.jpg",
					"backdrop_path": "/backdrop.jpg",
					"release_date": "1999-10-15",
					"popularity": 10.5,
					"vote_average": 8.4
				}
			]
		}`))
	}))
	defer server.Close()

	client := NewClient("test-key", 1000)
	client.SetBaseURL(server.URL)

	page, err := client.SearchMedia(context.Background(), "movie", "fight club", 2)
	if err != nil {
		t.Fatalf("SearchMedia returned error: %v", err)
	}
	if page.Page != 2 || page.TotalPages != 5 || len(page.Results) != 1 {
		t.Fatalf("page = %+v, want page metadata and one result", page)
	}
	result := page.Results[0]
	if result.ID != 550 || result.MediaType != "movie" || result.Year != 1999 {
		t.Fatalf("result = %+v, want normalized movie result", result)
	}
}

func TestSearchMediaAllUsesMultiSearchAndFiltersToMoviesAndSeries(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/search/multi" {
			http.NotFound(w, r)
			return
		}
		q := r.URL.Query()
		if got := q.Get("query"); got != "fight club" {
			t.Fatalf("query = %q, want fight club", got)
		}
		if got := q.Get("include_adult"); got != "false" {
			t.Fatalf("include_adult = %q, want false", got)
		}
		if got := q.Get("page"); got != "2" {
			t.Fatalf("page = %q, want 2", got)
		}
		if got := q.Get("api_key"); got != "test-key" {
			t.Fatalf("api_key query = %q, want test-key", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"page": 2,
			"total_pages": 4,
			"total_results": 40,
			"results": [
				{
					"id": 550,
					"media_type": "movie",
					"title": "Fight Club",
					"release_date": "1999-10-15"
				},
				{
					"id": 1399,
					"media_type": "tv",
					"name": "Fight Club: The Series",
					"first_air_date": "2020-01-01"
				},
				{
					"id": 123,
					"media_type": "person",
					"name": "A Performer"
				}
			]
		}`))
	}))
	defer server.Close()

	client := NewClient("test-key", 1000)
	client.SetBaseURL(server.URL)

	page, err := client.SearchMedia(context.Background(), "all", "fight club", 2)
	if err != nil {
		t.Fatalf("SearchMedia returned error: %v", err)
	}
	if page.Page != 2 || page.TotalPages != 4 || page.TotalResults != 40 {
		t.Fatalf("page metadata = %+v, want TMDB pagination metadata", page)
	}
	if len(page.Results) != 2 {
		t.Fatalf("len(results) = %d, want 2", len(page.Results))
	}
	if page.Results[0].ID != 550 || page.Results[0].MediaType != "movie" || page.Results[0].Year != 1999 {
		t.Fatalf("results[0] = %+v, want normalized movie", page.Results[0])
	}
	if page.Results[1].ID != 1399 || page.Results[1].MediaType != "series" || page.Results[1].Year != 2020 {
		t.Fatalf("results[1] = %+v, want normalized series", page.Results[1])
	}
}

func TestDiscoverSectionTrendingSeries(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/trending/tv/week" {
			http.NotFound(w, r)
			return
		}
		if got := r.URL.Query().Get("page"); got != "1" {
			t.Fatalf("page = %q, want 1", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"page": 1,
			"total_pages": 1,
			"total_results": 1,
			"results": [
				{
					"id": 1399,
					"name": "Game of Thrones",
					"first_air_date": "2011-04-17"
				}
			]
		}`))
	}))
	defer server.Close()

	client := NewClient("test-key", 1000)
	client.SetBaseURL(server.URL)

	page, err := client.DiscoverSection(context.Background(), "trending_series", 1)
	if err != nil {
		t.Fatalf("DiscoverSection returned error: %v", err)
	}
	if len(page.Results) != 1 {
		t.Fatalf("len(results) = %d, want 1", len(page.Results))
	}
	result := page.Results[0]
	if result.ID != 1399 || result.MediaType != "series" || result.Year != 2011 {
		t.Fatalf("result = %+v, want normalized series result", result)
	}
}

func TestGetCollection(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/collection/86311" {
			http.NotFound(w, r)
			return
		}
		if got := r.URL.Query().Get("api_key"); got != "test-key" {
			t.Fatalf("api_key query = %q, want test-key", got)
		}
		w.Header().Set("Content-Type", "application/json")
		// Trimmed MCU-style payload — parts ordered chronologically by
		// release date, with one part omitting media_type to exercise the
		// "default to movie" branch.
		_, _ = w.Write([]byte(`{
			"id": 86311,
			"name": "The Avengers Collection",
			"parts": [
				{"id": 24428, "media_type": "movie", "title": "The Avengers", "release_date": "2012-04-25"},
				{"id": 99861, "media_type": "movie", "title": "Avengers: Age of Ultron", "release_date": "2015-04-22"},
				{"id": 299536, "title": "Avengers: Infinity War", "release_date": "2018-04-25"}
			]
		}`))
	}))
	defer server.Close()

	client := NewClient("test-key", 1000)
	client.SetBaseURL(server.URL)

	got, err := client.GetCollection(context.Background(), 86311)
	if err != nil {
		t.Fatalf("GetCollection: %v", err)
	}
	if got == nil {
		t.Fatal("GetCollection returned nil")
	}
	if got.ID != 86311 {
		t.Errorf("ID = %d, want 86311", got.ID)
	}
	if got.Name != "The Avengers Collection" {
		t.Errorf("Name = %q, want The Avengers Collection", got.Name)
	}
	if len(got.Parts) != 3 {
		t.Fatalf("len(parts) = %d, want 3", len(got.Parts))
	}

	// Order assertion: TMDB returns parts in curated order; the client
	// preserves that order so downstream sync writes items consistently.
	wantOrder := []int{24428, 99861, 299536}
	for i, want := range wantOrder {
		if got.Parts[i].ID != want {
			t.Errorf("parts[%d].ID = %d, want %d", i, got.Parts[i].ID, want)
		}
	}

	// Media type defaulting: third part omitted media_type in the wire
	// payload; client must default to "movie" so the resolver doesn't see
	// an empty string.
	if got.Parts[0].MediaType != "movie" {
		t.Errorf("parts[0].MediaType = %q, want movie", got.Parts[0].MediaType)
	}
	if got.Parts[2].MediaType != "movie" {
		t.Errorf("parts[2].MediaType = %q (omitted in payload), want movie default", got.Parts[2].MediaType)
	}

	if got.Parts[0].Title != "The Avengers" {
		t.Errorf("parts[0].Title = %q, want The Avengers", got.Parts[0].Title)
	}
	if got.Parts[0].ReleaseDate != "2012-04-25" {
		t.Errorf("parts[0].ReleaseDate = %q, want 2012-04-25", got.Parts[0].ReleaseDate)
	}
}

func TestGetCollectionRejectsNonPositiveID(t *testing.T) {
	client := NewClient("test-key", 1000)
	if _, err := client.GetCollection(context.Background(), 0); err == nil {
		t.Fatal("expected error on id=0")
	}
	if _, err := client.GetCollection(context.Background(), -7); err == nil {
		t.Fatal("expected error on negative id")
	}
}

func TestGetExternalIDs(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/movie/123/external_ids" {
			http.NotFound(w, r)
			return
		}
		if got := r.URL.Query().Get("api_key"); got != "test-key" {
			t.Fatalf("api_key query = %q, want test-key", got)
		}

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"imdb_id": "tt0133093",
			"tvdb_id": 12345
		}`))
	}))
	defer server.Close()

	client := NewClient("test-key", 1000)
	client.SetBaseURL(server.URL)

	ids, err := client.GetExternalIDs(context.Background(), "movie", 123)
	if err != nil {
		t.Fatalf("GetExternalIDs returned error: %v", err)
	}
	if ids == nil {
		t.Fatal("GetExternalIDs returned nil ids")
	}
	if ids.IMDbID != "tt0133093" {
		t.Fatalf("IMDbID = %q, want tt0133093", ids.IMDbID)
	}
	if ids.TVDBID != 12345 {
		t.Fatalf("TVDBID = %d, want 12345", ids.TVDBID)
	}
}

func TestGetCertificationUsesSubResourceEndpoints(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/movie/603/release_dates":
			// Type 3 (theatrical) US entry wins over the earlier NR entry.
			_, _ = w.Write([]byte(`{"results":[
				{"iso_3166_1":"DE","release_dates":[{"certification":"16","type":3}]},
				{"iso_3166_1":"US","release_dates":[{"certification":"NR","type":2},{"certification":"R","type":3}]}
			]}`))
		case "/tv/1396/content_ratings":
			_, _ = w.Write([]byte(`{"results":[
				{"iso_3166_1":"DE","rating":"16"},
				{"iso_3166_1":"US","rating":"TV-MA"}
			]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client := NewClient("test-key", 1000)
	client.SetBaseURL(server.URL)

	movieCert, err := client.GetCertification(context.Background(), "movie", 603)
	if err != nil {
		t.Fatalf("movie GetCertification returned error: %v", err)
	}
	if movieCert != "R" {
		t.Fatalf("movie certification = %q, want R", movieCert)
	}

	// Both Silo-facing "series" and TMDB-facing "tv" resolve TV titles.
	for _, mediaType := range []string{"series", "tv"} {
		tvCert, err := client.GetCertification(context.Background(), mediaType, 1396)
		if err != nil {
			t.Fatalf("tv GetCertification(%q) returned error: %v", mediaType, err)
		}
		if tvCert != "TV-MA" {
			t.Fatalf("tv certification (%q) = %q, want TV-MA", mediaType, tvCert)
		}
	}

	if _, err := client.GetCertification(context.Background(), "bogus", 1); err == nil {
		t.Fatal("GetCertification accepted invalid media type")
	}
}

// TestGetCertificationIgnoresForeignFallback pins the enforcement-path
// contract: a title with only foreign certifications resolves to "" (fail
// closed on the US ladder), unlike the display path, which falls back to any
// country. A Canadian "PG" must not read as US PG.
func TestGetCertificationIgnoresForeignFallback(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/movie/1/release_dates":
			_, _ = w.Write([]byte(`{"results":[{"iso_3166_1":"CA","release_dates":[{"certification":"PG","type":3}]}]}`))
		case "/tv/2/content_ratings":
			_, _ = w.Write([]byte(`{"results":[{"iso_3166_1":"AU","rating":"PG"}]}`))
		case "/movie/3/release_dates":
			// Festival NR + theatrical PG-13 in the US: the rated entry wins.
			_, _ = w.Write([]byte(`{"results":[{"iso_3166_1":"US","release_dates":[{"certification":"NR","type":2},{"certification":"PG-13","type":3}]}]}`))
		case "/movie/4/release_dates":
			// Two US theatrical entries that disagree (PG re-release + R):
			// enforcement must take the STRICTEST, not the first.
			_, _ = w.Write([]byte(`{"results":[{"iso_3166_1":"US","release_dates":[{"certification":"PG","type":3},{"certification":"R","type":3}]}]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client := NewClient("test-key", 1000)
	client.SetBaseURL(server.URL)

	if cert, err := client.GetCertification(context.Background(), "movie", 1); err != nil || cert != "" {
		t.Fatalf("foreign-only movie cert = %q, %v; want empty", cert, err)
	}
	if cert, err := client.GetCertification(context.Background(), "tv", 2); err != nil || cert != "" {
		t.Fatalf("foreign-only tv cert = %q, %v; want empty", cert, err)
	}
	if cert, err := client.GetCertification(context.Background(), "movie", 3); err != nil || cert != "PG-13" {
		t.Fatalf("US theatrical cert = %q, %v; want PG-13", cert, err)
	}
	if cert, err := client.GetCertification(context.Background(), "movie", 4); err != nil || cert != "R" {
		t.Fatalf("disagreeing US certs = %q, %v; want strictest (R)", cert, err)
	}
}

// TestGetCertificationCallerStopsWaitingOnCancel pins the DoChan behavior: a
// caller whose context dies mid-flight returns promptly with ctx.Err() while
// the shared detached fetch continues for the surviving waiters.
func TestGetCertificationCallerStopsWaitingOnCancel(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		<-release
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"results":[{"iso_3166_1":"US","rating":"TV-PG"}]}`))
	}))
	defer server.Close()

	client := NewClient("test-key", 1000)
	client.SetBaseURL(server.URL)

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() {
		_, err := client.GetCertification(ctx, "tv", 1396)
		errCh <- err
	}()
	<-started
	cancel()

	select {
	case err := <-errCh:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancelled caller error = %v, want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("cancelled caller still blocked on the shared fetch")
	}

	// The detached fetch is still completable for a fresh caller.
	close(release)
	if cert, err := client.GetCertification(context.Background(), "tv", 1396); err != nil || cert != "TV-PG" {
		t.Fatalf("surviving caller cert = %q, %v; want TV-PG", cert, err)
	}
}

// TestGetCertificationSurvivesFirstCallerCancellation pins the singleflight
// context fix: the shared fetch runs detached from the initiating caller, so
// that caller disconnecting must not poison the result for concurrent waiters.
func TestGetCertificationSurvivesFirstCallerCancellation(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		<-release
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"results":[{"iso_3166_1":"US","rating":"TV-PG"}]}`))
	}))
	defer server.Close()

	client := NewClient("test-key", 1000)
	client.SetBaseURL(server.URL)

	firstCtx, cancelFirst := context.WithCancel(context.Background())
	firstErr := make(chan error, 1)
	go func() {
		_, err := client.GetCertification(firstCtx, "tv", 1396)
		firstErr <- err
	}()
	<-started

	secondCert := make(chan string, 1)
	secondErr := make(chan error, 1)
	go func() {
		cert, err := client.GetCertification(context.Background(), "tv", 1396)
		secondCert <- cert
		secondErr <- err
	}()
	// Let the second caller pile onto the in-flight singleflight key, then
	// kill the initiating caller's context before the upstream responds.
	time.Sleep(50 * time.Millisecond)
	cancelFirst()
	time.Sleep(50 * time.Millisecond)
	close(release)

	if err := <-secondErr; err != nil {
		t.Fatalf("second caller failed after first caller cancelled: %v", err)
	}
	if cert := <-secondCert; cert != "TV-PG" {
		t.Fatalf("second caller cert = %q, want TV-PG", cert)
	}
	<-firstErr // first caller may or may not error; just reap it
}

func TestGetCertificationCachesIncludingEmpty(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		// No certification anywhere: the common case, which must be cached
		// too or fail-closed filtering refetches it on every page load.
		_, _ = w.Write([]byte(`{"results":[]}`))
	}))
	defer server.Close()

	client := NewClient("test-key", 1000)
	client.SetBaseURL(server.URL)

	for i := 0; i < 3; i++ {
		cert, err := client.GetCertification(context.Background(), "movie", 42)
		if err != nil {
			t.Fatalf("GetCertification returned error: %v", err)
		}
		if cert != "" {
			t.Fatalf("certification = %q, want empty", cert)
		}
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("upstream calls = %d, want 1 (empty result must be cached)", got)
	}
}

func TestGetCertificationSingleflightsConcurrentCallers(t *testing.T) {
	var calls atomic.Int32
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		<-release
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"results":[{"iso_3166_1":"US","rating":"TV-PG"}]}`))
	}))
	defer server.Close()

	client := NewClient("test-key", 1000)
	client.SetBaseURL(server.URL)

	const callers = 8
	results := make(chan string, callers)
	errs := make(chan error, callers)
	for i := 0; i < callers; i++ {
		go func() {
			cert, err := client.GetCertification(context.Background(), "tv", 1396)
			results <- cert
			errs <- err
		}()
	}
	// Give the goroutines time to pile onto the singleflight, then release
	// the one in-flight upstream request.
	time.Sleep(50 * time.Millisecond)
	close(release)

	for i := 0; i < callers; i++ {
		if err := <-errs; err != nil {
			t.Fatalf("GetCertification returned error: %v", err)
		}
		if cert := <-results; cert != "TV-PG" {
			t.Fatalf("certification = %q, want TV-PG", cert)
		}
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("upstream calls = %d, want 1 (singleflight)", got)
	}
}

func TestDetailCarriesRoutingIdentifiers(t *testing.T) {
	var movie movieDetailResponse
	if err := json.Unmarshal([]byte(`{
		"id": 129, "title": "Spirited Away", "release_date": "2001-07-20", "original_language": "ja",
		"origin_country": ["JP"],
		"genres": [{"id": 16, "name": "Animation"}, {"id": 14, "name": "Fantasy"}],
		"production_companies": [{"id": 10342, "name": "Studio Ghibli"}],
		"keywords": {"keywords": [{"id": 210024, "name": "anime"}]}
	}`), &movie); err != nil {
		t.Fatal(err)
	}
	m := normalizeMovieDetail(&movie)
	if !slices.Equal(m.GenreIDs, []int{16, 14}) || !slices.Equal(m.CompanyIDs, []int{10342}) ||
		!slices.Equal(m.OriginCountries, []string{"JP"}) || m.OriginalLanguage != "ja" || m.Year != 2001 {
		t.Fatalf("movie = %+v", m)
	}

	var tv tvDetailResponse
	if err := json.Unmarshal([]byte(`{
		"id": 95396, "name": "Severance", "first_air_date": "2022-02-17", "original_language": "en",
		"origin_country": ["US"],
		"genres": [{"id": 18, "name": "Drama"}],
		"networks": [{"id": 2552, "name": "Apple TV+"}]
	}`), &tv); err != nil {
		t.Fatal(err)
	}
	s := normalizeTVDetail(&tv)
	if !slices.Equal(s.GenreIDs, []int{18}) || !slices.Equal(s.NetworkIDs, []int{2552}) ||
		!slices.Equal(s.OriginCountries, []string{"US"}) || s.Year != 2022 {
		t.Fatalf("series = %+v", s)
	}
}

func TestNotFoundWrapsErrNotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"status_message":"The resource you requested could not be found."}`))
	}))
	defer srv.Close()
	client := NewClient("key", 40)
	client.baseURL = srv.URL
	_, err := client.GetMediaDetail(context.Background(), "movie", 1)
	if !errors.Is(err, ErrNotFound) || !strings.Contains(err.Error(), "HTTP 404") {
		t.Fatalf("err = %v, want ErrNotFound with the HTTP detail", err)
	}
}

func TestGetCertificationsListsEveryCountry(t *testing.T) {
	var calls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/movie/129/release_dates":
			_, _ = w.Write([]byte(`{"results":[
				{"iso_3166_1":"JP","release_dates":[{"certification":"G","type":3},{"certification":"","type":4},{"certification":"G","type":5}]},
				{"iso_3166_1":"de","release_dates":[{"certification":"6","type":3}]}
			]}`))
		case "/tv/209867/content_ratings":
			_, _ = w.Write([]byte(`{"results":[{"iso_3166_1":"JP","rating":"PG12"},{"iso_3166_1":"KR","rating":""}]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	client := NewClient("test-key", 1000)
	client.SetBaseURL(server.URL)

	movie, err := client.GetCertifications(context.Background(), "movie", 129)
	if err != nil || len(movie) != 2 || len(movie["JP"]) != 1 || movie["JP"][0] != "G" || movie["DE"][0] != "6" {
		t.Fatalf("movie certifications = %v, %v", movie, err)
	}
	series, err := client.GetCertifications(context.Background(), "series", 209867)
	if err != nil || len(series) != 1 || series["JP"][0] != "PG12" {
		t.Fatalf("series certifications = %v, %v", series, err)
	}
	// Cached like GetCertification.
	before := calls
	if _, err := client.GetCertifications(context.Background(), "movie", 129); err != nil || calls != before {
		t.Fatalf("second read made %d calls, err %v", calls-before, err)
	}
	// The detail carries the same map.
	if got := movieCertifications(&releaseDatesResponse{Results: []releaseDatesCountryEntry{{ISO3166: "fr", ReleaseDates: []releaseDateEntry{{Certification: "U"}}}}}); got["FR"][0] != "U" {
		t.Fatalf("detail certifications = %v", got)
	}
}

func TestUSCertificationFromMatchesGetCertification(t *testing.T) {
	movie := map[string][]string{"US": {"NR", "PG", "R"}, "JP": {"G"}}
	if got := USCertificationFrom("movie", movie); got != "R" {
		t.Fatalf("movie = %q, want the strictest, R", got)
	}
	if got := USCertificationFrom("series", map[string][]string{"US": {"TV-14", "TV-MA"}}); got != "TV-14" {
		t.Fatalf("series = %q, want the first", got)
	}
	if got := USCertificationFrom("movie", map[string][]string{"JP": {"G"}}); got != "" {
		t.Fatalf("no US entry = %q", got)
	}
}

func TestGetListPagesMixedEntriesInListOrder(t *testing.T) {
	var pages []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/list/310" {
			http.NotFound(w, r)
			return
		}
		page := r.URL.Query().Get("page")
		pages = append(pages, page)
		w.Header().Set("Content-Type", "application/json")
		switch page {
		case "1":
			_, _ = w.Write([]byte(`{"id":310,"page":1,"total_pages":2,"items":[
				{"id":10096,"media_type":"movie","title":"13 Going on 30"},
				{"id":1399,"media_type":"tv","name":"Game of Thrones"},
				{"id":287,"media_type":"person","name":"Brad Pitt"}
			]}`))
		case "2":
			_, _ = w.Write([]byte(`{"id":310,"page":2,"total_pages":2,"items":[
				{"id":550,"media_type":"movie","title":"Fight Club"}
			]}`))
		default:
			w.WriteHeader(http.StatusBadRequest)
		}
	}))
	defer server.Close()

	client := NewClient("test-key", 1000)
	client.SetBaseURL(server.URL)

	got, err := client.GetList(context.Background(), 310, 0)
	if err != nil {
		t.Fatalf("GetList: %v", err)
	}
	want := []CollectionResult{
		{ID: 10096, MediaType: "movie", Title: "13 Going on 30"},
		{ID: 1399, MediaType: "tv", Title: "Game of Thrones"},
		{ID: 550, MediaType: "movie", Title: "Fight Club"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("GetList = %#v, want %#v", got, want)
	}
	if !reflect.DeepEqual(pages, []string{"1", "2"}) {
		t.Fatalf("requested pages = %v, want [1 2]", pages)
	}

	pages = nil
	got, err = client.GetList(context.Background(), 310, 2)
	if err != nil {
		t.Fatalf("GetList(limit=2): %v", err)
	}
	if len(got) != 2 || !reflect.DeepEqual(pages, []string{"1"}) {
		t.Fatalf("GetList(limit=2) = %d entries from pages %v, want 2 entries from page 1", len(got), pages)
	}
}

func TestGetListRejectsNonPositiveID(t *testing.T) {
	client := NewClient("test-key", 1000)
	if _, err := client.GetList(context.Background(), 0, 10); err == nil {
		t.Fatal("GetList(0) succeeded, want error")
	}
}

// A title page polls its detail while a request downloads, and several people
// may have it open: they share one fetch for the cache's TTL, each with a copy
// of its own. A failure is not cached.
func TestGetMediaDetailIsCachedBriefly(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/movie/129":
			_, _ = w.Write([]byte(`{"id": 129, "title": "Spirited Away", "genres": [{"id": 16, "name": "Animation"}],
				"recommendations": {"results": [{"id": 4935, "title": "Howl's Moving Castle"}]}}`))
		case "/tv/95396":
			_, _ = w.Write([]byte(`{"id": 95396, "name": "Severance"}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	client := NewClient("key", 40)
	defer client.Close()
	client.SetBaseURL(server.URL)
	ctx := context.Background()

	first, err := client.GetMediaDetail(ctx, "movie", 129)
	if err != nil {
		t.Fatal(err)
	}
	first.Genres[0], first.Recommendations[0].Title = "changed", "changed"
	second, err := client.GetMediaDetail(ctx, "movie", 129)
	if err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 1 {
		t.Fatalf("upstream calls = %d, want 1 while the detail is cached", calls.Load())
	}
	if second.Genres[0] != "Animation" || second.Recommendations[0].Title != "Howl's Moving Castle" {
		t.Fatalf("second detail = %+v; a caller's change reached the cache", second)
	}

	// "series" and "tv" name the same title.
	for _, mediaType := range []string{"series", "tv"} {
		if detail, err := client.GetMediaDetail(ctx, mediaType, 95396); err != nil || detail.Title != "Severance" {
			t.Fatalf("GetMediaDetail(%s) = %+v, %v", mediaType, detail, err)
		}
	}
	if calls.Load() != 2 {
		t.Fatalf("upstream calls = %d, want 2", calls.Load())
	}

	for range 2 {
		if _, err := client.GetMediaDetail(ctx, "movie", 404); !errors.Is(err, ErrNotFound) {
			t.Fatalf("err = %v, want ErrNotFound", err)
		}
	}
	if calls.Load() != 4 {
		t.Fatalf("upstream calls = %d, want each failure fetched again", calls.Load())
	}
}

// blockedDetailServer answers /movie/129 once release is closed. started is
// closed when the first request arrives; calls counts every request.
func blockedDetailServer(t *testing.T) (client *Client, started <-chan struct{}, release func(), calls *atomic.Int32) {
	t.Helper()
	startedCh := make(chan struct{})
	releaseCh := make(chan struct{})
	var startOnce, releaseOnce sync.Once
	calls = new(atomic.Int32)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		startOnce.Do(func() { close(startedCh) })
		<-releaseCh
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id": 129, "title": "Spirited Away"}`))
	}))
	release = func() { releaseOnce.Do(func() { close(releaseCh) }) }
	client = NewClient("key", 1000)
	client.SetBaseURL(server.URL)
	t.Cleanup(func() {
		release()
		server.Close()
		client.Close()
	})
	return client, startedCh, release, calls
}

// waitingCtx closes waiting the first time its caller selects on Done. A
// follower does that only once it has joined the in-flight fetch.
type waitingCtx struct {
	context.Context
	once    sync.Once
	waiting chan struct{}
}

func newWaitingCtx(parent context.Context) *waitingCtx {
	return &waitingCtx{Context: parent, waiting: make(chan struct{})}
}

func (c *waitingCtx) Done() <-chan struct{} {
	c.once.Do(func() { close(c.waiting) })
	return c.Context.Done()
}

type detailResult struct {
	detail *MediaDetail
	err    error
}

func getMediaDetailAsync(ctx context.Context, client *Client) <-chan detailResult {
	out := make(chan detailResult, 1)
	go func() {
		detail, err := client.GetMediaDetail(ctx, "movie", 129)
		out <- detailResult{detail, err}
	}()
	return out
}

func awaitDetail(t *testing.T, who string, ch <-chan detailResult) detailResult {
	t.Helper()
	select {
	case result := <-ch:
		return result
	case <-time.After(5 * time.Second):
		t.Fatalf("%s is still blocked on the shared detail fetch", who)
		return detailResult{}
	}
}

// The caller that starts a shared detail fetch may disconnect; everyone else
// waiting on the same title still gets it.
func TestGetMediaDetailSurvivesLeaderCancellation(t *testing.T) {
	client, started, release, calls := blockedDetailServer(t)

	leaderCtx, cancelLeader := context.WithCancel(context.Background())
	defer cancelLeader()
	leader := getMediaDetailAsync(leaderCtx, client)
	<-started

	followerCtx := newWaitingCtx(context.Background())
	follower := getMediaDetailAsync(followerCtx, client)
	select {
	case <-followerCtx.waiting:
	case <-time.After(5 * time.Second):
		t.Fatal("follower never waited on its own context")
	}

	cancelLeader()
	if got := awaitDetail(t, "canceled leader", leader); !errors.Is(got.err, context.Canceled) {
		t.Fatalf("leader err = %v, want context.Canceled", got.err)
	}
	release()
	got := awaitDetail(t, "follower", follower)
	if got.err != nil || got.detail.Title != "Spirited Away" {
		t.Fatalf("follower = %+v, %v; want the detail after the leader canceled", got.detail, got.err)
	}
	if calls.Load() != 1 {
		t.Fatalf("upstream calls = %d, want 1 shared fetch", calls.Load())
	}
}

// A waiting caller that disconnects stops waiting at once, and the shared
// fetch still completes for the caller that started it.
func TestGetMediaDetailFollowerStopsWaitingOnCancel(t *testing.T) {
	client, started, release, calls := blockedDetailServer(t)

	leader := getMediaDetailAsync(context.Background(), client)
	<-started

	followerCtx, cancelFollower := context.WithCancel(context.Background())
	defer cancelFollower()
	waiting := newWaitingCtx(followerCtx)
	follower := getMediaDetailAsync(waiting, client)
	select {
	case <-waiting.waiting:
	case <-time.After(5 * time.Second):
		t.Fatal("follower never waited on its own context")
	}

	cancelFollower()
	if got := awaitDetail(t, "canceled follower", follower); !errors.Is(got.err, context.Canceled) {
		t.Fatalf("follower err = %v, want context.Canceled", got.err)
	}
	release()
	got := awaitDetail(t, "leader", leader)
	if got.err != nil || got.detail.Title != "Spirited Away" {
		t.Fatalf("leader = %+v, %v; want the detail after the follower canceled", got.detail, got.err)
	}
	if calls.Load() != 1 {
		t.Fatalf("upstream calls = %d, want 1 shared fetch", calls.Load())
	}
}

// Every slice and map a detail holds is copied, so no caller shares one with
// the cache; a field added later is caught here.
func TestCloneMediaDetailCopiesEveryReference(t *testing.T) {
	var detail MediaDetail
	v := reflect.ValueOf(&detail).Elem()
	for i := range v.NumField() {
		field := v.Field(i)
		switch field.Kind() {
		case reflect.Slice:
			field.Set(reflect.MakeSlice(field.Type(), 1, 1))
		case reflect.Map:
			m := reflect.MakeMap(field.Type())
			m.SetMapIndex(reflect.New(field.Type().Key()).Elem(), reflect.MakeSlice(field.Type().Elem(), 1, 1))
			field.Set(m)
		case reflect.Pointer, reflect.Interface, reflect.Chan, reflect.Func:
			t.Fatalf("MediaDetail.%s is a %s; teach cloneMediaDetail to copy it", v.Type().Field(i).Name, field.Kind())
		}
	}
	cloned := reflect.ValueOf(cloneMediaDetail(&detail)).Elem()
	for i := range v.NumField() {
		name := v.Type().Field(i).Name
		switch v.Field(i).Kind() {
		case reflect.Slice:
			if cloned.Field(i).Pointer() == v.Field(i).Pointer() {
				t.Errorf("MediaDetail.%s shares its backing array with the cached detail", name)
			}
		case reflect.Map:
			if cloned.Field(i).Pointer() == v.Field(i).Pointer() {
				t.Errorf("MediaDetail.%s shares its map with the cached detail", name)
			}
			for _, key := range v.Field(i).MapKeys() {
				if cloned.Field(i).MapIndex(key).Pointer() == v.Field(i).MapIndex(key).Pointer() {
					t.Errorf("MediaDetail.%s[%v] shares its slice with the cached detail", name, key)
				}
			}
		}
	}
}
