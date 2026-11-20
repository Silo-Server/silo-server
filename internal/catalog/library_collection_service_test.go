package catalog

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/collectionutil"
	"github.com/Silo-Server/silo-server/internal/mdblist"
	"github.com/Silo-Server/silo-server/internal/models"
)

func TestMDBListEntryItemTypeSeries(t *testing.T) {
	if got := mdbListEntryItemType(mdblistEntry{MediaType: "tv"}); got != "series" {
		t.Fatalf("MDBList tv type = %q, want series", got)
	}
}

func TestVirtualPlaybackItemURIPreferenceAndFallbacks(t *testing.T) {
	tests := []struct {
		name    string
		item    *models.MediaItem
		want    string
		wantErr bool
	}{
		{
			name: "movie prefers imdb",
			item: &models.MediaItem{Type: "movie", ImdbID: "TT0133093", TmdbID: "603"},
			want: "virtual://movie/tt0133093",
		},
		{
			name: "movie falls back to tmdb",
			item: &models.MediaItem{Type: "movie", TmdbID: "603"},
			want: "virtual://movie/tmdb/603",
		},
		{
			name: "series prefers tvdb over tmdb",
			item: &models.MediaItem{Type: "series", TvdbID: "393159", TmdbID: "111"},
			want: "virtual://series/tvdb/393159",
		},
		{
			name: "series falls back to tmdb",
			item: &models.MediaItem{Type: "series", TmdbID: "111"},
			want: "virtual://series/tmdb/111",
		},
		{
			name:    "invalid identifiers",
			item:    &models.MediaItem{Type: "movie", ImdbID: "not-imdb", TmdbID: "0"},
			wantErr: true,
		},
		{
			name:    "unsupported media type",
			item:    &models.MediaItem{Type: "episode", ImdbID: "tt1"},
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := virtualPlaybackItemURI(tt.item)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("virtualPlaybackItemURI() = %q, want error", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("virtualPlaybackItemURI(): %v", err)
			}
			if got != tt.want {
				t.Fatalf("virtualPlaybackItemURI() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestVirtualPlaybackContentIDUsesCanonicalProviderPriority(t *testing.T) {
	tests := []struct {
		name string
		item *models.MediaItem
		want string
	}{
		{
			name: "movie tmdb before imdb",
			item: &models.MediaItem{Type: "movie", TmdbID: "603", ImdbID: "tt0133093"},
			want: "movie-tmdb-603",
		},
		{
			name: "movie imdb fallback",
			item: &models.MediaItem{Type: "movie", ImdbID: "TT0133093"},
			want: "movie-imdb-tt0133093",
		},
		{
			name: "series tvdb before tmdb and imdb",
			item: &models.MediaItem{Type: "series", TvdbID: "393159", TmdbID: "111", ImdbID: "tt1"},
			want: "series-tvdb-393159",
		},
		{
			name: "series tmdb before imdb",
			item: &models.MediaItem{Type: "series", TmdbID: "111", ImdbID: "tt1"},
			want: "series-tmdb-111",
		},
		{
			name: "series imdb fallback",
			item: &models.MediaItem{Type: "series", ImdbID: "tt1"},
			want: "series-imdb-tt1",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := virtualPlaybackContentID(tt.item)
			if err != nil {
				t.Fatalf("virtualPlaybackContentID(): %v", err)
			}
			if got != tt.want {
				t.Fatalf("virtualPlaybackContentID()=%q, want %q", got, tt.want)
			}
		})
	}
}

func TestVirtualPlaybackIdentityAvailableWithoutIMDb(t *testing.T) {
	if !virtualPlaybackIdentityAvailable("movie", "", 603, 0) {
		t.Fatal("TMDB-only movie identity was rejected")
	}
	if !virtualPlaybackIdentityAvailable("tv", "", 0, 393159) {
		t.Fatal("TVDB-only series identity was rejected")
	}
	if !virtualPlaybackIdentityAvailable("show", "", 0, 393159) {
		t.Fatal("MDBList show TVDB identity was rejected")
	}
	if virtualPlaybackIdentityAvailable("movie", "", 0, 393159) {
		t.Fatal("movie unexpectedly accepted a TVDB-only identity")
	}
	if virtualPlaybackIdentityAvailable("tv", "invalid", 0, 0) {
		t.Fatal("invalid series identity was accepted")
	}
}

func TestQueueVirtualMetadataRefreshInvokesBoundedWorker(t *testing.T) {
	refreshed := make(chan string, 1)
	service := &LibraryCollectionService{
		RefreshVirtualItem: func(_ context.Context, contentID string) error {
			refreshed <- contentID
			return nil
		},
	}
	service.queueVirtualMetadataRefresh("content-1")
	select {
	case got := <-refreshed:
		if got != "content-1" {
			t.Fatalf("refreshed content ID = %q, want content-1", got)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for virtual metadata refresh")
	}
}

func TestConfiguredVirtualVariantsCachesProfileDiscoveryPerMediaType(t *testing.T) {
	calls := 0
	service := &LibraryCollectionService{
		VirtualVariants: func(_ context.Context, virtualURI, mediaType string) ([]VirtualPlaybackVariant, error) {
			calls++
			if mediaType != "movie" {
				t.Fatalf("mediaType=%q, want movie", mediaType)
			}
			return []VirtualPlaybackVariant{{
				VirtualURI: virtualURI + "?profile=1080p", Label: "1080p",
				OwnerInstallationID: 11,
			}}, nil
		},
	}
	ctx := context.WithValue(context.Background(), collectionVirtualVariantCacheKey{}, &collectionVirtualVariantCache{
		entries: make(map[string][]VirtualPlaybackVariant),
	})
	first, err := service.configuredVirtualVariants(ctx, "virtual://movie/tt100", "movie")
	if err != nil {
		t.Fatalf("first profiles: %v", err)
	}
	second, err := service.configuredVirtualVariants(ctx, "virtual://movie/tt200", "movie")
	if err != nil {
		t.Fatalf("second profiles: %v", err)
	}
	if calls != 1 {
		t.Fatalf("profile callback calls=%d, want 1", calls)
	}
	if first[0].VirtualURI != "virtual://movie/tt100?profile=1080p" ||
		second[0].VirtualURI != "virtual://movie/tt200?profile=1080p" {
		t.Fatalf("rebased profiles first=%q second=%q", first[0].VirtualURI, second[0].VirtualURI)
	}
}

// TestPickCandidatesByPriority_ReturnsAllInOrder pins the fallback semantic
// that the legacy resolveMDBListEntry preserved: when external IDs resolve
// to different content_ids, all candidates are returned in priority order so
// the caller can pick the first library-resident match. Series priority is
// TVDB > TMDB > IMDb.
func TestPickCandidatesByPriority_ReturnsAllInOrder(t *testing.T) {
	lookup := &ExternalIDLookup{
		ByTVDB: map[string]string{"100": "tvdb-hit"},
		ByTMDB: map[string]string{"200": "tmdb-hit"},
		ByIMDb: map[string]string{"tt300": "imdb-hit"},
	}
	tvdbID := 100
	entry := mdblistEntry{TVDBID: &tvdbID, ID: 200, IMDbID: "tt300"}

	candidates := pickCandidatesByPriority(lookup, entry, "series")
	expected := []string{"tvdb-hit", "tmdb-hit", "imdb-hit"}
	if len(candidates) != 3 {
		t.Fatalf("expected 3 candidates; got %v", candidates)
	}
	for i, want := range expected {
		if candidates[i] != want {
			t.Errorf("candidates[%d] = %q; want %q", i, candidates[i], want)
		}
	}
}

// TestPickCandidatesByPriority_DedupsAcrossProviders verifies that when all
// three external IDs resolve to the same content_id, that ID is returned
// exactly once (so the membership check + chosen-match loop don't redundant-
// scan the same candidate).
func TestPickCandidatesByPriority_DedupsAcrossProviders(t *testing.T) {
	lookup := &ExternalIDLookup{
		ByTVDB: map[string]string{"100": "shared"},
		ByTMDB: map[string]string{"200": "shared"},
		ByIMDb: map[string]string{"tt300": "shared"},
	}
	tvdbID := 100
	entry := mdblistEntry{TVDBID: &tvdbID, ID: 200, IMDbID: "tt300"}
	candidates := pickCandidatesByPriority(lookup, entry, "series")
	if len(candidates) != 1 || candidates[0] != "shared" {
		t.Fatalf("expected single deduped candidate 'shared'; got %v", candidates)
	}
}

func TestFetchMDBListEntriesDoesNotDialPrivateHosts(t *testing.T) {
	t.Parallel()

	var hits atomic.Int32
	svc := NewLibraryCollectionService(nil, nil, nil, &http.Client{
		Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			hits.Add(1)
			return nil, errors.New("HTTP client must not be used for a rejected MDBList URL")
		}),
	})

	_, err := svc.fetchMDBListEntries(context.Background(), "http://127.0.0.1:8096/")
	if !errors.Is(err, collectionutil.ErrMDBListURL) {
		t.Fatalf("fetchMDBListEntries(loopback) = %v, want ErrMDBListURL", err)
	}
	if hits.Load() != 0 {
		t.Fatalf("HTTP client was used %d times for a private URL", hits.Load())
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}

// blockingRoundTripper blocks until the request context is done, so callers
// can prove fetchMDBListEntries propagates deadline and cancellation rather
// than hanging on a stalled MDBList socket.
type blockingRoundTripper struct {
	started chan struct{}
	once    sync.Once
}

func (b *blockingRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	b.once.Do(func() {
		if b.started != nil {
			close(b.started)
		}
	})
	<-req.Context().Done()
	return nil, req.Context().Err()
}

func TestFetchMDBListEntriesHonorsContextDeadline(t *testing.T) {
	t.Parallel()

	transport := &blockingRoundTripper{started: make(chan struct{})}
	svc := &LibraryCollectionService{httpClient: &http.Client{Transport: transport}}

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	_, err := svc.fetchMDBListEntries(ctx, "https://mdblist.com/lists/example-user/watchlist")
	select {
	case <-transport.started:
	default:
		t.Fatal("HTTP transport was never dialed")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("fetchMDBListEntries deadline error = %v, want context.DeadlineExceeded", err)
	}
}

func TestFetchMDBListEntriesHonorsContextCancellation(t *testing.T) {
	t.Parallel()

	transport := &blockingRoundTripper{started: make(chan struct{})}
	svc := &LibraryCollectionService{httpClient: &http.Client{Transport: transport}}

	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		_, err := svc.fetchMDBListEntries(ctx, "https://mdblist.com/lists/example-user/watchlist")
		result <- err
	}()

	select {
	case <-transport.started:
	case <-time.After(5 * time.Second):
		t.Fatal("HTTP transport was never dialed")
	}
	cancel()

	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("fetchMDBListEntries cancel error = %v, want context.Canceled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("fetchMDBListEntries did not return after cancellation")
	}
}

// fakeMDBListAPI records calls and returns canned ListItems, standing in for
// *mdblist.Client so tests can prove which fetch path ran without a network.
type fakeMDBListAPI struct {
	items  []mdblist.ListItem
	err    error
	users  []string
	lists  []string
	maxes  []int
	called bool
}

func (f *fakeMDBListAPI) ListItems(_ context.Context, user, list string, maxItems int) ([]mdblist.ListItem, error) {
	f.called = true
	f.users = append(f.users, user)
	f.lists = append(f.lists, list)
	f.maxes = append(f.maxes, maxItems)
	return f.items, f.err
}

func TestFetchMDBListEntriesUsesAPIFetcherWhenSet(t *testing.T) {
	httpHits := 0
	svc := &LibraryCollectionService{
		MDBListAPI: &fakeMDBListAPI{items: []mdblist.ListItem{{
			TMDBID: 603, IMDbID: "tt0133093", MediaType: "movie",
			Title: "The Matrix", ReleaseYear: 1999, ReleaseDate: "1999-03-31", Rank: 1000,
		}}},
		httpClient: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			httpHits++
			return nil, errors.New("the public /json path must not run when the API fetcher is set")
		})},
	}
	limit := 5
	entries, err := svc.fetchMDBListEntriesWithAPI(context.Background(), []string{"https://mdblist.com/lists/alice/horror/json"}, &limit)
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if httpHits != 0 {
		t.Fatalf("public /json path was used %d times", httpHits)
	}
	api := svc.MDBListAPI.(*fakeMDBListAPI)
	if !api.called || api.users[0] != "alice" || api.lists[0] != "horror" {
		t.Fatalf("API calls = %+v, want alice/horror", api)
	}
	if api.maxes[0] != collectionutil.SourceFetchLimit(&limit) {
		t.Fatalf("maxItems = %d, want SourceFetchLimit(%d)=%d", api.maxes[0], limit, collectionutil.SourceFetchLimit(&limit))
	}
	if len(entries) != 1 || entries[0].ID != 603 || entries[0].Released != "1999-03-31" || entries[0].Rank != 1000 {
		t.Fatalf("mapped entries = %+v", entries)
	}
}

// TestFetchMDBListEntriesPagesPastDefaultTruncation proves the catalog's /json
// path pages past the feed's 2000-entry default through the shared helper: a
// 3500-entry list arrives whole, in order, across four limit/offset requests.
func TestFetchMDBListEntriesPagesPastDefaultTruncation(t *testing.T) {
	const total = 3500
	var offsets []string
	var paths []string
	svc := &LibraryCollectionService{
		httpClient: &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			query := req.URL.Query()
			offsets = append(offsets, query.Get("offset"))
			paths = append(paths, req.URL.Path)
			offset, _ := strconv.Atoi(query.Get("offset"))
			limit, _ := strconv.Atoi(query.Get("limit"))
			end := offset + limit
			if end > total {
				end = total
			}
			var b strings.Builder
			b.WriteByte('[')
			for i := offset; i < end; i++ {
				if i > offset {
					b.WriteByte(',')
				}
				fmt.Fprintf(&b, `{"id":%d,"title":"Item %d","mediatype":"movie","release_year":2000}`, i, i)
			}
			b.WriteByte(']')
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(strings.NewReader(b.String())),
				Header:     make(http.Header),
			}, nil
		})},
	}

	entries, err := svc.fetchMDBListEntries(context.Background(), "https://mdblist.com/lists/alice/large/json")
	if err != nil {
		t.Fatalf("fetchMDBListEntries: %v", err)
	}
	if len(entries) != total {
		t.Fatalf("entries = %d, want %d (must page past 2000)", len(entries), total)
	}
	for i, entry := range entries {
		if entry.ID != i {
			t.Fatalf("entries[%d].ID = %d, want %d", i, entry.ID, i)
		}
	}
	if got := strings.Join(offsets, ","); got != "0,1000,2000,3000" {
		t.Fatalf("offsets = %s, want 0,1000,2000,3000", got)
	}
	for _, path := range paths {
		if path != "/lists/alice/large/json" {
			t.Fatalf("path = %q, want /lists/alice/large/json", path)
		}
	}
}

func TestFetchMDBListEntriesFallsBackToJSONWhenAPINil(t *testing.T) {
	httpHits := 0
	svc := &LibraryCollectionService{
		httpClient: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			httpHits++
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(strings.NewReader(`[{"id":603,"title":"The Matrix","mediatype":"movie","release_year":1999}]`)),
				Header:     make(http.Header),
			}, nil
		})},
	}
	entries, err := svc.fetchMDBListEntriesWithAPI(context.Background(), []string{"https://mdblist.com/lists/alice/horror/json"}, nil)
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if httpHits != 1 {
		t.Fatalf("public /json path used %d times, want 1", httpHits)
	}
	if len(entries) != 1 || entries[0].ID != 603 || entries[0].Title != "The Matrix" {
		t.Fatalf("entries = %+v", entries)
	}
}

func TestFetchMDBListEntriesFallsBackWhenAPIReportsNotConfigured(t *testing.T) {
	httpHits := 0
	svc := &LibraryCollectionService{
		MDBListAPI: &fakeMDBListAPI{err: mdblist.ErrNotConfigured},
		httpClient: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			httpHits++
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(strings.NewReader(`[{"id":1,"title":"Fallback","mediatype":"movie","release_year":2000}]`)),
				Header:     make(http.Header),
			}, nil
		})},
	}
	entries, err := svc.fetchMDBListEntriesWithAPI(context.Background(), []string{"https://mdblist.com/lists/alice/horror/json"}, nil)
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if httpHits != 1 || len(entries) != 1 || entries[0].Title != "Fallback" {
		t.Fatalf("httpHits=%d entries=%+v, want the /json fallback", httpHits, entries)
	}
}

func TestFetchMDBListEntriesFallsBackOnEmptyItemsWithTotal(t *testing.T) {
	httpHits := 0
	svc := &LibraryCollectionService{
		MDBListAPI: &fakeMDBListAPI{err: fmt.Errorf("%w: total=300", mdblist.ErrEmptyItemsWithTotal)},
		httpClient: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			httpHits++
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(strings.NewReader(`[{"id":3082,"title":"Netflix Shows","mediatype":"tv","release_year":2020}]`)),
				Header:     make(http.Header),
			}, nil
		})},
	}
	entries, err := svc.fetchMDBListEntriesWithAPI(context.Background(), []string{"https://mdblist.com/lists/garycrawfordgc/netflix-shows/json"}, nil)
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if httpHits != 1 {
		t.Fatalf("public /json path used %d times, want 1", httpHits)
	}
	if len(entries) != 1 || entries[0].ID != 3082 || entries[0].Title != "Netflix Shows" {
		t.Fatalf("entries = %+v, want the non-empty /json fallback", entries)
	}
	if !svc.MDBListAPI.(*fakeMDBListAPI).called {
		t.Fatal("API fetcher was not attempted before falling back")
	}
}

func TestFetchMDBListEntriesFallbackMatrix(t *testing.T) {
	// Sentinel errors the API path must defer to /json for. Each must still
	// attempt the API first, then return the non-empty feed.
	fallbackErrs := map[string]error{
		"empty items":      mdblist.ErrEmptyItemsWithTotal,
		"incomplete items": mdblist.ErrIncompleteItems,
		"list not found":   mdblist.ErrListNotFound,
		"rate limited":     mdblist.ErrRateLimit,
		"not configured":   mdblist.ErrNotConfigured,
	}
	for name, sentinel := range fallbackErrs {
		t.Run(name, func(t *testing.T) {
			httpHits := 0
			svc := &LibraryCollectionService{
				MDBListAPI: &fakeMDBListAPI{err: fmt.Errorf("wrapped: %w", sentinel)},
				httpClient: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
					httpHits++
					return &http.Response{
						StatusCode: http.StatusOK,
						Body:       io.NopCloser(strings.NewReader(`[{"id":1,"title":"Fallback","mediatype":"movie","release_year":2000}]`)),
						Header:     make(http.Header),
					}, nil
				})},
			}
			entries, err := svc.fetchMDBListEntriesWithAPI(context.Background(), []string{"https://mdblist.com/lists/alice/horror/json"}, nil)
			if err != nil {
				t.Fatalf("fetch: %v", err)
			}
			if !svc.MDBListAPI.(*fakeMDBListAPI).called {
				t.Fatal("API fetcher was not attempted")
			}
			if httpHits != 1 || len(entries) != 1 || entries[0].Title != "Fallback" {
				t.Fatalf("httpHits=%d entries=%+v, want the /json fallback", httpHits, entries)
			}
		})
	}
}

func TestFetchMDBListEntriesSurfacesUnauthorizedWithoutFallback(t *testing.T) {
	// A bad/expired key must stay visible: ErrUnauthorized (and generic errors)
	// must not silently degrade to the unauthenticated feed.
	httpHits := 0
	svc := &LibraryCollectionService{
		MDBListAPI: &fakeMDBListAPI{err: mdblist.ErrUnauthorized},
		httpClient: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			httpHits++
			return nil, errors.New("must not fall back on a rejected key")
		})},
	}
	_, err := svc.fetchMDBListEntriesWithAPI(context.Background(), []string{"https://mdblist.com/lists/alice/horror/json"}, nil)
	if !errors.Is(err, mdblist.ErrUnauthorized) {
		t.Fatalf("err = %v, want ErrUnauthorized surfaced", err)
	}
	if httpHits != 0 {
		t.Fatalf("public /json path used %d times after an auth failure", httpHits)
	}
}

func TestFetchMDBListEntriesSurfacesAPIErrorWithoutFallback(t *testing.T) {
	httpHits := 0
	apiErr := errors.New("invalid api key")
	svc := &LibraryCollectionService{
		MDBListAPI: &fakeMDBListAPI{err: apiErr},
		httpClient: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			httpHits++
			return nil, errors.New("must not fall back on an API error")
		})},
	}
	_, err := svc.fetchMDBListEntriesWithAPI(context.Background(), []string{"https://mdblist.com/lists/alice/horror/json"}, nil)
	if !errors.Is(err, apiErr) {
		t.Fatalf("err = %v, want the API error surfaced", err)
	}
	if httpHits != 0 {
		t.Fatalf("public /json path was used %d times after an API error", httpHits)
	}
}

func TestAPIMappedReleaseDateDrivesFutureGate(t *testing.T) {
	// A show is exempt from the movie release window; use a movie to prove the
	// API's release_date populates Released so isFutureDate sees a future date.
	future := time.Now().UTC().AddDate(1, 0, 0).Format("2006-01-02")
	entries := apiListItemsToEntries([]mdblist.ListItem{{
		TMDBID: 999, MediaType: "movie", Title: "Upcoming", ReleaseYear: time.Now().Year() + 1, ReleaseDate: future,
	}})
	if len(entries) != 1 || entries[0].Released != future {
		t.Fatalf("entries = %+v, want Released=%q", entries, future)
	}
	if !isFutureDate(entries[0].ReleaseYear, entries[0].Released) {
		t.Fatalf("isFutureDate(%d, %q) = false, want true", entries[0].ReleaseYear, entries[0].Released)
	}
}

func TestAPIMappedTVDBPointerIsCopiedNotAliased(t *testing.T) {
	tvdb := 305089
	entries := apiListItemsToEntries([]mdblist.ListItem{{TMDBID: 65942, TVDBID: &tvdb, MediaType: "show"}})
	if entries[0].TVDBID == nil || *entries[0].TVDBID != 305089 {
		t.Fatalf("TVDBID = %v, want 305089", entries[0].TVDBID)
	}
	tvdb = 1
	if *entries[0].TVDBID != 305089 {
		t.Fatal("entry TVDBID aliases the source pointer")
	}
}

// fakeTMDBFranchiseFetcher is a stand-in for tmdbFranchiseAdapter used in
// catalog-package tests. It records the IDs it was asked to fetch so callers
// can assert that the configured CollectionID flows end-to-end without
// truncation, and returns canned entries in the order TMDB would have.
type fakeTMDBFranchiseFetcher struct {
	calls   []int
	entries []TMDBCollectionEntry
	err     error
}

func (f *fakeTMDBFranchiseFetcher) GetCollection(_ context.Context, id int) ([]TMDBCollectionEntry, error) {
	f.calls = append(f.calls, id)
	if f.err != nil {
		return nil, f.err
	}
	// Return a fresh slice so the caller can sort/mutate without
	// corrupting the fixture for later assertions.
	out := make([]TMDBCollectionEntry, len(f.entries))
	copy(out, f.entries)
	return out, nil
}

func TestFakeTMDBFranchiseFetcherSatisfiesInterface(t *testing.T) {
	// Static-typed assertion the fake implements the interface — a regression
	// guard so future signature changes break here, not in router wiring.
	var _ TMDBCollectionByIDFetcher = (*fakeTMDBFranchiseFetcher)(nil)
}

func TestFakeTMDBFranchiseFetcherReturnsEntriesInOrder(t *testing.T) {
	want := []TMDBCollectionEntry{
		{ID: 1726, MediaType: "movie", Title: "Iron Man"},
		{ID: 10138, MediaType: "movie", Title: "Iron Man 2"},
		{ID: 68721, MediaType: "movie", Title: "Iron Man 3"},
	}
	f := &fakeTMDBFranchiseFetcher{entries: want}

	got, err := f.GetCollection(context.Background(), 131292)
	if err != nil {
		t.Fatalf("fetcher: %v", err)
	}
	if len(got) != len(want) {
		t.Fatalf("len(got) = %d, want %d", len(got), len(want))
	}
	for i, w := range want {
		if got[i] != w {
			t.Errorf("got[%d] = %+v, want %+v", i, got[i], w)
		}
	}
	if len(f.calls) != 1 || f.calls[0] != 131292 {
		t.Errorf("calls = %v, want [131292]", f.calls)
	}
}

func TestFakeTMDBFranchiseFetcherPropagatesError(t *testing.T) {
	sentinel := errors.New("tmdb: down")
	f := &fakeTMDBFranchiseFetcher{err: sentinel}
	if _, err := f.GetCollection(context.Background(), 1); !errors.Is(err, sentinel) {
		t.Fatalf("err = %v, want sentinel", err)
	}
}

// TestValidateTMDBFranchiseConfig pins the failure-message format that
// surfaces to the admin in the sync_runs table when a placeholder template
// is applied without filling in the real TMDB collection ID.
//
// This is the unit-testable slice of syncTMDBFranchiseCollection — the
// fetch-and-match body requires a real repository for sync run recording
// and is exercised by the broader sync integration coverage rather than a
// dedicated catalog-package unit test (the user prefers fast tests; see
// the project's "no testcontainers" note).
func TestValidateTMDBFranchiseConfig(t *testing.T) {
	cases := []struct {
		name         string
		collectionID int
		wantEmpty    bool
		wantContains string
	}{
		{
			name:         "valid id",
			collectionID: 86311,
			wantEmpty:    true,
		},
		{
			name:         "placeholder zero id",
			collectionID: 0,
			wantContains: "collection_id",
		},
		{
			name:         "negative id",
			collectionID: -1,
			wantContains: "must be > 0",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := validateTMDBFranchiseConfig(c.collectionID)
			if c.wantEmpty {
				if got != "" {
					t.Errorf("got %q, want empty (valid)", got)
				}
				return
			}
			if got == "" {
				t.Fatalf("got empty string, want non-empty message")
			}
			if !strings.Contains(got, c.wantContains) {
				t.Errorf("got %q, want substring %q", got, c.wantContains)
			}
		})
	}
}

// TestValidateTMDBDiscoverConfig pins the failure-message format that surfaces
// to the admin when a discover-mode collection's source_config is incomplete.
func TestValidateTMDBDiscoverConfig(t *testing.T) {
	cases := []struct {
		name            string
		cfg             libraryCollectionSourceConfig
		wantMessagePart string
		wantMediaType   string
	}{
		{
			name: "valid",
			cfg: libraryCollectionSourceConfig{
				MediaType: "movie",
				Discover:  &libraryCollectionDiscoverConfig{SortBy: "popularity.desc"},
			},
			wantMediaType: "movie",
		},
		{
			name: "defaults media_type to movie when blank",
			cfg: libraryCollectionSourceConfig{
				Discover: &libraryCollectionDiscoverConfig{SortBy: "popularity.desc"},
			},
			wantMediaType: "movie",
		},
		{
			name:            "missing discover spec",
			cfg:             libraryCollectionSourceConfig{MediaType: "movie"},
			wantMessagePart: "discover spec",
		},
		{
			name: "invalid media_type",
			cfg: libraryCollectionSourceConfig{
				MediaType: "all",
				Discover:  &libraryCollectionDiscoverConfig{SortBy: "popularity.desc"},
			},
			wantMessagePart: "media_type",
		},
		{
			name: "missing sort_by",
			cfg: libraryCollectionSourceConfig{
				MediaType: "movie",
				Discover:  &libraryCollectionDiscoverConfig{},
			},
			wantMessagePart: "sort_by",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			reason, mediaType := validateTMDBDiscoverConfig(c.cfg)
			if c.wantMessagePart == "" {
				if reason != "" {
					t.Fatalf("got %q, want empty", reason)
				}
				if mediaType != c.wantMediaType {
					t.Errorf("mediaType = %q, want %q", mediaType, c.wantMediaType)
				}
				return
			}
			if reason == "" {
				t.Fatal("got empty reason, want non-empty")
			}
			if !strings.Contains(reason, c.wantMessagePart) {
				t.Errorf("got %q, want substring %q", reason, c.wantMessagePart)
			}
			if mediaType != "" {
				t.Errorf("mediaType = %q, want empty when invalid", mediaType)
			}
		})
	}
}

func TestCollectionPreparationDoesNotRequireRepository(t *testing.T) {
	tracker := &collectionVirtualCreationTracker{}
	ctx := context.WithValue(context.Background(), collectionVirtualCreationTrackerKey{}, tracker)
	service := &LibraryCollectionService{
		VirtualVariants: func(_ context.Context, uri, _ string) ([]VirtualPlaybackVariant, error) {
			return []VirtualPlaybackVariant{{OwnerInstallationID: 11, VirtualURI: uri}}, nil
		},
		TMDBDigitalReleases: &fakeDigitalReleaseChecker{released: map[int]bool{100: true}},
	}
	collection := &models.LibraryCollection{ID: "prepared", LibraryIDs: []int{1}, SourceConfig: json.RawMessage(`{"virtual_playback":true}`)}
	releaseDate := time.Now().UTC().Format("2006") + "-01-01"
	if service.releaseGate(ctx).skipTheatricalMovie(ctx, 100, "", "Prepared", time.Now().UTC().Year(), releaseDate) {
		t.Fatal("released movie skipped")
	}
	item, err := service.createVirtualCollectionItem(ctx, collection, "movie", "Prepared", time.Now().UTC().Year(), "tt1234567", 100, 0, releaseDate)
	if err != nil {
		t.Fatal(err)
	}
	if item.ReleaseDate == nil || *item.ReleaseDate != releaseDate || service.TMDBDigitalReleases.(*fakeDigitalReleaseChecker).calls[100] != 1 {
		t.Fatal("release metadata or verified evidence was lost during preparation")
	}
	if got := tracker.items[item.ContentID]; got.item == nil || len(got.variants) != 1 {
		t.Fatalf("prepared state = %+v", got)
	}
	service.VirtualVariants = func(context.Context, string, string) ([]VirtualPlaybackVariant, error) {
		return nil, errors.New("profile preparation failed")
	}
	if _, err := service.createVirtualCollectionItem(ctx, collection, "series", "Failure", 2000, "tt7654321", 0, 0); !errors.Is(err, ErrProviderUnavailable) {
		t.Fatalf("expected member-scoped provider failure, got %v", err)
	}
	if tracker.err != nil {
		t.Fatalf("one member's provider failure must not poison the collection run: %v", tracker.err)
	}
}

func TestAcceptPreparedItemsDisabledPlaybackClearsRetainedClaims(t *testing.T) {
	for _, mode := range []string{"disabled-virtual", "disabled-physical", "enabled-physical"} {
		t.Run(mode, func(t *testing.T) {
			physical := mode != "disabled-virtual"
			pool := newVirtualMediaTestPool(t)
			ctx := context.Background()
			_, err := pool.Exec(ctx, `
				INSERT INTO media_folders(id,name,type,enabled) VALUES(3101,'Acceptance','movies',true);
				INSERT INTO library_collections(id,slug,title,collection_type,library_id,source_config)
				VALUES('accept-disabled','accept-disabled','Acceptance','manual',3101,'{"virtual_playback":false}');
				INSERT INTO library_collection_libraries(collection_id,library_id) VALUES('accept-disabled',3101);
				INSERT INTO media_items(content_id,type,title,sort_title,status,virtual_owner_installation_id,virtual_source)
				VALUES('movie-accept-disabled','movie','Acceptance','Acceptance','matched',11,'collection:accept-disabled');
				INSERT INTO library_collection_items(collection_id,media_item_id,position) VALUES('accept-disabled','movie-accept-disabled',0);
				INSERT INTO media_files(content_id,media_folder_id,file_path,file_size,container,virtual_owner_installation_id)
				VALUES('movie-accept-disabled',3101,'virtual://movie/tmdb/3101',0,'virtual',11);
				INSERT INTO virtual_media_source_claims(plugin_installation_id,source_key,content_id,media_folder_id,owns_item_metadata)
				VALUES(11,'collection:accept-disabled','movie-accept-disabled',3101,true),(11,'request:keep','movie-accept-disabled',3101,false);
				INSERT INTO virtual_media_file_source_claims(plugin_installation_id,source_key,content_id,media_folder_id,file_path)
				VALUES(11,'collection:accept-disabled','movie-accept-disabled',3101,'virtual://movie/tmdb/3101'),(11,'request:keep','movie-accept-disabled',3101,'virtual://movie/tmdb/3101')`)
			if err != nil {
				t.Fatal(err)
			}
			if physical {
				if _, err := pool.Exec(ctx, `INSERT INTO media_files(content_id,media_folder_id,file_path,file_size,container) VALUES('movie-accept-disabled',3101,'/local/movie.mkv',1024,NULL)`); err != nil {
					t.Fatal(err)
				}
			}
			repo := NewLibraryCollectionRepository(pool)
			snapshot := &models.LibraryCollection{ID: "accept-disabled", LibraryID: 3101, LibraryIDs: []int{3101}, CollectionType: "manual", SourceConfig: json.RawMessage(`{"virtual_playback":false}`)}
			if mode == "enabled-physical" {
				snapshot.SourceConfig = json.RawMessage(`{"virtual_playback":true}`)
				if _, err := pool.Exec(ctx, `UPDATE library_collections SET source_config=$1 WHERE id=$2`, snapshot.SourceConfig, snapshot.ID); err != nil {
					t.Fatal(err)
				}
			}
			service := NewLibraryCollectionService(repo, NewItemRepository(pool), NewLibraryItemRepository(pool), nil)
			service.VirtualVariants = func(context.Context, string, string) ([]VirtualPlaybackVariant, error) {
				t.Fatal("physical acceptance called provider")
				return nil, errors.New("provider unavailable")
			}
			ctx = context.WithValue(ctx, collectionVirtualCreationTrackerKey{}, &collectionVirtualCreationTracker{})
			if _, _, err := service.acceptCollectionItems(ctx, snapshot, []LibraryCollectionItemInput{{MediaItemID: "movie-accept-disabled", SourceRank: 7}}); err != nil {
				t.Fatal(err)
			}
			var claims, fileClaims, members, files, owners int
			var source string
			if err := pool.QueryRow(ctx, `SELECT
				(SELECT count(*) FROM virtual_media_source_claims WHERE source_key='collection:accept-disabled'),
				(SELECT count(*) FROM virtual_media_file_source_claims WHERE source_key='collection:accept-disabled'),
				(SELECT count(*) FROM library_collection_items WHERE collection_id='accept-disabled' AND source_rank=7),
				(SELECT count(*) FROM media_files WHERE content_id='movie-accept-disabled'),
				(SELECT count(*) FROM virtual_media_source_claims WHERE content_id='movie-accept-disabled' AND owns_item_metadata),
				virtual_source FROM media_items WHERE content_id='movie-accept-disabled'`).Scan(&claims, &fileClaims, &members, &files, &owners, &source); err != nil {
				t.Fatal(err)
			}
			wantFiles, wantOwners, wantSource := 1, 1, "request:keep"
			if physical {
				wantFiles, wantOwners, wantSource = 2, 0, ""
			}
			if claims != 0 || fileClaims != 0 || members != 1 || files != wantFiles || owners != wantOwners || source != wantSource {
				t.Fatalf("claims=%d fileClaims=%d members=%d files=%d owners=%d source=%q", claims, fileClaims, members, files, owners, source)
			}
		})
	}
}

// fakeTMDBPresetFetcher is a stand-in for the TMDB preset adapter in tests
// that exercise sync gating without provider access.
type fakeTMDBPresetFetcher struct {
	entries []TMDBCollectionEntry
	err     error
}

func (f *fakeTMDBPresetFetcher) GetCollectionPreset(_ context.Context, _, _, _ string, _ int) ([]TMDBCollectionEntry, error) {
	return f.entries, f.err
}

func TestTMDBPresetSyncKeepsPhysicalMovieDuringProviderOutage(t *testing.T) {
	pool := newVirtualMediaTestPool(t)
	ctx := context.WithValue(context.Background(), collectionVirtualCreationTrackerKey{}, &collectionVirtualCreationTracker{})
	if _, err := pool.Exec(ctx, `
		INSERT INTO media_folders(id,name,type,enabled) VALUES(3201,'Physical','movies',true);
		INSERT INTO library_collections(id,slug,title,collection_type,library_id,source_config)
		VALUES('preset-physical','preset-physical','Physical','manual',3201,'{"virtual_playback":true}');
		INSERT INTO library_collection_libraries(collection_id,library_id) VALUES('preset-physical',3201);
		INSERT INTO media_items(content_id,type,title,sort_title,status,tmdb_id,imdb_id)
		VALUES('movie-physical-keep','movie','Physical Keep','Physical Keep','matched','424242','tt4242424');
		INSERT INTO media_item_libraries(content_id,media_folder_id) VALUES('movie-physical-keep',3201);
		INSERT INTO media_files(content_id,media_folder_id,file_path,file_size,container)
		VALUES('movie-physical-keep',3201,'/local/keep.mkv',1024,'mkv')`); err != nil {
		t.Fatalf("seed physical movie: %v", err)
	}
	service := NewLibraryCollectionService(NewLibraryCollectionRepository(pool), NewItemRepository(pool), NewLibraryItemRepository(pool), nil)
	service.TMDBCollections = &fakeTMDBPresetFetcher{entries: []TMDBCollectionEntry{{
		ID: 424242, MediaType: "movie", Title: "Physical Keep",
		IMDbID: "tt4242424", ReleaseDate: "2010-05-15",
	}}}
	// The home-release provider is down: incomplete evidence must not evict
	// a locally playable member.
	service.TMDBDigitalReleases = &fakeDigitalReleaseChecker{err: errors.New("tmdb down")}
	collection := &models.LibraryCollection{ID: "preset-physical", LibraryID: 3201, LibraryIDs: []int{3201}, CollectionType: "manual", SourceConfig: json.RawMessage(`{"virtual_playback":true}`)}
	run, err := service.syncTMDBPresetCollection(ctx, collection, libraryCollectionSourceConfig{Preset: "popular", MediaType: "movie"}, SyncCollectionOptions{SkipCollage: true})
	if err != nil {
		t.Fatalf("preset sync failed: %v", err)
	}
	if run.Message != "Matched 1 of 1 entries" {
		t.Fatalf("run message = %q, want the physical member retained", run.Message)
	}
	var members int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM library_collection_items WHERE collection_id='preset-physical' AND media_item_id='movie-physical-keep'`).Scan(&members); err != nil {
		t.Fatal(err)
	}
	if members != 1 {
		t.Fatal("provider outage evicted a physical collection member")
	}
}

// setupSkipUnresolvableSync seeds a three-entry TMDB preset collection whose
// first member cannot resolve provider variants, and returns the service wired
// with fakes plus the seeded collection row.
func setupSkipUnresolvableSync(t *testing.T) (*pgxpool.Pool, context.Context, *LibraryCollectionService, *models.LibraryCollection) {
	t.Helper()
	pool := newVirtualMediaTestPool(t)
	ctx := context.WithValue(context.Background(), collectionVirtualCreationTrackerKey{}, &collectionVirtualCreationTracker{})
	ctx = context.WithValue(ctx, collectionVirtualVariantCacheKey{}, &collectionVirtualVariantCache{
		entries: make(map[string][]VirtualPlaybackVariant, 2),
	})
	if _, err := pool.Exec(ctx, `
		INSERT INTO media_folders(id,name,type,enabled) VALUES(3301,'Skip Unresolvable','movies',true);
		INSERT INTO library_collections(id,slug,title,collection_type,library_id,source_config)
		VALUES('skip-unresolvable','skip-unresolvable','Skip Unresolvable','tmdb',3301,'{"mode":"tmdb_preset","preset":"popular","virtual_playback":true}');
		INSERT INTO library_collection_libraries(collection_id,library_id) VALUES('skip-unresolvable',3301)`); err != nil {
		t.Fatalf("seed collection: %v", err)
	}

	repo := NewLibraryCollectionRepository(pool)
	service := NewLibraryCollectionService(repo, NewItemRepository(pool), NewLibraryItemRepository(pool), nil)
	service.TMDBCollections = &fakeTMDBPresetFetcher{entries: []TMDBCollectionEntry{
		{ID: 730001, MediaType: "movie", Title: "Unresolvable Member", ReleaseDate: "2010-05-15"},
		{ID: 730002, MediaType: "movie", Title: "Second Member", ReleaseDate: "2011-05-15"},
		{ID: 730003, MediaType: "movie", Title: "Third Member", ReleaseDate: "2012-05-15"},
	}}
	service.TMDBDigitalReleases = &fakeDigitalReleaseChecker{released: map[int]bool{730001: true, 730002: true, 730003: true}}
	// The first member's provider cannot resolve variants. The run must not
	// cache that failure, so the following members resolve normally.
	service.VirtualVariants = func(_ context.Context, uri, _ string) ([]VirtualPlaybackVariant, error) {
		if strings.Contains(uri, "730001") {
			return nil, errors.New("provider connection refused")
		}
		return []VirtualPlaybackVariant{{OwnerInstallationID: 11, VirtualURI: uri}}, nil
	}

	collection, err := repo.GetByID(ctx, "skip-unresolvable")
	if err != nil {
		t.Fatal(err)
	}
	return pool, ctx, service, collection
}

// collectCollectionMemberIDs returns the committed member media-item ids in
// position order.
func collectCollectionMemberIDs(t *testing.T, pool *pgxpool.Pool, ctx context.Context, collectionID string) []string {
	t.Helper()
	var members []string
	rows, err := pool.Query(ctx, `SELECT media_item_id FROM library_collection_items WHERE collection_id=$1 ORDER BY position`, collectionID)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			t.Fatal(err)
		}
		members = append(members, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return members
}

// A member whose provider variants cannot be resolved is skipped with a run
// warning; the other members still commit.
func TestTMDBPresetSyncSkipsUnresolvableMember(t *testing.T) {
	pool, ctx, service, collection := setupSkipUnresolvableSync(t)

	run, err := service.syncTMDBPresetCollection(ctx, collection, libraryCollectionSourceConfig{
		Preset: "popular", MediaType: "movie", VirtualPlayback: true,
	}, SyncCollectionOptions{SkipCollage: true})
	if err != nil {
		t.Fatalf("one unresolvable member failed the collection sync: %v", err)
	}
	if run.Status != "warning" {
		t.Fatalf("run status = %q, want warning", run.Status)
	}
	if run.ItemsMatched != 2 || run.ItemsUnmatched != 1 {
		t.Fatalf("run matched=%d unmatched=%d, want 2/1", run.ItemsMatched, run.ItemsUnmatched)
	}
	if run.Message != "Matched 2 of 3 entries" {
		t.Fatalf("run message = %q", run.Message)
	}
	var warnings []string
	if err := json.Unmarshal(run.Warnings, &warnings); err != nil {
		t.Fatalf("decode warnings: %v", err)
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], "Unresolvable Member") || !strings.Contains(warnings[0], "provider connection refused") {
		t.Fatalf("warnings = %v, want the skipped member and its provider error", warnings)
	}

	if members := collectCollectionMemberIDs(t, pool, ctx, collection.ID); len(members) != 2 || members[0] != "movie-tmdb-730002" || members[1] != "movie-tmdb-730003" {
		t.Fatalf("members = %v, want [movie-tmdb-730002 movie-tmdb-730003]", members)
	}
	// The skipped member left no virtual state behind, so the next sync
	// retries it from scratch instead of repairing a half-written item.
	var files, claims int
	if err := pool.QueryRow(ctx, `
		SELECT
		  (SELECT count(*) FROM media_files WHERE content_id='movie-tmdb-730001'),
		  (SELECT count(*) FROM virtual_media_file_source_claims WHERE content_id='movie-tmdb-730001')`).Scan(&files, &claims); err != nil {
		t.Fatal(err)
	}
	if files != 0 || claims != 0 {
		t.Fatalf("skipped member files=%d claims=%d, want 0/0", files, claims)
	}
	var lastStatus string
	if err := pool.QueryRow(ctx, `SELECT last_sync_status FROM library_collections WHERE id=$1`, collection.ID).Scan(&lastStatus); err != nil {
		t.Fatal(err)
	}
	if lastStatus != "warning" {
		t.Fatalf("last_sync_status = %q, want warning", lastStatus)
	}
}

// A failure to read the collection itself is still a failed run, and it must
// leave the accepted members in place.
func TestTMDBPresetSyncListFailureKeepsMembers(t *testing.T) {
	pool, ctx, service, collection := setupSkipUnresolvableSync(t)

	if _, err := service.syncTMDBPresetCollection(ctx, collection, libraryCollectionSourceConfig{
		Preset: "popular", MediaType: "movie", VirtualPlayback: true,
	}, SyncCollectionOptions{SkipCollage: true}); err != nil {
		t.Fatalf("preset sync failed: %v", err)
	}
	service.TMDBCollections = &fakeTMDBPresetFetcher{err: errors.New("tmdb upstream unavailable")}
	run, err := service.SyncCollectionWithOptions(ctx, collection.ID, SyncCollectionOptions{SkipCollage: true})
	if err == nil || run != nil {
		t.Fatalf("provider list failure: run=%+v err=%v, want nil run and an error", run, err)
	}
	runs, err := NewLibraryCollectionRepository(pool).ListSyncRuns(ctx, collection.ID, 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) == 0 || runs[0].Status != "failed" || !strings.Contains(runs[0].Message, "tmdb upstream unavailable") {
		t.Fatalf("latest sync run = %+v, want a failed run naming the provider", runs)
	}
	var memberCount int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM library_collection_items WHERE collection_id=$1`, collection.ID).Scan(&memberCount); err != nil {
		t.Fatal(err)
	}
	if memberCount != 2 {
		t.Fatalf("provider list failure changed membership to %d, want 2", memberCount)
	}
}

// An existing virtual member whose provider variants fail to refresh transiently
// must preserve its collection membership and source claims, while the other members
// commit.
func TestTMDBPresetSyncPreservesExistingMemberOnTransientFailure(t *testing.T) {
	pool, _, service, collection := setupSkipUnresolvableSync(t)
	bgCtx := context.Background()

	// First sync via public entry point succeeds for members 730002 and 730003.
	if _, err := service.SyncCollectionWithOptions(bgCtx, collection.ID, SyncCollectionOptions{SkipCollage: true}); err != nil {
		t.Fatalf("first sync failed: %v", err)
	}

	// Now member 730002 is an existing member with files and claims.
	// Next sync includes 730002 and 730003, but 730002's provider variant refresh fails.
	service.TMDBCollections = &fakeTMDBPresetFetcher{entries: []TMDBCollectionEntry{
		{ID: 730002, MediaType: "movie", Title: "Second Member", ReleaseDate: "2011-05-15"},
		{ID: 730003, MediaType: "movie", Title: "Third Member", ReleaseDate: "2012-05-15"},
	}}
	var failureCalls atomic.Int32
	service.VirtualVariants = func(_ context.Context, uri, _ string) ([]VirtualPlaybackVariant, error) {
		if strings.Contains(uri, "730002") {
			failureCalls.Add(1)
			return nil, errors.New("provider connection refused")
		}
		return []VirtualPlaybackVariant{{OwnerInstallationID: 11, VirtualURI: uri}}, nil
	}

	// Run second sync via public entry point with fresh context to ensure fresh tracker and variant cache.
	run, err := service.SyncCollectionWithOptions(bgCtx, collection.ID, SyncCollectionOptions{SkipCollage: true})
	if err != nil {
		t.Fatalf("second sync failed: %v", err)
	}
	if failureCalls.Load() == 0 {
		t.Fatal("expected failure callback to be called during second sync")
	}
	if run.Status != "warning" {
		t.Fatalf("run status = %q, want warning", run.Status)
	}
	// Both members must remain in the collection.
	members := collectCollectionMemberIDs(t, pool, bgCtx, collection.ID)
	if len(members) != 2 || members[0] != "movie-tmdb-730002" || members[1] != "movie-tmdb-730003" {
		t.Fatalf("members = %v, want both members preserved", members)
	}
	// 730002's claims must be preserved.
	var claims int
	if err := pool.QueryRow(bgCtx, `SELECT count(*) FROM virtual_media_file_source_claims WHERE content_id='movie-tmdb-730002' AND source_key=$1`, "collection:"+collection.ID).Scan(&claims); err != nil || claims == 0 {
		t.Fatalf("existing member claims lost: claims=%d err=%v", claims, err)
	}
}

func TestMaterializeVirtualPlaybackPropagatesError(t *testing.T) {
	service := &LibraryCollectionService{
		VirtualVariants: func(_ context.Context, _, _ string) ([]VirtualPlaybackVariant, error) {
			return nil, errors.New("plugin connection failure")
		},
		TMDBDigitalReleases: &fakeDigitalReleaseChecker{released: map[int]bool{100: true}},
	}
	item := &models.MediaItem{Type: "movie", ImdbID: "tt1234567", TmdbID: "100", Title: "Seeking a Friend", Year: 2012}
	contentID, _ := virtualPlaybackContentID(item)
	item.ContentID = contentID

	collection := &models.LibraryCollection{
		ID:           "test-collection",
		LibraryIDs:   []int{1},
		SourceConfig: json.RawMessage(`{"virtual_playback": true}`),
	}
	err := service.materializeVirtualPlayback(context.Background(), collection, item)
	if err == nil || !strings.Contains(err.Error(), "getting virtual profile variants: plugin connection failure") {
		t.Fatalf("expected getting virtual profile variants error, got: %v", err)
	}
}

func TestCollectionManualAndRepairReleaseErrorsPrecedeStorage(t *testing.T) {
	service := &LibraryCollectionService{TMDBDigitalReleases: &fakeDigitalReleaseChecker{err: context.DeadlineExceeded}}
	collection := &models.LibraryCollection{ID: "release-failure", LibraryIDs: []int{1}, SourceConfig: json.RawMessage(`{"virtual_playback":true}`)}
	item := &models.MediaItem{ContentID: "movie-tmdb-100", Type: "movie", Title: "Released", TmdbID: "100", Year: 2000}
	for _, membership := range []bool{false, true} {
		if _, err := service.EnsureCollectionItemMaterializedWithOptions(context.Background(), collection, item, VirtualMaterializeOptions{RequireMembership: membership}); !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("membership=%v: lookup failure was lost or storage accessed: %v", membership, err)
		}
	}
}

func TestEnsureCollectionItemMaterializedRejectsFutureDatedMovie(t *testing.T) {
	service := &LibraryCollectionService{
		VirtualVariants: func(_ context.Context, uri, _ string) ([]VirtualPlaybackVariant, error) {
			return []VirtualPlaybackVariant{{OwnerInstallationID: 11, VirtualURI: uri}}, nil
		},
	}
	futureYear := time.Now().Year() + 2
	item := &models.MediaItem{Type: "movie", ImdbID: "tt9999999", Title: "Future Movie", Year: futureYear}
	contentID, _ := virtualPlaybackContentID(item)
	item.ContentID = contentID

	collection := &models.LibraryCollection{
		ID:           "test-collection",
		LibraryIDs:   []int{1},
		SourceConfig: json.RawMessage(`{"virtual_playback": true}`),
	}
	err := service.materializeVirtualPlayback(context.Background(), collection, item)
	if err == nil || !strings.Contains(err.Error(), "movie is not yet released") {
		t.Fatalf("expected future movie to be rejected, got: %v", err)
	}
}

func TestEnsureCollectionItemMaterializedChecksDigitalReleasesWhenConfigured(t *testing.T) {
	checker := &fakeDigitalReleaseChecker{released: map[int]bool{100: false, 200: true}}
	service := &LibraryCollectionService{
		TMDBDigitalReleases: checker,
		VirtualVariants: func(_ context.Context, uri, _ string) ([]VirtualPlaybackVariant, error) {
			return []VirtualPlaybackVariant{{OwnerInstallationID: 11, VirtualURI: uri}}, nil
		},
	}
	collection := &models.LibraryCollection{
		ID:           "test-collection",
		LibraryIDs:   []int{1},
		SourceConfig: json.RawMessage(`{"virtual_playback": true}`),
	}

	unreleased := &models.MediaItem{Type: "movie", ImdbID: "tt100", TmdbID: "100", Title: "Theatrical Only", Year: 2024}
	unreleased.ContentID, _ = virtualPlaybackContentID(unreleased)
	if err := service.materializeVirtualPlayback(context.Background(), collection, unreleased); err == nil || !strings.Contains(err.Error(), "movie has no confirmed home release") {
		t.Fatalf("expected unreleased movie to be rejected, got: %v", err)
	}

	tracker := &collectionVirtualCreationTracker{}
	ctx := context.WithValue(context.Background(), collectionVirtualCreationTrackerKey{}, tracker)
	released := &models.MediaItem{Type: "movie", ImdbID: "tt200", TmdbID: "200", Title: "Digitally Released", Year: 2024}
	released.ContentID, _ = virtualPlaybackContentID(released)
	res, err := service.EnsureCollectionItemMaterializedWithOptions(ctx, collection, released, VirtualMaterializeOptions{RequireMembership: false})
	if err != nil {
		t.Fatalf("expected released movie to materialize, got: %v", err)
	}
	if res.ContentID != released.ContentID {
		t.Fatalf("materialize result ContentID = %q, want %q", res.ContentID, released.ContentID)
	}
}

func TestCollectionAcceptanceBlocksAliasAttachedAfterPreparation(t *testing.T) {
	sfx := uniqueReleaseSuffix(t)
	tmdbID := "4244" + sfx
	imdbID := "tt424244" + sfx[:6]
	movieID := "movie-tmdb-" + tmdbID
	pool := newVirtualMediaTestPool(t)
	ctx := context.Background()
	if _, err := pool.Exec(ctx, `
		INSERT INTO media_folders(id,name,type,enabled) VALUES(972,'AliasRace','movies',true);
		INSERT INTO library_collections(id,slug,title,collection_type,library_id,source_config)
		VALUES('alias-race','alias-race','Alias Race','manual',972,'{"virtual_playback":true}');
		INSERT INTO library_collection_libraries(collection_id,library_id) VALUES('alias-race',972);
		INSERT INTO media_items(content_id,type,title,sort_title,status,tmdb_id)
		VALUES('`+movieID+`','movie','Alias Race','Alias Race','matched','`+tmdbID+`');
		INSERT INTO media_files(content_id,media_folder_id,file_path,file_size,container,probe_source,virtual_owner_installation_id)
		VALUES('`+movieID+`',972,'virtual://movie/`+tmdbID+`?profile=1080p',0,'virtual','virtual_collection',11)`); err != nil {
		t.Fatalf("seed virtual member: %v", err)
	}
	service := NewLibraryCollectionService(NewLibraryCollectionRepository(pool), NewItemRepository(pool), NewLibraryItemRepository(pool), nil)
	tmdbInt, err := strconv.Atoi(tmdbID)
	if err != nil {
		t.Fatal(err)
	}
	service.TMDBDigitalReleases = &fakeDigitalReleaseChecker{released: map[int]bool{tmdbInt: true}}
	service.VirtualVariants = func(_ context.Context, uri, _ string) ([]VirtualPlaybackVariant, error) {
		return []VirtualPlaybackVariant{{OwnerInstallationID: 11, VirtualURI: uri}}, nil
	}
	collection := &models.LibraryCollection{ID: "alias-race", LibraryID: 972, LibraryIDs: []int{972}, CollectionType: "manual", SourceConfig: json.RawMessage(`{"virtual_playback":true}`)}
	tracker := &collectionVirtualCreationTracker{}
	prepCtx := context.WithValue(ctx, collectionVirtualCreationTrackerKey{}, tracker)
	item, err := service.items.GetByID(ctx, movieID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.EnsureCollectionItemMaterializedWithOptions(prepCtx, collection, item, VirtualMaterializeOptions{RequireMembership: false}); err != nil {
		t.Fatalf("preparation failed: %v", err)
	}
	// Between preparation and acceptance, enrichment attaches an IMDb alias
	// that already carries a future override. Acceptance must observe the
	// current alias set rather than the prepared snapshot.
	if _, err := pool.Exec(ctx, `
		UPDATE media_items SET imdb_id='`+imdbID+`' WHERE content_id='`+movieID+`';
		INSERT INTO media_item_provider_ids(content_id,item_type,provider,provider_id)
		VALUES('`+movieID+`','movie','imdb','`+imdbID+`');
		INSERT INTO verified_release_override_history
		(media_type,provider,provider_id,season_number,episode_number,revision,action,release_at,evidence_note,actor_account_id)
		VALUES('movie','imdb','`+imdbID+`',0,0,1,'set','2099-01-01T00:00:00Z','verified future',1)`); err != nil {
		t.Fatalf("attach alias with future override: %v", err)
	}
	// Acceptance must refuse the stale preparation with a retryable
	// conflict rather than deciding on the changed alias set.
	_, _, err = service.acceptCollectionItems(prepCtx, collection, []LibraryCollectionItemInput{{MediaItemID: movieID}})
	if err == nil || !errors.Is(err, ErrReleaseOverrideConflict) && !strings.Contains(err.Error(), "alias set changed") {
		t.Fatalf("acceptance missed the attached alias: %v", err)
	}
	// Fresh preparation observes the attached future alias and blocks.
	tracker2 := &collectionVirtualCreationTracker{}
	prepCtx2 := context.WithValue(ctx, collectionVirtualCreationTrackerKey{}, tracker2)
	item2, err := service.items.GetByID(ctx, movieID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.EnsureCollectionItemMaterializedWithOptions(prepCtx2, collection, item2, VirtualMaterializeOptions{RequireMembership: false}); err == nil {
		t.Fatal("expected repreparation to block on the attached future override")
	}
}

func TestTMDBPresetSyncUsesStoredAliasOutsideTargetLibrary(t *testing.T) {
	sfx := uniqueReleaseSuffix(t)
	tmdbID := "4251" + sfx
	imdbID := "tt4251" + sfx[:6]
	movieID := "movie-tmdb-" + tmdbID
	tmdbInt, err := strconv.Atoi(tmdbID)
	if err != nil {
		t.Fatal(err)
	}
	pool := newVirtualMediaTestPool(t)
	ctx := context.WithValue(context.Background(), collectionVirtualCreationTrackerKey{}, &collectionVirtualCreationTracker{})
	if _, err := pool.Exec(ctx, `
		INSERT INTO media_folders(id,name,type,enabled) VALUES(3202,'Elsewhere','movies',true),(3203,'Target','movies',true);
		INSERT INTO library_collections(id,slug,title,collection_type,library_id,source_config)
		VALUES('preset-alias','preset-alias','Alias','manual',3203,'{"virtual_playback":true}');
		INSERT INTO library_collection_libraries(collection_id,library_id) VALUES('preset-alias',3203);
		INSERT INTO media_items(content_id,type,title,sort_title,status,tmdb_id,imdb_id)
		VALUES('`+movieID+`','movie','Elsewhere Movie','Elsewhere Movie','matched','`+tmdbID+`','`+imdbID+`');
		INSERT INTO media_item_libraries(content_id,media_folder_id) VALUES('`+movieID+`',3202);
		INSERT INTO verified_release_override_history
		(media_type,provider,provider_id,season_number,episode_number,revision,action,release_at,evidence_note,actor_account_id)
		VALUES('movie','imdb','`+imdbID+`',0,0,1,'set','2020-01-01T00:00:00Z','verified past',1)`); err != nil {
		t.Fatalf("seed out-of-library movie with past alias override: %v", err)
	}
	service := NewLibraryCollectionService(NewLibraryCollectionRepository(pool), NewItemRepository(pool), NewLibraryItemRepository(pool), nil)
	// The TMDB source entry carries no IMDb alias and the provider reports
	// theatrical-only; the stored past alias outside the target library
	// must still permit virtual materialization into it.
	service.TMDBCollections = &fakeTMDBPresetFetcher{entries: []TMDBCollectionEntry{{
		ID: tmdbInt, MediaType: "movie", Title: "Elsewhere Movie", ReleaseDate: "2010-05-15",
	}}}
	service.TMDBDigitalReleases = &fakeDigitalReleaseChecker{released: map[int]bool{tmdbInt: false}}
	service.VirtualVariants = func(_ context.Context, uri, _ string) ([]VirtualPlaybackVariant, error) {
		return []VirtualPlaybackVariant{{OwnerInstallationID: 11, VirtualURI: uri}}, nil
	}
	collection := &models.LibraryCollection{ID: "preset-alias", LibraryID: 3203, LibraryIDs: []int{3203}, CollectionType: "manual", SourceConfig: json.RawMessage(`{"virtual_playback":true}`)}
	run, err := service.syncTMDBPresetCollection(ctx, collection, libraryCollectionSourceConfig{Preset: "popular", MediaType: "movie", VirtualPlayback: true}, SyncCollectionOptions{SkipCollage: true})
	if err != nil {
		t.Fatalf("preset sync failed: %v", err)
	}
	if run.Message != "Matched 1 of 1 entries" {
		t.Fatalf("run message = %q, want the stored alias to permit", run.Message)
	}
}

func TestMultiItemAcceptanceCompletesWithConcurrentOverrideMutation(t *testing.T) {
	sfx := uniqueReleaseSuffix(t)
	tmdbA := "4252" + sfx
	tmdbB := "4253" + sfx
	movieA := "movie-tmdb-" + tmdbA
	movieB := "movie-tmdb-" + tmdbB
	tmdbIntA, err := strconv.Atoi(tmdbA)
	if err != nil {
		t.Fatal(err)
	}
	tmdbIntB, err := strconv.Atoi(tmdbB)
	if err != nil {
		t.Fatal(err)
	}
	pool := newVirtualMediaTestPool(t)
	ctx := context.Background()
	if _, err := pool.Exec(ctx, `
		INSERT INTO media_folders(id,name,type,enabled) VALUES(3204,'MultiDebt','movies',true);
		INSERT INTO library_collections(id,slug,title,collection_type,library_id,source_config)
		VALUES('multi-debt','multi-debt','Multi Debt','manual',3204,'{"virtual_playback":true}');
		INSERT INTO library_collection_libraries(collection_id,library_id) VALUES('multi-debt',3204);
		INSERT INTO media_items(content_id,type,title,sort_title,status,tmdb_id)
		VALUES('`+movieA+`','movie','Debt A','Debt A','matched','`+tmdbA+`'),
		      ('`+movieB+`','movie','Debt B','Debt B','matched','`+tmdbB+`');
		INSERT INTO media_files(content_id,media_folder_id,file_path,file_size,container,probe_source,virtual_owner_installation_id)
		VALUES('`+movieA+`',3204,'virtual://movie/`+tmdbA+`?profile=1080p',0,'virtual','virtual_collection',11),
		      ('`+movieB+`',3204,'virtual://movie/`+tmdbB+`?profile=1080p',0,'virtual','virtual_collection',11);
		INSERT INTO users(username,role,enabled) VALUES('override-multi-`+sfx+`','admin',true)`); err != nil {
		t.Fatalf("seed multi-item fixtures: %v", err)
	}
	var actor int
	if err := pool.QueryRow(ctx, `SELECT id FROM users WHERE username='override-multi-`+sfx+`'`).Scan(&actor); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DELETE FROM users WHERE id=$1`, actor) })
	newService := func() *LibraryCollectionService {
		service := NewLibraryCollectionService(NewLibraryCollectionRepository(pool), NewItemRepository(pool), NewLibraryItemRepository(pool), nil)
		service.TMDBDigitalReleases = &fakeDigitalReleaseChecker{released: map[int]bool{tmdbIntA: true, tmdbIntB: true}}
		service.VirtualVariants = func(_ context.Context, uri, _ string) ([]VirtualPlaybackVariant, error) {
			return []VirtualPlaybackVariant{{OwnerInstallationID: 11, VirtualURI: uri}}, nil
		}
		return service
	}
	collection := &models.LibraryCollection{ID: "multi-debt", LibraryID: 3204, LibraryIDs: []int{3204}, CollectionType: "manual", SourceConfig: json.RawMessage(`{"virtual_playback":true}`)}
	prepare := func(t *testing.T) (context.Context, *LibraryCollectionService) {
		t.Helper()
		service := newService()
		tracker := &collectionVirtualCreationTracker{}
		prepCtx := context.WithValue(ctx, collectionVirtualCreationTrackerKey{}, tracker)
		for _, id := range []string{movieA, movieB} {
			item, err := service.items.GetByID(ctx, id)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := service.EnsureCollectionItemMaterializedWithOptions(prepCtx, collection, item, VirtualMaterializeOptions{RequireMembership: false}); err != nil {
				t.Fatalf("prepare %s: %v", id, err)
			}
		}
		return prepCtx, service
	}
	isDeadlock := func(err error) bool {
		return err != nil && strings.Contains(strings.ToLower(err.Error()), "deadlock")
	}
	isRevisionConflict := func(err error) bool {
		return errors.Is(err, ErrReleaseOverrideConflict)
	}
	// Deterministic concurrency coverage. The participants share
	// overlapping resources: acceptance takes content locks on movieA and
	// movieB (sorted) plus the collection row, and validates a snapshot
	// covering B's release identity with a shared lock; each mutate takes
	// the exclusive lock on that same B identity before its revision 0->1
	// insert plus debt write. A start barrier releases all four
	// goroutines into the same contention window instead of running
	// sequentially, so the scheduler interleaves the acceptance-vs-mutate
	// and mutate-vs-mutate orderings through the same locks. Convergence
	// holds either way because the acceptance decision predates the
	// override. Expected classes are whitelisted: acceptance succeeds on
	// its pre-override snapshot while exactly one mutate wins revision
	// 0->1 and the losers observe a revision conflict. Deadlocks or any
	// other error class fail.
	prepCtx, service := prepare(t)
	start := make(chan struct{})
	acceptDone := make(chan error, 1)
	mutateDone := make(chan error, 3)
	go func() {
		<-start
		_, _, acceptErr := service.acceptCollectionItems(prepCtx, collection, []LibraryCollectionItemInput{{MediaItemID: movieA}, {MediaItemID: movieB}})
		acceptDone <- acceptErr
	}()
	for i := 0; i < 3; i++ {
		go func() {
			<-start
			_, err := NewReleaseOverrideRepository(pool).Mutate(context.Background(), actor, ReleaseOverrideMutation{
				ReleaseIdentity: ReleaseIdentity{MediaType: "movie", Provider: "tmdb", ProviderID: tmdbB},
				ReleaseAt:       "2099-01-01",
				EvidenceNote:    "verified future",
			}, false)
			mutateDone <- err
		}()
	}
	close(start)
	select {
	case err := <-acceptDone:
		if isDeadlock(err) {
			t.Fatalf("deadlock between acceptance and override mutation: %v", err)
		}
		if err != nil {
			t.Fatalf("acceptance must succeed with a pre-override snapshot: %v", err)
		}
	case <-time.After(120 * time.Second):
		t.Fatal("acceptance did not complete")
	}
	wins, conflicts := 0, 0
	for i := 0; i < 3; i++ {
		select {
		case err := <-mutateDone:
			if isDeadlock(err) {
				t.Fatalf("deadlock between override mutations: %v", err)
			}
			switch {
			case err == nil:
				wins++
			case isRevisionConflict(err):
				conflicts++
			default:
				t.Fatalf("unexpected override mutation error class: %v", err)
			}
		case <-time.After(120 * time.Second):
			t.Fatal("override mutations did not complete")
		}
	}
	if wins != 1 || conflicts != 2 {
		t.Fatalf("override race settled wins=%d conflicts=%d, want exactly 1 win and 2 revision conflicts", wins, conflicts)
	}
	// Atomic effects: exactly one committed revision for B, the
	// acceptance's membership writes landed, and the winner's debt
	// enqueue is durable.
	var revisions int
	var maxRevision int64
	if err := pool.QueryRow(ctx, `SELECT count(*), COALESCE(max(revision),0) FROM verified_release_override_history WHERE media_type='movie' AND provider='tmdb' AND provider_id=$1`, tmdbB).Scan(&revisions, &maxRevision); err != nil {
		t.Fatal(err)
	}
	if revisions != 1 || maxRevision != 1 {
		t.Fatalf("override history rows=%d maxRevision=%d, want 1 row at revision 1", revisions, maxRevision)
	}
	var members int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM library_collection_items WHERE collection_id='multi-debt'`).Scan(&members); err != nil {
		t.Fatal(err)
	}
	if members != 2 {
		t.Fatalf("collection members after race = %d, want 2 (acceptance must be atomic)", members)
	}
	var movieBTargeted int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM metadata_refresh_debt WHERE content_id=$1 AND target_type='item'`, movieB).Scan(&movieBTargeted); err != nil {
		t.Fatal(err)
	}
	if movieBTargeted != 1 {
		t.Fatalf("metadata_refresh_debt rows targeting B = %d, want 1 (winner's enqueue must be durable)", movieBTargeted)
	}
	// Convergence is deterministic: B carries a committed future override
	// (exactly one of the racing mutations wins revision 0->1), so fresh
	// preparation must refuse B while A still prepares, and the winning
	// mutation's debt enqueue must have landed for B.
	service3 := newService()
	tracker3 := &collectionVirtualCreationTracker{}
	prepCtx3 := context.WithValue(ctx, collectionVirtualCreationTrackerKey{}, tracker3)
	itemA, err := service3.items.GetByID(ctx, movieA)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service3.EnsureCollectionItemMaterializedWithOptions(prepCtx3, collection, itemA, VirtualMaterializeOptions{RequireMembership: false}); err != nil {
		t.Fatalf("reprepare A: %v", err)
	}
	itemB, err := service3.items.GetByID(ctx, movieB)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service3.EnsureCollectionItemMaterializedWithOptions(prepCtx3, collection, itemB, VirtualMaterializeOptions{RequireMembership: false}); err == nil {
		t.Fatal("expected the committed future override to block B on convergence")
	}
	var debts int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM metadata_refresh_debt WHERE content_id=$1`, movieB).Scan(&debts); err != nil {
		t.Fatal(err)
	}
	if debts != 1 {
		t.Fatalf("override mutation debt rows for B = %d, want 1", debts)
	}
}

func TestCollectionAcceptanceConflictsOnRemovedPermittingAlias(t *testing.T) {
	sfx := uniqueReleaseSuffix(t)
	tmdbID := "4254" + sfx
	imdbID := "tt4254" + sfx[:6]
	movieID := "movie-tmdb-" + tmdbID
	pool := newVirtualMediaTestPool(t)
	ctx := context.Background()
	if _, err := pool.Exec(ctx, `
		INSERT INTO media_folders(id,name,type,enabled) VALUES(3205,'AliasRemoved','movies',true);
		INSERT INTO library_collections(id,slug,title,collection_type,library_id,source_config)
		VALUES('alias-removed','alias-removed','Alias Removed','manual',3205,'{"virtual_playback":true}');
		INSERT INTO library_collection_libraries(collection_id,library_id) VALUES('alias-removed',3205);
		INSERT INTO media_items(content_id,type,title,sort_title,status,tmdb_id,imdb_id)
		VALUES('`+movieID+`','movie','Alias Removed','Alias Removed','matched','`+tmdbID+`','`+imdbID+`');
		INSERT INTO media_item_provider_ids(content_id,item_type,provider,provider_id)
		VALUES('`+movieID+`','movie','imdb','`+imdbID+`');
		INSERT INTO media_files(content_id,media_folder_id,file_path,file_size,container,probe_source,virtual_owner_installation_id)
		VALUES('`+movieID+`',3205,'virtual://movie/`+tmdbID+`?profile=1080p',0,'virtual','virtual_collection',11);
		INSERT INTO verified_release_override_history
		(media_type,provider,provider_id,season_number,episode_number,revision,action,release_at,evidence_note,actor_account_id)
		VALUES('movie','imdb','`+imdbID+`',0,0,1,'set','2020-01-01T00:00:00Z','verified past',1)`); err != nil {
		t.Fatalf("seed movie with permitting alias: %v", err)
	}
	service := NewLibraryCollectionService(NewLibraryCollectionRepository(pool), NewItemRepository(pool), NewLibraryItemRepository(pool), nil)
	// The provider is unreachable: only the stored past override can permit.
	service.TMDBDigitalReleases = &fakeDigitalReleaseChecker{err: errors.New("tmdb down")}
	service.VirtualVariants = func(_ context.Context, uri, _ string) ([]VirtualPlaybackVariant, error) {
		return []VirtualPlaybackVariant{{OwnerInstallationID: 11, VirtualURI: uri}}, nil
	}
	collection := &models.LibraryCollection{ID: "alias-removed", LibraryID: 3205, LibraryIDs: []int{3205}, CollectionType: "manual", SourceConfig: json.RawMessage(`{"virtual_playback":true}`)}
	tracker := &collectionVirtualCreationTracker{}
	prepCtx := context.WithValue(ctx, collectionVirtualCreationTrackerKey{}, tracker)
	item, err := service.items.GetByID(ctx, movieID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.EnsureCollectionItemMaterializedWithOptions(prepCtx, collection, item, VirtualMaterializeOptions{RequireMembership: false}); err != nil {
		t.Fatalf("preparation via past alias failed: %v", err)
	}
	// Enrichment corrects the mistaken alias away before acceptance. The
	// obsolete past override must not retain authority: acceptance conflicts
	// instead of materializing without current evidence.
	if _, err := pool.Exec(ctx, `
		UPDATE media_items SET imdb_id='' WHERE content_id='`+movieID+`';
		DELETE FROM media_item_provider_ids WHERE content_id='`+movieID+`' AND provider='imdb'`); err != nil {
		t.Fatalf("remove mistaken alias: %v", err)
	}
	_, _, err = service.acceptCollectionItems(prepCtx, collection, []LibraryCollectionItemInput{{MediaItemID: movieID}})
	if err == nil || !errors.Is(err, ErrReleaseOverrideConflict) && !strings.Contains(err.Error(), "alias set changed") {
		t.Fatalf("acceptance used the removed alias: %v", err)
	}
	// Fresh preparation without the alias has neither override nor provider
	// evidence and must fail closed.
	tracker2 := &collectionVirtualCreationTracker{}
	prepCtx2 := context.WithValue(ctx, collectionVirtualCreationTrackerKey{}, tracker2)
	item2, err := service.items.GetByID(ctx, movieID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.EnsureCollectionItemMaterializedWithOptions(prepCtx2, collection, item2, VirtualMaterializeOptions{RequireMembership: false}); err == nil {
		t.Fatal("expected fail-closed preparation without alias or evidence")
	}
}
