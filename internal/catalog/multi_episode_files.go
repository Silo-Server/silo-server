package catalog

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// A multi-episode file (S01E01-E02) links to its first episode only:
// media_files.episode_id names that episode and multi_episode_start/end hold
// the range. Every other episode the range spans is served by the same file.
// These statements give such an episode the folder membership and the file a
// direct link would.

// ServedEpisodeFilesSQL selects (episode_id, media_file_id) for every present
// file serving an episode in $1::text[]: a file linked to it, or a
// multi-episode file linked to an earlier episode of its season whose range
// spans it.
const ServedEpisodeFilesSQL = `
	SELECT mf.episode_id, mf.id
	FROM media_files mf
	WHERE mf.episode_id = ANY($1::text[])
	  AND mf.missing_since IS NULL
	UNION
	SELECT covered.content_id, mf.id
	FROM episodes covered
	JOIN episodes first ON first.series_id = covered.series_id
	     AND first.season_number = covered.season_number
	     AND first.episode_number < covered.episode_number
	JOIN media_files mf ON mf.episode_id = first.content_id
	     AND mf.missing_since IS NULL
	     AND mf.multi_episode_end > mf.multi_episode_start
	     AND covered.episode_number BETWEEN mf.multi_episode_start AND mf.multi_episode_end
	WHERE covered.content_id = ANY($1::text[])`

// NoServingEpisodeFileSQL holds for a membership el whose episode has no
// present file in its folder, linked or spanning it.
const NoServingEpisodeFileSQL = `NOT EXISTS (
		SELECT 1
		FROM media_files mf
		WHERE mf.media_folder_id = el.media_folder_id
		  AND mf.episode_id = el.episode_id
		  AND mf.missing_since IS NULL
	)
	AND NOT EXISTS (
		SELECT 1
		FROM episodes covered
		JOIN episodes first ON first.series_id = covered.series_id
		     AND first.season_number = covered.season_number
		     AND first.episode_number < covered.episode_number
		JOIN media_files mf ON mf.episode_id = first.content_id
		     AND mf.media_folder_id = el.media_folder_id
		     AND mf.missing_since IS NULL
		     AND mf.multi_episode_end > mf.multi_episode_start
		     AND covered.episode_number BETWEEN mf.multi_episode_start AND mf.multi_episode_end
		WHERE covered.content_id = el.episode_id
	)`

// InsertSpannedEpisodeMemberships adds the missing memberships of episodes
// spanned by present multi-episode files, after each file's first episode, and
// returns the series of the added rows. filePredicate is an SQL condition on
// media_files mf that selects the files to consider, with args as its
// parameters from $1. Callers recompute the series' membership denorms.
func InsertSpannedEpisodeMemberships(ctx context.Context, tx pgx.Tx, filePredicate string, args ...any) ([]string, error) {
	var seriesIDs []string
	if err := tx.QueryRow(ctx, `
		WITH inserted AS (
			INSERT INTO episode_libraries (
				episode_id, media_folder_id, first_seen_at, first_seen_scan_run_id
			)
			SELECT covered.content_id,
			       mf.media_folder_id,
			       MIN(mf.created_at),
			       (array_agg(mf.first_seen_scan_run_id ORDER BY mf.created_at ASC, mf.id ASC))[1]
			FROM media_files mf
			JOIN episodes first ON first.content_id = mf.episode_id
			JOIN episodes covered ON covered.series_id = first.series_id
			     AND covered.season_number = first.season_number
			     AND covered.episode_number > first.episode_number
			     AND covered.episode_number BETWEEN mf.multi_episode_start AND mf.multi_episode_end
			WHERE `+filePredicate+`
			  AND mf.missing_since IS NULL
			  AND mf.multi_episode_end > mf.multi_episode_start
			GROUP BY covered.content_id, mf.media_folder_id
			ON CONFLICT (episode_id, media_folder_id) DO NOTHING
			RETURNING episode_id
		)
		SELECT COALESCE(array_agg(DISTINCT e.series_id), ARRAY[]::text[])
		FROM inserted i
		JOIN episodes e ON e.content_id = i.episode_id
	`, args...).Scan(&seriesIDs); err != nil {
		return nil, fmt.Errorf("adding spanned episode library membership: %w", err)
	}
	return seriesIDs, nil
}
