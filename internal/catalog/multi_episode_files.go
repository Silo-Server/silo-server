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

// MaxEpisodeSpan bounds how many episodes past its first one a multi-episode
// file serves, so a lookup by episode searches only a few earlier episodes.
// Watch history import expands a multi-episode play over the same bound.
const MaxEpisodeSpan = 10

// spannedEpisodeJoinSQL joins episodes "covered" to the present multi-episode
// files "mf" spanning it, linked to an earlier episode "first" of its season.
var spannedEpisodeJoinSQL = fmt.Sprintf(`
	JOIN episodes first ON first.series_id = covered.series_id
	     AND first.season_number = covered.season_number
	     AND first.episode_number BETWEEN covered.episode_number - %d AND covered.episode_number - 1
	JOIN media_files mf ON mf.episode_id = first.content_id
	     AND mf.missing_since IS NULL
	     AND mf.multi_episode_end > mf.multi_episode_start
	     AND covered.episode_number BETWEEN mf.multi_episode_start AND mf.multi_episode_end`, MaxEpisodeSpan)

// ServedEpisodeFilesSQL selects (episode_id, media_file_id) for every present
// file serving an episode in $1::text[]: a file linked to it, or a
// multi-episode file spanning it.
var ServedEpisodeFilesSQL = `
	SELECT mf.episode_id, mf.id
	FROM media_files mf
	WHERE mf.episode_id = ANY($1::text[])
	  AND mf.missing_since IS NULL
	UNION
	SELECT covered.content_id, mf.id
	FROM episodes covered` + spannedEpisodeJoinSQL + `
	WHERE covered.content_id = ANY($1::text[])`

// NoServingEpisodeFileSQL holds for a membership el whose episode has no
// present file in its folder, linked or spanning it.
var NoServingEpisodeFileSQL = `NOT EXISTS (
		SELECT 1
		FROM media_files mf
		WHERE mf.media_folder_id = el.media_folder_id
		  AND mf.episode_id = el.episode_id
		  AND mf.missing_since IS NULL
	)
	AND NOT EXISTS (
		SELECT 1
		FROM episodes covered` + spannedEpisodeJoinSQL + `
		WHERE covered.content_id = el.episode_id
		  AND mf.media_folder_id = el.media_folder_id
	)`

// EpisodeHasServingFileSQL holds for an episode e with a present file, linked
// or spanning it.
var EpisodeHasServingFileSQL = `(EXISTS (
		SELECT 1 FROM media_files mf
		WHERE mf.episode_id = e.content_id AND mf.missing_since IS NULL
	) OR EXISTS (
		SELECT 1
		FROM episodes covered` + spannedEpisodeJoinSQL + `
		WHERE covered.content_id = e.content_id
	))`

// SpanningEpisodeFileExistsSQL holds when a present multi-episode file linked
// to an earlier episode spans the episode whose content ID is episodeExpr, and
// that file meets fileCondition, an SQL condition on media_files mf.
func SpanningEpisodeFileExistsSQL(episodeExpr, fileCondition string) string {
	return `EXISTS (
		SELECT 1
		FROM episodes covered` + spannedEpisodeJoinSQL + `
		WHERE covered.content_id = ` + episodeExpr + `
		  AND ` + fileCondition + `
	)`
}

// SpannedEpisodesSQL selects (content_id, season_number, episode_number) of
// the later episodes that the present multi-episode files matching
// filePredicate, an SQL condition on media_files mf, serve.
func SpannedEpisodesSQL(filePredicate string) string {
	return `SELECT DISTINCT covered.content_id, covered.season_number, covered.episode_number
		FROM episodes covered` + spannedEpisodeJoinSQL + `
		WHERE ` + filePredicate
}

// SpanReachSQL selects the episodes in the text[] parameter param and the ones
// a multi-episode file linked to one of them could span. Membership cleanup
// after a file changes checks them all.
func SpanReachSQL(param string) string {
	return fmt.Sprintf(`(
		SELECT later.content_id
		FROM episodes listed
		JOIN episodes later ON later.series_id = listed.series_id
		     AND later.season_number = listed.season_number
		     AND later.episode_number BETWEEN listed.episode_number AND listed.episode_number + %d
		WHERE listed.content_id = ANY(%s)
		UNION
		SELECT unnest(%s)
	)`, MaxEpisodeSpan, param, param)
}

// InsertSpannedEpisodeMemberships adds the missing memberships of episodes
// spanned by present multi-episode files, after each file's first episode, and
// returns the series of the added rows. filePredicate is an SQL condition on
// media_files mf that selects the files to consider, with args as its
// parameters from $1. Callers recompute the series' membership denorms.
//
// Curated rebind and split lock a series row before changing its memberships,
// so this locks the candidate series first, in content ID order, and inserts in
// a later statement.
func InsertSpannedEpisodeMemberships(ctx context.Context, tx pgx.Tx, filePredicate string, args ...any) ([]string, error) {
	candidateSeriesIDs, err := spannedMembershipCandidateSeries(ctx, tx, filePredicate, args...)
	if err != nil {
		return nil, err
	}
	if len(candidateSeriesIDs) == 0 {
		return nil, nil
	}
	if _, err := tx.Exec(ctx, lockSeriesAirDatesSQL, candidateSeriesIDs); err != nil {
		return nil, fmt.Errorf("locking series for spanned episode library membership: %w", err)
	}

	var seriesIDs []string
	if err := tx.QueryRow(ctx, `
		WITH inserted AS (
			INSERT INTO episode_libraries (
				episode_id, media_folder_id, first_seen_at, first_seen_scan_run_id
			)
			SELECT covered.content_id,
			       mf.media_folder_id,
			       MIN(mf.created_at),
			       (array_agg(mf.first_seen_scan_run_id ORDER BY mf.created_at ASC, mf.id ASC))[1]`+missingSpannedMembershipSQL(filePredicate)+`
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

// missingSpannedMembershipSQL is the FROM and WHERE clauses selecting the
// spanned episodes of the files matching filePredicate that have no
// membership in the file's folder yet.
func missingSpannedMembershipSQL(filePredicate string) string {
	return `
		FROM episodes covered` + spannedEpisodeJoinSQL + `
		WHERE ` + filePredicate + `
		  AND NOT EXISTS (
			SELECT 1 FROM episode_libraries el
			WHERE el.episode_id = covered.content_id
			  AND el.media_folder_id = mf.media_folder_id
		  )`
}

// spannedMembershipCandidateSeries returns the series that
// InsertSpannedEpisodeMemberships would add memberships to.
func spannedMembershipCandidateSeries(ctx context.Context, tx pgx.Tx, filePredicate string, args ...any) ([]string, error) {
	var seriesIDs []string
	if err := tx.QueryRow(ctx, `
		SELECT COALESCE(array_agg(DISTINCT covered.series_id), ARRAY[]::text[])`+missingSpannedMembershipSQL(filePredicate), args...).Scan(&seriesIDs); err != nil {
		return nil, fmt.Errorf("finding spanned episode library membership: %w", err)
	}
	return seriesIDs, nil
}
