package simkl

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/historyimport"
	"github.com/Silo-Server/silo-server/internal/watchsync"
)

const ratingsActivitiesFixture = `{
	"movies":{"rated_at":"2026-05-04T12:00:00Z"},
	"tv_shows":{"rated_at":"2026-05-04T12:05:00Z"},
	"anime":{"rated_at":"2026-05-04T12:10:00Z"}
}`

// ratingsServer answers /sync/activities with activities and each ratings read
// with the body lists holds for its type, recording the ratings requests.
type ratingsServer struct {
	t          *testing.T
	activities string
	lists      map[string]string
	mu         sync.Mutex
	requests   []*http.Request
}

func (s *ratingsServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if r.Method == http.MethodGet && r.URL.Path == "/sync/activities" {
		_, _ = w.Write([]byte(s.activities))
		return
	}
	s.mu.Lock()
	s.requests = append(s.requests, r)
	s.mu.Unlock()
	listType, filter, ok := strings.Cut(strings.TrimPrefix(r.URL.Path, "/sync/ratings/"), "/")
	if r.Method != http.MethodGet || !ok {
		s.t.Errorf("unexpected request %s %s", r.Method, r.URL.RequestURI())
		http.NotFound(w, r)
		return
	}
	if filter != simklEveryRating {
		s.t.Errorf("%s rating filter = %q, want %q", listType, filter, simklEveryRating)
	}
	if r.URL.RawQuery != "" {
		s.t.Errorf("%s query = %q, want a full read without date_from", listType, r.URL.RawQuery)
	}
	body, found := s.lists[listType]
	if !found {
		body = `{}`
	}
	_, _ = w.Write([]byte(body))
}

func (s *ratingsServer) readTypes() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	types := make([]string, 0, len(s.requests))
	for _, r := range s.requests {
		listType, _, _ := strings.Cut(strings.TrimPrefix(r.URL.Path, "/sync/ratings/"), "/")
		types = append(types, listType)
	}
	sort.Strings(types)
	return types
}

func fetchRatingsFrom(t *testing.T, server *ratingsServer, cursors map[string]string) watchsync.RatingImportBatch {
	t.Helper()
	httpServer := httptest.NewServer(server)
	defer httpServer.Close()
	batch, err := NewProvider(httpServer.Client(), httpServer.URL).FetchRatings(context.Background(), watchsync.ServerConfig{
		ClientID:     "client-id",
		ClientSecret: "client-secret",
	}, watchsync.Connection{AccessToken: "token", SyncCursors: cursors})
	if err != nil {
		t.Fatalf("FetchRatings: %v", err)
	}
	return batch
}

func TestFetchRatingsSkipsUnchangedRatedAt(t *testing.T) {
	// Movies and shows match their cursors; anime was never rated (null).
	server := &ratingsServer{t: t, activities: `{
		"movies":{"rated_at":"2026-05-04T12:00:00Z"},
		"tv_shows":{"rated_at":"2026-05-04T12:05:00Z"},
		"anime":{"rated_at":null}
	}`}
	batch := fetchRatingsFrom(t, server, map[string]string{
		simklCursorRatingsMovies: "2026-05-04T12:00:00Z",
		simklCursorRatingsShows:  "2026-05-04T12:05:00Z",
	})
	if reads := server.readTypes(); len(reads) != 0 {
		t.Fatalf("ratings reads = %v, want none", reads)
	}
	if len(batch.Rows) != 0 || len(batch.SnapshotKinds) != 0 || len(batch.UpdatedCursors) != 0 {
		t.Fatalf("batch = %#v, want no rows, snapshot kinds, or cursors", batch)
	}
}

func TestFetchRatingsReadsChangedKindsInFull(t *testing.T) {
	const old = "2026-05-01T00:00:00Z"
	unchanged := map[string]string{
		simklCursorRatingsMovies: "2026-05-04T12:00:00Z",
		simklCursorRatingsShows:  "2026-05-04T12:05:00Z",
		simklCursorRatingsAnime:  "2026-05-04T12:10:00Z",
	}
	withOld := func(key string) map[string]string {
		cursors := make(map[string]string, len(unchanged))
		for k, v := range unchanged {
			cursors[k] = v
		}
		cursors[key] = old
		return cursors
	}
	allCursors := map[string]string{
		simklCursorRatingsMovies: "2026-05-04T12:00:00Z",
		simklCursorRatingsShows:  "2026-05-04T12:05:00Z",
		simklCursorRatingsAnime:  "2026-05-04T12:10:00Z",
	}
	for _, tc := range []struct {
		name          string
		cursors       map[string]string
		wantReads     []string
		wantSnapshots []string
		wantCursors   []string
	}{
		{
			// Anime movies are movies, so a movie snapshot includes anime.
			name:          "movies changed",
			cursors:       withOld(simklCursorRatingsMovies),
			wantReads:     []string{"anime", "movies"},
			wantSnapshots: []string{historyimport.KindMovie},
			wantCursors:   []string{simklCursorRatingsAnime, simklCursorRatingsMovies},
		},
		{
			name:          "shows changed",
			cursors:       withOld(simklCursorRatingsShows),
			wantReads:     []string{"anime", "shows"},
			wantSnapshots: []string{historyimport.KindSeries},
			wantCursors:   []string{simklCursorRatingsAnime, simklCursorRatingsShows},
		},
		{
			name:          "anime changed",
			cursors:       withOld(simklCursorRatingsAnime),
			wantReads:     []string{"anime", "movies", "shows"},
			wantSnapshots: []string{historyimport.KindMovie, historyimport.KindSeries},
			wantCursors:   []string{simklCursorRatingsAnime, simklCursorRatingsMovies, simklCursorRatingsShows},
		},
		{
			name:          "first read",
			cursors:       nil,
			wantReads:     []string{"anime", "movies", "shows"},
			wantSnapshots: []string{historyimport.KindMovie, historyimport.KindSeries},
			wantCursors:   []string{simklCursorRatingsAnime, simklCursorRatingsMovies, simklCursorRatingsShows},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := &ratingsServer{t: t, activities: ratingsActivitiesFixture}
			batch := fetchRatingsFrom(t, server, tc.cursors)
			if reads := server.readTypes(); !reflect.DeepEqual(reads, tc.wantReads) {
				t.Fatalf("ratings reads = %v, want %v", reads, tc.wantReads)
			}
			if !reflect.DeepEqual(batch.SnapshotKinds, tc.wantSnapshots) {
				t.Fatalf("snapshot kinds = %v, want %v", batch.SnapshotKinds, tc.wantSnapshots)
			}
			gotCursors := make([]string, 0, len(batch.UpdatedCursors))
			for key, value := range batch.UpdatedCursors {
				if value != allCursors[key] {
					t.Errorf("cursor %s = %q, want the rated_at activity %q", key, value, allCursors[key])
				}
				gotCursors = append(gotCursors, key)
			}
			sort.Strings(gotCursors)
			if !reflect.DeepEqual(gotCursors, tc.wantCursors) {
				t.Fatalf("updated cursors = %v, want %v", gotCursors, tc.wantCursors)
			}
		})
	}
}

func TestFetchRatingsMapsMoviesShowsAndAnime(t *testing.T) {
	server := &ratingsServer{t: t, activities: ratingsActivitiesFixture, lists: map[string]string{
		"movies": `{"movies":[
			{"user_rating":7,"user_rated_at":"2026-03-01T10:00:00.000Z","status":"completed","movie":{"title":"Heat","year":1995,"ids":{"simkl":1,"imdb":"tt0113277","tmdb":"949"}}},
			{"user_rating":null,"user_rated_at":null,"movie":{"title":"Unrated","year":2001,"ids":{"imdb":"tt0000002"}}},
			{"user_rating":4,"movie":{"title":"No IDs","year":2002,"ids":{}}}
		]}`,
		"shows": `{"shows":[
			{"user_rating":10,"user_rated_at":"2026-03-02T10:00:00Z","status":"watching","show":{"title":"The Wire","year":2002,"ids":{"simkl":2,"imdb":"tt0306414","tvdb":"79126"}}}
		]}`,
		"anime": `{"anime":[
			{"user_rating":9,"user_rated_at":"2026-03-03T10:00:00Z","anime_type":"tv","show":{"title":"Cowboy Bebop","year":1998,"ids":{"simkl":37089,"mal":"1","tvdb":"76885"}}},
			{"user_rating":8,"user_rated_at":"2026-03-04T10:00:00Z","anime_type":"movie","show":{"title":"Akira","year":1988,"ids":{"simkl":3,"imdb":"tt0094625","tmdb":"149"}}}
		]}`,
	}}
	batch := fetchRatingsFrom(t, server, nil)

	byKey := make(map[string]watchsync.RemoteRating, len(batch.Rows))
	for _, row := range batch.Rows {
		byKey[row.ProviderItemKey] = row
	}
	if len(batch.Rows) != 4 || len(byKey) != 4 {
		t.Fatalf("rows = %#v, want the four rated items", batch.Rows)
	}
	movie := byKey["imdb:tt0113277"]
	if movie.Kind != historyimport.KindMovie || movie.Rating != 7 || movie.Title != "Heat" || movie.TMDBID != "949" ||
		!movie.RatedAt.Equal(time.Date(2026, 3, 1, 10, 0, 0, 0, time.UTC)) || movie.Provider != "simkl" {
		t.Fatalf("movie row = %#v", movie)
	}
	if show := byKey["tvdb:79126"]; show.Kind != historyimport.KindSeries || show.Rating != 10 || show.IMDbID != "tt0306414" {
		t.Fatalf("show row = %#v", show)
	}
	if anime := byKey["tvdb:76885"]; anime.Kind != historyimport.KindSeries || anime.Rating != 9 || anime.Title != "Cowboy Bebop" {
		t.Fatalf("anime series row = %#v, want a series", anime)
	}
	if animeMovie := byKey["imdb:tt0094625"]; animeMovie.Kind != historyimport.KindMovie || animeMovie.Rating != 8 || animeMovie.TMDBID != "149" {
		t.Fatalf("anime movie row = %#v, want a movie", animeMovie)
	}
	if len(batch.Warnings) != 1 {
		t.Fatalf("warnings = %v, want one for the movie with no ids", batch.Warnings)
	}
}

func TestRatingRowsFromListClassifiesAnimeByType(t *testing.T) {
	// Every entry carries a TMDB id, which for a movie-like anime entry can
	// be a TMDB movie id.
	const idsJSON = `{"simkl":7,"imdb":"tt0000007","tmdb":"550","tvdb":"76885","mal":"1"}`
	for _, tc := range []struct {
		name      string
		animeType string // "" leaves the field out
		wantKind  string
		wantTMDB  string
		wantKey   string
	}{
		{name: "movie", animeType: `"movie"`, wantKind: historyimport.KindMovie, wantTMDB: "550", wantKey: "imdb:tt0000007"},
		{name: "movie uppercase", animeType: `"MOVIE"`, wantKind: historyimport.KindMovie, wantTMDB: "550", wantKey: "imdb:tt0000007"},
		{name: "tv", animeType: `"tv"`, wantKind: historyimport.KindSeries, wantTMDB: "550", wantKey: "tvdb:76885"},
		{name: "ova", animeType: `"ova"`, wantKind: historyimport.KindSeries, wantKey: "tvdb:76885"},
		{name: "ona", animeType: `"ona"`, wantKind: historyimport.KindSeries, wantKey: "tvdb:76885"},
		{name: "special", animeType: `"special"`, wantKind: historyimport.KindSeries, wantKey: "tvdb:76885"},
		{name: "music video", animeType: `"music video"`, wantKind: historyimport.KindSeries, wantKey: "tvdb:76885"},
		{name: "null", animeType: `null`, wantKind: historyimport.KindSeries, wantKey: "tvdb:76885"},
		{name: "missing", wantKind: historyimport.KindSeries, wantKey: "tvdb:76885"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			typeField := ""
			if tc.animeType != "" {
				typeField = `"anime_type":` + tc.animeType + `,`
			}
			var list simklRatingsList
			body := `{"anime":[{"user_rating":8,` + typeField + `"show":{"title":"T","year":2000,"ids":` + idsJSON + `}}]}`
			if err := json.Unmarshal([]byte(body), &list); err != nil {
				t.Fatalf("decode: %v", err)
			}
			rows, untyped, skipped, warnings := ratingRowsFromList(list, simklTypeAnime, "simkl")
			if len(rows) != 1 || len(skipped) != 0 || len(warnings) != 0 {
				t.Fatalf("rows = %#v warnings = %v, want one row", rows, warnings)
			}
			if wantUntyped := tc.wantKind == historyimport.KindSeries && tc.wantTMDB == ""; untyped != wantUntyped {
				t.Fatalf("untyped = %v, want %v", untyped, wantUntyped)
			}
			row := rows[0]
			if row.Kind != tc.wantKind || row.TMDBID != tc.wantTMDB || row.ProviderItemKey != tc.wantKey {
				t.Fatalf("row kind %q tmdb %q key %q, want %q %q %q",
					row.Kind, row.TMDBID, row.ProviderItemKey, tc.wantKind, tc.wantTMDB, tc.wantKey)
			}
			if row.IMDbID != "tt0000007" || row.TVDBID != "76885" {
				t.Fatalf("row imdb %q tvdb %q, want both kept", row.IMDbID, row.TVDBID)
			}
		})
	}
}

func TestRatingRowsFromListUntypedAnimeWithOnlyTMDBHasNoExternalID(t *testing.T) {
	var list simklRatingsList
	if err := json.Unmarshal([]byte(`{"anime":[
		{"user_rating":7,"anime_type":"ova","show":{"title":"OVA","ids":{"simkl":9,"tmdb":"550"}}},
		{"user_rating":7,"show":{"title":"Untyped","ids":{"tmdb":"551"}}}
	]}`), &list); err != nil {
		t.Fatalf("decode: %v", err)
	}
	rows, _, skipped, warnings := ratingRowsFromList(list, simklTypeAnime, "simkl")
	// The OVA keeps only its Simkl id; the untyped entry has no id left.
	if len(rows) != 1 || rows[0].ProviderItemKey != "simkl:9" || rows[0].Kind != historyimport.KindSeries ||
		rows[0].TMDBID != "" || rows[0].IMDbID != "" || rows[0].TVDBID != "" {
		t.Fatalf("rows = %#v, want one series row keyed by its Simkl id without a TMDB id", rows)
	}
	if len(warnings) != 1 {
		t.Fatalf("warnings = %v, want one for the entry with no usable id", warnings)
	}
	if !reflect.DeepEqual(skipped, map[string]bool{historyimport.KindSeries: true}) {
		t.Fatalf("skipped kinds = %v, want series for the untyped entry", skipped)
	}
}

// ratingsWriteServer records one POST body and answers with response.
func ratingsWriteServer(t *testing.T, wantPath, response string, got any, calls *int) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*calls++
		if r.Method != http.MethodPost || r.URL.Path != wantPath {
			t.Errorf("request = %s %s, want POST %s", r.Method, r.URL.Path, wantPath)
		}
		if err := json.NewDecoder(r.Body).Decode(got); err != nil {
			t.Errorf("decode body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(response))
	}))
}

func TestExportRatingsSendsOneBatch(t *testing.T) {
	ratedAt := time.Date(2026, 4, 5, 6, 7, 8, 0, time.UTC)
	var got map[string][]map[string]any
	var calls int
	server := ratingsWriteServer(t, "/sync/ratings", `{"added":{"movies":1,"shows":1},"not_found":{"movies":[],"shows":[]}}`, &got, &calls)
	defer server.Close()

	result, err := NewProvider(server.Client(), server.URL).ExportRatings(context.Background(), watchsync.ServerConfig{ClientID: "c"}, watchsync.Connection{AccessToken: "t"}, []watchsync.LocalRating{
		{LocalFavorite: watchsync.LocalFavorite{MediaItemID: "m1", Kind: historyimport.KindMovie, IMDbID: "tt0113277", TMDBID: "949", ProviderItemKey: "imdb:tt0113277"}, Rating: 6, RatedAt: ratedAt},
		// An agreed row for a removed media item carries only its key.
		{LocalFavorite: watchsync.LocalFavorite{MediaItemID: "s1", Kind: historyimport.KindSeries, ProviderItemKey: "tvdb:79126"}, Rating: 8},
		{LocalFavorite: watchsync.LocalFavorite{MediaItemID: "no-ids", Kind: historyimport.KindMovie}, Rating: 4},
		{LocalFavorite: watchsync.LocalFavorite{MediaItemID: "episode", Kind: historyimport.KindEpisode, TVDBID: "1"}, Rating: 4},
		{LocalFavorite: watchsync.LocalFavorite{MediaItemID: "out-of-range", Kind: historyimport.KindMovie, IMDbID: "tt1"}, Rating: 11},
	})
	if err != nil {
		t.Fatalf("ExportRatings: %v", err)
	}
	if calls != 1 {
		t.Fatalf("requests = %d, want one batch", calls)
	}
	movies, shows := got["movies"], got["shows"]
	if len(movies) != 1 || movies[0]["rating"] != float64(6) || movies[0]["rated_at"] != "2026-04-05T06:07:08Z" ||
		!reflect.DeepEqual(movies[0]["ids"], map[string]any{"imdb": "tt0113277", "tmdb": float64(949)}) {
		t.Fatalf("movies payload = %#v", movies)
	}
	if len(shows) != 1 || shows[0]["rating"] != float64(8) || shows[0]["rated_at"] != nil ||
		!reflect.DeepEqual(shows[0]["ids"], map[string]any{"tvdb": float64(79126)}) {
		t.Fatalf("shows payload = %#v", shows)
	}
	if len(got) != 2 {
		t.Fatalf("payload keys = %#v, want movies and shows only", got)
	}
	wantSent := []string{"m1", "s1"}
	if !reflect.DeepEqual(result.Sent, wantSent) || len(result.NotFound) != 0 {
		t.Fatalf("result = %#v, want sent %v", result, wantSent)
	}
}

func TestExportRatingsSkipsRequestWithoutUsableItems(t *testing.T) {
	var got any
	var calls int
	server := ratingsWriteServer(t, "/sync/ratings", `{}`, &got, &calls)
	defer server.Close()

	result, err := NewProvider(server.Client(), server.URL).ExportRatings(context.Background(), watchsync.ServerConfig{ClientID: "c"}, watchsync.Connection{AccessToken: "t"}, []watchsync.LocalRating{
		{LocalFavorite: watchsync.LocalFavorite{MediaItemID: "no-ids", Kind: historyimport.KindMovie}, Rating: 4},
	})
	if err != nil {
		t.Fatalf("ExportRatings: %v", err)
	}
	if calls != 0 || len(result.Sent) != 0 || len(result.NotFound) != 0 {
		t.Fatalf("calls = %d result = %#v, want no request and nothing sent", calls, result)
	}
}

func TestExportRatingsMapsNotFoundByAnySharedID(t *testing.T) {
	var got any
	var calls int
	// The movie echo carries only the TMDB id, the show echo only the IMDb id,
	// and a show echo's TMDB id equals a movie's TMDB id, which must not match.
	server := ratingsWriteServer(t, "/sync/ratings", `{
		"added":{"movies":1,"shows":0,"statuses":[]},
		"not_found":{
			"movies":[{"rating":6,"ids":{"tmdb":"949"},"type":"movie"}],
			"shows":[{"rating":8,"ids":{"imdb":"tt0306414"},"type":"show"},{"rating":8,"ids":{"tmdb":1438},"type":"show"}]
		}
	}`, &got, &calls)
	defer server.Close()

	result, err := NewProvider(server.Client(), server.URL).ExportRatings(context.Background(), watchsync.ServerConfig{ClientID: "c"}, watchsync.Connection{AccessToken: "t"}, []watchsync.LocalRating{
		{LocalFavorite: watchsync.LocalFavorite{MediaItemID: "m1", Kind: historyimport.KindMovie, IMDbID: "tt0113277", TMDBID: "949", ProviderItemKey: "imdb:tt0113277"}, Rating: 6},
		{LocalFavorite: watchsync.LocalFavorite{MediaItemID: "s1", Kind: historyimport.KindSeries, IMDbID: "tt0306414", TVDBID: "79126", ProviderItemKey: "tvdb:79126"}, Rating: 8},
		{LocalFavorite: watchsync.LocalFavorite{MediaItemID: "m2", Kind: historyimport.KindMovie, TMDBID: "1438", ProviderItemKey: "tmdb:1438"}, Rating: 10},
	})
	if err != nil {
		t.Fatalf("ExportRatings: %v", err)
	}
	if want := []string{"m1", "s1"}; !reflect.DeepEqual(result.NotFound, want) {
		t.Fatalf("not found = %v, want %v", result.NotFound, want)
	}
	if want := []string{"m2"}; !reflect.DeepEqual(result.Sent, want) {
		t.Fatalf("sent = %v, want %v", result.Sent, want)
	}
}

func TestExportRatingsReportsByMediaItemIDOnly(t *testing.T) {
	var got any
	var calls int
	// A movie and a series share the key tmdb:550; only the series is
	// echoed as not found.
	server := ratingsWriteServer(t, "/sync/ratings", `{
		"added":{"movies":1,"shows":0},
		"not_found":{"movies":[],"shows":[{"rating":8,"ids":{"tmdb":550},"type":"show"}]}
	}`, &got, &calls)
	defer server.Close()

	result, err := NewProvider(server.Client(), server.URL).ExportRatings(context.Background(), watchsync.ServerConfig{ClientID: "c"}, watchsync.Connection{AccessToken: "t"}, []watchsync.LocalRating{
		{LocalFavorite: watchsync.LocalFavorite{MediaItemID: "movie", Kind: historyimport.KindMovie, TMDBID: "550", ProviderItemKey: "tmdb:550"}, Rating: 6},
		{LocalFavorite: watchsync.LocalFavorite{MediaItemID: "series", Kind: historyimport.KindSeries, TMDBID: "550", ProviderItemKey: "tmdb:550"}, Rating: 8},
	})
	if err != nil {
		t.Fatalf("ExportRatings: %v", err)
	}
	if want := []string{"movie"}; !reflect.DeepEqual(result.Sent, want) {
		t.Fatalf("sent = %v, want %v and no provider item key", result.Sent, want)
	}
	if want := []string{"series"}; !reflect.DeepEqual(result.NotFound, want) {
		t.Fatalf("not found = %v, want %v and no provider item key", result.NotFound, want)
	}
}

func TestRemoveRatingsSendsIDsOnly(t *testing.T) {
	var got map[string][]map[string]any
	var calls int
	server := ratingsWriteServer(t, "/sync/ratings/remove", `{
		"deleted":{"movies":1,"shows":0},
		"not_found":{"movies":[],"shows":[{"ids":{"tvdb":"79126"},"type":"show"}]}
	}`, &got, &calls)
	defer server.Close()

	result, err := NewProvider(server.Client(), server.URL).RemoveRatings(context.Background(), watchsync.ServerConfig{ClientID: "c"}, watchsync.Connection{AccessToken: "t"}, []watchsync.LocalFavorite{
		{MediaItemID: "m1", Kind: historyimport.KindMovie, ProviderItemKey: "tmdb:949"},
		{MediaItemID: "s1", Kind: historyimport.KindSeries, IMDbID: "tt0306414", TVDBID: "79126", ProviderItemKey: "tvdb:79126"},
		{MediaItemID: "no-ids", Kind: historyimport.KindSeries},
	})
	if err != nil {
		t.Fatalf("RemoveRatings: %v", err)
	}
	if calls != 1 {
		t.Fatalf("requests = %d, want one batch", calls)
	}
	if want := []map[string]any{{"ids": map[string]any{"tmdb": float64(949)}}}; !reflect.DeepEqual(got["movies"], want) {
		t.Fatalf("movies payload = %#v, want ids without a rating", got["movies"])
	}
	if want := []map[string]any{{"ids": map[string]any{"imdb": "tt0306414", "tvdb": float64(79126)}}}; !reflect.DeepEqual(got["shows"], want) {
		t.Fatalf("shows payload = %#v, want ids without a rating", got["shows"])
	}
	if want := []string{"m1"}; !reflect.DeepEqual(result.Sent, want) {
		t.Fatalf("sent = %v, want %v", result.Sent, want)
	}
	if want := []string{"s1"}; !reflect.DeepEqual(result.NotFound, want) {
		t.Fatalf("not found = %v, want %v", result.NotFound, want)
	}
}

func TestRatingExportRequiresWatchedForMoviesOnly(t *testing.T) {
	provider := NewProvider(nil, "")
	var gate watchsync.RatingExportWatchGate = provider
	if !gate.RatingExportRequiresWatched(historyimport.KindMovie) {
		t.Fatal("movie ratings must wait for a completed play")
	}
	if gate.RatingExportRequiresWatched(historyimport.KindSeries) {
		t.Fatal("series ratings must not wait for a completed play")
	}
}

func TestFetchRatingsUntypedAnimeIsNotAMovieSnapshot(t *testing.T) {
	server := &ratingsServer{t: t, activities: ratingsActivitiesFixture, lists: map[string]string{
		"movies": `{"movies":[]}`,
		"shows":  `{"shows":[]}`,
		// The documented ratings read carries no anime_type.
		"anime": `{"anime":[{"user_rating":8,"show":{"title":"Akira","year":1988,"ids":{"simkl":3,"imdb":"tt0094625"}}}]}`,
	}}
	batch := fetchRatingsFrom(t, server, nil)
	if slices.Contains(batch.SnapshotKinds, historyimport.KindMovie) {
		t.Fatalf("snapshot kinds = %v; an untyped anime entry could be a movie", batch.SnapshotKinds)
	}
	if !slices.Contains(batch.SnapshotKinds, historyimport.KindSeries) || len(batch.Warnings) == 0 {
		t.Fatalf("snapshot kinds = %v warnings = %v, want series kept and a warning", batch.SnapshotKinds, batch.Warnings)
	}
}

func TestFetchRatingsSkippedEntryIsNotASnapshot(t *testing.T) {
	for _, tc := range []struct {
		name          string
		lists         map[string]string
		wantSnapshots []string
	}{
		{
			// The untyped entry's only id is TMDB, which animeRatingIdentity
			// clears, so it is skipped as a series. Being untyped, it also
			// keeps the movie snapshot out.
			name: "untyped anime with only a tmdb id",
			lists: map[string]string{
				"anime": `{"anime":[{"user_rating":7,"show":{"title":"Untyped","ids":{"tmdb":"551"}}}]}`,
			},
			wantSnapshots: nil,
		},
		{
			name: "show with no ids",
			lists: map[string]string{
				"shows": `{"shows":[{"user_rating":6,"show":{"title":"No IDs","year":2003,"ids":{}}}]}`,
			},
			wantSnapshots: []string{historyimport.KindMovie},
		},
		{
			name: "typed anime movie with no ids",
			lists: map[string]string{
				"anime": `{"anime":[{"user_rating":6,"anime_type":"movie","show":{"title":"No IDs","ids":{}}}]}`,
			},
			wantSnapshots: []string{historyimport.KindSeries},
		},
		{
			name: "movie with no ids",
			lists: map[string]string{
				"movies": `{"movies":[{"user_rating":4,"movie":{"title":"No IDs","year":2002,"ids":{}}}]}`,
			},
			wantSnapshots: []string{historyimport.KindSeries},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := &ratingsServer{t: t, activities: ratingsActivitiesFixture, lists: tc.lists}
			batch := fetchRatingsFrom(t, server, nil)
			if !reflect.DeepEqual(batch.SnapshotKinds, tc.wantSnapshots) {
				t.Fatalf("snapshot kinds = %v, want %v", batch.SnapshotKinds, tc.wantSnapshots)
			}
			if len(batch.Rows) != 0 || len(batch.Warnings) == 0 {
				t.Fatalf("rows = %#v warnings = %v, want no rows and a warning", batch.Rows, batch.Warnings)
			}
		})
	}
}
