package jellycompat

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/models"
)

// fakeSeasonByIDRepo implements imageSeasonRepository for the season-parent path.
type fakeSeasonByIDRepo struct{ seasons map[string]*models.Season }

func (f *fakeSeasonByIDRepo) GetByID(_ context.Context, id string) (*models.Season, error) {
	if s, ok := f.seasons[id]; ok {
		return s, nil
	}
	return nil, catalog.ErrSeasonNotFound
}

// fakeSeasonEpisodeRepo implements episodeRepoForBatchLoader, serving episodes by
// (seriesID, seasonNumber) for the season-parent episode listing.
type fakeSeasonEpisodeRepo struct{ bySeason map[string][]*models.Episode }

func episodeBySeasonKey(seriesID string, seasonNum int) string {
	return fmt.Sprintf("%s|%d", seriesID, seasonNum)
}

func (f *fakeSeasonEpisodeRepo) GetByIDs(_ context.Context, contentIDs []string) ([]*models.Episode, error) {
	want := make(map[string]bool, len(contentIDs))
	for _, id := range contentIDs {
		want[id] = true
	}
	var out []*models.Episode
	for _, eps := range f.bySeason {
		for _, ep := range eps {
			if ep != nil && want[ep.ContentID] {
				out = append(out, ep)
			}
		}
	}
	return out, nil
}

func (f *fakeSeasonEpisodeRepo) HasFilesByIDs(context.Context, []string) (map[string]bool, error) {
	return map[string]bool{}, nil
}

func (f *fakeSeasonEpisodeRepo) ListBySeason(_ context.Context, seriesID string, seasonNum int) ([]*models.Episode, error) {
	return f.bySeason[episodeBySeasonKey(seriesID, seasonNum)], nil
}

func (f *fakeSeasonEpisodeRepo) ListBySeriesIDs(ctx context.Context, seriesIDs []string) (map[string][]*models.Episode, error) {
	out := make(map[string][]*models.Episode, len(seriesIDs))
	for _, seriesID := range seriesIDs {
		episodes, err := f.ListBySeries(ctx, seriesID)
		if err != nil {
			return nil, err
		}
		out[seriesID] = episodes
	}
	return out, nil
}

func (f *fakeSeasonEpisodeRepo) ListBySeries(_ context.Context, seriesID string) ([]*models.Episode, error) {
	var out []*models.Episode
	for key, eps := range f.bySeason {
		if strings.HasPrefix(key, seriesID+"|") {
			out = append(out, eps...)
		}
	}
	return out, nil
}

// ListAdjacentInSeries returns the target episode plus its immediate
// previous/next neighbors across the whole series, mirroring the repo's
// natural (season, episode) ordering.
func (f *fakeSeasonEpisodeRepo) ListIDsBySeriesIDs(context.Context, []string) (map[string][]string, error) {
	return nil, nil
}

func (f *fakeSeasonEpisodeRepo) ListIDsBySeasonIDs(context.Context, []string) (map[string][]string, error) {
	return nil, nil
}

func (f *fakeSeasonEpisodeRepo) ListAdjacentInSeriesFor(ctx context.Context, seriesID string, seasonNumber, episodeNumber int, _ catalog.AccessFilter) ([]*models.Episode, error) {
	all, _ := f.ListBySeries(ctx, seriesID)
	sort.SliceStable(all, func(i, j int) bool {
		if all[i].SeasonNumber == all[j].SeasonNumber {
			return all[i].EpisodeNumber < all[j].EpisodeNumber
		}
		return all[i].SeasonNumber < all[j].SeasonNumber
	})
	targetIdx := -1
	for i, ep := range all {
		if ep.SeasonNumber == seasonNumber && ep.EpisodeNumber == episodeNumber {
			targetIdx = i
			break
		}
	}
	if targetIdx < 0 {
		return nil, nil
	}
	start := targetIdx - 1
	if start < 0 {
		start = 0
	}
	end := targetIdx + 2
	if end > len(all) {
		end = len(all)
	}
	return all[start:end], nil
}

// parentFilterCase is one ParentId filter and the number of episodes it must
// list. A filter the parent cannot satisfy lists none.
type parentFilterCase struct {
	filter    string
	wantItems int
}
