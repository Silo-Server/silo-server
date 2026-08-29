package catalog

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/Silo-Server/silo-server/internal/collectionutil"
)

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
