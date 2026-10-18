package catalog

import (
	"context"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
)

// resolvePostgresTargets checks an anchor directly, then plays from the
// profile's latest progress in the card's scope: that episode while it is
// unfinished, else the next available episode after it, else the first
// available episode. The latest progress is read once per request from the
// profile's own rows, and the next episode is one ordered index probe, so the
// cost no longer grows with how far the profile has watched a series.
func (r *PlayableTargetResolver) resolvePostgresTargets(ctx context.Context, args []any, fileConditions, keysByOrd []string, progress string) (map[string]PlayableTarget, error) {
	fileSQL := strings.Join(fileConditions, " AND ")
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
		), profile_progress AS MATERIALIZED (
			-- Read the profile's own rows once and join episodes by key, rather
			-- than probing progress for every episode of every displayed series.
			SELECT progress.media_item_id, progress.completed, progress.updated_at
			FROM %[2]s progress
			WHERE progress.position_seconds > 0 OR progress.completed
		), latest AS (
			-- Each card's latest-progress episode. Ties, as a bulk mark-watched
			-- leaves, go to the episode that comes last in play order (specials
			-- after regular seasons).
			SELECT DISTINCT ON (scope.ord) scope.ord, episode.content_id, episode.season_number,
			       episode.episode_number, progress.completed
			FROM profile_progress progress
			JOIN episodes episode ON episode.content_id = progress.media_item_id
			JOIN target_scopes scope ON episode.series_id = scope.series_id
			  AND (scope.season_number IS NULL OR episode.season_number = scope.season_number)
			ORDER BY scope.ord, progress.updated_at DESC,
			  CASE WHEN episode.season_number = 0 THEN 1 ELSE 0 END DESC,
			  episode.season_number DESC, episode.episode_number DESC, episode.content_id DESC
		), anchors AS (
			-- The anchor is the latest-progress *available* episode. Usually that
			-- is the latest row, so only one file is checked per card; a card
			-- whose latest episode has no available file searches its own rows.
			SELECT latest.ord, latest.content_id, latest.season_number, latest.episode_number, latest.completed
			FROM latest
			WHERE EXISTS (SELECT 1 FROM media_files mf WHERE mf.episode_id = latest.content_id AND %[1]s)
			UNION ALL
			SELECT fallback.ord, fallback.content_id, fallback.season_number, fallback.episode_number, fallback.completed
			FROM latest
			CROSS JOIN LATERAL (
				SELECT scope.ord, episode.content_id, episode.season_number, episode.episode_number, progress.completed
				FROM target_scopes scope
				JOIN episodes episode ON episode.series_id = scope.series_id
				  AND (scope.season_number IS NULL OR episode.season_number = scope.season_number)
				JOIN %[2]s progress ON progress.media_item_id = episode.content_id
				WHERE scope.ord = latest.ord
				  AND (progress.position_seconds > 0 OR progress.completed)
				  AND EXISTS (SELECT 1 FROM media_files mf WHERE mf.episode_id = episode.content_id AND %[1]s)
				ORDER BY progress.updated_at DESC,
				  CASE WHEN episode.season_number = 0 THEN 1 ELSE 0 END DESC,
				  episode.season_number DESC, episode.episode_number DESC, episode.content_id DESC
				LIMIT 1
			) fallback
			WHERE NOT EXISTS (SELECT 1 FROM media_files mf WHERE mf.episode_id = latest.content_id AND %[1]s)
		)
		SELECT requested.ord, target.play_content_id, target_episode.season_number
		FROM requested
		LEFT JOIN anchors anchor ON anchor.ord = requested.ord
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
				CASE WHEN NOT anchor.completed THEN anchor.content_id END,
				CASE WHEN anchor.completed THEN %[3]s END,
				%[4]s
			) AS play_content_id
			-- Prevent the outer null filter from inlining and evaluating all
			-- winner subqueries a second time for the SELECT projection.
			OFFSET 0
		) target
		-- One primary-key probe per winner, not per candidate.
		LEFT JOIN episodes target_episode ON target_episode.content_id = target.play_content_id
		WHERE target.play_content_id IS NOT NULL
	`, fileSQL, progress, nextPlayableEpisodeSQL(fileSQL), firstPlayableEpisodeSQL(fileSQL))

	result := make(map[string]PlayableTarget, len(keysByOrd))
	err := r.queryPlayableTargets(ctx, query, args, func(rows pgx.Rows) error {
		var ord int64
		var contentID string
		var season *int
		if err := rows.Scan(&ord, &contentID, &season); err != nil {
			return fmt.Errorf("scanning postgres playable target: %w", err)
		}
		if ord < 1 || ord > int64(len(keysByOrd)) {
			return fmt.Errorf("postgres playable target ordinality %d is outside the requested set", ord)
		}
		result[keysByOrd[ord-1]] = PlayableTarget{ContentID: contentID, SeasonNumber: season}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("resolving postgres playable targets: %w", err)
	}
	return result, nil
}

// firstPlayableEpisodeSQL is the card's first available episode in play order.
func firstPlayableEpisodeSQL(fileSQL string) string {
	return "COALESCE(" + playableEpisodeWinnerSQL(fileSQL, "episode.season_number IS DISTINCT FROM 0", "") + ", " +
		playableEpisodeWinnerSQL(fileSQL, "episode.season_number = 0", "") + ")"
}

// nextPlayableEpisodeSQL is the first available episode after the card's
// anchor in play order: later regular episodes first, then specials. The
// (season, episode) bound lets the episode series index start at the anchor;
// the content ID only orders two scopes that share a season and episode.
func nextPlayableEpisodeSQL(fileSQL string) string {
	regular := playableEpisodeWinnerSQL(fileSQL, "episode.season_number IS DISTINCT FROM 0", `AND anchor.season_number IS DISTINCT FROM 0
				  AND (episode.season_number, episode.episode_number) >= (anchor.season_number, anchor.episode_number)
				  AND (episode.season_number, episode.episode_number, episode.content_id) > (anchor.season_number, anchor.episode_number, anchor.content_id)`)
	specials := playableEpisodeWinnerSQL(fileSQL, "episode.season_number = 0", `AND (anchor.season_number IS DISTINCT FROM 0
				  OR (episode.episode_number, episode.content_id) > (anchor.episode_number, anchor.content_id))`)
	return "COALESCE(" + regular + ", " + specials + ")"
}

// playableEpisodeWinnerSQL takes one ordered winner per scope before comparing
// those winners. A season with stale explicit identity can have two scopes;
// picking each scope's first match preserves the legacy union's global order.
// Separating regular seasons and specials lets the episode series index supply
// the order, so a lookup stops after its first available match.
func playableEpisodeWinnerSQL(fileSQL, seasonSQL, extraSQL string) string {
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
			LIMIT 1)`, seasonSQL, fileSQL, extraSQL)
}
