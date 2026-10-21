package catalog

import (
	"context"
	"fmt"
	"strings"
)

// SeasonAvailability counts one season's episodes: how many have aired (by
// the provider's air dates), how many are dated to air later, how many are
// known at all, and how many have a file in an enabled library, in all and
// among the aired ones.
type SeasonAvailability struct {
	Aired     int
	Upcoming  int
	Total     int
	Have      int
	HaveAired int
}

// Availability is the poster badge for one series or season. Have is what is
// in the library; Total is released seasons on a series, or known episodes on
// a season.
type Availability struct {
	Have  int
	Total int
}

// SeriesSeasonAvailability returns per-season episode counts for each series,
// specials (season 0) excluded. An episode has a file when one in an enabled
// library the viewer can access is linked to it, or when a present
// multi-episode file of its season spans it: such a file links to its first
// episode only. filter's library allow/deny and playback-quality ceiling
// narrow both presence branches so a badge never counts holdings the viewer
// cannot reach.
func (r *ItemRepository) SeriesSeasonAvailability(ctx context.Context, seriesContentIDs []string, filter AccessFilter) (map[string]map[int]SeasonAvailability, error) {
	out := map[string]map[int]SeasonAvailability{}
	if r == nil || r.pool == nil || len(seriesContentIDs) == 0 {
		return out, nil
	}
	if filter.AllowedLibraryIDs != nil && len(filter.AllowedLibraryIDs) == 0 {
		return out, nil
	}

	args := []any{seriesContentIDs}
	bind := func(value any) int {
		args = append(args, value)
		return len(args)
	}
	elLibrary := make([]string, 0, 2)
	mfLibrary := make([]string, 0, 3)
	if filter.AllowedLibraryIDs != nil {
		idx := bind(filter.AllowedLibraryIDs)
		elLibrary = append(elLibrary, fmt.Sprintf("el.media_folder_id = ANY($%d)", idx))
		mfLibrary = append(mfLibrary, fmt.Sprintf("mf.media_folder_id = ANY($%d)", idx))
	}
	if len(filter.DisabledLibraryIDs) > 0 {
		idx := bind(filter.DisabledLibraryIDs)
		elLibrary = append(elLibrary, fmt.Sprintf("NOT (el.media_folder_id = ANY($%d))", idx))
		mfLibrary = append(mfLibrary, fmt.Sprintf("NOT (mf.media_folder_id = ANY($%d))", idx))
	}
	if ceiling := MediaFileQualityCeilingSQL("mf", filter.MaxPlaybackQuality); ceiling != "" {
		mfLibrary = append(mfLibrary, ceiling)
	}
	elExtra := ""
	if len(elLibrary) > 0 {
		elExtra = " AND " + strings.Join(elLibrary, " AND ")
	}
	mfExtra := ""
	if len(mfLibrary) > 0 {
		mfExtra = " AND " + strings.Join(mfLibrary, " AND ")
	}

	rows, err := r.pool.Query(ctx, fmt.Sprintf(`
		WITH present AS (
			SELECT el.episode_id AS content_id
			FROM episode_libraries el
			JOIN episodes e ON e.content_id = el.episode_id
			JOIN media_folders f ON f.id = el.media_folder_id AND f.enabled
			WHERE e.series_id = ANY($1) AND e.season_number > 0%s
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
			WHERE first.series_id = ANY($1) AND first.season_number > 0%s
		)
		SELECT e.series_id, e.season_number,
		       count(*) FILTER (WHERE e.air_date <= current_date),
		       count(*) FILTER (WHERE e.air_date > current_date),
		       count(*),
		       count(p.content_id),
		       count(p.content_id) FILTER (WHERE e.air_date <= current_date)
		FROM episodes e
		LEFT JOIN present p ON p.content_id = e.content_id
		WHERE e.series_id = ANY($1) AND e.season_number > 0
		GROUP BY e.series_id, e.season_number
	`, elExtra, mfExtra), args...)
	if err != nil {
		return nil, fmt.Errorf("series season availability: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var series string
		var season int
		var a SeasonAvailability
		if err := rows.Scan(&series, &season, &a.Aired, &a.Upcoming, &a.Total, &a.Have, &a.HaveAired); err != nil {
			return nil, err
		}
		if out[series] == nil {
			out[series] = map[int]SeasonAvailability{}
		}
		out[series][season] = a
	}
	return out, rows.Err()
}

// EpisodeMetadataIncomplete reports series whose episode list is not a
// reliable picture of what the provider says exists. A poster badge built
// from that list would call a partial season complete.
//
// A series is incomplete when media_items.episode_metadata_incomplete is set
// (episode rows still carry refresh debt), or when a regular season row has
// no episode rows. The second case covers a GetEpisodes failure that left the
// flag false because refreshSeriesEpisodeMetadataState only inspects existing
// episode rows — without this gate SeriesBadge would treat the loaded seasons
// as the full set and could show Complete.
func (r *ItemRepository) EpisodeMetadataIncomplete(ctx context.Context, seriesContentIDs []string) (map[string]bool, error) {
	out := map[string]bool{}
	if r == nil || r.pool == nil || len(seriesContentIDs) == 0 {
		return out, nil
	}
	rows, err := r.pool.Query(ctx, `
		SELECT content_id FROM media_items
		WHERE content_id = ANY($1) AND episode_metadata_incomplete
		UNION
		SELECT s.series_id
		FROM seasons s
		WHERE s.series_id = ANY($1)
		  AND s.season_number > 0
		  AND NOT EXISTS (
			SELECT 1 FROM episodes e
			WHERE e.series_id = s.series_id AND e.season_number = s.season_number
		  )
	`, seriesContentIDs)
	if err != nil {
		return nil, fmt.Errorf("episode metadata incomplete: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out[id] = true
	}
	return out, rows.Err()
}

// SeasonBadge is the episode fraction for one season poster. A season that
// has not aired and has no files stays blank. Specials never reach this
// function: the availability query excludes season 0.
func SeasonBadge(a SeasonAvailability) (Availability, bool) {
	if a.Total <= 0 || (a.Aired == 0 && a.Have == 0) {
		return Availability{}, false
	}
	have := a.Have
	if have > a.Total {
		have = a.Total
	}
	return Availability{Have: have, Total: a.Total}, true
}

// SeriesBadge is complete released seasons over released seasons. A season
// counts as released once an episode has aired, and complete when every
// known episode of it is in an enabled library. Specials are already absent
// from the map.
func SeriesBadge(seasons map[int]SeasonAvailability) (Availability, bool) {
	released, complete := 0, 0
	for number, season := range seasons {
		if number <= 0 || season.Aired <= 0 {
			continue
		}
		released++
		if season.Total > 0 && season.Have >= season.Total {
			complete++
		}
	}
	if released == 0 {
		return Availability{}, false
	}
	return Availability{Have: complete, Total: released}, true
}
