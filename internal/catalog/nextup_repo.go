package catalog

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/userstore"
)

// NextUpQuery controls what the next-up lookup returns.
type NextUpQuery struct {
	UserID           int
	ProfileID        string
	LibraryID        *int
	LibraryIDs       []int
	SeriesID         string // optional: filter to single series
	AccessFilter     AccessFilter
	Limit            int
	EnableResumable  bool       // include in-progress episodes
	EnableRewatching bool       // accepted but deferred (no-op)
	DateCutoff       *time.Time // only series with activity after this date
}

// NextUpResult is one row from the next-up query.
type NextUpResult struct {
	ContentID     string
	SeriesID      string
	SeriesTitle   string
	SeasonNumber  int
	EpisodeNumber int
	CompletedAt   time.Time // when the preceding episode was completed
	IsResumable   bool      // true if this is an in-progress item (enableResumable)
}

// NextUpRepository queries for next unwatched episodes per series.
type NextUpRepository struct {
	pool          *pgxpool.Pool
	storeProvider userstore.UserStoreProvider
}

// NewNextUpRepository creates a NextUpRepository.
func NewNextUpRepository(pool *pgxpool.Pool, storeProvider userstore.UserStoreProvider) *NextUpRepository {
	return &NextUpRepository{pool: pool, storeProvider: storeProvider}
}

// ListNextUp returns the next unwatched episode per series for the given user.
func (r *NextUpRepository) ListNextUp(ctx context.Context, q NextUpQuery) ([]NextUpResult, error) {
	if r.storeProvider == nil || q.UserID <= 0 || q.ProfileID == "" {
		return nil, nil
	}

	limit := q.Limit
	if limit <= 0 {
		limit = 20
	}

	query, args := buildListNextUpQuery(q, limit)

	rows, err := r.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("querying next-up episodes: %w", err)
	}
	defer rows.Close()

	var results []NextUpResult
	for rows.Next() {
		var res NextUpResult
		if err := rows.Scan(
			&res.ContentID, &res.SeriesID, &res.SeriesTitle,
			&res.SeasonNumber, &res.EpisodeNumber, &res.CompletedAt,
		); err != nil {
			return nil, fmt.Errorf("scanning next-up row: %w", err)
		}
		results = append(results, res)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating next-up rows: %w", err)
	}

	if q.EnableResumable {
		resumable, rErr := r.listResumableFirstEpisodes(ctx, q)
		if rErr != nil {
			return nil, rErr
		}

		if q.SeriesID != "" && len(resumable) > 0 {
			// Show-detail tile: the in-progress episode wins over whatever
			// "next aired" row the completed-episodes branch produced.
			// The user is mid-watching this episode; surfacing the
			// following one would skip past their actual position.
			return resumable, nil
		}

		// Global next-up: dedup by series, completed-next row takes priority.
		seen := make(map[string]bool, len(results))
		for _, res := range results {
			seen[res.SeriesID] = true
		}
		for _, res := range resumable {
			if !seen[res.SeriesID] {
				results = append(results, res)
			}
		}
	}

	return results, nil
}

func buildListNextUpQuery(q NextUpQuery, limit int) (string, []interface{}) {
	args := []interface{}{q.UserID, q.ProfileID, limit}
	argIdx := 4

	seriesFilter := ""
	if q.SeriesID != "" {
		seriesFilter = fmt.Sprintf(" AND e.series_id = $%d", argIdx)
		args = append(args, q.SeriesID)
		argIdx++
	}

	dateCutoffFilter := ""
	if q.DateCutoff != nil {
		dateCutoffFilter = fmt.Sprintf(" AND uwp.updated_at >= $%d", argIdx)
		args = append(args, *q.DateCutoff)
		argIdx++
	}

	// When resumable items are disabled, suppress next-up only if the series has
	// newer in-progress activity than the most recent completed episode. Older
	// partial watches should not block the user's current progression.
	inProgressExclusion := ""
	if !q.EnableResumable {
		inProgressExclusion = `
		,
		eligible_series AS (
			SELECT ce.*
			FROM completed_episodes ce
			WHERE NOT EXISTS (
				SELECT 1 FROM user_watch_progress uwp_ip
				JOIN episodes e_ip ON e_ip.content_id = uwp_ip.media_item_id
				WHERE uwp_ip.user_id = $1
				  AND uwp_ip.profile_id = $2
				  AND uwp_ip.completed = FALSE
				  AND e_ip.series_id = ce.series_id
				  AND uwp_ip.updated_at > ce.updated_at
			)
		)`
	}

	sourceTable := "completed_episodes"
	if !q.EnableResumable {
		sourceTable = "eligible_series"
	}

	query := fmt.Sprintf(`
		WITH completed_episodes AS (
			SELECT DISTINCT ON (e.series_id)
				e.series_id,
				e.season_number,
				e.episode_number,
				uwp.updated_at
			FROM user_watch_progress uwp
			JOIN episodes e ON e.content_id = uwp.media_item_id
			WHERE uwp.user_id = $1
			  AND uwp.profile_id = $2
			  AND uwp.completed = TRUE
			  AND NOT EXISTS (
				  SELECT 1
				  FROM user_history_hidden_items hhi
				  WHERE hhi.user_id = uwp.user_id
				    AND hhi.profile_id = uwp.profile_id
				    AND hhi.media_item_id = uwp.media_item_id
				    AND uwp.updated_at <= hhi.hidden_before
			  )
			  %s
			  %s
			ORDER BY e.series_id, uwp.updated_at DESC, e.season_number DESC, e.episode_number DESC
		)
		%s
		SELECT
			next_ep.content_id,
			next_ep.series_id,
			si.title,
			next_ep.season_number,
			next_ep.episode_number,
			es.updated_at
		FROM %s es
		JOIN media_items si ON si.content_id = es.series_id
		JOIN LATERAL (
			SELECT e2.content_id, e2.series_id, e2.season_number, e2.episode_number
			FROM episodes e2
			WHERE e2.series_id = es.series_id
			  AND (e2.season_number, e2.episode_number) > (es.season_number, es.episode_number)
			  AND EXISTS (SELECT 1 FROM media_files mf WHERE mf.episode_id = e2.content_id AND mf.missing_since IS NULL)
			  AND NOT EXISTS (
				  SELECT 1 FROM user_watch_progress uwp2
				  WHERE uwp2.user_id = $1
				    AND uwp2.profile_id = $2
				    AND uwp2.media_item_id = e2.content_id
			  )
			ORDER BY e2.season_number, e2.episode_number
			LIMIT 1
		) next_ep ON true
		ORDER BY es.updated_at DESC
		LIMIT $3`, seriesFilter, dateCutoffFilter, inProgressExclusion, sourceTable)

	return query, args
}

// buildListResumableFirstEpisodesQuery builds the SQL + args for the
// resumable-first-episode lookup. Pulled out of listResumableFirstEpisodes
// so the SeriesID-driven gate-drop is unit-testable without a live database.
//
// When q.SeriesID is set the WHERE clause is scoped to that series and the
// "no completed episodes for this series" exclusion is dropped: the
// show-detail tile is supposed to surface the in-progress episode even when
// the user already finished earlier episodes of the same show. Without this
// gate-drop the endpoint silently falls through to buildListNextUpQuery's
// "next aired" row, which is the bug Codex flagged on PR #64.
//
// When q.SeriesID is empty (global /Shows/NextUp) the gate is preserved —
// series with completed episodes go through the main query, and this branch
// only contributes "started but no episode finished yet" series.
func buildListResumableFirstEpisodesQuery(q NextUpQuery, inProgressIDs []string) (string, []interface{}) {
	args := []interface{}{q.UserID, q.ProfileID, inProgressIDs}
	seriesFilter := ""
	if q.SeriesID != "" {
		args = append(args, q.SeriesID)
		seriesFilter = fmt.Sprintf(" AND e.series_id = $%d", len(args))
	}

	completedSeriesGate := `
		  AND NOT EXISTS (
			  SELECT 1 FROM user_watch_progress uwp_c
			  JOIN episodes e_c ON e_c.content_id = uwp_c.media_item_id
			  WHERE uwp_c.user_id = $1
			    AND uwp_c.profile_id = $2
			    AND uwp_c.completed = TRUE
			    AND e_c.series_id = e.series_id
		  )`
	if q.SeriesID != "" {
		completedSeriesGate = ""
	}

	query := fmt.Sprintf(`
		SELECT DISTINCT ON (e.series_id)
			e.content_id,
			e.series_id,
			si.title,
			e.season_number,
			e.episode_number,
			uwp.updated_at
		FROM user_watch_progress uwp
		JOIN episodes e ON e.content_id = uwp.media_item_id
		JOIN media_items si ON si.content_id = e.series_id
		WHERE uwp.user_id = $1
		  AND uwp.profile_id = $2
		  AND uwp.completed = FALSE
		  AND uwp.media_item_id = ANY($3)%s%s
		ORDER BY e.series_id, uwp.updated_at DESC`, seriesFilter, completedSeriesGate)

	return query, args
}

// listResumableFirstEpisodes finds in-progress episodes for the user.
//
// Two callers, two semantics, one query:
//
//   - Global next-up (q.SeriesID == ""): only contribute series the user has
//     never completed any episode of. Series with completed episodes go
//     through buildListNextUpQuery's "next unwatched after the last
//     completed" path; the resumable branch fills the gap for series that
//     don't have a completed-episode anchor yet.
//   - Show-detail tile (q.SeriesID != ""): the in-progress episode wins
//     unconditionally, even when the user has already completed earlier
//     episodes in the same series. The completed-then-mid-watching-the-
//     next-one case is exactly what the show-detail "continue watching"
//     tile is for, so the no-completed-episodes gate would defeat the
//     endpoint's purpose. ListNextUp also flips its dedup priority to let
//     this row win over the completed-next row from the main query.
func (r *NextUpRepository) listResumableFirstEpisodes(ctx context.Context, q NextUpQuery) ([]NextUpResult, error) {
	store, err := r.storeProvider.ForUser(ctx, q.UserID)
	if err != nil {
		return nil, fmt.Errorf("getting user store: %w", err)
	}

	inProgressEntries, err := store.ListProgress(ctx, q.ProfileID, "in_progress", 100, 0)
	if err != nil {
		return nil, fmt.Errorf("listing in-progress: %w", err)
	}
	if len(inProgressEntries) == 0 {
		return nil, nil
	}

	inProgressIDs := make([]string, 0, len(inProgressEntries))
	for _, entry := range inProgressEntries {
		inProgressIDs = append(inProgressIDs, entry.MediaItemID)
	}

	query, args := buildListResumableFirstEpisodesQuery(q, inProgressIDs)

	rows, err := r.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("querying resumable episodes: %w", err)
	}
	defer rows.Close()

	var results []NextUpResult
	for rows.Next() {
		var res NextUpResult
		res.IsResumable = true
		if err := rows.Scan(
			&res.ContentID, &res.SeriesID, &res.SeriesTitle,
			&res.SeasonNumber, &res.EpisodeNumber, &res.CompletedAt,
		); err != nil {
			return nil, fmt.Errorf("scanning resumable row: %w", err)
		}
		results = append(results, res)
	}
	return results, rows.Err()
}
