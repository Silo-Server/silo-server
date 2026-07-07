package trakt

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestGetUserListDecodesMixedTypesInListOrder(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/users/jjjonesjr33/lists/saw-timeline/items" {
			t.Fatalf("path = %s, want /users/jjjonesjr33/lists/saw-timeline/items", r.URL.Path)
		}
		writeJSON(t, w, []map[string]any{
			{
				"rank": 1,
				"type": "movie",
				"movie": map[string]any{
					"title": "Saw",
					"year":  2004,
					"ids":   map[string]any{"trakt": 10, "tmdb": 176, "imdb": "tt0387564"},
				},
			},
			{
				"rank": 2,
				"type": "show",
				"show": map[string]any{
					"title": "Saw: The Series",
					"year":  2024,
					"ids":   map[string]any{"trakt": 11, "tvdb": 999001},
				},
			},
			// Unknown types (person, episode, season) are skipped, not fatal.
			{
				"rank": 3,
				"type": "person",
			},
		})
	}))
	defer server.Close()

	client := NewClient("client-id", 1000)
	client.SetBaseURL(server.URL)

	results, err := client.GetUserList(context.Background(), "jjjonesjr33", "saw-timeline", 10, "")
	if err != nil {
		t.Fatalf("GetUserList: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("results = %d, want 2 (unknown types skipped)", len(results))
	}
	if results[0].MediaType != "movie" || results[0].TMDBID != 176 || results[0].Title != "Saw" {
		t.Fatalf("first entry = %+v, want the Saw movie", results[0])
	}
	if results[1].MediaType != "tv" || results[1].TVDBID != 999001 {
		t.Fatalf("second entry = %+v, want the show mapped to media type tv", results[1])
	}
	if results[0].Rank != 1 || results[1].Rank != 2 {
		t.Fatalf("ranks = %d,%d, want list order preserved", results[0].Rank, results[1].Rank)
	}
}

func TestGetUserListRequiresUserAndSlug(t *testing.T) {
	client := NewClient("client-id", 1000)
	if _, err := client.GetUserList(context.Background(), "", "slug", 10, ""); err == nil {
		t.Fatal("empty user must error")
	}
	if _, err := client.GetUserList(context.Background(), "user", "", 10, ""); err == nil {
		t.Fatal("empty list slug must error")
	}
}
