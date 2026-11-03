package jellycompat

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/config"

	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/sections"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

// TestLatestFastPathEligible pins the eligibility rules for the native
// /Items/Latest fast path. Eligibility is decided off the ACTUAL browse params
// the fallback would receive (buildLatestBrowseParams), so any filter the
// synthetic recently-added section cannot reproduce — including params added to
// buildBrowseParams in the future — must disqualify the request rather than be
// silently ignored by the cached path.
func TestLatestFastPathEligible(t *testing.T) {
	played := true
	base := func() itemsQuery { return itemsQuery{parentLibraryID: 7, startIndex: 0, limit: 24} }
	paramsFor := func(q itemsQuery) url.Values { return buildLatestBrowseParams(q) }

	if !latestFastPathEligible(paramsFor(base()), "movie") {
		t.Fatalf("a plain first-page movies-library Latest should be eligible")
	}
	// Latest returns a bare array, so it never asks BrowseItems to count.
	counted := base()
	counted.enableTotalRecordCount = true
	unplayed := false
	counted.isPlayed = &unplayed
	if got := paramsFor(counted).Get("include_total"); got != "false" {
		t.Fatalf("Latest include_total = %q, want false", got)
	}
	if !latestFastPathEligible(paramsFor(base()), "series") {
		t.Fatalf("a plain first-page series-library Latest should be eligible")
	}
	knownRating := base()
	knownRating.maxOfficialRating = "PG-13"
	if !latestFastPathEligible(paramsFor(knownRating), "movie") {
		t.Fatalf("a known max rating should stay eligible (it folds into the access filter)")
	}

	cases := []struct {
		name    string
		libType string
		mutate  func(*itemsQuery)
	}{
		{name: "non-video library", libType: ""},
		{name: "deeper page", libType: "movie", mutate: func(q *itemsQuery) { q.startIndex = 24 }},
		{name: "played filter", libType: "movie", mutate: func(q *itemsQuery) { q.isPlayed = &played }},
		{name: "limit beyond shared fetch budget", libType: "movie", mutate: func(q *itemsQuery) { q.limit = compatLatestCacheFetchLimit + 1 }},
		{name: "unknown rating string", libType: "movie", mutate: func(q *itemsQuery) { q.maxOfficialRating = "NOT-A-RATING" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			q := base()
			if tc.mutate != nil {
				tc.mutate(&q)
			}
			if latestFastPathEligible(paramsFor(q), tc.libType) {
				t.Fatalf("%s must NOT be eligible for the native fast path", tc.name)
			}
		})
	}

	// The future-drift guard: a param buildBrowseParams starts emitting that
	// this gate has never heard of must disqualify the fast path on its own.
	future := paramsFor(base())
	future.Set("years", "2020")
	if latestFastPathEligible(future, "movie") {
		t.Fatal("an unrecognized browse param must disqualify the fast path")
	}
}

// TestLatestRecentlyAddedConfigParity is the data-parity assertion for the
// native /Items/Latest fast path. A DB-backed comparison of the emitted rows is
// out of scope for a unit test, so instead we prove the two surfaces feed the
// SAME recently-added query the SAME type filter: the config the compat path
// builds must round-trip through the native ParseConfigFilters/filter_type that
// buildRecentlyAddedQuery reads. Same type filter + same library + same scope +
// same limit + same ORDER BY mil.first_seen_at DESC ⇒ identical membership and
// ordering.
func TestLatestRecentlyAddedConfigParity(t *testing.T) {
	cases := []struct {
		name           string
		itemTypes      []string
		wantFilterType string
	}{
		{name: "movies library", itemTypes: []string{"movie"}, wantFilterType: "movie"},
		{name: "series library", itemTypes: []string{"series"}, wantFilterType: "series"},
		{name: "empty/mixed library", itemTypes: nil, wantFilterType: ""},
		{name: "mixed explicit types", itemTypes: []string{"movie", "series"}, wantFilterType: ""},
		{name: "unsupported type", itemTypes: []string{"boxset"}, wantFilterType: ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := latestRecentlyAddedConfig(tc.itemTypes)
			// Native reads the filter type via the shared ParseConfigFilters; the
			// compat config must decode to exactly the same value.
			got := sections.ParseConfigFilters(cfg).FilterType
			if got != tc.wantFilterType {
				t.Fatalf("latestRecentlyAddedConfig(%v) filter_type = %q, want %q", tc.itemTypes, got, tc.wantFilterType)
			}
		})
	}
}

// seriesEpisodeSource is an episodeListSource that returns a fixed episode set
// per series via ListBySeriesIDs (the only method the series watch-state rollup
// uses). Other methods are unused by this path.
type seriesEpisodeSource struct {
	bySeries  map[string][]*models.Episode
	listCalls int
}

func (s *seriesEpisodeSource) ListBySeason(context.Context, string, int) ([]*models.Episode, error) {
	return nil, nil
}

func (s *seriesEpisodeSource) ListBySeriesGroupedBySeason(context.Context, string) (map[int][]*models.Episode, error) {
	return nil, nil
}

func (s *seriesEpisodeSource) ListBySeriesIDs(_ context.Context, ids []string) (map[string][]*models.Episode, error) {
	s.listCalls++
	out := make(map[string][]*models.Episode, len(ids))
	for _, id := range ids {
		if eps, ok := s.bySeries[id]; ok {
			out[id] = eps
		}
	}
	return out, nil
}

func (s *seriesEpisodeSource) GetByIDs(context.Context, []string) ([]*models.Episode, error) {
	return nil, nil
}

// completedProgressStoreProvider hands back a fixed completedProgressStore.
type completedProgressStoreProvider struct {
	store userstore.UserStore
}

func (p *completedProgressStoreProvider) ForUser(context.Context, int) (userstore.UserStore, error) {
	return p.store, nil
}

func (p *completedProgressStoreProvider) Close() error { return nil }

// TestLatestLimitZeroQueriesNothing: Latest has no total to report, so
// Limit=0 answers an empty array without browsing (the stub's BrowseItems
// panics).
func TestLatestLimitZeroQueriesNothing(t *testing.T) {
	h := NewItemsHandler(&stubContentService{}, nil, NewResourceIDCodec(), &config.Config{}, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	req := httptest.NewRequest(http.MethodGet, "/Items/Latest?Limit=0&IsPlayed=false&EnableTotalRecordCount=true", nil)
	req = req.WithContext(context.WithValue(req.Context(), compatSessionKey, &Session{Token: "token", ProfileID: "profile-1", StreamAppUserID: 1}))
	rec := httptest.NewRecorder()
	h.HandleLatest(rec, req)
	if rec.Code != http.StatusOK || strings.TrimSpace(rec.Body.String()) != "[]" {
		t.Fatalf("Limit=0 Latest = %d %q, want 200 []", rec.Code, rec.Body.String())
	}
}
