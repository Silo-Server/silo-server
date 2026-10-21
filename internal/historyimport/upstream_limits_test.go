package historyimport

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestDecodeLimitedJSONRejectsBodiesPastTheLimit(t *testing.T) {
	t.Parallel()

	var out struct{ Name string }
	if _, err := decodeLimitedJSON(strings.NewReader(`{"Name":"ok"}`), &out, 64); err != nil || out.Name != "ok" {
		t.Fatalf("small body: err = %v, name = %q", err, out.Name)
	}
	body := `{"Name":"` + strings.Repeat("a", 100) + `"}`
	if _, err := decodeLimitedJSON(strings.NewReader(body), &out, 64); !errors.Is(err, errUpstreamBytesLimit) {
		t.Fatalf("large body: err = %v, want the byte limit", err)
	}
	// A body exactly at the limit is accepted.
	exact := `{"Name":"` + strings.Repeat("b", 50) + `"}`
	if _, err := decodeLimitedJSON(strings.NewReader(exact), &out, int64(len(exact))); err != nil {
		t.Fatalf("body at the limit: err = %v", err)
	}
}

func TestListingBudgetCapsTotalBytes(t *testing.T) {
	t.Parallel()

	budget := listingBudget{bytes: maxListingBytes - 10}
	var out struct{ Name string }
	err := decodeUpstreamJSON(context.Background(), strings.NewReader(`{"Name":"more than ten bytes"}`), &out, &budget)
	if !errors.Is(err, ErrUpstreamResponseTooLarge) || !strings.Contains(err.Error(), "listing exceeds") {
		t.Fatalf("err = %v, want a listing size error", err)
	}
}

// oversizedJSONHandler streams a well-formed JSON body whose single string
// field pushes it past maxUpstreamResponseBytes.
func oversizedJSONHandler(prefix, suffix string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(prefix))
		chunk := bytes.Repeat([]byte("a"), 1<<20)
		for written := int64(0); written <= maxUpstreamResponseBytes; written += int64(len(chunk)) {
			if _, err := w.Write(chunk); err != nil {
				return
			}
		}
		_, _ = w.Write([]byte(suffix))
	}
}

func TestHistoryImportClientsRejectOversizedResponses(t *testing.T) {
	t.Parallel()

	ctx := trustLoopback(context.Background())
	cases := []struct {
		name           string
		prefix, suffix string
		fetch          func(baseURL string) error
	}{
		{
			name:   "jellyfin",
			prefix: `{"Items":[{"Name":"`, suffix: `"}]}`,
			fetch: func(baseURL string) error {
				auth := jellyfinLocalAuth{BaseURL: baseURL, UserID: "u", AccessToken: "t"}
				_, err := newUnthrottledJellyfinClient().FetchItems(ctx, auth, "IsPlayed", jellyfinPlayableItemTypes)
				return err
			},
		},
		{
			name:   "emby",
			prefix: `{"Items":[{"Name":"`, suffix: `"}]}`,
			fetch: func(baseURL string) error {
				client := NewEmbyClient()
				client.limiter = nil
				_, err := client.FetchItems(ctx, embyLocalAuth{BaseURL: baseURL, UserID: "u", AccessToken: "t"}, "IsPlayed")
				return err
			},
		},
		{
			name:   "plex",
			prefix: `{"MediaContainer":{"Metadata":[{"title":"`, suffix: `"}]}}`,
			fetch: func(baseURL string) error {
				_, err := newUnthrottledPlexClient().FetchUserHistory(ctx, baseURL, "t", "1")
				return err
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			server := httptest.NewServer(oversizedJSONHandler(tc.prefix, tc.suffix))
			defer server.Close()

			err := tc.fetch(server.URL)
			if !errors.Is(err, ErrUpstreamResponseTooLarge) {
				t.Fatalf("err = %v, want ErrUpstreamResponseTooLarge", err)
			}
			if got := userFacingRunError(ExecutionSummary{}, err); got != RunErrorSourceTooLarge {
				t.Fatalf("run error = %q, want %q", got, RunErrorSourceTooLarge)
			}
			if got := PublicRunError(RunErrorSourceTooLarge); got != RunErrorSourceTooLarge {
				t.Fatalf("public run error = %q, want the message unchanged", got)
			}
		})
	}
}

// oversizedFavorites answers the IsFavorite listing with a body past the
// response limit and passes every other request to next.
func oversizedFavorites(next http.Handler) http.Handler {
	oversized := oversizedJSONHandler(`{"Items":[{"Name":"`, `"}]}`)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("Filters") == "IsFavorite" {
			oversized(w, r)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// Providers downgrade some failed listings to warnings, but never a size-limit
// failure: the run must fail instead of importing without that listing.
func TestHistoryImportProvidersFailOnOversizedSecondaryListings(t *testing.T) {
	t.Parallel()

	ctx := trustLoopback(context.Background())
	cases := []struct {
		name  string
		fetch func(t *testing.T) error
	}{
		{
			name: "jellyfin favorites",
			fetch: func(t *testing.T) error {
				fetch := newJellyfinFetchHandler(t, map[string][]jellyfinItem{
					"IsPlayed": {{ID: "matrix", Type: "Movie", Name: "The Matrix", ProviderIDs: map[string]string{"Tmdb": "603"}, UserData: jellyfinUserData{Played: true}}},
				}, nil)
				server := httptest.NewServer(oversizedFavorites(fetch))
				t.Cleanup(server.Close)
				_, _, err := NewJellyfinProvider(newUnthrottledJellyfinClient(), jellyfinLocalAuth{BaseURL: server.URL, UserID: "user-1", AccessToken: "token-1"}).Fetch(ctx)
				return err
			},
		},
		{
			name: "emby favorites",
			fetch: func(t *testing.T) error {
				fake := &fakeEmby{t: t, played: []embyItem{playedEmbyItem(embyItem{ID: "movie-1", Name: "Arrival", Type: "Movie", ProviderIDs: map[string]string{"Tmdb": "329865"}}, time.Now(), 1)}}
				server := httptest.NewServer(oversizedFavorites(fake))
				t.Cleanup(server.Close)
				client := NewEmbyClient()
				client.limiter = nil
				_, _, err := NewEmbyProvider(client, embyLocalAuth{BaseURL: server.URL, UserID: "emby-user", AccessToken: "token"}).Fetch(ctx)
				return err
			},
		},
		{
			name: "plex section",
			fetch: func(t *testing.T) error {
				oversized := oversizedJSONHandler(`{"MediaContainer":{"Metadata":[{"title":"`, `"}]}}`)
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					switch r.URL.Path {
					case "/library/sections":
						w.Header().Set("Content-Type", "application/json")
						_, _ = w.Write([]byte(`{"MediaContainer":{"Directory":[{"key":"1","type":"movie","title":"Movies"}]}}`))
					case "/library/sections/1/all":
						oversized(w, r)
					default:
						t.Errorf("unexpected request %s", r.URL.Path)
						w.WriteHeader(http.StatusNotFound)
					}
				}))
				t.Cleanup(server.Close)
				_, _, err := NewPlexServerProvider(newUnthrottledPlexClient(), []string{server.URL}, "server-token").Fetch(ctx)
				return err
			},
		},
		{
			name: "plex watchlist lookup",
			fetch: func(t *testing.T) error {
				oversized := oversizedJSONHandler(`{"MediaContainer":{"Metadata":[{"title":"`, `"}]}}`)
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.URL.Path == "/library/sections/watchlist/all" {
						w.Header().Set("Content-Type", "application/json")
						_, _ = w.Write([]byte(`{"MediaContainer":{"totalSize":1,"Metadata":[{"ratingKey":"wl-1","type":"movie","title":"Dune: Part Two"}]}}`))
						return
					}
					oversized(w, r)
				}))
				t.Cleanup(server.Close)
				client := newUnthrottledPlexClient()
				client.discoverBaseURL = server.URL
				_, _, err := client.FetchWatchlist(ctx, "tok")
				return err
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if err := tc.fetch(t); !errors.Is(err, ErrUpstreamResponseTooLarge) {
				t.Fatalf("Fetch err = %v, want ErrUpstreamResponseTooLarge", err)
			}
		})
	}
}

// A heavy but legitimate page, several MiB, still decodes.
func TestJellyfinFetchItemsAcceptsLargeLegitimatePage(t *testing.T) {
	t.Parallel()

	name := strings.Repeat("n", 20<<10)
	var page strings.Builder
	page.WriteString(`{"TotalRecordCount":` + strconv.Itoa(jellyfinPageSize) + `,"Items":[`)
	for i := range jellyfinPageSize {
		if i > 0 {
			page.WriteByte(',')
		}
		page.WriteString(`{"Id":"` + strconv.Itoa(i) + `","Type":"Movie","Name":"` + name + `"}`)
	}
	page.WriteString(`]}`)
	body := page.String()
	if len(body) < 3<<20 {
		t.Fatalf("fixture is %d bytes, want a multi-MiB page", len(body))
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	defer server.Close()

	auth := jellyfinLocalAuth{BaseURL: server.URL, UserID: "u", AccessToken: "t"}
	items, err := newUnthrottledJellyfinClient().FetchItems(trustLoopback(context.Background()), auth, "IsPlayed", jellyfinPlayableItemTypes)
	if err != nil {
		t.Fatalf("FetchItems: %v", err)
	}
	if len(items) != jellyfinPageSize {
		t.Fatalf("items = %d, want %d", len(items), jellyfinPageSize)
	}
}

// A Jellyfin server that answers every page full and never reports a total
// would be paged forever; the item cap ends the listing with an error.
func TestJellyfinFetchItemsStopsEndlessFullPages(t *testing.T) {
	t.Parallel()

	page := `{"Items":[` + strings.TrimSuffix(strings.Repeat(`{},`, jellyfinPageSize), ",") + `]}`
	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(page))
	}))
	defer server.Close()

	auth := jellyfinLocalAuth{BaseURL: server.URL, UserID: "u", AccessToken: "t"}
	_, err := newUnthrottledJellyfinClient().FetchItems(trustLoopback(context.Background()), auth, "IsPlayed", jellyfinPlayableItemTypes)
	if !errors.Is(err, ErrUpstreamResponseTooLarge) {
		t.Fatalf("err = %v, want ErrUpstreamResponseTooLarge", err)
	}
	if want := int64(maxListingItems/jellyfinPageSize + 1); requests.Load() != want {
		t.Fatalf("requests = %d, want %d", requests.Load(), want)
	}
}

// A Plex server that reports a huge totalSize but hands out one row per page
// is stopped by the page cap.
func TestPlexFetchUserHistoryStopsAtPageCap(t *testing.T) {
	t.Parallel()

	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"MediaContainer":{"totalSize":1000000000,"Metadata":[{"ratingKey":"1"}]}}`))
	}))
	defer server.Close()

	_, err := newUnthrottledPlexClient().FetchUserHistory(trustLoopback(context.Background()), server.URL, "t", "1")
	if !errors.Is(err, ErrUpstreamResponseTooLarge) {
		t.Fatalf("err = %v, want ErrUpstreamResponseTooLarge", err)
	}
	if requests.Load() != maxListingPages {
		t.Fatalf("requests = %d, want %d", requests.Load(), maxListingPages)
	}
}

// Emby trusts a reported total over short pages, so the same page cap ends a
// total that never arrives.
func TestEmbyFetchItemsStopsAtPageCap(t *testing.T) {
	t.Parallel()

	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"TotalRecordCount":1000000000,"Items":[{"Id":"1"}]}`))
	}))
	defer server.Close()

	client := NewEmbyClient()
	client.limiter = nil
	_, err := client.FetchItems(trustLoopback(context.Background()), embyLocalAuth{BaseURL: server.URL, UserID: "u", AccessToken: "t"}, "IsPlayed")
	if !errors.Is(err, ErrUpstreamResponseTooLarge) {
		t.Fatalf("err = %v, want ErrUpstreamResponseTooLarge", err)
	}
	if requests.Load() != maxListingPages {
		t.Fatalf("requests = %d, want %d", requests.Load(), maxListingPages)
	}
}
