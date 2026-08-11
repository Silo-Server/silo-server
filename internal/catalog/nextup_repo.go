package catalog

import (
	"context"
	"fmt"
	"log/slog"
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

	var results []NextUpResult
	if q.SeriesID != "" {
		query, args := buildListNextUpQuery(q, limit, nil)
		rows, err := r.pool.Query(ctx, query, args...)
		if err != nil {
			return nil, fmt.Errorf("querying next-up episodes: %w", err)
		}
		defer rows.Close()

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
	} else {
		remaining := limit
		var cursor *nextUpWalkCursor
		batchesRun := 0
		exhausted := false

		// Every batch resumes below the previous compound frontier, so appending
		// batches preserves strictly descending (updated_at, media_item_id) order.
		for batch := 0; batch < nextUpAnchorMaxBatches; batch++ {
			batchResults, frontier, err := r.listNextUpBatch(ctx, q, remaining, cursor)
			if err != nil {
				return nil, err
			}
			batchesRun++
			results = append(results, batchResults...)
			remaining -= len(batchResults)

			if remaining <= 0 {
				exhausted = true
				break
			}
			if frontier == nil || frontier.n < nextUpAnchorMaxSeries {
				exhausted = true
				break
			}
			cursor = &frontier.nextUpWalkCursor
		}

		if !exhausted && remaining > 0 {
			slog.WarnContext(ctx, "next-up anchor walk hit batch cap; eligible series tail left unscanned",
				"component", "catalog",
				"profile_id", q.ProfileID,
				"batches", batchesRun,
				"results_found", len(results))
		}
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

type nextUpWalkCursor struct {
	updatedAt   time.Time
	mediaItemID string
	seen        []string
}

type nextUpWalkFrontier struct {
	nextUpWalkCursor
	n int
}

// nextUpAnchorMaxSeries bounds the number of distinct series anchors visited by
// one global next-up batch. A row-based cap let one series consume the window;
// counting distinct series prevents that, while batching continues past a full
// batch whose anchors are all ineligible. Series-scoped calls stay unbounded so
// they can anchor on the series' last completed episode regardless of age.
const nextUpAnchorMaxSeries = 96

// nextUpAnchorMaxBatches bounds a global call to 10 × 96 = 960 visited series
// as a runaway guard.
const nextUpAnchorMaxBatches = 10

func (r *NextUpRepository) listNextUpBatch(
	ctx context.Context,
	q NextUpQuery,
	limit int,
	cursor *nextUpWalkCursor,
) ([]NextUpResult, *nextUpWalkFrontier, error) {
	query, args := buildListNextUpQuery(q, limit, cursor)
	rows, err := r.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, nil, fmt.Errorf("querying next-up episodes: %w", err)
	}
	defer rows.Close()

	var results []NextUpResult
	var frontier *nextUpWalkFrontier
	for rows.Next() {
		currentFrontier := nextUpWalkFrontier{}
		var contentID, seriesID, seriesTitle *string
		var seasonNumber, episodeNumber *int
		var completedAt *time.Time
		if err := rows.Scan(
			&currentFrontier.updatedAt,
			&currentFrontier.mediaItemID,
			&currentFrontier.seen,
			&currentFrontier.n,
			&contentID,
			&seriesID,
			&seriesTitle,
			&seasonNumber,
			&episodeNumber,
			&completedAt,
		); err != nil {
			return nil, nil, fmt.Errorf("scanning next-up row: %w", err)
		}
		frontier = &currentFrontier
		if contentID == nil {
			continue
		}
		results = append(results, NextUpResult{
			ContentID:     *contentID,
			SeriesID:      *seriesID,
			SeriesTitle:   *seriesTitle,
			SeasonNumber:  *seasonNumber,
			EpisodeNumber: *episodeNumber,
			CompletedAt:   *completedAt,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, nil, fmt.Errorf("iterating next-up rows: %w", err)
	}
	return results, frontier, nil
}

func buildListNextUpQuery(q NextUpQuery, limit int, cursor *nextUpWalkCursor) (string, []interface{}) {
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

	// seedSeen is projected alongside the seed's picked series, which the global
	// walk exposes as `pick`; the series-scoped branch has no walk and no seed.
	cursorFilter := ""
	seedSeen := "ARRAY[pick.series_id]"
	if q.SeriesID == "" && cursor != nil {
		cursorUpdatedAtArg := argIdx
		cursorMediaItemIDArg := argIdx + 1
		cursorSeenArg := argIdx + 2
		cursorFilter = fmt.Sprintf(`
				  AND (uwp.updated_at, uwp.media_item_id) < ($%d, $%d)
				  AND NOT (e.series_id = ANY($%d))`, cursorUpdatedAtArg, cursorMediaItemIDArg, cursorSeenArg)
		seedSeen = fmt.Sprintf("$%d::text[] || pick.series_id", cursorSeenArg)
		args = append(args, cursor.updatedAt, cursor.mediaItemID, cursor.seen)
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
				  AND uwp_ip.position_seconds > 0
				  AND e_ip.series_id = ce.series_id
				  AND uwp_ip.updated_at > ce.updated_at
			)
		)`
	}

	sourceTable := "completed_episodes"
	if !q.EnableResumable {
		sourceTable = "eligible_series"
	}

	// Global queries use a recursive loose-index scan to find at most
	// nextUpAnchorMaxSeries distinct series without letting one series' rows
	// consume the budget. The compound cursor is the total order required when
	// bulk updates give many rows the same timestamp; because media_item_id is
	// unique it can only order the walk, never decide which episode of a series
	// anchors it — see the anchor lateral below.
	// Series-scoped queries keep the unbounded shape so the anchor is the
	// series' last completed episode regardless of age. Hidden rows and the date
	// cutoff are excluded inside every walk step so they neither become anchors
	// nor advance the cursor.
	var completedEpisodesCTE string
	if q.SeriesID != "" {
		completedEpisodesCTE = fmt.Sprintf(`completed_episodes AS (
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
		)`, seriesFilter, dateCutoffFilter)
	} else {
		// Picking the series and picking which of its episodes to anchor on are
		// two different orderings, so they are two different steps.
		//
		// The walk advances on (updated_at, media_item_id), the total order that
		// guarantees forward progress. media_item_id is unique, so no two rows
		// ever tie on that pair and any season/episode clause appended behind it
		// is unreachable. A bulk mark-watched series — every row written with one
		// timestamp — would then anchor on whichever content_id sorts highest,
		// which is an arbitrary season once IDs are not scan-ordered (season 2
		// scanned before a season-1 backfill), and the rail would surface an
		// already-watched episode's successor.
		//
		// So `pick` chooses the series and the cursor position, and the anchor
		// lateral then chooses the episode by (season, episode) among that
		// series' rows sharing pick.updated_at — its newest completed timestamp.
		// Pinning updated_at rather than re-sorting keeps this an index probe,
		// and both rows carry the same updated_at, so cursor order and the
		// reported CompletedAt are unchanged by the split.
		anchorLateral := `JOIN LATERAL (
				SELECT e_a.season_number, e_a.episode_number
				FROM episodes e_a
				JOIN user_watch_progress uwp_a
				  ON uwp_a.media_item_id = e_a.content_id
				 AND uwp_a.user_id = $1
				 AND uwp_a.profile_id = $2
				 AND uwp_a.completed = TRUE
				 AND uwp_a.updated_at = pick.updated_at
				WHERE e_a.series_id = pick.series_id
				  AND NOT EXISTS (
					  SELECT 1
					  FROM user_history_hidden_items hhi
					  WHERE hhi.user_id = uwp_a.user_id
					    AND hhi.profile_id = uwp_a.profile_id
					    AND hhi.media_item_id = uwp_a.media_item_id
					    AND uwp_a.updated_at <= hhi.hidden_before
				  )
				ORDER BY e_a.season_number DESC, e_a.episode_number DESC
				LIMIT 1
			) anchor ON TRUE`

		completedEpisodesCTE = fmt.Sprintf(`RECURSIVE walk AS (
			(
				SELECT
					pick.series_id,
					anchor.season_number,
					anchor.episode_number,
					pick.updated_at,
					pick.media_item_id,
					%s AS seen,
					1 AS n
				FROM (
					SELECT e.series_id, uwp.updated_at, uwp.media_item_id
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
					ORDER BY uwp.updated_at DESC, uwp.media_item_id DESC
					LIMIT 1
				) pick
				%s
			)
			UNION ALL
			SELECT
				pick.series_id,
				anchor.season_number,
				anchor.episode_number,
				pick.updated_at,
				pick.media_item_id,
				w.seen || pick.series_id,
				w.n + 1
			FROM walk w
			JOIN LATERAL (
				SELECT e.series_id, uwp.updated_at, uwp.media_item_id
				FROM user_watch_progress uwp
				JOIN episodes e ON e.content_id = uwp.media_item_id
				WHERE uwp.user_id = $1
				  AND uwp.profile_id = $2
				  AND uwp.completed = TRUE
				  AND (uwp.updated_at, uwp.media_item_id) < (w.updated_at, w.media_item_id)
				  AND NOT (e.series_id = ANY(w.seen))
				  AND NOT EXISTS (
					  SELECT 1
					  FROM user_history_hidden_items hhi
					  WHERE hhi.user_id = uwp.user_id
					    AND hhi.profile_id = uwp.profile_id
					    AND hhi.media_item_id = uwp.media_item_id
					    AND uwp.updated_at <= hhi.hidden_before
				  )
				  %s
				ORDER BY uwp.updated_at DESC, uwp.media_item_id DESC
				LIMIT 1
			) pick ON true
			%s
			WHERE w.n < %d
		),
		completed_episodes AS (
			SELECT series_id, season_number, episode_number, updated_at
			FROM walk
		)`, seedSeen, cursorFilter, dateCutoffFilter, anchorLateral,
			dateCutoffFilter, anchorLateral, nextUpAnchorMaxSeries)
	}

	if q.SeriesID != "" {
		query := fmt.Sprintf(`
		WITH %s
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
				    AND (uwp2.completed = TRUE OR uwp2.position_seconds > 0)
			  )
			ORDER BY e2.season_number, e2.episode_number
			LIMIT 1
		) next_ep ON true
		ORDER BY es.updated_at DESC
		LIMIT $3`, completedEpisodesCTE, inProgressExclusion, sourceTable)
		return query, args
	}

	resultQuery := fmt.Sprintf(`
		SELECT
			next_ep.content_id,
			next_ep.series_id,
			si.title,
			next_ep.season_number,
			next_ep.episode_number,
			es.updated_at AS completed_at
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
				    AND (uwp2.completed = TRUE OR uwp2.position_seconds > 0)
			  )
			ORDER BY e2.season_number, e2.episode_number
			LIMIT 1
		) next_ep ON true
		ORDER BY es.updated_at DESC
		LIMIT $3`, sourceTable)

	query := fmt.Sprintf(`
		WITH %s
		%s
		,
		frontier AS (
			SELECT w.updated_at, w.media_item_id, w.seen, w.n
			FROM walk w
			ORDER BY w.n DESC
			LIMIT 1
		)
		SELECT
			f.updated_at AS frontier_updated_at,
			f.media_item_id AS frontier_media_item_id,
			f.seen AS frontier_seen,
			f.n AS frontier_n,
			r.content_id,
			r.series_id,
			r.title,
			r.season_number,
			r.episode_number,
			r.completed_at
		FROM frontier f
		LEFT JOIN (
			%s
		) r ON true
		ORDER BY r.completed_at DESC NULLS LAST`, completedEpisodesCTE, inProgressExclusion, resultQuery)

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
		  AND uwp.position_seconds > 0
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
