package catalog

import (
	"context"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
)

// resolvePostgresTargets checks an anchor directly, then tries the latest
// resumable episode, first unwatched episode, and first available episode.
// Each branch returns at most one ID. COALESCE stops after a winner, so a valid
// anchor avoids reading the rest of a long-running series entirely.
func (r *PlayableTargetResolver) resolvePostgresTargets(ctx context.Context, args []any, fileConditions, keysByOrd []string, progress string) (map[string]PlayableTarget, error) {
	fileSQL := strings.Join(fileConditions, " AND ")
	availableSQL := fmt.Sprintf(`EXISTS (SELECT 1 FROM media_files mf WHERE mf.episode_id = episode.content_id AND %s)`, fileSQL)
	// An episode whose ID carries its series' prefix finds a completed row in
	// completed_progress. Any other episode, such as one of a local series,
	// keeps the single-row lookup. CASE tests completion before the file
	// lookup, so a long-running series skips the file lookups for every
	// episode the profile has finished.
	//
	// The set is unnested from an array because PostgreSQL estimates a few rows
	// for unnest of a computed array whatever the profile's history, so it
	// plans a hashed subplan; a large estimate for the set itself turns
	// membership into a rescan of the whole set for every episode. A hashed
	// subplan is not held to work_mem at run time, so the set is hashed only
	// while it fits the memory PostgreSQL grants a hash node; a larger set
	// falls back to the single-row lookup. completedSetBytesPerID is a generous
	// estimate of what one ID costs across the array and the hash table.
	unwatchedSQL := fmt.Sprintf(`CASE WHEN
			CASE WHEN scope.series_id LIKE 'series-%%' AND starts_with(episode.content_id, `+seriesEpisodePrefixSQL+` || '-')
				AND (SELECT count(*) FROM completed_progress completed WHERE completed.completed) * %d
					<= pg_size_bytes(current_setting('work_mem')) * current_setting('hash_mem_multiplier')::float8
			THEN episode.content_id = ANY (SELECT unnest((SELECT array_agg(completed.media_item_id) FROM completed_progress completed WHERE completed.completed)))
			ELSE EXISTS (
				SELECT 1 FROM %s progress
				WHERE progress.media_item_id = episode.content_id AND progress.completed
				-- Keep the unique-key lookup when profile statistics lag a bulk import.
				OFFSET 0
			) END
			THEN false ELSE %s END`, completedSetBytesPerID, progress, availableSQL)
	query := fmt.Sprintf(`
		WITH requested AS (
			SELECT content_id, media_type, series_id, season_number, preferred_content_id, ord
			FROM unnest($1::text[], $2::text[], $3::text[], $4::integer[], $5::text[]) WITH ORDINALITY
			  AS requested(content_id, media_type, series_id, season_number, preferred_content_id, ord)
		), target_scopes AS MATERIALIZED (
			-- Every card's scopes, for the set-wide reads below. MATERIALIZED
			-- keeps resumable from inlining this and deriving every card's
			-- scopes again for each resumable row. Per-card branches read their
			-- own scopes through cardScopesSQL instead of scanning this.
			SELECT requested.ord, scope.series_id, scope.season_number
			FROM requested CROSS JOIN LATERAL `+cardScopesSQL+` scope
		), completed_progress AS MATERIALIZED (
			-- The profile's progress in the requested series, read once; the
			-- lookup keeps the completed rows.
			-- An episode ID embeds its series' anchor
			-- (episode-<provider>-<id>-<season>-<episode>, contentid.ForEpisode),
			-- so each provider-anchored series is one range of the progress
			-- primary key. Walking a long-running series then tests each
			-- episode against this set instead of looking up its progress row.
			-- The fenced LATERAL keeps the read to one range seek per series:
			-- joined freely, the planner may read the profile's whole history
			-- and filter it. Completion is tested at the lookup, not here, so
			-- the completed-history cursor index cannot serve the read; its
			-- leading updated_at cannot bound the range, and each series would
			-- rescan every completed row.
			SELECT progress.media_item_id, progress.completed
			FROM (
				SELECT DISTINCT `+strings.ReplaceAll(seriesEpisodePrefixSQL, "scope.", "")+` AS prefix
				FROM target_scopes WHERE series_id LIKE 'series-%%'
			) series
			CROSS JOIN LATERAL (
				SELECT progress.media_item_id, progress.completed
				FROM %[2]s progress
				WHERE progress.media_item_id >= (series.prefix || '-') COLLATE "C"
				  AND progress.media_item_id < (series.prefix || '.') COLLATE "C"
				-- One key-range seek per series, never a pass over the profile.
				OFFSET 0
			) progress
		), resumable AS MATERIALIZED (
			-- Every card's newest resumable episode, chosen once per statement.
			-- The progress relation's truncated updated_at keeps an index from
			-- supplying this order, so a per-card subquery re-read all of the
			-- profile's resumable rows for each card. MATERIALIZED keeps the
			-- single reference below from inlining it back into every card;
			-- it runs only when a card without a valid anchor first reads it.
			SELECT DISTINCT ON (scope.ord) scope.ord, episode.content_id
			FROM target_scopes scope
			JOIN episodes episode ON episode.series_id = scope.series_id
			  AND (scope.season_number IS NULL OR episode.season_number = scope.season_number)
			JOIN %[2]s progress ON progress.media_item_id = episode.content_id
			WHERE progress.position_seconds > 0 AND NOT progress.completed
			  AND EXISTS (SELECT 1 FROM media_files mf WHERE mf.episode_id = episode.content_id AND %[1]s)
			ORDER BY scope.ord, progress.updated_at DESC,
			  CASE WHEN episode.season_number = 0 THEN 1 ELSE 0 END,
			  episode.season_number, episode.episode_number, episode.content_id
		)
		SELECT requested.ord, target.play_content_id, target_episode.season_number
		FROM requested
		CROSS JOIN LATERAL (
			SELECT COALESCE(
				CASE WHEN requested.media_type = 'movie' AND EXISTS (
					SELECT 1 FROM media_files mf WHERE mf.content_id = requested.content_id AND %[1]s
				) THEN requested.content_id
				WHEN requested.media_type = 'episode' AND (EXISTS (
					SELECT 1 FROM media_files mf WHERE mf.episode_id = requested.content_id AND %[1]s
				) OR `+SpanningEpisodeFileExistsSQL("requested.content_id", "%[1]s")+`) THEN requested.content_id END,
				(SELECT episode.content_id
				 FROM `+cardScopesSQL+` scope
				 JOIN episodes episode ON episode.series_id = scope.series_id
				   AND (scope.season_number IS NULL OR episode.season_number = scope.season_number)
				 WHERE episode.content_id = requested.preferred_content_id
				   AND EXISTS (SELECT 1 FROM media_files mf WHERE mf.episode_id = episode.content_id AND %[1]s)
				 LIMIT 1),
				(SELECT resumable.content_id FROM resumable WHERE resumable.ord = requested.ord),
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
	`, fileSQL, progress, firstPlayableEpisodeSQL(unwatchedSQL), firstPlayableEpisodeSQL(availableSQL))

	result := make(map[string]PlayableTarget, len(keysByOrd))
	// queryPlayableTargets keeps one generic plan per connection. Planning
	// this statement takes 4-7 ms, several times what a show page's single
	// card takes to run. Its generic plan reads the profile's resumable rows
	// once and its completed rows in the requested series once. Only episodes
	// whose IDs lack their series' prefix check completion by key.
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

// completedSetBytesPerID bounds the memory one completed episode ID takes in
// the hashed membership set: the array element, the hash table's tuple and
// bucket, and allocation overhead, for an ID of a few dozen bytes.
const completedSetBytesPerID = 100

// seriesEpisodePrefixSQL is the episode content ID prefix of a provider-anchored
// series (series-<provider>-<id> becomes episode-<provider>-<id>), for a scope
// whose series_id starts with "series-".
const seriesEpisodePrefixSQL = `'episode-' || substr(scope.series_id, 8)`

// firstPlayableEpisodeSQL takes one ordered winner per scope before comparing
// those winners. A season with stale explicit identity can have two scopes;
// picking each scope's first match preserves the legacy union's global order.
// Separating regular seasons and specials lets the episode series index supply
// the order, so ordinary unseen cards stop after their first available match.
// The season is a range bound rather than "IS NULL OR equal" so that the
// episode index applies it and a season card reads only its own season. With
// the OR, each season card of a long-running series read the whole series.
func firstPlayableEpisodeSQL(episodeSQL string) string {
	first := func(seasonSQL string) string {
		return fmt.Sprintf(`(SELECT winner.content_id
			FROM `+cardScopesSQL+` scope
			CROSS JOIN LATERAL (
				SELECT episode.content_id, episode.season_number, episode.episode_number
				FROM episodes episode
				WHERE episode.series_id = scope.series_id
				  AND episode.season_number BETWEEN COALESCE(scope.season_number, -2147483648)
				    AND COALESCE(scope.season_number, 2147483647)
				  AND %s
				  AND %s
				ORDER BY episode.season_number, episode.episode_number, episode.content_id
				LIMIT 1
			) winner
			ORDER BY winner.season_number, winner.episode_number, winner.content_id
			LIMIT 1)`, seasonSQL, episodeSQL)
	}
	return "COALESCE(" + first("episode.season_number IS DISTINCT FROM 0") + ", " + first("episode.season_number = 0") + ")"
}

// cardScopesSQL derives one card's episode scopes from its requested row: a
// series card's whole series, and a season card's season by its explicit
// identity, by its season row, or both when the two disagree. Per-card
// branches read it directly, since scanning target_scopes for each card costs
// cards x scopes.
const cardScopesSQL = `(SELECT requested.content_id AS series_id, NULL::integer AS season_number
		WHERE requested.media_type = 'series'
		UNION ALL
		SELECT requested.series_id, requested.season_number
		WHERE requested.media_type = 'season' AND requested.series_id <> '' AND requested.season_number >= 0
		UNION ALL
		SELECT season.series_id, season.season_number
		FROM seasons season
		WHERE requested.media_type = 'season' AND season.content_id = requested.content_id
		  AND NOT (requested.series_id <> '' AND requested.season_number >= 0
		    AND requested.series_id = season.series_id AND requested.season_number = season.season_number))`
