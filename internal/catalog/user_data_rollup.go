package catalog

import (
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

// EpisodeRollupUserData computes aggregate watch state for a season or series
// from pre-fetched per-episode progress. Completed history should already be
// folded into progressMap by the caller's userstore helper.
func EpisodeRollupUserData(episodes []*models.Episode, progressMap map[string]userstore.WatchProgress) *SeasonUserData {
	if len(episodes) == 0 {
		return &SeasonUserData{}
	}

	watchedCount := 0
	inProgressCount := 0
	for _, ep := range episodes {
		if ep == nil {
			continue
		}
		progress, ok := progressMap[ep.ContentID]
		if ok && progress.Completed {
			watchedCount++
			continue
		}
		if ok && progress.PositionSeconds > 0 {
			inProgressCount++
		}
	}

	unplayedCount := len(episodes) - watchedCount
	return &SeasonUserData{
		WatchedCount:    watchedCount,
		UnplayedCount:   unplayedCount,
		InProgressCount: inProgressCount,
		Played:          watchedCount == len(episodes),
	}
}
