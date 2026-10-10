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

	_, err := svc.fetchMDBListEntries(context.Background(), "http://127.0.0.1:8096/", 0)
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
