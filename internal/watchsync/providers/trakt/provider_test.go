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

func TestProviderIdentityAndCapabilities(t *testing.T) {
	provider := NewProvider(nil, "")

	if provider.Key() != "trakt" {
		t.Fatalf("got key %q, want trakt", provider.Key())
	}
	if provider.DisplayName() != "Trakt" {
		t.Fatalf("got display name %q, want Trakt", provider.DisplayName())
	}
	if provider.Capabilities() != (watchsync.Capabilities{
		ImportWatched:    true,
		ImportProgress:   true,
		ExportWatched:    true,
		ExportUnwatched:  true,
		ImportFavorites:  true,
		ExportFavorites:  true,
		RemoveFavorites:  true,
		ImportWatchlist:  true,
		ExportWatchlist:  true,
		RemoveWatchlist:  true,
		ScrobblePlayback: true,
	}) {
		t.Fatalf("unexpected capabilities: %#v", provider.Capabilities())
	}
}

func TestStartDeviceAuthRequiresConfiguredServerConfig(t *testing.T) {
	provider := NewProvider(http.DefaultClient, "http://127.0.0.1")

	if _, err := provider.StartDeviceAuth(context.Background(), watchsync.ServerConfig{
		ClientID: "client-id",
	}); err == nil {
		t.Fatal("expected unconfigured server config to be rejected")
	}
}

func TestStartDeviceAuthSendsTraktHeadersAndDecodesResponse(t *testing.T) {
	var gotPath string
	var gotHeaders http.Header
	var gotBody map[string]string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotHeaders = r.Header.Clone()
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Fatalf("decode request body: %v", err)
		}

		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(map[string]any{
			"device_code":      "device-code",
			"user_code":        "user-code",
			"verification_url": "https://trakt.tv/activate",
			"expires_in":       600,
			"interval":         5,
		}); err != nil {
			t.Fatalf("encode response: %v", err)
		}
	}))
	defer server.Close()

	provider := NewProvider(server.Client(), server.URL+"/")
	before := time.Now()
	session, err := provider.StartDeviceAuth(context.Background(), watchsync.ServerConfig{
		ClientID:     "client-id",
		ClientSecret: "client-secret",
	})
	if err != nil {
		t.Fatalf("start device auth: %v", err)
	}
	after := time.Now()

	if gotPath != "/oauth/device/code" {
		t.Fatalf("got path %q, want /oauth/device/code", gotPath)
	}
	if gotHeaders.Get("Content-Type") != "application/json" {
		t.Fatalf("got content type %q, want application/json", gotHeaders.Get("Content-Type"))
	}
	if gotHeaders.Get("trakt-api-version") != "2" {
		t.Fatalf("got trakt api version %q, want 2", gotHeaders.Get("trakt-api-version"))
	}
	if gotHeaders.Get("trakt-api-key") != "client-id" {
		t.Fatalf("got trakt api key %q, want client-id", gotHeaders.Get("trakt-api-key"))
	}
	if gotBody["client_id"] != "client-id" {
		t.Fatalf("got client_id %q, want client-id", gotBody["client_id"])
	}
	if session.Provider != "trakt" {
		t.Fatalf("got provider %q, want trakt", session.Provider)
	}
	if session.DeviceCode != "device-code" {
		t.Fatalf("got device code %q, want device-code", session.DeviceCode)
	}
	if session.UserCode != "user-code" {
		t.Fatalf("got user code %q, want user-code", session.UserCode)
	}
	if session.VerificationURL != "https://trakt.tv/activate" {
		t.Fatalf("got verification URL %q, want https://trakt.tv/activate", session.VerificationURL)
	}
	if session.IntervalSeconds != 5 {
		t.Fatalf("got interval %d, want 5", session.IntervalSeconds)
	}
	if session.ExpiresAt.Before(before.Add(600*time.Second)) ||
		session.ExpiresAt.After(after.Add(600*time.Second)) {
		t.Fatalf("got expires at %s, want about 600s in the future", session.ExpiresAt)
	}
}

func TestStartDeviceAuthRejectsIncompleteResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"device_code":"device-code","expires_in":600,"interval":5}`))
	}))
	defer server.Close()

	provider := NewProvider(server.Client(), server.URL)
	_, err := provider.StartDeviceAuth(context.Background(), watchsync.ServerConfig{
		ClientID:     "client-id",
		ClientSecret: "client-secret",
	})
	if err == nil {
		t.Fatal("expected incomplete response to be rejected")
	}
}

func TestRemoveHistorySendsTraktRemovePayload(t *testing.T) {
	var gotPath string
	var gotBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Fatalf("decode request body: %v", err)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	provider := NewProvider(server.Client(), server.URL)
	result, err := provider.RemoveHistory(context.Background(), watchsync.ServerConfig{
		ClientID:     "client-id",
		ClientSecret: "client-secret",
	}, watchsync.Connection{AccessToken: "token"}, []watchsync.LocalPlay{
		{
			HistoryID: "history-1",
			Kind:      historyimport.KindMovie,
			IMDbID:    "tt123",
			TMDBID:    "456",
		},
		{
			HistoryID:       "history-2",
			Kind:            historyimport.KindEpisode,
			SeriesTVDBID:    "789",
			SeasonNumber:    0,
			EpisodeNumber:   2,
			ProviderItemKey: "show:tvdb:789:s0:e2",
		},
	})
	if err != nil {
		t.Fatalf("RemoveHistory: %v", err)
	}
	if gotPath != "/sync/history/remove" {
		t.Fatalf("got path %q, want /sync/history/remove", gotPath)
	}
	if len(result.Sent) != 2 {
		t.Fatalf("sent history IDs = %#v, want 2 IDs", result.Sent)
	}
	if _, ok := gotBody["watched_at"]; ok {
		t.Fatalf("remove payload unexpectedly included watched_at: %#v", gotBody)
	}
	movies, _ := gotBody["movies"].([]any)
	if len(movies) != 1 {
		t.Fatalf("movies payload = %#v, want 1 movie", gotBody["movies"])
	}
	episodes, _ := gotBody["episodes"].([]any)
	if len(episodes) != 1 {
		t.Fatalf("episodes payload = %#v, want 1 episode", gotBody["episodes"])
	}
	episode, _ := episodes[0].(map[string]any)
	if episode["season"] != float64(0) || episode["number"] != float64(2) {
		t.Fatalf("episode payload = %#v, want S00E02", episode)
	}
}

func TestFetchFavoritesGetsMoviesAndShows(t *testing.T) {
	var paths []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/users/me/favorites/movies/added":
			_, _ = w.Write([]byte(`[{"listed_at":"2026-05-04T12:00:00Z","movie":{"title":"Movie","year":2026,"ids":{"imdb":"tt123","tmdb":456}}}]`))
		case "/users/me/favorites/shows/added":
			_, _ = w.Write([]byte(`[{"listed_at":"2026-05-04T13:00:00Z","show":{"title":"Show","year":2025,"ids":{"tvdb":789,"tmdb":987}}}]`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	provider := NewProvider(server.Client(), server.URL)
	rows, err := provider.FetchFavorites(context.Background(), watchsync.ServerConfig{
		ClientID:     "client-id",
		ClientSecret: "client-secret",
	}, watchsync.Connection{AccessToken: "token"})
	if err != nil {
		t.Fatalf("FetchFavorites: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("rows = %#v, want 2", rows)
	}
	if paths[0] != "/users/me/favorites/movies/added" || paths[1] != "/users/me/favorites/shows/added" {
		t.Fatalf("paths = %#v", paths)
	}
	if rows[0].Kind != historyimport.KindMovie || rows[0].ProviderItemKey != "imdb:tt123" {
		t.Fatalf("movie row = %#v", rows[0])
	}
	if rows[1].Kind != historyimport.KindSeries || rows[1].ProviderItemKey != "tvdb:789" {
		t.Fatalf("show row = %#v", rows[1])
	}
}

func TestExportFavoritesSendsMovieAndShowPayload(t *testing.T) {
	var gotPath string
	var gotBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Fatalf("decode request body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"added":{"movies":1,"shows":1},"existing":{"movies":0,"shows":0},"not_found":{"movies":[],"shows":[]},"list":{"updated_at":"2026-05-04T12:00:00Z","item_count":2}}`))
	}))
	defer server.Close()

	provider := NewProvider(server.Client(), server.URL)
	result, err := provider.ExportFavorites(context.Background(), watchsync.ServerConfig{
		ClientID:     "client-id",
		ClientSecret: "client-secret",
	}, watchsync.Connection{AccessToken: "token"}, []watchsync.LocalFavorite{
		{MediaItemID: "movie-1", Kind: historyimport.KindMovie, IMDbID: "tt123"},
		{MediaItemID: "show-1", Kind: historyimport.KindSeries, TVDBID: "789"},
	})
	if err != nil {
		t.Fatalf("ExportFavorites: %v", err)
	}
	if gotPath != "/sync/favorites" {
		t.Fatalf("got path %q, want /sync/favorites", gotPath)
	}
	if len(result.Sent) != 4 {
		t.Fatalf("sent = %#v, want media IDs and provider keys", result.Sent)
	}
	if movies, _ := gotBody["movies"].([]any); len(movies) != 1 {
		t.Fatalf("movies payload = %#v", gotBody["movies"])
	}
	if shows, _ := gotBody["shows"].([]any); len(shows) != 1 {
		t.Fatalf("shows payload = %#v", gotBody["shows"])
	}
}

func TestRemoveFavoritesCanUseProviderItemKeys(t *testing.T) {
	var gotBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/sync/favorites/remove" {
			t.Fatalf("got path %q, want /sync/favorites/remove", r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Fatalf("decode request body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"deleted":{"movies":0,"shows":1},"not_found":{"movies":[],"shows":[]},"list":{"updated_at":"2026-05-04T12:00:00Z","item_count":0}}`))
	}))
	defer server.Close()

	provider := NewProvider(server.Client(), server.URL)
	result, err := provider.RemoveFavorites(context.Background(), watchsync.ServerConfig{
		ClientID:     "client-id",
		ClientSecret: "client-secret",
	}, watchsync.Connection{AccessToken: "token"}, []watchsync.LocalFavorite{
		{MediaItemID: "show-1", Kind: historyimport.KindSeries, ProviderItemKey: "tvdb:789"},
	})
	if err != nil {
		t.Fatalf("RemoveFavorites: %v", err)
	}
	if len(result.Sent) != 2 {
		t.Fatalf("sent = %#v, want media ID and provider key", result.Sent)
	}
	shows, _ := gotBody["shows"].([]any)
	if len(shows) != 1 {
		t.Fatalf("shows payload = %#v", gotBody["shows"])
	}
}

func TestHistoryPayloadsIncludeTVDBOnlyMovieIDs(t *testing.T) {
	play := watchsync.LocalPlay{
		HistoryID: "history-tvdb",
		Kind:      historyimport.KindMovie,
		TVDBID:    "12345",
		WatchedAt: time.Date(2026, 5, 4, 12, 0, 0, 0, time.UTC),
	}

	addPayload := buildHistoryPayload([]watchsync.LocalPlay{play})
	if len(addPayload.Movies) != 1 || addPayload.Movies[0].IDs.TVDB != 12345 {
		t.Fatalf("add payload movie IDs = %#v, want TVDB 12345", addPayload.Movies)
	}

	removePayload := buildHistoryRemovePayload([]watchsync.LocalPlay{play})
	if len(removePayload.Movies) != 1 || removePayload.Movies[0].IDs.TVDB != 12345 {
		t.Fatalf("remove payload movie IDs = %#v, want TVDB 12345", removePayload.Movies)
	}
}
