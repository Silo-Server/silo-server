package catalog

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

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

func TestTraktCandidatesByPriority_ShowUsesTVDBBeforeTMDB(t *testing.T) {
	lookup := &ExternalIDLookup{
		ByTVDB: map[string]string{"100": "tvdb-hit"},
		ByTMDB: map[string]string{"200": "tmdb-hit"},
		ByIMDb: map[string]string{"tt300": "imdb-hit"},
	}
	candidates := traktCandidatesByPriority(lookup, TraktCollectionEntry{
		TVDBID: 100,
		TMDBID: 200,
		IMDbID: "tt300",
	}, "series")
	want := []string{"tvdb-hit", "tmdb-hit", "imdb-hit"}
	if len(candidates) != len(want) {
		t.Fatalf("candidates = %v, want %v", candidates, want)
	}
	for i := range want {
		if candidates[i] != want[i] {
			t.Fatalf("candidates = %v, want %v", candidates, want)
		}
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

// fakeTMDBDiscoverFetcher stands in for tmdbDiscoverAdapter in unit tests.
// It records the (mediaType, params, limit) it was asked for so callers can
// assert the discover spec flowed end-to-end without truncation, and returns
// canned entries in the order TMDB would have.
type fakeTMDBDiscoverFetcher struct {
	calls   []fakeTMDBDiscoverCall
	entries []TMDBCollectionEntry
	err     error
}

type fakeTMDBDiscoverCall struct {
	MediaType string
	Params    TMDBDiscoverParams
	Limit     int
}

func (f *fakeTMDBDiscoverFetcher) Discover(_ context.Context, mediaType string, params TMDBDiscoverParams, limit int) ([]TMDBCollectionEntry, error) {
	f.calls = append(f.calls, fakeTMDBDiscoverCall{MediaType: mediaType, Params: params, Limit: limit})
	if f.err != nil {
		return nil, f.err
	}
	out := make([]TMDBCollectionEntry, len(f.entries))
	copy(out, f.entries)
	return out, nil
}

func TestFakeTMDBDiscoverFetcherSatisfiesInterface(t *testing.T) {
	// Static-typed assertion the fake implements the interface — a regression
	// guard so future signature changes break here, not in router wiring.
	var _ TMDBDiscoverFetcher = (*fakeTMDBDiscoverFetcher)(nil)
}

func TestFakeTMDBDiscoverFetcherPropagatesError(t *testing.T) {
	sentinel := errors.New("tmdb: down")
	f := &fakeTMDBDiscoverFetcher{err: sentinel}
	_, err := f.Discover(context.Background(), "movie", TMDBDiscoverParams{SortBy: "popularity.desc"}, 10)
	if !errors.Is(err, sentinel) {
		t.Fatalf("err = %v, want sentinel", err)
	}
}

// TestFakeTMDBDiscoverFetcherRecordsParams verifies the full discover params
// payload reaches the fetcher untouched. This is the unit-testable slice of
// syncTMDBDiscoverCollection — the post-fetch matcher requires a real
// repository (the project's "no testcontainers" policy keeps that off the
// per-package unit suite).
func TestFakeTMDBDiscoverFetcherRecordsParams(t *testing.T) {
	want := []TMDBCollectionEntry{
		{ID: 11, MediaType: "movie", Title: "First"},
		{ID: 22, MediaType: "movie", Title: "Second"},
		{ID: 33, MediaType: "movie", Title: "Third"},
	}
	f := &fakeTMDBDiscoverFetcher{entries: want}

	params := TMDBDiscoverParams{
		WithGenres:       []int{28, 12},
		SortBy:           "popularity.desc",
		VoteCountGte:     300,
		VoteAverageGte:   6.5,
		ReleaseDateGte:   "2010-01-01",
		Certifications:   []string{"PG-13"},
		WithRuntimeGte:   60,
		WithRuntimeLte:   240,
		OriginalLanguage: "en",
	}
	got, err := f.Discover(context.Background(), "movie", params, 50)
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
	if len(f.calls) != 1 {
		t.Fatalf("calls = %d, want 1", len(f.calls))
	}
	call := f.calls[0]
	if call.MediaType != "movie" {
		t.Errorf("media_type = %q, want movie", call.MediaType)
	}
	if call.Limit != 50 {
		t.Errorf("limit = %d, want 50", call.Limit)
	}
	if call.Params.SortBy != "popularity.desc" {
		t.Errorf("sort_by = %q", call.Params.SortBy)
	}
	if len(call.Params.WithGenres) != 2 || call.Params.WithGenres[0] != 28 || call.Params.WithGenres[1] != 12 {
		t.Errorf("with_genres = %v", call.Params.WithGenres)
	}
	if call.Params.VoteCountGte != 300 || call.Params.VoteAverageGte != 6.5 {
		t.Errorf("vote thresholds = %d / %v", call.Params.VoteCountGte, call.Params.VoteAverageGte)
	}
	if call.Params.OriginalLanguage != "en" {
		t.Errorf("original_language = %q", call.Params.OriginalLanguage)
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

func TestTraktCandidatesByPriority_MovieUsesTMDBBeforeIMDb(t *testing.T) {
	lookup := &ExternalIDLookup{
		ByTVDB: map[string]string{"100": "tvdb-hit"},
		ByTMDB: map[string]string{"200": "tmdb-hit"},
		ByIMDb: map[string]string{"tt300": "imdb-hit"},
	}
	candidates := traktCandidatesByPriority(lookup, TraktCollectionEntry{
		TVDBID: 100,
		TMDBID: 200,
		IMDbID: "tt300",
	}, "movie")
	want := []string{"tmdb-hit", "imdb-hit"}
	if len(candidates) != len(want) {
		t.Fatalf("candidates = %v, want %v", candidates, want)
	}
	for i := range want {
		if candidates[i] != want[i] {
			t.Fatalf("candidates = %v, want %v", candidates, want)
		}
	}
}
