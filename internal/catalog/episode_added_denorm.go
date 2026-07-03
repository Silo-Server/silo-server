package catalog

import (
	"context"
	"fmt"
)

// updateSeriesLatestEpisodeAddedSQL fully recomputes the denorm from
// episode_libraries after membership changes; series with no memberships reset
// to NULL.
const updateSeriesLatestEpisodeAddedSQL = `
	UPDATE media_items mi
	SET latest_episode_added_at = sub.latest_added
	FROM (
		SELECT s.series_id, (
			SELECT MAX(el.first_seen_at)
			FROM episode_libraries el
			JOIN episodes e ON e.content_id = el.episode_id
			WHERE e.series_id = s.series_id
		) AS latest_added
		FROM unnest($1::text[]) AS s(series_id)
	) sub
	WHERE mi.content_id = sub.series_id
	  AND mi.type = 'series'
	  AND (mi.latest_episode_added_at IS DISTINCT FROM sub.latest_added)`

// RecomputeSeriesLatestEpisodeAdded refreshes latest_episode_added_at for the
// supplied series IDs after episode library membership changes.
func RecomputeSeriesLatestEpisodeAdded(ctx context.Context, execer itemExecer, seriesIDs []string) error {
	seriesIDs = compactNonEmptyStrings(seriesIDs)
	if len(seriesIDs) == 0 {
		return nil
	}
	if _, err := execer.Exec(ctx, updateSeriesLatestEpisodeAddedSQL, seriesIDs); err != nil {
		return fmt.Errorf("recomputing latest episode added denorm: %w", err)
	}
	return nil
}
