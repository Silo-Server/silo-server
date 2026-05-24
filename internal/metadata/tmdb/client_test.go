package tmdb

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

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
		if r.URL.Path != "/movie/123" {
			http.NotFound(w, r)
			return
		}
		if got := r.URL.Query().Get("append_to_response"); got != "external_ids" {
			t.Fatalf("append_to_response = %q, want external_ids", got)
		}
		if got := r.URL.Query().Get("api_key"); got != "test-key" {
			t.Fatalf("api_key query = %q, want test-key", got)
		}

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"external_ids": {
				"imdb_id": "tt0133093",
				"tvdb_id": 12345
			}
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
