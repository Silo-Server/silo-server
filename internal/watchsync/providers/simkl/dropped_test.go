package simkl

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"github.com/Silo-Server/silo-server/internal/historyimport"
	"github.com/Silo-Server/silo-server/internal/watchsync"
)

func TestFetchDroppedReadsShowsAndAnime(t *testing.T) {
	var paths []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		paths = append(paths, r.URL.RequestURI())
		switch r.URL.Path {
		case "/sync/activities":
			_, _ = w.Write([]byte(`{"tv_shows":{"all":"2026-05-04T12:00:00Z"},"anime":{"all":"2026-05-04T12:10:00Z"}}`))
		case "/sync/all-items/shows/dropped":
			_, _ = w.Write([]byte(`{"shows":[{"status":"dropped","show":{"title":"Rick and Morty","year":2013,"ids":{"simkl":1,"imdb":"tt2861424","tmdb":60625,"tvdb":275274}}}]}`))
		case "/sync/all-items/anime/dropped":
			_, _ = w.Write([]byte(`{}`))
		default:
			t.Errorf("unexpected request %s", r.URL.RequestURI())
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	batch, err := NewProvider(server.Client(), server.URL).FetchDropped(context.Background(), watchsync.ServerConfig{ClientID: "c"}, watchsync.Connection{AccessToken: "t"})
	if err != nil {
		t.Fatal(err)
	}
	if !batch.Complete || len(batch.Rows) != 1 {
		t.Fatalf("batch = %#v, want one show in a complete read", batch)
	}
	if row := batch.Rows[0]; row.Kind != historyimport.KindSeries || row.ProviderItemKey != "tvdb:275274" || row.TMDBID != "60625" || !row.DroppedAt.IsZero() {
		t.Fatalf("row = %#v", row)
	}
	if batch.UpdatedCursors[simklCursorDroppedShows] != "2026-05-04T12:00:00Z" || batch.UpdatedCursors[simklCursorDroppedAnime] != "2026-05-04T12:10:00Z" {
		t.Fatalf("cursors = %v", batch.UpdatedCursors)
	}
	if !slices.Contains(paths, "/sync/all-items/shows/dropped?extended=full") {
		t.Fatalf("paths = %v", paths)
	}
}

func TestFetchDroppedSkipsUnchangedActivity(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/sync/activities" {
			t.Errorf("unchanged activity must not read the dropped lists: %s", r.URL.RequestURI())
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"tv_shows":{"all":"a"},"anime":{"all":"b"}}`))
	}))
	defer server.Close()

	conn := watchsync.Connection{AccessToken: "t", SyncCursors: map[string]string{simklCursorDroppedShows: "a", simklCursorDroppedAnime: "b"}}
	batch, err := NewProvider(server.Client(), server.URL).FetchDropped(context.Background(), watchsync.ServerConfig{ClientID: "c"}, conn)
	if err != nil {
		t.Fatal(err)
	}
	if batch.Complete || len(batch.Rows) != 0 {
		t.Fatalf("batch = %#v, want an incomplete empty read", batch)
	}
}

func TestExportAndRemoveDroppedMoveShowsBetweenLists(t *testing.T) {
	var bodies []simklListPayload
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/sync/add-to-list" {
			t.Errorf("request = %s %s", r.Method, r.URL.Path)
		}
		var body simklListPayload
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		bodies = append(bodies, body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{}`))
	}))
	defer server.Close()

	items := []watchsync.LocalFavorite{
		{MediaItemID: "series-1", Kind: historyimport.KindSeries, TMDBID: "1", ProviderItemKey: "tmdb:1"},
		{MediaItemID: "movie-1", Kind: historyimport.KindMovie, TMDBID: "2", ProviderItemKey: "tmdb:2"},
	}
	provider := NewProvider(server.Client(), server.URL)
	cfg, conn := watchsync.ServerConfig{ClientID: "c"}, watchsync.Connection{AccessToken: "t"}
	if _, err := provider.ExportDropped(context.Background(), cfg, conn, items); err != nil {
		t.Fatal(err)
	}
	if _, err := provider.RemoveDropped(context.Background(), cfg, conn, items); err != nil {
		t.Fatal(err)
	}
	if len(bodies) != 2 {
		t.Fatalf("bodies = %#v", bodies)
	}
	for i, to := range []string{"dropped", "watching"} {
		if len(bodies[i].Movies) != 0 || len(bodies[i].Shows) != 1 || bodies[i].Shows[0].To != to {
			t.Fatalf("body %d = %#v, want one show moved to %s", i, bodies[i], to)
		}
	}
}
