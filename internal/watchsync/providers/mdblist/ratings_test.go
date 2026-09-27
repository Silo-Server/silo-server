package mdblist

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/historyimport"
	"github.com/Silo-Server/silo-server/internal/watchsync"
)

func TestFetchRatingsUsesCursorPagination(t *testing.T) {
	var queries []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/sync/ratings" {
			t.Errorf("path = %q, want /sync/ratings", r.URL.Path)
		}
		queries = append(queries, r.URL.RawQuery)
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Query().Get("cursor") == "ratings-next" {
			// The second page has no shows list; the first page's list still
			// makes the show ratings a complete snapshot.
			_, _ = w.Write([]byte(`{"movies":[{"rated_at":"2025-10-22T09:00:00Z","rating":6,"movie":{"title":"Heat","year":1995,"ids":{"tmdb":"949"}}}],"pagination":{"next_cursor":null}}`))
			return
		}
		_, _ = w.Write([]byte(`{
			"movies":[
				{"rated_at":"2025-10-21T14:00:00Z","rating":8,"movie":{"title":"The Avengers","year":2012,"ids":{"trakt":24428,"imdb":"tt0848228","tmdb":24428,"kitsu":67890}}},
				{"rated_at":null,"rating":null,"movie":{"title":"Unrated","year":2001,"ids":{"imdb":"tt0000001"}}},
				{"rated_at":null,"rating":0,"movie":{"title":"Cleared","year":2002,"ids":{"imdb":"tt0000002"}}}
			],
			"shows":[{"rated_at":"2025-10-20T15:00:00Z","rating":9.0,"show":{"title":"Breaking Bad","year":2008,"ids":{"imdb":"tt0903747","tmdb":1396,"tvdb":81189}}}],
			"seasons":[{"rated_at":"2025-10-15T20:00:00Z","rating":8,"season":{"number":1,"show":{"ids":{"tmdb":1396}}}}],
			"episodes":[{"rated_at":"2025-10-15T21:00:00Z","rating":10,"episode":{"season":1,"number":1,"ids":{"tmdb":62085},"show":{"ids":{"tmdb":1396}}}}],
			"pagination":{"total":4,"limit":1000,"next_cursor":"ratings-next"}
		}`))
	}))
	defer server.Close()

	batch, err := NewProvider(server.Client(), server.URL).FetchRatings(context.Background(), watchsync.ServerConfig{}, watchsync.Connection{AccessToken: "k"})
	if err != nil {
		t.Fatalf("fetch ratings: %v", err)
	}
	if len(queries) != 2 || strings.Contains(queries[0], "cursor=") || !strings.Contains(queries[1], "cursor=ratings-next") {
		t.Fatalf("unexpected pagination queries: %#v", queries)
	}
	for _, query := range queries {
		if !strings.Contains(query, "limit=1000") {
			t.Fatalf("query %q does not request 1000 items", query)
		}
	}
	if !slices.Equal(batch.SnapshotKinds, []string{historyimport.KindMovie, historyimport.KindSeries}) {
		t.Fatalf("snapshot kinds = %v, want movie and series", batch.SnapshotKinds)
	}
	if len(batch.Rows) != 3 {
		t.Fatalf("rows = %#v, want two rated movies and one rated show", batch.Rows)
	}
	avengers, show, heat := batch.Rows[0], batch.Rows[1], batch.Rows[2]
	if avengers.Kind != historyimport.KindMovie || avengers.Rating != 8 || avengers.ProviderItemKey != "imdb:tt0848228" ||
		avengers.IMDbID != "tt0848228" || avengers.TMDBID != "24428" || avengers.Title != "The Avengers" || avengers.Year != 2012 ||
		!avengers.RatedAt.Equal(time.Date(2025, 10, 21, 14, 0, 0, 0, time.UTC)) || avengers.Provider != "mdblist" {
		t.Fatalf("movie row = %#v", avengers)
	}
	if show.Kind != historyimport.KindSeries || show.Rating != 9 || show.ProviderItemKey != "tvdb:81189" ||
		show.TVDBID != "81189" || show.TMDBID != "1396" || show.IMDbID != "tt0903747" {
		t.Fatalf("show row = %#v", show)
	}
	if heat.Kind != historyimport.KindMovie || heat.Rating != 6 || heat.ProviderItemKey != "tmdb:949" {
		t.Fatalf("second page movie row = %#v", heat)
	}
}

func TestFetchRatingsLeavesSeriesOutOfSnapshotWithoutShowsList(t *testing.T) {
	cases := map[string]struct {
		body      string
		wantKinds []string
	}{
		// The documented sample response: no shows key and legacy pagination.
		"documented sample": {
			body:      `{"movies":[{"rated_at":"2025-10-21T14:00:00Z","rating":8,"movie":{"title":"The Avengers","year":2012,"ids":{"imdb":"tt0848228"}}}],"seasons":[],"episodes":[],"pagination":{"offset":0,"limit":1000,"total_movies":1,"has_more":false}}`,
			wantKinds: []string{historyimport.KindMovie},
		},
		"null shows": {
			body:      `{"movies":[],"shows":null,"pagination":{"next_cursor":null}}`,
			wantKinds: []string{historyimport.KindMovie},
		},
		"empty shows": {
			body:      `{"movies":[],"shows":[],"pagination":{"next_cursor":null}}`,
			wantKinds: []string{historyimport.KindMovie, historyimport.KindSeries},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(tc.body))
			}))
			defer server.Close()

			batch, err := NewProvider(server.Client(), server.URL).FetchRatings(context.Background(), watchsync.ServerConfig{}, watchsync.Connection{AccessToken: "k"})
			if err != nil {
				t.Fatalf("fetch ratings: %v", err)
			}
			if !slices.Equal(batch.SnapshotKinds, tc.wantKinds) {
				t.Fatalf("snapshot kinds = %v, want %v", batch.SnapshotKinds, tc.wantKinds)
			}
		})
	}
}

func TestFetchRatingsLeavesKindWithUnidentifiedTitleOutOfSnapshot(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"movies":[{"rating":7,"movie":{"ids":{"imdb":"tt0113277"}}}],"shows":[{"rating":8,"show":{"title":"No IDs","ids":{"trakt":5}}},{"rating":9,"show":{"ids":{"tvdb":81189}}}],"pagination":{"next_cursor":null}}`))
	}))
	defer server.Close()

	batch, err := NewProvider(server.Client(), server.URL).FetchRatings(context.Background(), watchsync.ServerConfig{}, watchsync.Connection{AccessToken: "k"})
	if err != nil {
		t.Fatalf("fetch ratings: %v", err)
	}
	if !slices.Equal(batch.SnapshotKinds, []string{historyimport.KindMovie}) {
		t.Fatalf("snapshot kinds = %v, want movie only", batch.SnapshotKinds)
	}
	if len(batch.Warnings) != 1 || !strings.Contains(batch.Warnings[0], historyimport.KindSeries) {
		t.Fatalf("warnings = %#v, want one series warning", batch.Warnings)
	}
	if len(batch.Rows) != 2 {
		t.Fatalf("rows = %#v, want the identified movie and show", batch.Rows)
	}
}

func TestFetchRatingsCountsMDBListOnlyIDsAsUnidentified(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"movies":[
				{"rating":7,"movie":{"title":"MDBList only","ids":{"mdblist":"8plj","trakt":5}}},
				{"rating":6,"movie":{"ids":{"imdb":"tt0113277"}}}
			],
			"shows":[{"rating":9,"show":{"ids":{"tvdb":81189,"mdblist":"9abc"}}}],
			"pagination":{"next_cursor":null}
		}`))
	}))
	defer server.Close()

	batch, err := NewProvider(server.Client(), server.URL).FetchRatings(context.Background(), watchsync.ServerConfig{}, watchsync.Connection{AccessToken: "k"})
	if err != nil {
		t.Fatalf("fetch ratings: %v", err)
	}
	if !slices.Equal(batch.SnapshotKinds, []string{historyimport.KindSeries}) {
		t.Fatalf("snapshot kinds = %v, want series only", batch.SnapshotKinds)
	}
	if len(batch.Warnings) != 1 || !strings.Contains(batch.Warnings[0], historyimport.KindMovie) {
		t.Fatalf("warnings = %#v, want one movie warning", batch.Warnings)
	}
	if len(batch.Rows) != 2 || batch.Rows[0].ProviderItemKey != "imdb:tt0113277" || batch.Rows[1].ProviderItemKey != "tvdb:81189" {
		t.Fatalf("rows = %#v, want the IMDb movie and the TVDB show only", batch.Rows)
	}
}

// ratingsPage renders one ratings page of movies, shows, and episodes with
// the given pagination. Entries are numbered from first so pages do not
// repeat an entry.
func ratingsPage(first, movies, shows, episodes int, pagination string) string {
	var movieRows, showRows, episodeRows []string
	for i := range movies {
		movieRows = append(movieRows, fmt.Sprintf(`{"rating":7,"movie":{"ids":{"tmdb":%d}}}`, first+i+1))
	}
	for i := range shows {
		showRows = append(showRows, fmt.Sprintf(`{"rating":8,"show":{"ids":{"tvdb":%d}}}`, first+movies+i+1))
	}
	for i := range episodes {
		episodeRows = append(episodeRows, fmt.Sprintf(`{"rating":9,"episode":{"ids":{"tmdb":%d}}}`, first+movies+shows+i+1))
	}
	return fmt.Sprintf(`{"movies":[%s],"shows":[%s],"episodes":[%s],"pagination":%s}`,
		strings.Join(movieRows, ","), strings.Join(showRows, ","), strings.Join(episodeRows, ","), pagination)
}

func TestFetchRatingsPagesByOffsetUntilTotal(t *testing.T) {
	// Offset pagination without next_cursor or has_more: only total says
	// that more entries remain. Each page mixes movies, shows, and episodes,
	// and the offset counts all of them.
	pages := map[string]string{
		"":     ratingsPage(0, 500, 250, 250, `{"total":2500,"limit":1000,"offset":0,"next_cursor":null}`),
		"1000": ratingsPage(1000, 500, 250, 250, `{"total":2500,"limit":1000,"offset":1000,"next_cursor":null}`),
		"2000": ratingsPage(2000, 250, 125, 125, `{"total":2500,"limit":1000,"offset":2000,"next_cursor":null}`),
	}
	var offsets []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		offset := r.URL.Query().Get("offset")
		offsets = append(offsets, offset)
		body, ok := pages[offset]
		if !ok {
			t.Errorf("unexpected offset %q", offset)
			body = `{"movies":[],"shows":[],"pagination":{"total":2500,"next_cursor":null}}`
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	defer server.Close()

	batch, err := NewProvider(server.Client(), server.URL).FetchRatings(context.Background(), watchsync.ServerConfig{}, watchsync.Connection{AccessToken: "k"})
	if err != nil {
		t.Fatalf("fetch ratings: %v", err)
	}
	if !slices.Equal(offsets, []string{"", "1000", "2000"}) {
		t.Fatalf("offsets = %#v, want three pages", offsets)
	}
	if len(batch.Rows) != 1875 {
		t.Fatalf("rows = %d, want every rated movie and show", len(batch.Rows))
	}
	// Offset pages can shift under a concurrent change, so the read imports
	// what it saw but claims no snapshot.
	if len(batch.SnapshotKinds) != 0 || len(batch.Warnings) != 1 || !strings.Contains(batch.Warnings[0], "offset") {
		t.Fatalf("snapshot kinds = %v warnings = %v, want no snapshot and an offset warning", batch.SnapshotKinds, batch.Warnings)
	}
}

func TestFetchRatingsShortOfTotalIsNotASnapshot(t *testing.T) {
	cases := map[string]struct {
		// pages maps "cursor|offset" to a response body; any other request
		// gets an empty page.
		pages     map[string]string
		wantPages []string
		wantRows  int
	}{
		"cursor read ends short": {
			pages: map[string]string{
				"|":   ratingsPage(0, 2, 0, 0, `{"total":5,"limit":1000,"next_cursor":"c2"}`),
				"c2|": ratingsPage(2, 0, 1, 0, `{"total":5,"limit":1000,"next_cursor":null}`),
			},
			// The read falls back to the offset of the entries read so
			// far, which comes back empty.
			wantPages: []string{"|", "c2|", "|3"},
			wantRows:  3,
		},
		"offset read ends short": {
			pages: map[string]string{
				"|": ratingsPage(0, 1, 1, 1, `{"total":10,"limit":1000,"offset":0,"next_cursor":null}`),
			},
			wantPages: []string{"|", "|3"},
			wantRows:  2,
		},
		"legacy per-type totals": {
			pages: map[string]string{
				"|": `{"movies":[{"rating":8,"movie":{"ids":{"imdb":"tt0848228"}}}],"shows":[],"seasons":[],"episodes":[],"pagination":{"offset":0,"limit":1000,"total_movies":2,"total_seasons":1,"has_more":false}}`,
			},
			wantPages: []string{"|", "|1"},
			wantRows:  1,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			var requested []string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				key := r.URL.Query().Get("cursor") + "|" + r.URL.Query().Get("offset")
				requested = append(requested, key)
				body, ok := tc.pages[key]
				if !ok {
					body = `{"movies":[],"shows":[],"pagination":{"next_cursor":null}}`
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(body))
			}))
			defer server.Close()

			batch, err := NewProvider(server.Client(), server.URL).FetchRatings(context.Background(), watchsync.ServerConfig{}, watchsync.Connection{AccessToken: "k"})
			if err != nil {
				t.Fatalf("fetch ratings: %v", err)
			}
			if !slices.Equal(requested, tc.wantPages) {
				t.Fatalf("pages = %#v, want %#v", requested, tc.wantPages)
			}
			if len(batch.SnapshotKinds) != 0 {
				t.Fatalf("snapshot kinds = %v, want none for a short read", batch.SnapshotKinds)
			}
			if len(batch.Warnings) != 1 || !strings.Contains(batch.Warnings[0], "entries") {
				t.Fatalf("warnings = %#v, want one short-read warning", batch.Warnings)
			}
			if len(batch.Rows) != tc.wantRows {
				t.Fatalf("rows = %d, want %d: a short read still imports what it read", len(batch.Rows), tc.wantRows)
			}
		})
	}
}

func TestFetchRatingsRepeatedEntryIsNotASnapshot(t *testing.T) {
	cases := map[string]struct {
		// pages maps "cursor|offset" to a response body; any other request
		// gets an empty page.
		pages     map[string]string
		wantKinds []string
		wantRows  int
		// wantWarning is the reason a read claims no snapshot; empty for a
		// clean read.
		wantWarning string
	}{
		// Offsets can shift without a repeat: B removed and X added after
		// the boundary skips an entry while the count still reaches total.
		"offset read without a repeat": {
			pages: map[string]string{
				"|":  ratingsPage(0, 2, 1, 0, `{"total":5,"limit":3,"offset":0,"next_cursor":null}`),
				"|3": ratingsPage(3, 1, 1, 0, `{"total":5,"limit":3,"offset":3,"next_cursor":null}`),
			},
			wantRows:    5,
			wantWarning: "offset",
		},
		"clean cursor read": {
			pages: map[string]string{
				"|":   ratingsPage(0, 2, 1, 0, `{"total":5,"limit":3,"next_cursor":"c2"}`),
				"c2|": ratingsPage(3, 1, 1, 0, `{"total":5,"limit":3,"next_cursor":null}`),
			},
			wantKinds: []string{historyimport.KindMovie, historyimport.KindSeries},
			wantRows:  5,
		},
		// A rating added between the requests shifts page 2 by one: it
		// repeats page 1's show (tvdb 3) and the read never sees one entry,
		// yet the count still reaches total.
		"offset read repeats a rated title": {
			pages: map[string]string{
				"|":  ratingsPage(0, 2, 1, 0, `{"total":5,"limit":3,"offset":0,"next_cursor":null}`),
				"|3": `{"movies":[],"shows":[{"rating":8,"show":{"ids":{"tvdb":3}}},{"rating":8,"show":{"ids":{"tvdb":5}}}],"pagination":{"total":5,"limit":3,"offset":3,"next_cursor":null}}`,
			},
			wantRows:    5,
			wantWarning: "repeated",
		},
		"offset read repeats an entry without a rating row": {
			pages: map[string]string{
				"|":  `{"movies":[{"rating":7,"movie":{"ids":{"tmdb":1}}}],"shows":[],"episodes":[{"rating":9,"episode":{"ids":{"tmdb":2}}}],"pagination":{"total":4,"limit":2,"offset":0,"next_cursor":null}}`,
				"|2": `{"movies":[{"rating":7,"movie":{"ids":{"tmdb":4}}}],"shows":[],"episodes":[{"rating":9,"episode":{"ids":{"tmdb":2}}}],"pagination":{"total":4,"limit":2,"offset":2,"next_cursor":null}}`,
			},
			wantRows:    2,
			wantWarning: "repeated",
		},
		"cursor read repeats a rated title": {
			pages: map[string]string{
				"|":   ratingsPage(0, 2, 1, 0, `{"total":5,"limit":3,"next_cursor":"c2"}`),
				"c2|": `{"movies":[{"rating":7,"movie":{"ids":{"tmdb":2}}},{"rating":7,"movie":{"ids":{"tmdb":4}}}],"shows":[],"pagination":{"total":5,"limit":3,"next_cursor":null}}`,
			},
			wantRows:    5,
			wantWarning: "repeated",
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, ok := tc.pages[r.URL.Query().Get("cursor")+"|"+r.URL.Query().Get("offset")]
				if !ok {
					body = `{"movies":[],"shows":[],"pagination":{"next_cursor":null}}`
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(body))
			}))
			defer server.Close()

			batch, err := NewProvider(server.Client(), server.URL).FetchRatings(context.Background(), watchsync.ServerConfig{}, watchsync.Connection{AccessToken: "k"})
			if err != nil {
				t.Fatalf("fetch ratings: %v", err)
			}
			if !slices.Equal(batch.SnapshotKinds, tc.wantKinds) {
				t.Fatalf("snapshot kinds = %v, want %v", batch.SnapshotKinds, tc.wantKinds)
			}
			if tc.wantWarning != "" {
				if len(batch.Warnings) != 1 || !strings.Contains(batch.Warnings[0], tc.wantWarning) {
					t.Fatalf("warnings = %#v, want one %q warning", batch.Warnings, tc.wantWarning)
				}
			} else if len(batch.Warnings) != 0 {
				t.Fatalf("warnings = %#v, want none for a clean read", batch.Warnings)
			}
			if len(batch.Rows) != tc.wantRows {
				t.Fatalf("rows = %d, want %d: an unstable read still imports what it read", len(batch.Rows), tc.wantRows)
			}
		})
	}
}

func TestExportRatingsSendsRatingPayload(t *testing.T) {
	var gotPath, gotMethod string
	var gotBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotMethod = r.URL.Path, r.Method
		gotBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"updated":{"movies":1,"shows":1},"not_found":{"movies":0,"shows":0},"errors":[]}`))
	}))
	defer server.Close()

	ratedAt := time.Date(2025, 10, 21, 16, 0, 0, 0, time.FixedZone("CEST", 2*60*60))
	result, err := NewProvider(server.Client(), server.URL).ExportRatings(context.Background(), watchsync.ServerConfig{}, watchsync.Connection{AccessToken: "k"}, []watchsync.LocalRating{
		{LocalFavorite: watchsync.LocalFavorite{MediaItemID: "m1", Kind: historyimport.KindMovie, IMDbID: "tt0848228", TMDBID: "24428"}, Rating: 8, RatedAt: ratedAt},
		{LocalFavorite: watchsync.LocalFavorite{MediaItemID: "s1", Kind: historyimport.KindSeries, ProviderItemKey: "tvdb:81189"}, Rating: 10},
		{LocalFavorite: watchsync.LocalFavorite{MediaItemID: "e1", Kind: historyimport.KindEpisode, TMDBID: "62085"}, Rating: 6},
		{LocalFavorite: watchsync.LocalFavorite{MediaItemID: "m2", Kind: historyimport.KindMovie}, Rating: 4},
	})
	if err != nil {
		t.Fatalf("export ratings: %v", err)
	}
	if gotMethod != http.MethodPost || gotPath != "/sync/ratings" {
		t.Fatalf("request = %s %s, want POST /sync/ratings", gotMethod, gotPath)
	}
	assertJSONEqual(t, gotBody, `{
		"movies":[{"ids":{"imdb":"tt0848228","tmdb":24428},"rating":8,"rated_at":"2025-10-21T14:00:00Z"}],
		"shows":[{"ids":{"tvdb":81189},"rating":10}]
	}`)
	if !slices.Equal(result.Sent, []string{"m1", "s1"}) || len(result.NotFound) != 0 {
		t.Fatalf("result = %#v, want the movie and show sent", result)
	}
	if len(result.Failed) != 2 || result.Failed["e1"] == "" || result.Failed["m2"] == "" {
		t.Fatalf("failed = %#v, want the episode and the movie without ids", result.Failed)
	}
}

func TestRemoveRatingsSendsIDsOnly(t *testing.T) {
	var gotPath string
	var gotBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"deleted":{"movies":1,"shows":1},"not_found":{"movies":0,"shows":0}}`))
	}))
	defer server.Close()

	result, err := NewProvider(server.Client(), server.URL).RemoveRatings(context.Background(), watchsync.ServerConfig{}, watchsync.Connection{AccessToken: "k"}, []watchsync.LocalFavorite{
		{MediaItemID: "m1", Kind: historyimport.KindMovie, ProviderItemKey: "tmdb:24428"},
		{MediaItemID: "s1", Kind: historyimport.KindSeries, IMDbID: "tt0903747"},
	})
	if err != nil {
		t.Fatalf("remove ratings: %v", err)
	}
	if gotPath != "/sync/ratings/remove" {
		t.Fatalf("path = %q, want /sync/ratings/remove", gotPath)
	}
	assertJSONEqual(t, gotBody, `{"movies":[{"ids":{"tmdb":24428}}],"shows":[{"ids":{"imdb":"tt0903747"}}]}`)
	if !slices.Equal(result.Sent, []string{"m1", "s1"}) || len(result.Failed) != 0 || len(result.NotFound) != 0 {
		t.Fatalf("result = %#v, want both removals sent", result)
	}
}

func TestRatingWritesAcceptDocumentedResponseShapes(t *testing.T) {
	cases := map[string]struct {
		removing bool
		body     string
	}{
		"set, sample updated counts":    {body: `{"updated":{"movies":1,"seasons":0,"episodes":0}}`},
		"set, schema counts":            {body: `{"updated":{"movies":1},"not_found":{"movies":0,"shows":0},"errors":[]}`},
		"set, empty not_found lists":    {body: `{"added":{"movies":1},"not_found":{"movies":[],"shows":[]}}`},
		"remove, sample removed counts": {removing: true, body: `{"removed":{"movies":1,"seasons":0,"episodes":0}}`},
		"remove, schema deleted counts": {removing: true, body: `{"deleted":{"movies":1},"not_found":{"movies":0,"shows":0}}`},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			result := writeOneRating(t, tc.removing, tc.body)
			if !slices.Equal(result.Sent, []string{"m1"}) || len(result.NotFound) != 0 || len(result.Failed) != 0 {
				t.Fatalf("result = %#v, want m1 sent", result)
			}
		})
	}
}

func TestExportRatingsDoesNotClaimNotFoundBatchWasSent(t *testing.T) {
	result := writeRatings(t, false, `{"updated":{"movies":1},"not_found":{"movies":1,"shows":0}}`)
	if len(result.Sent) != 0 || len(result.NotFound) != 0 || result.Failed["m1"] == "" || result.Failed["s1"] == "" {
		t.Fatalf("result = %#v, want the whole batch failed", result)
	}
}

func TestRemoveRatingsTreatsNotFoundBatchAsReconciled(t *testing.T) {
	result := writeRatings(t, true, `{"deleted":{"movies":1},"not_found":{"movies":0,"shows":1}}`)
	if !slices.Equal(result.NotFound, []string{"m1", "s1"}) || len(result.Sent) != 0 || len(result.Failed) != 0 {
		t.Fatalf("result = %#v, want the whole batch reconciled as not found", result)
	}
}

func TestRatingWritesFailBatchThatReportsErrors(t *testing.T) {
	for _, removing := range []bool{false, true} {
		t.Run(fmt.Sprintf("removing=%t", removing), func(t *testing.T) {
			result := writeRatings(t, removing, `{"updated":{"movies":1},"not_found":{"movies":1},"errors":[{"message":"invalid rating"}]}`)
			if len(result.Sent) != 0 || len(result.NotFound) != 0 || result.Failed["m1"] == "" || result.Failed["s1"] == "" {
				t.Fatalf("result = %#v, want the whole batch failed", result)
			}
		})
	}
}

func TestRatingWritesReportUnidentifiedItemsWithoutCallingMDBList(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t.Error("MDBList should not be called for items without ids")
	}))
	defer server.Close()
	p := NewProvider(server.Client(), server.URL)
	conn := watchsync.Connection{AccessToken: "k"}
	item := watchsync.LocalFavorite{MediaItemID: "m1", Kind: historyimport.KindMovie}

	exported, err := p.ExportRatings(context.Background(), watchsync.ServerConfig{}, conn, []watchsync.LocalRating{{LocalFavorite: item, Rating: 8}})
	if err != nil {
		t.Fatalf("export ratings: %v", err)
	}
	removed, err := p.RemoveRatings(context.Background(), watchsync.ServerConfig{}, conn, []watchsync.LocalFavorite{item})
	if err != nil {
		t.Fatalf("remove ratings: %v", err)
	}
	for _, result := range []watchsync.ExportResult{exported, removed} {
		if len(result.Sent) != 0 || len(result.NotFound) != 0 || result.Failed["m1"] == "" {
			t.Fatalf("result = %#v, want m1 failed", result)
		}
	}
}

func TestExportRatingsSplitsRequestsAtShowLimit(t *testing.T) {
	var showCounts []int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload mdblistRatingsPayload
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Errorf("decode payload: %v", err)
		}
		showCounts = append(showCounts, len(payload.Shows))
		w.Header().Set("Content-Type", "application/json")
		if len(showCounts) == 2 {
			// Only the second request's items share this rejection.
			_, _ = w.Write([]byte(`{"updated":{"shows":0},"not_found":{"shows":1}}`))
			return
		}
		_, _ = w.Write([]byte(`{"updated":{"shows":200}}`))
	}))
	defer server.Close()

	items := make([]watchsync.LocalRating, 0, maxRatingWriteEntries+1)
	for i := range maxRatingWriteEntries + 1 {
		items = append(items, watchsync.LocalRating{
			LocalFavorite: watchsync.LocalFavorite{MediaItemID: fmt.Sprintf("s%d", i), Kind: historyimport.KindSeries, TVDBID: fmt.Sprint(1000 + i)},
			Rating:        6,
		})
	}
	result, err := NewProvider(server.Client(), server.URL).ExportRatings(context.Background(), watchsync.ServerConfig{}, watchsync.Connection{AccessToken: "k"}, items)
	if err != nil {
		t.Fatalf("export ratings: %v", err)
	}
	if !slices.Equal(showCounts, []int{maxRatingWriteEntries, 1}) {
		t.Fatalf("shows per request = %v, want [%d 1]", showCounts, maxRatingWriteEntries)
	}
	last := fmt.Sprintf("s%d", maxRatingWriteEntries)
	if len(result.Sent) != maxRatingWriteEntries || slices.Contains(result.Sent, last) || len(result.Failed) != 1 || result.Failed[last] == "" {
		t.Fatalf("result sent %d, failed %#v; want the first request sent and the second failed", len(result.Sent), result.Failed)
	}
}

// writeOneRating sets or removes the rating of one movie against a server
// that answers with body.
func writeOneRating(t *testing.T, removing bool, body string) watchsync.ExportResult {
	t.Helper()
	return writeRatingItems(t, removing, body, []watchsync.LocalFavorite{
		{MediaItemID: "m1", Kind: historyimport.KindMovie, IMDbID: "tt0848228"},
	})
}

// writeRatings sets or removes the ratings of one movie and one show in a
// single request against a server that answers with body.
func writeRatings(t *testing.T, removing bool, body string) watchsync.ExportResult {
	t.Helper()
	return writeRatingItems(t, removing, body, []watchsync.LocalFavorite{
		{MediaItemID: "m1", Kind: historyimport.KindMovie, IMDbID: "tt0848228"},
		{MediaItemID: "s1", Kind: historyimport.KindSeries, TVDBID: "81189"},
	})
}

func writeRatingItems(t *testing.T, removing bool, body string, items []watchsync.LocalFavorite) watchsync.ExportResult {
	t.Helper()
	wantPath := "/sync/ratings"
	if removing {
		wantPath = "/sync/ratings/remove"
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != wantPath {
			t.Errorf("path = %q, want %s", r.URL.Path, wantPath)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	defer server.Close()
	p := NewProvider(server.Client(), server.URL)
	conn := watchsync.Connection{AccessToken: "k"}

	var result watchsync.ExportResult
	var err error
	if removing {
		result, err = p.RemoveRatings(context.Background(), watchsync.ServerConfig{}, conn, items)
	} else {
		ratings := make([]watchsync.LocalRating, 0, len(items))
		for _, item := range items {
			ratings = append(ratings, watchsync.LocalRating{LocalFavorite: item, Rating: 8})
		}
		result, err = p.ExportRatings(context.Background(), watchsync.ServerConfig{}, conn, ratings)
	}
	if err != nil {
		t.Fatalf("write ratings: %v", err)
	}
	return result
}

func assertJSONEqual(t *testing.T, got []byte, want string) {
	t.Helper()
	var gotValue, wantValue any
	if err := json.Unmarshal(got, &gotValue); err != nil {
		t.Fatalf("decode request body %q: %v", got, err)
	}
	if err := json.Unmarshal([]byte(want), &wantValue); err != nil {
		t.Fatalf("decode expected body: %v", err)
	}
	if !reflect.DeepEqual(gotValue, wantValue) {
		t.Fatalf("request body = %s, want %s", got, want)
	}
}
