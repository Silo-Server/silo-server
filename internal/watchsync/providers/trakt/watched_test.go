package trakt

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/historyimport"
	"github.com/Silo-Server/silo-server/internal/watchsync"
)

func TestFetchWatchedImportsEveryPageAndEpisodeProgress(t *testing.T) {
	var requests []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		kind := strings.TrimPrefix(r.URL.Path, "/sync/watched/")
		page := r.URL.Query().Get("page")
		requests = append(requests, kind+":"+page)
		if r.URL.Query().Get("limit") != "250" {
			t.Errorf("limit = %q, want 250", r.URL.Query().Get("limit"))
		}
		if r.Header.Get("Authorization") != "Bearer test-token" {
			t.Errorf("missing authorization")
		}
		w.Header().Set("Content-Type", "application/json")
		// Deliberately return short pages without pagination headers. Neither
		// condition should prevent fetching the remaining watched records.
		switch page {
		case "1", "2":
			switch kind {
			case "movies":
				writeWatchedFixture(t, w, `[{"plays":2,"last_watched_at":"2026-09-01T12:00:00Z","movie":{"title":"Movie %s","year":2020,"ids":{"tmdb":10%s}}}]`, page, page)
			case "shows":
				if r.URL.Query().Get("extended") != "progress" {
					// Current Trakt default: no seasons or episodes without progress.
					writeWatchedFixture(t, w, `[{"show":{"title":"Show","ids":{"tmdb":200}}}]`)
					return
				}
				writeWatchedFixture(t, w, `[{"show":{"title":"Show %s","year":2021,"ids":{"tmdb":20%s,"tvdb":30%s,"imdb":"tt40%s"}},"seasons":[{"number":0,"episodes":[{"number":1,"plays":3,"last_watched_at":"2026-09-02T12:00:00Z"}]},{"number":2,"episodes":[{"number":5,"plays":1,"last_watched_at":"2026-09-03T12:00:00Z"}]}]}]`, page, page, page, page)
			default:
				t.Errorf("unexpected path %s", r.URL.Path)
				http.NotFound(w, r)
			}
		case "3":
			writeWatchedFixture(t, w, `[]`)
		default:
			t.Errorf("unexpected page %q", page)
			http.Error(w, "unexpected page", 500)
		}
	}))
	defer server.Close()
	rows, err := NewProvider(server.Client(), server.URL).FetchWatched(context.Background(), watchsync.ServerConfig{}, watchsync.Connection{AccessToken: "test-token"})
	if err != nil {
		t.Fatal(err)
	}
	wantRequests := []string{"movies:1", "movies:2", "movies:3", "shows:1", "shows:2", "shows:3"}
	if !reflect.DeepEqual(requests, wantRequests) {
		t.Fatalf("requests = %v, want %v", requests, wantRequests)
	}
	if len(rows) != 6 {
		t.Fatalf("got %d records, want 2 movies and 4 episodes", len(rows))
	}
	for i, row := range rows[:2] {
		if row.Kind != historyimport.KindMovie || row.TMDBID != fmt.Sprintf("10%d", i+1) || row.PlayCount != 2 {
			t.Errorf("movie %d = %#v", i, row)
		}
	}
	for i, row := range rows[2:] {
		show := i/2 + 1
		season, episode, plays, day := 0, 1, 3, 2
		if i%2 == 1 {
			season, episode, plays, day = 2, 5, 1, 3
		}
		watchedAt := time.Date(2026, 9, day, 12, 0, 0, 0, time.UTC)
		if row.Kind != historyimport.KindEpisode || row.SeriesTMDBID != fmt.Sprintf("20%d", show) || row.SeriesTVDBID != fmt.Sprintf("30%d", show) || row.SeriesIMDbID != fmt.Sprintf("tt40%d", show) || row.SeasonNumber != season || row.EpisodeNumber != episode || row.PlayCount != plays || row.LastWatchedAt == nil || !row.LastWatchedAt.Equal(watchedAt) {
			t.Errorf("episode %d = %#v", i, row)
		}
	}
}

func TestFetchWatchedDoesNotReturnPartialHistoryOnLaterPageFailure(t *testing.T) {
	for _, kind := range []string{"movies", "shows"} {
		for _, failure := range []string{"http", "json"} {
			t.Run(kind+"/"+failure, func(t *testing.T) {
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("Content-Type", "application/json")
					if r.URL.Query().Get("page") == "1" {
						writeWatchedFixture(t, w, `[{"plays":1,"last_watched_at":"2026-09-01T12:00:00Z","movie":{"ids":{"tmdb":123}},"show":{"ids":{"tmdb":456}},"seasons":[{"number":1,"episodes":[{"number":1,"plays":1,"last_watched_at":"2026-09-01T12:00:00Z"}]}]}]`)
						return
					}
					if r.URL.Path == "/sync/watched/"+kind {
						if failure == "http" {
							http.Error(w, "unavailable", http.StatusServiceUnavailable)
						} else {
							writeWatchedFixture(t, w, `[{`)
						}
						return
					}
					writeWatchedFixture(t, w, `[]`)
				}))
				defer server.Close()
				rows, err := NewProvider(server.Client(), server.URL).FetchWatched(context.Background(), watchsync.ServerConfig{}, watchsync.Connection{})
				if err == nil || rows != nil {
					t.Fatalf("got rows=%#v, err=%v; want no partial history and an error", rows, err)
				}
			})
		}
	}
}

func writeWatchedFixture(t *testing.T, w http.ResponseWriter, format string, args ...any) {
	t.Helper()
	if _, err := fmt.Fprintf(w, format, args...); err != nil {
		t.Errorf("write watched fixture: %v", err)
	}
}
