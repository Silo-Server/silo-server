package trakt

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/historyimport"
	"github.com/Silo-Server/silo-server/internal/watchsync"
)

type traktListCase struct {
	name       string
	moviesPath string
	showsPath  string
	fetch      func(*Provider, watchsync.Connection) ([]watchsync.RemoteFavorite, error)
}

func traktListCases() []traktListCase {
	return []traktListCase{
		{
			name:       "favorites",
			moviesPath: "/users/me/favorites/movies/added",
			showsPath:  "/users/me/favorites/shows/added",
			fetch: func(p *Provider, conn watchsync.Connection) ([]watchsync.RemoteFavorite, error) {
				return p.FetchFavorites(context.Background(), watchsync.ServerConfig{}, conn)
			},
		},
		{
			name:       "watchlist",
			moviesPath: "/sync/watchlist/movies",
			showsPath:  "/sync/watchlist/shows",
			fetch: func(p *Provider, conn watchsync.Connection) ([]watchsync.RemoteFavorite, error) {
				return p.FetchWatchlist(context.Background(), watchsync.ServerConfig{}, conn)
			},
		},
	}
}

func TestFetchListsImportEveryPage(t *testing.T) {
	for _, tc := range traktListCases() {
		t.Run(tc.name, func(t *testing.T) {
			var requests []string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				kind := map[string]string{tc.moviesPath: "movies", tc.showsPath: "shows"}[r.URL.Path]
				if kind == "" {
					t.Errorf("unexpected path %s", r.URL.Path)
					http.NotFound(w, r)
					return
				}
				page := r.URL.Query().Get("page")
				requests = append(requests, kind+":"+page)
				if r.URL.Query().Get("limit") != "250" {
					t.Errorf("limit = %q, want 250", r.URL.Query().Get("limit"))
				}
				if r.Header.Get("Authorization") != "Bearer test-token" {
					t.Errorf("missing authorization")
				}
				w.Header().Set("Content-Type", "application/json")
				// Short pages without pagination headers: only the empty page ends the list.
				switch page {
				case "1", "2":
					if kind == "movies" {
						writeTraktFixture(t, w, `[{"listed_at":"2026-05-0%sT12:00:00Z","movie":{"title":"Movie %s","year":2020,"ids":{"tmdb":10%s}}}]`, page, page, page)
					} else {
						writeTraktFixture(t, w, `[{"listed_at":"2026-06-0%sT12:00:00Z","show":{"title":"Show %s","year":2021,"ids":{"tvdb":30%s,"tmdb":20%s}}}]`, page, page, page, page)
					}
				case "3":
					writeTraktFixture(t, w, `[]`)
				default:
					t.Errorf("unexpected page %q", page)
					http.Error(w, "unexpected page", http.StatusInternalServerError)
				}
			}))
			defer server.Close()

			rows, err := tc.fetch(NewProvider(server.Client(), server.URL), watchsync.Connection{AccessToken: "test-token"})
			if err != nil {
				t.Fatal(err)
			}
			wantRequests := []string{"movies:1", "movies:2", "movies:3", "movies:1", "movies:2", "movies:3", "shows:1", "shows:2", "shows:3", "shows:1", "shows:2", "shows:3"}
			if !reflect.DeepEqual(requests, wantRequests) {
				t.Fatalf("requests = %v, want %v", requests, wantRequests)
			}
			want := []struct {
				key, kind, title string
				listedAt         time.Time
			}{
				{"tmdb:101", historyimport.KindMovie, "Movie 1", time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)},
				{"tmdb:102", historyimport.KindMovie, "Movie 2", time.Date(2026, 5, 2, 12, 0, 0, 0, time.UTC)},
				{"tvdb:301", historyimport.KindSeries, "Show 1", time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)},
				{"tvdb:302", historyimport.KindSeries, "Show 2", time.Date(2026, 6, 2, 12, 0, 0, 0, time.UTC)},
			}
			if len(rows) != len(want) {
				t.Fatalf("got %d rows, want %d: %#v", len(rows), len(want), rows)
			}
			for i, exp := range want {
				row := rows[i]
				if row.ProviderItemKey != exp.key || row.Kind != exp.kind || row.Title != exp.title || !row.FavoritedAt.Equal(exp.listedAt) {
					t.Errorf("row %d = %#v, want key %s kind %s title %s listed %s", i, row, exp.key, exp.kind, exp.title, exp.listedAt)
				}
			}
		})
	}
}

func TestFetchListsDoNotReturnPartialResultsOnLaterPageFailure(t *testing.T) {
	for _, tc := range traktListCases() {
		for _, kind := range []string{"movies", "shows"} {
			for _, failure := range []string{"http", "json"} {
				t.Run(tc.name+"/"+kind+"/"+failure, func(t *testing.T) {
					failingPath := tc.moviesPath
					if kind == "shows" {
						failingPath = tc.showsPath
					}
					server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						w.Header().Set("Content-Type", "application/json")
						if r.URL.Query().Get("page") == "1" {
							writeTraktFixture(t, w, `[{"listed_at":"2026-05-01T12:00:00Z","movie":{"ids":{"tmdb":123}},"show":{"ids":{"tvdb":456}}}]`)
							return
						}
						if r.URL.Path == failingPath {
							if failure == "http" {
								http.Error(w, "unavailable", http.StatusServiceUnavailable)
							} else {
								writeTraktFixture(t, w, `[{`)
							}
							return
						}
						writeTraktFixture(t, w, `[]`)
					}))
					defer server.Close()

					rows, err := tc.fetch(NewProvider(server.Client(), server.URL), watchsync.Connection{})
					if err == nil || rows != nil {
						t.Fatalf("got rows=%#v, err=%v; want no partial list and an error", rows, err)
					}
				})
			}
		}
	}
}

func TestFetchTraktPagesStopsOnPaginationHeaders(t *testing.T) {
	for _, tc := range []struct {
		name      string
		headers   map[string]string
		bodies    map[string]string
		wantPages []string
		wantRows  int
	}{
		{
			// Both pages are shorter than the limit; the page count decides.
			name:      "page count",
			headers:   map[string]string{"X-Pagination-Limit": "250", "X-Pagination-Page-Count": "2", "X-Pagination-Item-Count": "2"},
			bodies:    map[string]string{"1": `[{"movie":{"ids":{"tmdb":1}}}]`, "2": `[{"movie":{"ids":{"tmdb":2}}}]`},
			wantPages: []string{"1", "2", "1", "2"},
			wantRows:  2,
		},
		{
			// Trakt applied a smaller limit than requested; a page shorter than it is the last.
			name:      "applied limit",
			headers:   map[string]string{"X-Pagination-Limit": "2"},
			bodies:    map[string]string{"1": `[{"movie":{"ids":{"tmdb":1}}},{"movie":{"ids":{"tmdb":2}}}]`, "2": `[{"movie":{"ids":{"tmdb":3}}}]`},
			wantPages: []string{"1", "2", "1", "2"},
			wantRows:  3,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var pages []string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				page := r.URL.Query().Get("page")
				pages = append(pages, page)
				if got := r.URL.Query().Get("extended"); got != "full" {
					t.Errorf("extended = %q, want full", got)
				}
				if got := r.URL.Query().Get("limit"); got != "250" {
					t.Errorf("limit = %q, want 250", got)
				}
				body, ok := tc.bodies[page]
				if !ok {
					t.Errorf("unexpected page %q", page)
					http.Error(w, "unexpected page", http.StatusInternalServerError)
					return
				}
				for key, value := range tc.headers {
					w.Header().Set(key, value)
				}
				w.Header().Set("X-Pagination-Page", page)
				w.Header().Set("Content-Type", "application/json")
				writeTraktFixture(t, w, "%s", body)
			}))
			defer server.Close()

			query := url.Values{"extended": {"full"}, "page": {"9"}, "limit": {"5"}}
			rows, err := fetchTraktPages[traktFavoriteMovie](context.Background(), NewProvider(server.Client(), server.URL), watchsync.ServerConfig{}, watchsync.Connection{}, "/sync/watchlist/movies", query)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(pages, tc.wantPages) {
				t.Fatalf("pages = %v, want %v", pages, tc.wantPages)
			}
			if len(rows) != tc.wantRows {
				t.Fatalf("got %d rows, want %d", len(rows), tc.wantRows)
			}
			if query.Get("page") != "9" || query.Get("limit") != "5" {
				t.Fatalf("caller query was modified: %v", query)
			}
		})
	}
}

func TestFetchTraktPagesFailsAtPageCap(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		// A server that ignores page and sends no pagination headers never ends.
		w.Header().Set("Content-Type", "application/json")
		writeTraktFixture(t, w, `[{"movie":{"ids":{"tmdb":1}}}]`)
	}))
	defer server.Close()

	rows, err := fetchTraktPages[traktFavoriteMovie](context.Background(), NewProvider(server.Client(), server.URL), watchsync.ServerConfig{}, watchsync.Connection{}, "/sync/watchlist/movies", nil)
	if err == nil || rows != nil {
		t.Fatalf("got %d rows, err=%v; want no rows and an error", len(rows), err)
	}
	if requests != traktMaxPages {
		t.Fatalf("requests = %d, want %d", requests, traktMaxPages)
	}
}

func TestFetchTraktPagesFailsWhenTheListChangesMidRead(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// The list shrinks between page 1 and page 2, which shifts offsets.
		switch r.URL.Query().Get("page") {
		case "1":
			w.Header().Set("X-Pagination-Item-Count", "251")
			w.Header().Set("X-Pagination-Page-Count", "2")
			writeTraktFixture(t, w, `[{"listed_at":"2026-01-01T00:00:00Z","movie":{"title":"A","ids":{"trakt":1,"tmdb":1}}}]`)
		default:
			w.Header().Set("X-Pagination-Item-Count", "250")
			w.Header().Set("X-Pagination-Page-Count", "1")
			writeTraktFixture(t, w, `[{"listed_at":"2026-01-01T00:00:00Z","movie":{"title":"B","ids":{"trakt":2,"tmdb":2}}}]`)
		}
	}))
	defer server.Close()

	rows, err := fetchTraktPages[traktFavoriteMovie](context.Background(), NewProvider(server.Client(), server.URL),
		watchsync.ServerConfig{}, watchsync.Connection{AccessToken: "t"}, "/sync/watchlist/movies", nil)
	if err == nil || rows != nil {
		t.Fatalf("rows=%v err=%v, want an error and no rows", rows, err)
	}
}

func TestFetchTraktPagesFailsWhenAnEqualCountChangeShiftsPages(t *testing.T) {
	// Between the two passes one title was removed and another added, so the
	// item count is unchanged but page 2 now holds a different title.
	pass := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		page := r.URL.Query().Get("page")
		if page == "1" {
			pass++
		}
		w.Header().Set("X-Pagination-Item-Count", "251")
		w.Header().Set("X-Pagination-Page-Count", "2")
		switch {
		case page == "1":
			writeTraktFixture(t, w, `[{"listed_at":"2026-01-01T00:00:00Z","movie":{"title":"A","ids":{"trakt":1,"tmdb":1}}}]`)
		case pass == 1:
			writeTraktFixture(t, w, `[{"listed_at":"2026-01-01T00:00:00Z","movie":{"title":"B","ids":{"trakt":2,"tmdb":2}}}]`)
		default:
			writeTraktFixture(t, w, `[{"listed_at":"2026-01-01T00:00:00Z","movie":{"title":"C","ids":{"trakt":3,"tmdb":3}}}]`)
		}
	}))
	defer server.Close()

	rows, err := fetchTraktPages[traktFavoriteMovie](context.Background(), NewProvider(server.Client(), server.URL),
		watchsync.ServerConfig{}, watchsync.Connection{AccessToken: "t"}, "/sync/watchlist/movies", nil)
	if err == nil || rows != nil {
		t.Fatalf("rows=%v err=%v, want an error and no rows", rows, err)
	}
}

func TestFetchTraktPagesReadsASinglePageOnce(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		w.Header().Set("X-Pagination-Page-Count", "1")
		writeTraktFixture(t, w, `[{"listed_at":"2026-01-01T00:00:00Z","movie":{"title":"A","ids":{"trakt":1,"tmdb":1}}}]`)
	}))
	defer server.Close()

	rows, err := fetchTraktPages[traktFavoriteMovie](context.Background(), NewProvider(server.Client(), server.URL),
		watchsync.ServerConfig{}, watchsync.Connection{AccessToken: "t"}, "/sync/watchlist/movies", nil)
	if err != nil || len(rows) != 1 || requests != 1 {
		t.Fatalf("rows=%d requests=%d err=%v, want one row from one request", len(rows), requests, err)
	}
}

func TestFetchTraktPagesPacesReadsPerToken(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		w.Header().Set("X-Pagination-Page-Count", "3")
		writeTraktFixture(t, w, `[{"listed_at":"2026-01-01T00:00:00Z","movie":{"title":"A","ids":{"trakt":1,"tmdb":1}}}]`)
	}))
	defer server.Close()
	provider := NewProvider(server.Client(), server.URL)
	// Two pages at once, then one per hour: the third page must wait, which
	// the one-minute deadline refuses before the request is sent.
	provider.pages = watchsync.NewCredentialLimiter(time.Hour, 2)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	rows, err := fetchTraktPages[traktFavoriteMovie](ctx, provider, watchsync.ServerConfig{},
		watchsync.Connection{AccessToken: "t"}, "/sync/watchlist/movies", nil)
	if err == nil || rows != nil || requests != 2 {
		t.Fatalf("rows=%v requests=%d err=%v, want the read limiter to stop the third page", rows, requests, err)
	}
	// A refused wait defers the sync like a 429 rather than failing it.
	if _, ok := watchsync.AsRateLimited(err); !ok {
		t.Fatalf("err = %v, want a rate-limited deferral", err)
	}
}

func TestFetchTraktPagesShareTheBudgetOfOneAccount(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		writeTraktFixture(t, w, `[]`)
	}))
	defer server.Close()
	provider := NewProvider(server.Client(), server.URL)
	provider.pages = watchsync.NewCredentialLimiter(time.Hour, 1)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	// Two profiles linked to one Trakt account hold different tokens.
	first := watchsync.Connection{AccessToken: "token-a", ProviderAccountID: "trakt-user"}
	second := watchsync.Connection{AccessToken: "token-b", ProviderAccountID: "trakt-user"}
	if _, err := fetchTraktPages[traktFavoriteMovie](ctx, provider, watchsync.ServerConfig{}, first, "/sync/watchlist/movies", nil); err != nil {
		t.Fatal(err)
	}
	_, err := fetchTraktPages[traktFavoriteMovie](ctx, provider, watchsync.ServerConfig{}, second, "/sync/watchlist/movies", nil)
	if _, ok := watchsync.AsRateLimited(err); !ok || requests != 1 {
		t.Fatalf("requests=%d err=%v, want the second token to wait on the account's budget", requests, err)
	}
	// Another account has its own budget.
	other := watchsync.Connection{AccessToken: "token-c", ProviderAccountID: "other-user"}
	if _, err := fetchTraktPages[traktFavoriteMovie](ctx, provider, watchsync.ServerConfig{}, other, "/sync/watchlist/movies", nil); err != nil {
		t.Fatalf("another account: %v", err)
	}
}

func TestTraktPageBudgetStaysUnderTheGETLimit(t *testing.T) {
	// Trakt allows 500 authenticated GETs per five minutes.
	if perWindow := pageBurst + int((5*time.Minute)/pageInterval); perWindow >= 500 {
		t.Fatalf("paged reads allow %d GETs in five minutes, want fewer than 500", perWindow)
	}
}
