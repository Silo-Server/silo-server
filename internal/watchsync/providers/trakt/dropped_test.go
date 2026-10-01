package trakt

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/historyimport"
	"github.com/Silo-Server/silo-server/internal/watchsync"
)

func TestFetchDroppedReadsDroppedShowsAsCompleteSnapshot(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/users/hidden/dropped" {
			t.Errorf("unexpected path %s", r.URL.Path)
			http.NotFound(w, r)
			return
		}
		if r.URL.Query().Get("limit") != "250" {
			t.Errorf("limit = %q, want 250", r.URL.Query().Get("limit"))
		}
		w.Header().Set("X-Pagination-Page-Count", "1")
		writeTraktFixture(t, w, `[
			{"hidden_at":"2026-03-01T10:00:00.000Z","type":"show","show":{"title":"Rick and Morty","year":2013,"ids":{"trakt":69293,"imdb":"tt2861424","tmdb":60625,"tvdb":275274}}},
			{"hidden_at":"2026-03-02T10:00:00.000Z","type":"movie","movie":{"title":"Heat","ids":{"trakt":1}}}
		]`)
	}))
	defer server.Close()

	batch, err := NewProvider(server.Client(), server.URL).FetchDropped(context.Background(), watchsync.ServerConfig{ClientID: "c"}, watchsync.Connection{AccessToken: "t"})
	if err != nil {
		t.Fatal(err)
	}
	if !batch.Complete || len(batch.Rows) != 1 {
		t.Fatalf("batch = %#v, want one show in a complete read", batch)
	}
	row := batch.Rows[0]
	if row.Kind != historyimport.KindSeries || row.ProviderItemKey != "tvdb:275274" || row.TMDBID != "60625" || row.IMDbID != "tt2861424" ||
		!row.DroppedAt.Equal(time.Date(2026, 3, 1, 10, 0, 0, 0, time.UTC)) {
		t.Fatalf("row = %#v", row)
	}
}

func TestExportAndRemoveDroppedPostShowsAndMapNotFound(t *testing.T) {
	var paths []string
	var bodies []traktFavoritesPayload
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %s", r.Method)
		}
		paths = append(paths, r.URL.Path)
		var body traktFavoritesPayload
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		bodies = append(bodies, body)
		writeTraktFixture(t, w, `{"added":{"shows":1},"deleted":{"shows":1},"not_found":{"movies":[],"shows":[{"ids":{"tmdb":2}}]}}`)
	}))
	defer server.Close()

	items := []watchsync.LocalFavorite{
		{MediaItemID: "series-1", Kind: historyimport.KindSeries, TMDBID: "1", ProviderItemKey: "tmdb:1"},
		{MediaItemID: "series-2", Kind: historyimport.KindSeries, TMDBID: "2", ProviderItemKey: "tmdb:2"},
		{MediaItemID: "movie-1", Kind: historyimport.KindMovie, TMDBID: "3", ProviderItemKey: "tmdb:3"},
	}
	provider := NewProvider(server.Client(), server.URL)
	for _, send := range []func(context.Context, watchsync.ServerConfig, watchsync.Connection, []watchsync.LocalFavorite) (watchsync.ExportResult, error){
		provider.ExportDropped, provider.RemoveDropped,
	} {
		result, err := send(context.Background(), watchsync.ServerConfig{ClientID: "c"}, watchsync.Connection{AccessToken: "t"}, items)
		if err != nil {
			t.Fatal(err)
		}
		if !slices.Contains(result.Sent, "series-1") || !slices.Contains(result.NotFound, "series-2") || slices.Contains(result.Sent, "movie-1") {
			t.Fatalf("result = %#v", result)
		}
	}
	if !slices.Equal(paths, []string{"/users/hidden/dropped", "/users/hidden/dropped/remove"}) {
		t.Fatalf("paths = %v", paths)
	}
	for _, body := range bodies {
		if len(body.Shows) != 2 || len(body.Movies) != 0 {
			t.Fatalf("body = %#v, want the two shows only", body)
		}
	}
}
