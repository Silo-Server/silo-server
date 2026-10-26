package catalog

import (
	"context"
	"fmt"
)

// SeasonAvailability counts one season's episodes: how many have aired (by
// the provider's air dates), how many are dated to air later, and how many
// have a file in an enabled library, in all and among the aired ones.
type SeasonAvailability struct {
	Aired     int
	Upcoming  int
	Have      int
	HaveAired int
}

// SeriesSeasonAvailability returns per-season episode counts for each series,
// specials (season 0) excluded. An episode has a file when one in an enabled
// library is linked to it, or when a present multi-episode file of its season
// spans it: such a file links to its first episode only.
func (r *ItemRepository) SeriesSeasonAvailability(ctx context.Context, seriesContentIDs []string) (map[string]map[int]SeasonAvailability, error) {
	out := map[string]map[int]SeasonAvailability{}
	if len(seriesContentIDs) == 0 {
		return out, nil
	}
	rows, err := r.pool.Query(ctx, `
		WITH present AS (
			SELECT el.episode_id AS content_id
			FROM episode_libraries el
			JOIN episodes e ON e.content_id = el.episode_id
			JOIN media_folders f ON f.id = el.media_folder_id AND f.enabled
			WHERE e.series_id = ANY($1) AND e.season_number > 0
			UNION
			SELECT covered.content_id
			FROM episodes first
			JOIN media_files mf ON mf.episode_id = first.content_id
			     AND mf.missing_since IS NULL
			     AND mf.multi_episode_end > mf.multi_episode_start
			JOIN media_folders f ON f.id = mf.media_folder_id AND f.enabled
			JOIN episodes covered ON covered.series_id = first.series_id
			     AND covered.season_number = first.season_number
			     AND covered.episode_number BETWEEN mf.multi_episode_start AND mf.multi_episode_end
			WHERE first.series_id = ANY($1) AND first.season_number > 0
		)
		SELECT e.series_id, e.season_number,
		       count(*) FILTER (WHERE e.air_date <= current_date),
		       count(*) FILTER (WHERE e.air_date > current_date),
		       count(p.content_id),
		       count(p.content_id) FILTER (WHERE e.air_date <= current_date)
		FROM episodes e
		LEFT JOIN present p ON p.content_id = e.content_id
		WHERE e.series_id = ANY($1) AND e.season_number > 0
		GROUP BY e.series_id, e.season_number
	`, seriesContentIDs)
	if err != nil {
		return nil, fmt.Errorf("series season availability: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var series string
		var season int
		var a SeasonAvailability
		if err := rows.Scan(&series, &season, &a.Aired, &a.Upcoming, &a.Have, &a.HaveAired); err != nil {
			return nil, err
		}
		if out[series] == nil {
			out[series] = map[int]SeasonAvailability{}
		}
		out[series][season] = a
	}
	return out, rows.Err()
}
