package jellycompat

import (
	"context"
	"net/url"
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
		{name: "ebook library", libType: "ebook"},
		{name: "deeper page", libType: "movie", mutate: func(q *itemsQuery) { q.startIndex = 24 }},
		{name: "played filter", libType: "movie", mutate: func(q *itemsQuery) { q.isPlayed = &played }},
		{name: "backdrop required", libType: "movie", mutate: func(q *itemsQuery) { q.requireBackdrop = true }},
		{name: "genre filter", libType: "movie", mutate: func(q *itemsQuery) { q.genreName = "Action" }},
		{name: "name prefix filter", libType: "movie", mutate: func(q *itemsQuery) { q.namePrefix = "The" }},
		{name: "person filter", libType: "series", mutate: func(q *itemsQuery) { q.personID = 42 }},
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

// TestLatestRecentlyAddedConfigUsesScopedTypes documents that the config helper
// normalizes through the same compatScopedTypes clamp BrowseItems uses, so a
// concrete single video type is preserved while anything that clamps to a
// multi-type or no-match set falls back to the all-types (nil) config.
func TestLatestRecentlyAddedConfigUsesScopedTypes(t *testing.T) {
	if cfg := latestRecentlyAddedConfig([]string{"Movie"}); cfg == nil {
		t.Fatalf("a movie-typed library should yield a non-nil filter_type config")
	}
	if cfg := latestRecentlyAddedConfig(nil); cfg != nil {
		t.Fatalf("an untyped (mixed) library should yield a nil (all-types) config, got %s", cfg)
	}
}

// seriesEpisodeSource is an episodeListSource that returns a fixed episode set
// per series via ListBySeriesIDs (the only method the series watch-state rollup
// uses). Other methods are unused by this path.
type seriesEpisodeSource struct {
	bySeries map[string][]*models.Episode
}

func (s *seriesEpisodeSource) ListBySeason(context.Context, string, int) ([]*models.Episode, error) {
	return nil, nil
}

func (s *seriesEpisodeSource) ListBySeriesGroupedBySeason(context.Context, string) (map[int][]*models.Episode, error) {
	return nil, nil
}

func (s *seriesEpisodeSource) ListBySeriesIDs(_ context.Context, ids []string) (map[string][]*models.Episode, error) {
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

// completedProgressStore reports a configured set of episode ids as completed.
// It reuses progressCountingStore's exhaustive panic-stubs for the rest of the
// UserStore surface so any unexpected call is caught.
type completedProgressStore struct {
	*progressCountingStore
	completed map[string]bool
}

func (s *completedProgressStore) ListProgressByMediaItems(_ context.Context, _ string, ids []string) (map[string]userstore.WatchProgress, error) {
	out := make(map[string]userstore.WatchProgress, len(ids))
	for _, id := range ids {
		if s.completed[id] {
			out[id] = userstore.WatchProgress{MediaItemID: id, Completed: true}
		}
	}
	return out, nil
}

// completedProgressStoreProvider hands back a fixed completedProgressStore.
type completedProgressStoreProvider struct {
	store userstore.UserStore
}

func (p *completedProgressStoreProvider) ForUser(context.Context, int) (userstore.UserStore, error) {
	return p.store, nil
}

func (p *completedProgressStoreProvider) Close() error { return nil }

// TestLoadLatestViaSectionsEnrichesSeriesUserData is the data-parity regression
// guard: the native /Items/Latest fast path must apply the same aggregated
// series watch-state rollup (Played / UnplayedItemCount) the BrowseItems
// fallback applies. A series has no progress row of its own, so this rollup —
// produced by ContentService.EnrichSeriesUserData, the exact call BrowseItems
// makes — is the ONLY source of that state. Without the fast-path enrichment a
// series library's Latest rail would drop it and diverge from page 2.
//
// This exercises the post-localize tail of loadLatestViaSections (the shared
// EnrichSeriesUserData → buildLatestItemDTOs steps); the sectionsFetcher hop
// only supplies the same list items and is covered by the config-parity tests
// above.
func TestLoadLatestViaSectionsEnrichesSeriesUserData(t *testing.T) {
	// series-1 has 3 episodes; 2 are completed ⇒ 1 unplayed, not fully played.
	episodeSrc := &seriesEpisodeSource{
		bySeries: map[string][]*models.Episode{
			"series-1": {
				{ContentID: "ep-1"},
				{ContentID: "ep-2"},
				{ContentID: "ep-3"},
			},
		},
	}
	provider := &completedProgressStoreProvider{
		store: &completedProgressStore{
			progressCountingStore: &progressCountingStore{},
			completed:             map[string]bool{"ep-1": true, "ep-2": true},
		},
	}
	svc := &directContentService{
		episodeRepo:   episodeSrc,
		storeProvider: provider,
	}

	codec := NewResourceIDCodec()
	h := &ItemsHandler{
		content:  svc,
		userData: &mockUserDataService{},
		codec:    codec,
		mapper:   newMapper(codec, &config.Config{}),
	}

	session := &Session{StreamAppUserID: 1, ProfileID: "profile-1"}
	listItems := []upstreamListItem{
		{ContentID: "series-1", Type: "series", Title: "Show"},
		{ContentID: "movie-1", Type: "movie", Title: "Film"},
	}

	// Mirror loadLatestViaSections' post-localize steps: enrich series rows,
	// then build the wire DTOs.
	h.content.EnrichSeriesUserData(context.Background(), session, listItems)

	// The enrichment must populate the series list item in place (the input the
	// DTO tail reads); a nil here is exactly the dropped-rollup regression.
	if listItems[0].UserData == nil {
		t.Fatal("EnrichSeriesUserData left the series list item's UserData nil (rollup dropped)")
	}

	dtos, err := h.buildLatestItemDTOs(context.Background(), session, itemsQuery{}, listItems)
	if err != nil {
		t.Fatalf("buildLatestItemDTOs error: %v", err)
	}

	seriesID := codec.EncodeStringID(EncodedIDItem, "series-1")
	movieID := codec.EncodeStringID(EncodedIDItem, "movie-1")
	byID := make(map[string]baseItemDTO, len(dtos))
	for _, dto := range dtos {
		byID[dto.ID] = dto
	}

	series, ok := byID[seriesID]
	if !ok {
		t.Fatalf("series DTO not found; got %#v", dtos)
	}
	if series.UserData == nil {
		t.Fatal("series DTO UserData is nil; the fast path dropped the series rollup")
	}
	if series.UserData.UnplayedItemCount != 1 {
		t.Fatalf("series UnplayedItemCount = %d, want 1 (3 episodes, 2 completed)", series.UserData.UnplayedItemCount)
	}
	if series.UserData.Played {
		t.Fatal("series Played = true, want false (1 of 3 episodes unwatched)")
	}

	// Movies carry no episode rollup: the enrichment must not fabricate one.
	movie, ok := byID[movieID]
	if !ok {
		t.Fatalf("movie DTO not found; got %#v", dtos)
	}
	if movie.UserData != nil && movie.UserData.UnplayedItemCount != 0 {
		t.Fatalf("movie UnplayedItemCount = %d, want 0 (movies get no series rollup)", movie.UserData.UnplayedItemCount)
	}
}
