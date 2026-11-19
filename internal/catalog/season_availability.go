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
// specials (season 0) excluded. An episode has a file when a present file in
// an enabled library the viewer can access is linked to it and fits the
// playback-quality ceiling, or when a present multi-episode file of its
// season spans it the way playback serves it (as spannedEpisodeJoinSQL does,
// at most MaxEpisodeSpan episodes after the one it links to). filter's
// library allow/deny, playback-quality ceiling and, on a library-scoped read
// (ScopeFilesToLibrary), presentation library narrow both presence branches
// so a badge never counts holdings the viewer cannot reach there.
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
	// episode_libraries records membership, not a playable file: the file
	// must pass the same access rules, or a 2160p-only episode would count
	// for a 1080p viewer.
	fileConditions := mediaFileAccessConditions("mf", filter, bind)
	if filter.ScopeFilesToLibrary && filter.PresentationLibraryID != nil {
		fileConditions = append(fileConditions, fmt.Sprintf("mf.media_folder_id = $%d", bind(*filter.PresentationLibraryID)))
	}
	fileExtra := ""
	if len(fileConditions) > 0 {
		fileExtra = " AND " + strings.Join(fileConditions, " AND ")
	}

	// The cost follows the episodes of the requested series, not the size of
	// the library. files reads each of those episodes' present files by index:
	// OFFSET 0 keeps the planner from flattening the lateral into hash joins
	// over all of media_files and episode_libraries, and the enabled folders
	// are read once as an array rather than joined per file. One pass feeds both
	// presence branches: linked is the file's own episode (with its folder
	// membership), and a multi-episode file also covers the later episodes of
	// its range, as spannedEpisodeJoinSQL joins them.
	rows, err := r.pool.Query(ctx, fmt.Sprintf(`
		WITH eps AS MATERIALIZED (
			SELECT content_id, series_id, season_number, episode_number, air_date
			FROM episodes
			WHERE series_id = ANY($1) AND season_number > 0
		), files AS MATERIALIZED (
			SELECT e.content_id, e.series_id, e.season_number, e.episode_number,
			       mf.linked, mf.multi_episode_start, mf.multi_episode_end
			FROM eps e
			CROSS JOIN LATERAL (
				SELECT mf.multi_episode_start, mf.multi_episode_end,
				       EXISTS (
				           SELECT 1 FROM episode_libraries el
				           WHERE el.episode_id = mf.episode_id AND el.media_folder_id = mf.media_folder_id
				       ) AS linked
				FROM media_files mf
				WHERE mf.episode_id = e.content_id AND mf.missing_since IS NULL
				  AND mf.media_folder_id = ANY(ARRAY(SELECT id FROM media_folders WHERE enabled))%s
				OFFSET 0
			) mf
		), present AS (
			SELECT fl.content_id FROM files fl WHERE fl.linked
			UNION
			SELECT covered.content_id
			FROM files fl
			JOIN eps covered ON covered.series_id = fl.series_id
			     AND covered.season_number = fl.season_number
			     AND covered.episode_number BETWEEN fl.episode_number + 1 AND fl.episode_number + %d
			     AND covered.episode_number BETWEEN fl.multi_episode_start AND fl.multi_episode_end
			WHERE fl.multi_episode_end > fl.multi_episode_start
		)
		SELECT e.series_id, e.season_number,
		       count(*) FILTER (WHERE e.air_date <= current_date),
		       count(*) FILTER (WHERE e.air_date > current_date),
		       count(*),
		       count(p.content_id),
		       count(p.content_id) FILTER (WHERE e.air_date <= current_date)
		FROM eps e
		LEFT JOIN present p ON p.content_id = e.content_id
		GROUP BY e.series_id, e.season_number
	`, fileExtra, MaxEpisodeSpan), args...)
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
// (episode rows still carry refresh debt), or when a regular season row that
// should list episodes has none: its own air date has passed, or a later
// season has episodes. That covers a GetEpisodes failure that left the flag
// false because refreshSeriesEpisodeMetadataState only inspects existing
// episode rows — without this gate SeriesBadge would treat the loaded seasons
// as the full set and could show Complete. A trailing season with no air
// date or a future one is how a provider lists a renewal before its episodes
// are known, so it does not hide the badge.
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
		  AND (s.air_date <= current_date OR EXISTS (
			SELECT 1 FROM episodes later
			WHERE later.series_id = s.series_id AND later.season_number > s.season_number
		  ))
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
	return Availability{Have: a.Have, Total: a.Total}, true
}

// SeriesBadge is complete released seasons over released seasons. A season
// counts as released once an episode has aired, or, when none of its
// episodes carries an air date, once one is in the library: its own poster
// badges then, so the series must count it. A season dated to air later
// stays out even with an early file. A season is complete when every known
// episode of it is in an enabled library. Specials are already absent from
// the map.
func SeriesBadge(seasons map[int]SeasonAvailability) (Availability, bool) {
	released, complete := 0, 0
	for number, season := range seasons {
		undatedWithFiles := season.Aired == 0 && season.Upcoming == 0 && season.Have > 0
		if number <= 0 || (season.Aired <= 0 && !undatedWithFiles) {
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
