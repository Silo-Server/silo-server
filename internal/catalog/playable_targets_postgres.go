package catalog

import (
	"context"
	"fmt"
	"strings"
)

// resolvePostgresTargets checks an anchor directly, then tries the latest
// resumable episode, first unwatched episode, and first available episode.
// Each branch returns at most one ID. COALESCE stops after a winner, so a valid
// anchor avoids reading the rest of a long-running series entirely.
func (r *PlayableTargetResolver) resolvePostgresTargets(ctx context.Context, args []any, fileConditions, keysByOrd []string, progress string) (map[string]PlayableTarget, error) {
	fileSQL := strings.Join(fileConditions, " AND ")
	completedSQL := fmt.Sprintf(`AND NOT EXISTS (
		SELECT 1 FROM %s progress
		WHERE progress.media_item_id = episode.content_id AND progress.completed
		-- Keep the unique-key lookup when profile statistics lag a bulk import.
		OFFSET 0
	)`, progress)
	query := fmt.Sprintf(`
		WITH requested AS (
			SELECT content_id, media_type, series_id, season_number, preferred_content_id, ord
			FROM unnest($1::text[], $2::text[], $3::text[], $4::integer[], $5::text[]) WITH ORDINALITY
			  AS requested(content_id, media_type, series_id, season_number, preferred_content_id, ord)
		), target_scopes AS (
			SELECT ord, content_id AS series_id, NULL::integer AS season_number
			FROM requested WHERE media_type = 'series'
			UNION ALL
			SELECT ord, series_id, season_number
			FROM requested WHERE media_type = 'season' AND series_id <> '' AND season_number >= 0
			UNION ALL
			SELECT requested.ord, season.series_id, season.season_number
			FROM requested
			JOIN seasons season ON requested.media_type = 'season' AND season.content_id = requested.content_id
			WHERE NOT (requested.series_id <> '' AND requested.season_number >= 0
				AND requested.series_id = season.series_id AND requested.season_number = season.season_number)
		)
		SELECT requested.ord, target.play_content_id, target_episode.season_number
		FROM requested
		CROSS JOIN LATERAL (
			SELECT COALESCE(
				CASE WHEN requested.media_type = 'movie' AND EXISTS (
					SELECT 1 FROM media_files mf WHERE mf.content_id = requested.content_id AND %[1]s
				) THEN requested.content_id
				WHEN requested.media_type = 'episode' AND EXISTS (
					SELECT 1 FROM media_files mf WHERE mf.episode_id = requested.content_id AND %[1]s
				) THEN requested.content_id END,
				(SELECT episode.content_id
				 FROM target_scopes scope
				 JOIN episodes episode ON episode.series_id = scope.series_id
				   AND (scope.season_number IS NULL OR episode.season_number = scope.season_number)
				 WHERE scope.ord = requested.ord AND episode.content_id = requested.preferred_content_id
				   AND EXISTS (SELECT 1 FROM media_files mf WHERE mf.episode_id = episode.content_id AND %[1]s)
				 LIMIT 1),
				(SELECT episode.content_id
				 FROM %[2]s progress
				 JOIN episodes episode ON episode.content_id = progress.media_item_id
				 JOIN target_scopes scope ON scope.ord = requested.ord AND episode.series_id = scope.series_id
				   AND (scope.season_number IS NULL OR episode.season_number = scope.season_number)
				 WHERE progress.position_seconds > 0 AND NOT progress.completed
				   AND EXISTS (SELECT 1 FROM media_files mf WHERE mf.episode_id = episode.content_id AND %[1]s)
				 ORDER BY progress.updated_at DESC,
				   CASE WHEN episode.season_number = 0 THEN 1 ELSE 0 END,
				   episode.season_number, episode.episode_number, episode.content_id
				 LIMIT 1),
				%[3]s,
				%[4]s
			) AS play_content_id
			-- Prevent the outer null filter from inlining and evaluating all
			-- winner subqueries a second time for the SELECT projection.
			OFFSET 0
		) target
		-- One primary-key probe per winner, not per candidate.
		LEFT JOIN episodes target_episode ON target_episode.content_id = target.play_content_id
		WHERE target.play_content_id IS NOT NULL
	`, fileSQL, progress, firstPlayableEpisodeSQL(fileSQL, completedSQL), firstPlayableEpisodeSQL(fileSQL, ""))

	rows, err := r.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("resolving postgres playable targets: %w", err)
	}
	defer rows.Close()
	result := make(map[string]PlayableTarget, len(keysByOrd))
	for rows.Next() {
		var ord int64
		var contentID string
		var season *int
		if err := rows.Scan(&ord, &contentID, &season); err != nil {
			return nil, fmt.Errorf("scanning postgres playable target: %w", err)
		}
		if ord < 1 || ord > int64(len(keysByOrd)) {
			return nil, fmt.Errorf("postgres playable target ordinality %d is outside the requested set", ord)
		}
		result[keysByOrd[ord-1]] = PlayableTarget{ContentID: contentID, SeasonNumber: season}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating postgres playable targets: %w", err)
	}
	return result, nil
}

// firstPlayableEpisodeSQL takes one ordered winner per scope before comparing
// those winners. A season with stale explicit identity can have two scopes;
// picking each scope's first match preserves the legacy union's global order.
// Separating regular seasons and specials lets the episode series index supply
// the order, so ordinary unseen cards stop after their first available match.
func firstPlayableEpisodeSQL(fileSQL, progressSQL string) string {
	first := func(seasonSQL string) string {
		return fmt.Sprintf(`(SELECT winner.content_id
			FROM target_scopes scope
			CROSS JOIN LATERAL (
				SELECT episode.content_id, episode.season_number, episode.episode_number
				FROM episodes episode
				WHERE episode.series_id = scope.series_id
				  AND (scope.season_number IS NULL OR episode.season_number = scope.season_number)
				  AND %s
				  AND EXISTS (SELECT 1 FROM media_files mf WHERE mf.episode_id = episode.content_id AND %s)
				  %s
				ORDER BY episode.season_number, episode.episode_number, episode.content_id
				LIMIT 1
			) winner
			WHERE scope.ord = requested.ord
			ORDER BY winner.season_number, winner.episode_number, winner.content_id
			LIMIT 1)`, seasonSQL, fileSQL, progressSQL)
	}
	return "COALESCE(" + first("episode.season_number IS DISTINCT FROM 0") + ", " + first("episode.season_number = 0") + ")"
}
