package simkl

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/historyimport"
	"github.com/Silo-Server/silo-server/internal/userstore"
	"github.com/Silo-Server/silo-server/internal/watchsync"
)

func TestProviderIdentityAndCapabilities(t *testing.T) {
	provider := NewProvider(nil, "")

	if provider.Key() != "simkl" {
		t.Fatalf("key = %q, want simkl", provider.Key())
	}
	if provider.DisplayName() != "Simkl" {
		t.Fatalf("display name = %q, want Simkl", provider.DisplayName())
	}
	if provider.HistorySource() != userstore.WatchHistorySourceSimkl {
		t.Fatalf("history source = %q, want simkl", provider.HistorySource())
	}
	if provider.Capabilities() != (watchsync.Capabilities{
		ImportWatched:    true,
		ImportProgress:   true,
		ExportWatched:    true,
		ExportUnwatched:  true,
		ScrobblePlayback: true,
	}) {
		t.Fatalf("capabilities = %#v", provider.Capabilities())
	}
}

func TestStartDeviceAuthSendsSimklHeadersAndDecodesResponse(t *testing.T) {
	var gotPath string
	var gotAPIKey string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.RequestURI()
		gotAPIKey = r.Header.Get("simkl-api-key")
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"result":           "OK",
			"device_code":      "device-code",
			"user_code":        "ABCDE",
			"verification_url": "https://simkl.com/pin/",
			"expires_in":       900,
			"interval":         5,
		})
	}))
	defer server.Close()

	provider := NewProvider(server.Client(), server.URL)
	session, err := provider.StartDeviceAuth(context.Background(), watchsync.ServerConfig{
		ClientID:     "client-id",
		ClientSecret: "client-secret",
	})
	if err != nil {
		t.Fatalf("StartDeviceAuth: %v", err)
	}
	if gotPath != "/oauth/pin?client_id=client-id" {
		t.Fatalf("path = %q, want pin path", gotPath)
	}
	if gotAPIKey != "client-id" {
		t.Fatalf("simkl-api-key = %q, want client-id", gotAPIKey)
	}
	if session.Provider != "simkl" || session.UserCode != "ABCDE" ||
		session.VerificationURL != "https://simkl.com/pin/" || session.IntervalSeconds != 5 {
		t.Fatalf("session = %+v", session)
	}
	if time.Until(session.ExpiresAt) < 14*time.Minute {
		t.Fatalf("expires_at = %s, want about 15 minutes from now", session.ExpiresAt)
	}
}

func TestPollDeviceAuthReturnsPendingError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"result":"KO","message":"Authorization pending"}`))
	}))
	defer server.Close()

	provider := NewProvider(server.Client(), server.URL)
	_, err := provider.PollDeviceAuth(context.Background(), watchsync.ServerConfig{
		ClientID:     "client-id",
		ClientSecret: "client-secret",
	}, watchsync.DeviceAuthSession{UserCode: "ABCDE"})
	if err == nil {
		t.Fatal("PollDeviceAuth error = nil, want pending error")
	}
}

func TestFetchWatchedMapsMoviesShowsAndAnime(t *testing.T) {
	seen := map[string]int{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen[r.URL.Path]++
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/sync/activities":
			_, _ = w.Write([]byte(`{
				"movies":{"completed":"2026-05-04T12:00:00Z"},
				"tv_shows":{"watching":"2026-05-04T12:05:00Z","completed":"2026-05-04T12:10:00Z"},
				"anime":{"watching":"2026-05-04T12:15:00Z","completed":"2026-05-04T12:20:00Z"}
			}`))
		case "/sync/all-items/movies/completed":
			_, _ = w.Write([]byte(`{"movies":[{"status":"completed","last_watched_at":"2026-05-04T12:00:00Z","movie":{"title":"Inception","year":2010,"ids":{"imdb":"tt1375666","tmdb":"27205"}}}]}`))
		case "/sync/all-items/shows/watching":
			_, _ = w.Write([]byte(`{"shows":[{"status":"watching","show":{"title":"Breaking Bad","year":2008,"ids":{"tvdb":"81189","tmdb":"1396","imdb":"tt0903747"}},"seasons":[{"number":1,"episodes":[{"number":1,"watched_at":"2026-05-04T13:00:00Z","ids":{"tvdb":"349232"}},{"number":2}]}]}]}`))
		case "/sync/all-items/shows/completed":
			_, _ = w.Write([]byte(`{"shows":[]}`))
		case "/sync/all-items/anime/watching":
			_, _ = w.Write([]byte(`{"anime":[{"status":"watching","show":{"title":"Anime","year":2020,"ids":{"tmdb":"1429"}},"seasons":[{"number":1,"episodes":[{"number":4,"tvdb":{"season":2,"episode":4},"watched_at":"2026-05-04T14:00:00Z"}]}]}]}`))
		case "/sync/all-items/anime/completed":
			_, _ = w.Write([]byte(`{"anime":[]}`))
		default:
			t.Fatalf("unexpected path %s", r.URL.RequestURI())
		}
	}))
	defer server.Close()

	provider := NewProvider(server.Client(), server.URL)
	rows, err := provider.FetchWatched(context.Background(), watchsync.ServerConfig{
		ClientID:     "client-id",
		ClientSecret: "client-secret",
	}, watchsync.Connection{AccessToken: "token"})
	if err != nil {
		t.Fatalf("FetchWatched: %v", err)
	}
	if len(rows) != 3 {
		t.Fatalf("rows = %#v, want 3", rows)
	}
	if rows[0].Kind != historyimport.KindMovie || rows[0].IMDbID != "tt1375666" {
		t.Fatalf("movie row = %+v", rows[0])
	}
	if rows[1].Kind != historyimport.KindEpisode || rows[1].TVDBID != "349232" || rows[1].SeasonNumber != 1 {
		t.Fatalf("episode row = %+v", rows[1])
	}
	if rows[2].SeasonNumber != 2 || rows[2].EpisodeNumber != 4 {
		t.Fatalf("anime mapped row = %+v", rows[2])
	}
	if rows[2].ProviderItemKey != "show:tmdb:1429:s2:e4" {
		t.Fatalf("anime provider key = %q, want nested TVDB season/episode fallback", rows[2].ProviderItemKey)
	}
	if seen["/sync/all-items/shows/watching"] != 1 || seen["/sync/all-items/anime/watching"] != 1 {
		t.Fatalf("watching buckets were not fetched: %+v", seen)
	}
}

func TestFetchWatchedBatchSkipsUnchangedActivityCursors(t *testing.T) {
	var allItemsCalls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/sync/activities":
			_, _ = w.Write([]byte(`{
				"movies":{"completed":"2026-05-04T12:00:00Z"},
				"tv_shows":{"watching":"2026-05-04T12:05:00Z","completed":"2026-05-04T12:10:00Z"},
				"anime":{"watching":"2026-05-04T12:15:00Z","completed":"2026-05-04T12:20:00Z"}
			}`))
		default:
			allItemsCalls++
			_, _ = w.Write([]byte(`{}`))
		}
	}))
	defer server.Close()

	provider := NewProvider(server.Client(), server.URL)
	batch, err := provider.FetchWatchedBatch(context.Background(), watchsync.ServerConfig{
		ClientID:     "client-id",
		ClientSecret: "client-secret",
	}, watchsync.Connection{
		AccessToken: "token",
		SyncCursors: map[string]string{
			simklCursorInboundMoviesCompleted: "2026-05-04T12:00:00Z",
			simklCursorInboundShowsWatching:   "2026-05-04T12:05:00Z",
			simklCursorInboundShowsCompleted:  "2026-05-04T12:10:00Z",
			simklCursorInboundAnimeWatching:   "2026-05-04T12:15:00Z",
			simklCursorInboundAnimeCompleted:  "2026-05-04T12:20:00Z",
		},
	})
	if err != nil {
		t.Fatalf("FetchWatchedBatch: %v", err)
	}
	if len(batch.Rows) != 0 || allItemsCalls != 0 {
		t.Fatalf("batch rows=%+v all-items calls=%d, want skipped buckets", batch.Rows, allItemsCalls)
	}
}

func TestFetchProgressMapsPlaybackRows(t *testing.T) {
	seen := map[string]int{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen[r.URL.Path]++
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/sync/activities":
			_, _ = w.Write([]byte(`{
				"movies":{"playback":"2026-05-04T12:00:00Z"},
				"tv_shows":{"playback":"2026-05-04T12:05:00Z"},
				"anime":{"playback":"2026-05-04T12:10:00Z"}
			}`))
		case "/sync/playback/movies":
			_, _ = w.Write([]byte(`[{"id":123,"type":"movie","progress":45.5,"paused_at":"2026-05-04T12:00:00Z","movie":{"title":"Inception","year":2010,"ids":{"imdb":"tt1375666","tmdb":27205}}}]`))
		case "/sync/playback/episodes":
			_, _ = w.Write([]byte(`[{"id":124,"type":"episode","progress":12.5,"paused_at":"2026-05-04T12:02:00Z","show":{"title":"Breaking Bad","year":2008,"ids":{"tvdb":81189}},"episode":{"title":"Pilot","season":1,"number":1,"tvdb_season":2,"tvdb_number":4}}]`))
		default:
			t.Fatalf("unexpected path %s", r.URL.RequestURI())
		}
	}))
	defer server.Close()

	provider := NewProvider(server.Client(), server.URL)
	rows, err := provider.FetchProgress(context.Background(), watchsync.ServerConfig{
		ClientID:     "client-id",
		ClientSecret: "client-secret",
	}, watchsync.Connection{AccessToken: "token"})
	if err != nil {
		t.Fatalf("FetchProgress: %v", err)
	}
	if len(rows) != 2 || rows[0].ProgressPercent != 45.5 || rows[0].ProviderItemKey != "imdb:tt1375666" {
		t.Fatalf("rows = %+v", rows)
	}
	if rows[1].ProviderItemKey != "show:tvdb:81189:s2:e4" ||
		rows[1].SeasonNumber != 2 || rows[1].EpisodeNumber != 4 {
		t.Fatalf("episode progress row = %+v", rows[1])
	}
	if seen["/sync/playback/movies"] != 1 || seen["/sync/playback/episodes"] != 1 {
		t.Fatalf("typed playback endpoints were not fetched: %+v", seen)
	}
}

func TestFetchProgressBatchSkipsUnchangedPlaybackActivities(t *testing.T) {
	var playbackCalls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/sync/activities":
			_, _ = w.Write([]byte(`{
				"movies":{"playback":"2026-05-04T12:00:00Z"},
				"tv_shows":{"playback":"2026-05-04T12:05:00Z"},
				"anime":{"playback":"2026-05-04T12:10:00Z"}
			}`))
		default:
			playbackCalls++
			_, _ = w.Write([]byte(`[]`))
		}
	}))
	defer server.Close()

	provider := NewProvider(server.Client(), server.URL)
	batch, err := provider.FetchProgressBatch(context.Background(), watchsync.ServerConfig{
		ClientID:     "client-id",
		ClientSecret: "client-secret",
	}, watchsync.Connection{
		AccessToken: "token",
		SyncCursors: map[string]string{
			simklCursorProgressMovies: "2026-05-04T12:00:00Z",
			simklCursorProgressShows:  "2026-05-04T12:05:00Z",
			simklCursorProgressAnime:  "2026-05-04T12:10:00Z",
		},
	})
	if err != nil {
		t.Fatalf("FetchProgressBatch: %v", err)
	}
	if len(batch.Rows) != 0 || playbackCalls != 0 {
		t.Fatalf("batch rows=%+v playback calls=%d, want skipped playback", batch.Rows, playbackCalls)
	}
}

func TestExportHistorySendsSimklPayload(t *testing.T) {
	var gotPath string
	var gotBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"not_found":{"movies":[],"shows":[],"episodes":[]}}`))
	}))
	defer server.Close()

	provider := NewProvider(server.Client(), server.URL)
	result, err := provider.ExportHistory(context.Background(), watchsync.ServerConfig{
		ClientID:     "client-id",
		ClientSecret: "client-secret",
	}, watchsync.Connection{AccessToken: "token"}, []watchsync.LocalPlay{{
		HistoryID: "history-1",
		Kind:      historyimport.KindMovie,
		Title:     "Inception",
		Year:      2010,
		IMDbID:    "tt1375666",
		WatchedAt: time.Date(2026, 5, 4, 12, 0, 0, 0, time.UTC),
	}})
	if err != nil {
		t.Fatalf("ExportHistory: %v", err)
	}
	if gotPath != "/sync/history" {
		t.Fatalf("path = %q, want /sync/history", gotPath)
	}
	if len(result.Sent) != 1 || result.Sent[0] != "history-1" {
		t.Fatalf("sent = %#v", result.Sent)
	}
	movies, _ := gotBody["movies"].([]any)
	if len(movies) != 1 {
		t.Fatalf("body = %#v, want one movie", gotBody)
	}
	movie, _ := movies[0].(map[string]any)
	if movie["watched_at"] == "" {
		t.Fatalf("movie payload missing watched_at: %#v", movie)
	}
}

func TestExportHistoryMapsSimklNotFoundItems(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/sync/history" {
			t.Fatalf("path = %q, want /sync/history", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"not_found": {
				"movies": [{
					"title": "Missing Movie",
					"year": 2026,
					"watched_at": "2026-05-04T12:00:00Z",
					"ids": {"imdb": "tt0000001"}
				}],
				"shows": [{
					"title": "Missing Show",
					"year": 2024,
					"ids": {"tvdb": "12345"},
					"seasons": [{
						"number": 2,
						"episodes": [{
							"number": 3,
							"watched_at": "2026-05-04T13:00:00Z",
							"ids": {"tvdb": "67890"}
						}]
					}]
				}],
				"episodes": []
			}
		}`))
	}))
	defer server.Close()

	provider := NewProvider(server.Client(), server.URL)
	result, err := provider.ExportHistory(context.Background(), watchsync.ServerConfig{
		ClientID:     "client-id",
		ClientSecret: "client-secret",
	}, watchsync.Connection{AccessToken: "token"}, []watchsync.LocalPlay{
		{
			HistoryID: "movie-ok",
			Kind:      historyimport.KindMovie,
			Title:     "Known Movie",
			Year:      2010,
			IMDbID:    "tt1375666",
			WatchedAt: time.Date(2026, 5, 4, 11, 0, 0, 0, time.UTC),
		},
		{
			HistoryID: "movie-missing",
			Kind:      historyimport.KindMovie,
			Title:     "Missing Movie",
			Year:      2026,
			IMDbID:    "tt0000001",
			WatchedAt: time.Date(2026, 5, 4, 12, 0, 0, 0, time.UTC),
		},
		{
			HistoryID:     "episode-missing",
			Kind:          historyimport.KindEpisode,
			SeriesTitle:   "Missing Show",
			SeriesYear:    2024,
			SeriesTVDBID:  "12345",
			SeasonNumber:  2,
			EpisodeNumber: 3,
			TVDBID:        "67890",
			WatchedAt:     time.Date(2026, 5, 4, 13, 0, 0, 0, time.UTC),
		},
	})
	if err != nil {
		t.Fatalf("ExportHistory: %v", err)
	}
	if !containsString(result.Sent, "movie-ok") || len(result.Sent) != 1 {
		t.Fatalf("sent = %#v, want only movie-ok", result.Sent)
	}
	if !containsString(result.NotFound, "movie-missing") ||
		!containsString(result.NotFound, "episode-missing") ||
		len(result.NotFound) != 2 {
		t.Fatalf("not found = %#v, want missing movie and episode", result.NotFound)
	}
}

func TestStopTreatsCompletedConflictAsSuccess(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusConflict)
	}))
	defer server.Close()

	provider := NewProvider(server.Client(), server.URL)
	err := provider.Stop(context.Background(), watchsync.ServerConfig{
		ClientID:     "client-id",
		ClientSecret: "client-secret",
	}, watchsync.Connection{AccessToken: "token"}, watchsync.ScrobbleEvent{
		Kind:            historyimport.KindMovie,
		IMDbID:          "tt1375666",
		PositionSeconds: 5400,
		DurationSeconds: 6000,
		Completed:       true,
	})
	if err != nil {
		t.Fatalf("Stop: %v", err)
	}
}

func containsString(values []string, value string) bool {
	for _, candidate := range values {
		if candidate == value {
			return true
		}
	}
	return false
}
