package catalog

import (
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

// SeasonUserDataFromCounts builds the aggregate watch state DTO from a
// SQL-side rollup (userstore.SeriesEpisodeRollupStore). It must stay
// value-for-value identical to EpisodeRollupUserData over the same episodes.
func SeasonUserDataFromCounts(counts userstore.SeriesWatchCounts) *SeasonUserData {
	if counts.TotalEpisodes == 0 {
		return &SeasonUserData{}
	}
	return &SeasonUserData{
		WatchedCount:    counts.WatchedCount,
		UnplayedCount:   counts.TotalEpisodes - counts.WatchedCount,
		InProgressCount: counts.InProgressCount,
		Played:          counts.WatchedCount == counts.TotalEpisodes,
	}
}

// EpisodeRollupUserData computes aggregate watch state for a season or series
// from pre-fetched per-episode progress. Completed history should already be
// folded into progressMap by the caller's userstore helper.
func EpisodeRollupUserData(episodes []*models.Episode, progressMap map[string]userstore.WatchProgress) *SeasonUserData {
	if len(episodes) == 0 {
		return &SeasonUserData{}
	}

	watchedCount := 0
	inProgressCount := 0
	totalEpisodes := 0
	for _, ep := range episodes {
		if ep == nil {
			continue
		}
		totalEpisodes++
		progress, ok := progressMap[ep.ContentID]
		if ok && progress.Completed {
			watchedCount++
			continue
		}
		if ok && progress.PositionSeconds > 0 {
			inProgressCount++
		}
	}
	if totalEpisodes == 0 {
		return &SeasonUserData{}
	}

	unplayedCount := totalEpisodes - watchedCount
	return &SeasonUserData{
		WatchedCount:    watchedCount,
		UnplayedCount:   unplayedCount,
		InProgressCount: inProgressCount,
		Played:          watchedCount == totalEpisodes,
	}
}
