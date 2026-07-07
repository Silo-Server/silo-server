package trakt

import (
	"bytes"
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
	// The episode carries only a series id + season/episode number, so it must
	// land in the nested shows[] structure, not the flat episodes[] array.
	if _, ok := gotBody["episodes"]; ok {
		t.Fatalf("episodes payload unexpectedly present: %#v", gotBody["episodes"])
	}
	shows, _ := gotBody["shows"].([]any)
	if len(shows) != 1 {
		t.Fatalf("shows payload = %#v, want 1 show", gotBody["shows"])
	}
	show, _ := shows[0].(map[string]any)
	showIDs, _ := show["ids"].(map[string]any)
	if showIDs["tvdb"] != float64(789) {
		t.Fatalf("show ids = %#v, want tvdb 789", show["ids"])
	}
	seasons, _ := show["seasons"].([]any)
	if len(seasons) != 1 {
		t.Fatalf("seasons payload = %#v, want 1 season", show["seasons"])
	}
	season, _ := seasons[0].(map[string]any)
	if season["number"] != float64(0) {
		t.Fatalf("season payload = %#v, want season 0", season)
	}
	seasonEpisodes, _ := season["episodes"].([]any)
	if len(seasonEpisodes) != 1 {
		t.Fatalf("season episodes = %#v, want 1 episode", season["episodes"])
	}
	seasonEpisode, _ := seasonEpisodes[0].(map[string]any)
	if seasonEpisode["number"] != float64(2) {
		t.Fatalf("episode payload = %#v, want number 2", seasonEpisode)
	}
	if _, ok := seasonEpisode["watched_at"]; ok {
		t.Fatalf("remove episode unexpectedly included watched_at: %#v", seasonEpisode)
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

func TestHistoryEpisodeWithoutOwnIDsUsesNestedShowFallback(t *testing.T) {
	play := watchsync.LocalPlay{
		HistoryID:     "history-episode-no-ids",
		Kind:          historyimport.KindEpisode,
		SeriesTMDBID:  "999",
		SeasonNumber:  2,
		EpisodeNumber: 5,
		WatchedAt:     time.Date(2026, 5, 4, 12, 0, 0, 0, time.UTC),
	}

	addPayload := buildHistoryPayload([]watchsync.LocalPlay{play})
	if len(addPayload.Episodes) != 0 {
		t.Fatalf("add payload episodes = %#v, want 0 (episode has no own id)", addPayload.Episodes)
	}
	if len(addPayload.Shows) != 1 {
		t.Fatalf("add payload shows = %#v, want 1", addPayload.Shows)
	}
	addShow := addPayload.Shows[0]
	if addShow.IDs.TMDB != 999 {
		t.Fatalf("add payload show IDs = %#v, want show TMDB 999", addShow.IDs)
	}
	if len(addShow.Seasons) != 1 || addShow.Seasons[0].Number != 2 {
		t.Fatalf("add payload seasons = %#v, want 1 season numbered 2", addShow.Seasons)
	}
	if len(addShow.Seasons[0].Episodes) != 1 || addShow.Seasons[0].Episodes[0].Number != 5 {
		t.Fatalf("add payload episodes = %#v, want 1 episode numbered 5", addShow.Seasons[0].Episodes)
	}
	if addShow.Seasons[0].Episodes[0].WatchedAt != "2026-05-04T12:00:00Z" {
		t.Fatalf("add payload episode watched_at = %q", addShow.Seasons[0].Episodes[0].WatchedAt)
	}

	addJSON, err := json.Marshal(addPayload)
	if err != nil {
		t.Fatalf("marshal add payload: %v", err)
	}
	if bytes.Contains(addJSON, []byte(`"episodes":[{"watched_at"`)) {
		t.Fatalf("add payload JSON must not carry a flat episode with show sibling: %s", addJSON)
	}
	if !bytes.Contains(addJSON, []byte(`"shows":[{"ids":{"trakt":0,"slug":"","imdb":"","tmdb":999,"tvdb":0}`)) {
		t.Fatalf("add payload JSON missing nested show: %s", addJSON)
	}

	removePayload := buildHistoryRemovePayload([]watchsync.LocalPlay{play})
	if len(removePayload.Episodes) != 0 {
		t.Fatalf("remove payload episodes = %#v, want 0", removePayload.Episodes)
	}
	if len(removePayload.Shows) != 1 {
		t.Fatalf("remove payload shows = %#v, want 1", removePayload.Shows)
	}
	removeShow := removePayload.Shows[0]
	if removeShow.IDs.TMDB != 999 {
		t.Fatalf("remove payload show IDs = %#v, want show TMDB 999", removeShow.IDs)
	}
	if len(removeShow.Seasons) != 1 || removeShow.Seasons[0].Number != 2 {
		t.Fatalf("remove payload seasons = %#v, want 1 season numbered 2", removeShow.Seasons)
	}
	if len(removeShow.Seasons[0].Episodes) != 1 || removeShow.Seasons[0].Episodes[0].Number != 5 {
		t.Fatalf("remove payload episodes = %#v, want 1 episode numbered 5", removeShow.Seasons[0].Episodes)
	}

	removeJSON, err := json.Marshal(removePayload)
	if err != nil {
		t.Fatalf("marshal remove payload: %v", err)
	}
	if bytes.Contains(removeJSON, []byte(`"watched_at"`)) {
		t.Fatalf("remove payload JSON must not carry watched_at: %s", removeJSON)
	}
}

func TestHistoryNestedFallbackMergesEpisodesBySeason(t *testing.T) {
	plays := []watchsync.LocalPlay{
		{
			Kind:          historyimport.KindEpisode,
			SeriesTMDBID:  "999",
			SeasonNumber:  1,
			EpisodeNumber: 3,
			WatchedAt:     time.Date(2026, 5, 4, 12, 0, 0, 0, time.UTC),
		},
		{
			Kind:          historyimport.KindEpisode,
			SeriesTMDBID:  "999",
			SeasonNumber:  1,
			EpisodeNumber: 4,
			WatchedAt:     time.Date(2026, 5, 5, 12, 0, 0, 0, time.UTC),
		},
	}

	addPayload := buildHistoryPayload(plays)
	if len(addPayload.Shows) != 1 {
		t.Fatalf("shows = %#v, want 1 merged show", addPayload.Shows)
	}
	show := addPayload.Shows[0]
	if len(show.Seasons) != 1 {
		t.Fatalf("seasons = %#v, want 1 merged season", show.Seasons)
	}
	if len(show.Seasons[0].Episodes) != 2 {
		t.Fatalf("episodes = %#v, want 2 episodes under one season", show.Seasons[0].Episodes)
	}
	if show.Seasons[0].Episodes[0].Number != 3 || show.Seasons[0].Episodes[1].Number != 4 {
		t.Fatalf("episode numbers = %#v, want [3 4]", show.Seasons[0].Episodes)
	}

	removePayload := buildHistoryRemovePayload(plays)
	if len(removePayload.Shows) != 1 || len(removePayload.Shows[0].Seasons) != 1 || len(removePayload.Shows[0].Seasons[0].Episodes) != 2 {
		t.Fatalf("remove payload did not merge by show/season: %#v", removePayload.Shows)
	}
}

func TestHistoryEpisodeWithRealIDsKeepsEpisodeIDs(t *testing.T) {
	play := watchsync.LocalPlay{
		HistoryID:     "history-episode-with-ids",
		Kind:          historyimport.KindEpisode,
		TVDBID:        "54321",
		SeriesTMDBID:  "999",
		SeasonNumber:  2,
		EpisodeNumber: 5,
		WatchedAt:     time.Date(2026, 5, 4, 12, 0, 0, 0, time.UTC),
	}

	addPayload := buildHistoryPayload([]watchsync.LocalPlay{play})
	if len(addPayload.Episodes) != 1 {
		t.Fatalf("add payload episodes = %#v, want 1", addPayload.Episodes)
	}
	if addPayload.Episodes[0].IDs.TVDB != 54321 {
		t.Fatalf("add payload episode IDs = %#v, want TVDB 54321", addPayload.Episodes[0].IDs)
	}
	if len(addPayload.Shows) != 0 {
		t.Fatalf("add payload shows = %#v, want none", addPayload.Shows)
	}

	addJSON, err := json.Marshal(addPayload)
	if err != nil {
		t.Fatalf("marshal add payload: %v", err)
	}
	if !bytes.Contains(addJSON, []byte(`"tvdb":54321`)) {
		t.Fatalf("add payload JSON missing real episode id: %s", addJSON)
	}

	removePayload := buildHistoryRemovePayload([]watchsync.LocalPlay{play})
	if len(removePayload.Episodes) != 1 {
		t.Fatalf("remove payload episodes = %#v, want 1", removePayload.Episodes)
	}
	if removePayload.Episodes[0].IDs.TVDB != 54321 {
		t.Fatalf("remove payload episode IDs = %#v, want TVDB 54321", removePayload.Episodes[0].IDs)
	}
	if len(removePayload.Shows) != 0 {
		t.Fatalf("remove payload shows = %#v, want none", removePayload.Shows)
	}

	removeJSON, err := json.Marshal(removePayload)
	if err != nil {
		t.Fatalf("marshal remove payload: %v", err)
	}
	if !bytes.Contains(removeJSON, []byte(`"tvdb":54321`)) {
		t.Fatalf("remove payload JSON missing real episode id: %s", removeJSON)
	}
}
