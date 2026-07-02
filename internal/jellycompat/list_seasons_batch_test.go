package jellycompat

import (
	"context"
	"testing"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/models"
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

// TestListSeasons_UsesBatchEpisodeFetch verifies ListSeasons makes exactly
// one ListBySeriesGroupedBySeason call (replacing N per-season ListBySeason
// calls) and exactly one ListProgressByMediaItems call (replacing N
// per-season fetches inside enrichSeasonUserData) (audit 2026-05-01 §2.2).
func TestListSeasons_UsesBatchEpisodeFetch(t *testing.T) {
	provider := newProgressCountingStoreProvider()
	episodeRepo := &countingEpisodeListSource{
		groupedReturn: map[int][]*models.Episode{
			1: {
				{ContentID: "e1", SeriesID: "series-abc", SeasonNumber: 1, EpisodeNumber: 1},
				{ContentID: "e2", SeriesID: "series-abc", SeasonNumber: 1, EpisodeNumber: 2},
			},
			2: {
				{ContentID: "e3", SeriesID: "series-abc", SeasonNumber: 2, EpisodeNumber: 1},
			},
		},
	}
	seasonRepo := &stubSeasonListSource{
		seasons: []*models.Season{
			{ContentID: "s1", SeriesID: "series-abc", SeasonNumber: 1},
			{ContentID: "s2", SeriesID: "series-abc", SeasonNumber: 2},
		},
	}
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
		t.Fatalf("expected 2 seasons in result; got %d", len(result))
	}

	if episodeRepo.listBySeasonCalls != 0 {
		t.Fatalf("ListSeasons must not call ListBySeason on the batched path; got %d calls",
			episodeRepo.listBySeasonCalls)
	}
	if episodeRepo.listGroupedCalls != 1 {
		t.Fatalf("ListSeasons should call ListBySeriesGroupedBySeason exactly once; got %d",
			episodeRepo.listGroupedCalls)
	}
	if provider.store.listProgressCalls != 1 {
		t.Fatalf("ListSeasons should batch into 1 ListProgressByMediaItems call; got %d",
			provider.store.listProgressCalls)
	}
	// The single batch must include every episode from every season.
	if got, want := len(provider.store.lastListedMediaItemIDs), 3; got != want {
		t.Fatalf("expected single progress batch to cover all %d episodes; got %d ids",
			want, got)
	}
}

// TestListSeasons_FallbackUsesGroupedEpisodes verifies that when the
// seasons table is empty, ListSeasons still uses the single grouped fetch
// and derives synthetic seasons without issuing an extra aggregation
// query (audit 2026-05-01 §2.2 fallback path).
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
