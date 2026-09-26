package trakt

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/historyimport"
	"github.com/Silo-Server/silo-server/internal/watchsync"
)

func TestFetchRatingsReadsMoviesAndShowsAsCompleteSnapshots(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("limit") != "250" {
			t.Errorf("%s limit = %q, want 250", r.URL.Path, r.URL.Query().Get("limit"))
		}
		w.Header().Set("X-Pagination-Page-Count", "1")
		switch r.URL.Path {
		case "/sync/ratings/movies":
			writeTraktFixture(t, w, `[{"rated_at":"2026-03-01T10:00:00.000Z","rating":7,"type":"movie","movie":{"title":"Heat","year":1995,"ids":{"trakt":1,"imdb":"tt0113277","tmdb":949}}}]`)
		case "/sync/ratings/shows":
			writeTraktFixture(t, w, `[{"rated_at":"2026-03-02T10:00:00.000Z","rating":10,"type":"show","show":{"title":"The Wire","year":2002,"ids":{"trakt":2,"imdb":"tt0306414","tmdb":1438,"tvdb":79126}}}]`)
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	batch, err := NewProvider(server.Client(), server.URL).FetchRatings(context.Background(), watchsync.ServerConfig{ClientID: "c"}, watchsync.Connection{AccessToken: "t"})
	if err != nil {
		t.Fatal(err)
	}
	if len(batch.SnapshotKinds) != 2 {
		t.Fatalf("snapshot kinds = %v, want movie and series", batch.SnapshotKinds)
	}
	if len(batch.Rows) != 2 {
		t.Fatalf("rows = %#v", batch.Rows)
	}
	movie, show := batch.Rows[0], batch.Rows[1]
	if movie.Kind != historyimport.KindMovie || movie.Rating != 7 || movie.IMDbID != "tt0113277" || movie.TMDBID != "949" ||
		!movie.RatedAt.Equal(time.Date(2026, 3, 1, 10, 0, 0, 0, time.UTC)) {
		t.Fatalf("movie row = %#v", movie)
	}
	if show.Kind != historyimport.KindSeries || show.Rating != 10 || show.TVDBID != "79126" || show.ProviderItemKey != "tvdb:79126" {
		t.Fatalf("show row = %#v", show)
	}
}

func TestExportRatingsSendsRatingsAndMapsNotFound(t *testing.T) {
	ratedAt := time.Date(2026, 4, 5, 6, 7, 8, 0, time.UTC)
	var got traktRatingsPayload
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/sync/ratings" {
			t.Errorf("request = %s %s", r.Method, r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Error(err)
		}
		w.WriteHeader(http.StatusCreated)
		// Trakt echoes the missing show by TVDB id only.
		writeTraktFixture(t, w, `{"added":{"movies":1,"shows":0},"not_found":{"movies":[],"shows":[{"rating":8,"ids":{"tvdb":79126}}]}}`)
	}))
	defer server.Close()

	result, err := NewProvider(server.Client(), server.URL).ExportRatings(context.Background(), watchsync.ServerConfig{ClientID: "c"}, watchsync.Connection{AccessToken: "t"}, []watchsync.LocalRating{
		{LocalFavorite: watchsync.LocalFavorite{MediaItemID: "m1", Kind: historyimport.KindMovie, IMDbID: "tt0113277", ProviderItemKey: "imdb:tt0113277"}, Rating: 6, RatedAt: ratedAt},
		{LocalFavorite: watchsync.LocalFavorite{MediaItemID: "s1", Kind: historyimport.KindSeries, IMDbID: "tt0306414", TVDBID: "79126", ProviderItemKey: "imdb:tt0306414"}, Rating: 8},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Movies) != 1 || got.Movies[0].Rating != 6 || got.Movies[0].IDs.IMDb != "tt0113277" || got.Movies[0].RatedAt == nil || !got.Movies[0].RatedAt.Equal(ratedAt) {
		t.Fatalf("movie payload = %#v", got.Movies)
	}
	if len(got.Shows) != 1 || got.Shows[0].Rating != 8 || got.Shows[0].RatedAt != nil {
		t.Fatalf("show payload = %#v", got.Shows)
	}
	if !containsValue(result.Sent, "m1") || containsValue(result.Sent, "s1") || !containsValue(result.NotFound, "s1") {
		t.Fatalf("result = %#v, want movie sent and show not found", result)
	}
}

func TestRemoveRatingsSendsIDsOnly(t *testing.T) {
	var got map[string][]map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/sync/ratings/remove" {
			t.Errorf("request = %s %s", r.Method, r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Error(err)
		}
		writeTraktFixture(t, w, `{"deleted":{"movies":1},"not_found":{"movies":[],"shows":[]}}`)
	}))
	defer server.Close()

	result, err := NewProvider(server.Client(), server.URL).RemoveRatings(context.Background(), watchsync.ServerConfig{ClientID: "c"}, watchsync.Connection{AccessToken: "t"}, []watchsync.LocalFavorite{
		{MediaItemID: "m1", Kind: historyimport.KindMovie, ProviderItemKey: "tmdb:949"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(got["movies"]) != 1 || got["movies"][0]["rating"] != nil {
		t.Fatalf("remove payload = %#v, want ids without a rating", got)
	}
	if !containsValue(result.Sent, "m1") {
		t.Fatalf("result = %#v", result)
	}
}

func containsValue(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
