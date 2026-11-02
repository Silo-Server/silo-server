package jellycompat

import (
	"context"
	"errors"
	"testing"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

// stubItemAccessSource always permits access. The Search/GetByIDs methods are
// not exercised by ListSeasons.
type stubItemAccessSource struct{}

func (stubItemAccessSource) EnsureAccessible(_ context.Context, _ string, _ catalog.AccessFilter) error {
	return nil
}

func (stubItemAccessSource) Search(_ context.Context, _ string, _ []string, _, _ int, _ catalog.AccessFilter) ([]*models.MediaItem, int, error) {
	panic("unused")
}

func (stubItemAccessSource) GetByIDs(_ context.Context, _ []string) ([]*models.MediaItem, error) {
	panic("unused")
}

// stubSeasonListSource returns a fixed slice from ListBySeries. Other
// methods panic so the test catches unexpected use.
type stubSeasonListSource struct {
	seasons []*models.Season
}

func (s *stubSeasonListSource) ListBySeries(_ context.Context, _ string) ([]*models.Season, error) {
	return s.seasons, nil
}

func (s *stubSeasonListSource) GetBySeriesAndNumber(_ context.Context, _ string, _ int) (*models.Season, error) {
	panic("unused")
}

func (s *stubSeasonListSource) GetByID(_ context.Context, _ string) (*models.Season, error) {
	panic("unused")
}

// countingEpisodeListSource counts calls to ListBySeason and
// ListBySeriesGroupedBySeason. ListBySeason should be zero in the batched
// ListSeasons path.
type countingEpisodeListSource struct {
	listBySeasonCalls int
	listGroupedCalls  int
	groupedReturn     map[int][]*models.Episode
}

func (c *countingEpisodeListSource) ListBySeason(_ context.Context, seriesID string, seasonNum int) ([]*models.Episode, error) {
	c.listBySeasonCalls++
	return c.groupedReturn[seasonNum], nil
}

func (c *countingEpisodeListSource) ListBySeriesGroupedBySeason(_ context.Context, _ string) (map[int][]*models.Episode, error) {
	c.listGroupedCalls++
	out := make(map[int][]*models.Episode, len(c.groupedReturn))
	for k, v := range c.groupedReturn {
		out[k] = v
	}
	return out, nil
}

func (c *countingEpisodeListSource) ListBySeriesIDs(_ context.Context, _ []string) (map[string][]*models.Episode, error) {
	return map[string][]*models.Episode{}, nil
}

func (c *countingEpisodeListSource) GetByIDs(_ context.Context, _ []string) ([]*models.Episode, error) {
	return nil, nil
}

// TestListSeasons_FallbackUsesGroupedEpisodes verifies that with a store
// without the SQL rollup and an empty seasons table, ListSeasons uses the
// single grouped fetch and derives synthetic seasons from it (audit
// 2026-05-01 §2.2 fallback path).
func TestListSeasons_FallbackUsesGroupedEpisodes(t *testing.T) {
	provider := newProgressCountingStoreProvider()
	episodeRepo := &countingEpisodeListSource{
		groupedReturn: map[int][]*models.Episode{
			1: {
				{ContentID: "e1", SeriesID: "series-abc", SeasonNumber: 1, EpisodeNumber: 1},
			},
			2: {
				{ContentID: "e2", SeriesID: "series-abc", SeasonNumber: 2, EpisodeNumber: 1},
				{ContentID: "e3", SeriesID: "series-abc", SeasonNumber: 2, EpisodeNumber: 2},
			},
		},
	}
	// seasons table empty triggers the fallback path.
	seasonRepo := &stubSeasonListSource{seasons: nil}
	svc := &directContentService{
		itemRepo:      stubItemAccessSource{},
		seasonRepo:    seasonRepo,
		episodeRepo:   episodeRepo,
		storeProvider: provider,
	}

	session := &Session{StreamAppUserID: 1, ProfileID: "profile-1"}
	result, err := svc.ListSeasons(context.Background(), session, "series-abc", nil)
	if err != nil {
		t.Fatalf("ListSeasons returned error: %v", err)
	}
	if len(result) != 2 {
		t.Fatalf("expected 2 synthetic seasons; got %d", len(result))
	}
	// Seasons must be returned in ascending season-number order.
	if result[0].SeasonNumber != 1 || result[1].SeasonNumber != 2 {
		t.Fatalf("expected seasons in ascending order; got %d, %d",
			result[0].SeasonNumber, result[1].SeasonNumber)
	}
	// Episode counts derived from grouped, no per-season ListBySeason fan-out.
	if result[0].EpisodeCount != 1 || result[1].EpisodeCount != 2 {
		t.Fatalf("synthetic season EpisodeCount mismatch; got %d, %d",
			result[0].EpisodeCount, result[1].EpisodeCount)
	}
	if episodeRepo.listBySeasonCalls != 0 {
		t.Fatalf("fallback path must not call ListBySeason; got %d", episodeRepo.listBySeasonCalls)
	}
	if episodeRepo.listGroupedCalls != 1 {
		t.Fatalf("fallback path should call ListBySeriesGroupedBySeason exactly once; got %d",
			episodeRepo.listGroupedCalls)
	}
	if provider.store.listProgressCalls != 1 {
		t.Fatalf("fallback path should batch into 1 ListProgressByMediaItems call; got %d",
			provider.store.listProgressCalls)
	}
}

// TestListSeasons_UsesSeasonRollup verifies that a store with the SQL season
// rollup answers every season's EpisodeCount and UserData from one aggregate,
// without listing the series' episodes or reading their progress. A failed
// rollup on a live request falls back to the grouped episode fold; on a
// canceled request it returns the error instead.
func TestListSeasons_UsesSeasonRollup(t *testing.T) {
	// The grouped episodes disagree with the rollup on purpose, so the counts
	// show which path answered.
	grouped := map[int][]*models.Episode{
		1: {{ContentID: "e1", SeriesID: "series-abc", SeasonNumber: 1, EpisodeNumber: 1}},
	}
	seasonCounts := map[int]userstore.SeriesWatchCounts{
		0: {TotalEpisodes: 2, WatchedCount: 2},
		1: {TotalEpisodes: 10, WatchedCount: 3, InProgressCount: 1},
	}
	seasonRows := []*models.Season{
		{ContentID: "season-0", SeasonNumber: 0},
		{ContentID: "season-1", SeasonNumber: 1},
		{ContentID: "season-2", SeasonNumber: 2},
	}
	type wantSeason struct {
		number   int
		episodes int
		userData catalog.SeasonUserData
	}
	fromRollup := []wantSeason{
		{0, 2, catalog.SeasonUserData{WatchedCount: 2, Played: true}},
		{1, 10, catalog.SeasonUserData{WatchedCount: 3, UnplayedCount: 7, InProgressCount: 1}},
		// No available episodes: absent from the rollup, empty like the fold.
		{2, 0, catalog.SeasonUserData{}},
	}
	rollupErr := errors.New("rollup unavailable")
	for _, tc := range []struct {
		name         string
		seasons      []*models.Season
		err          error
		cancel       bool
		want         []wantSeason
		wantFallback bool
		wantErr      bool
	}{
		{name: "season rows", seasons: seasonRows, want: fromRollup},
		{name: "synthetic seasons", want: fromRollup[:2]},
		{name: "rollup failure", seasons: seasonRows, err: rollupErr, wantFallback: true, want: []wantSeason{
			{0, 0, catalog.SeasonUserData{}},
			{1, 1, catalog.SeasonUserData{UnplayedCount: 1}},
			{2, 0, catalog.SeasonUserData{}},
		}},
		{name: "canceled request", seasons: seasonRows, err: context.Canceled, cancel: true, wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := &seriesRollupCountingStore{progressCountingStore: &progressCountingStore{}, seasonCounts: seasonCounts, err: tc.err}
			episodeRepo := &countingEpisodeListSource{groupedReturn: grouped}
			svc := &directContentService{
				itemRepo:      stubItemAccessSource{},
				seasonRepo:    &stubSeasonListSource{seasons: tc.seasons},
				episodeRepo:   episodeRepo,
				storeProvider: &completedProgressStoreProvider{store: store},
			}
			ctx, cancel := context.WithCancel(t.Context())
			if tc.cancel {
				cancel()
			}
			defer cancel()

			result, err := svc.ListSeasons(ctx, &Session{StreamAppUserID: 1, ProfileID: "profile-1"}, "series-abc", nil)
			if (err != nil) != tc.wantErr {
				t.Fatalf("ListSeasons error = %v, want error %v", err, tc.wantErr)
			}
			if store.seasonRollupCalls != 1 {
				t.Fatalf("season rollup calls = %d, want 1", store.seasonRollupCalls)
			}
			fallbackCalls := 0
			if tc.wantFallback {
				fallbackCalls = 1
			}
			if episodeRepo.listGroupedCalls != fallbackCalls || store.listProgressCalls != fallbackCalls {
				t.Fatalf("grouped episode lists = %d, progress reads = %d, want %d each",
					episodeRepo.listGroupedCalls, store.listProgressCalls, fallbackCalls)
			}
			if len(result) != len(tc.want) {
				t.Fatalf("got %d seasons, want %d", len(result), len(tc.want))
			}
			for i, want := range tc.want {
				got := result[i]
				if got.SeasonNumber != want.number || got.EpisodeCount != want.episodes || got.UserData == nil || *got.UserData != want.userData {
					t.Errorf("season %d = number %d, %d episodes, UserData %+v; want number %d, %d episodes, UserData %+v",
						i, got.SeasonNumber, got.EpisodeCount, got.UserData, want.number, want.episodes, want.userData)
				}
			}
		})
	}
}
