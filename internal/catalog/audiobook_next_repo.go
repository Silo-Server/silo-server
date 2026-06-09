package catalog

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// NextInSeriesQuery controls the audiobook next-in-series lookup.
type NextInSeriesQuery struct {
	UserID    int
	ProfileID string
	Limit     int
	// LibraryID / LibraryIDs / DisabledLibraryIDs scope the candidate books in
	// SQL. Without this, a profile whose recently finished series live in a
	// different library would consume the LIMIT with candidates the caller
	// then filters out, starving a library-scoped section.
	LibraryID          *int
	LibraryIDs         []int
	DisabledLibraryIDs []int
}

// NextInSeriesResult is one row from the next-in-series query: the next
// unstarted audiobook in a series the profile has finished a book of.
type NextInSeriesResult struct {
	ContentID      string
	SeriesName     string
	SeriesIndex    *float64
	LastFinishedAt time.Time
}

// AudiobookNextRepository queries audiobook series progression for the
// next_in_series section.
type AudiobookNextRepository struct {
	pool *pgxpool.Pool
}

// NewAudiobookNextRepository creates an AudiobookNextRepository.
func NewAudiobookNextRepository(pool *pgxpool.Pool) *AudiobookNextRepository {
	return &AudiobookNextRepository{pool: pool}
}

// ListNextInSeries returns, for each audiobook series the profile has finished
// at least one book of, the lowest-indexed later book the profile has not
// started yet. Series surface in most-recently-finished order. Books already
// in progress are excluded — they belong to Continue Listening, not here.
//
// Candidate books are library-scoped in SQL (see NextInSeriesQuery); remaining
// access filtering (content rating) is applied by the caller when resolving
// content IDs to items.
func (r *AudiobookNextRepository) ListNextInSeries(ctx context.Context, q NextInSeriesQuery) ([]NextInSeriesResult, error) {
	if r == nil || r.pool == nil || q.UserID <= 0 || q.ProfileID == "" {
		return nil, nil
	}

	limit := q.Limit
	if limit <= 0 {
		limit = 20
	}

	args := []any{q.UserID, q.ProfileID}
	argIdx := 3
	candidateScope := ""
	if q.LibraryID != nil {
		candidateScope += fmt.Sprintf(`
			  AND EXISTS (
				  SELECT 1 FROM media_item_libraries mil
				  WHERE mil.content_id = m.content_id AND mil.media_folder_id = $%d
			  )`, argIdx)
		args = append(args, *q.LibraryID)
		argIdx++
	} else if q.LibraryIDs != nil {
		if len(q.LibraryIDs) == 0 {
			return nil, nil
		}
		candidateScope += fmt.Sprintf(`
			  AND EXISTS (
				  SELECT 1 FROM media_item_libraries mil
				  WHERE mil.content_id = m.content_id AND mil.media_folder_id = ANY($%d)
			  )`, argIdx)
		args = append(args, q.LibraryIDs)
		argIdx++
	}
	if len(q.DisabledLibraryIDs) > 0 {
		candidateScope += fmt.Sprintf(`
			  AND NOT EXISTS (
				  SELECT 1 FROM media_item_libraries mil_disabled
				  WHERE mil_disabled.content_id = m.content_id
				    AND mil_disabled.media_folder_id = ANY($%d)
			  )`, argIdx)
		args = append(args, q.DisabledLibraryIDs)
		argIdx++
	}

	query := fmt.Sprintf(`
		WITH finished_series AS (
			SELECT
				LOWER(BTRIM(s.series_name)) AS series_key,
				MIN(BTRIM(s.series_name)) AS series_name,
				MAX(s.series_index) AS max_finished_index,
				MAX(uwp.updated_at) AS last_finished_at
			FROM user_watch_progress uwp
			JOIN audiobook_series s ON s.content_id = uwp.media_item_id
			JOIN media_items mi ON mi.content_id = uwp.media_item_id
			WHERE uwp.user_id = $1
			  AND uwp.profile_id = $2
			  AND uwp.completed = TRUE
			  AND mi.type = 'audiobook'
			  AND s.series_index IS NOT NULL
			GROUP BY LOWER(BTRIM(s.series_name))
		)
		SELECT
			next_book.content_id,
			fs.series_name,
			next_book.series_index,
			fs.last_finished_at
		FROM finished_series fs
		JOIN LATERAL (
			SELECT m.content_id, s2.series_index
			FROM audiobook_series s2
			JOIN media_items m ON m.content_id = s2.content_id
			WHERE LOWER(BTRIM(s2.series_name)) = fs.series_key
			  AND s2.series_index IS NOT NULL
			  AND s2.series_index > fs.max_finished_index
			  AND m.type = 'audiobook'
			  AND EXISTS (
				  SELECT 1 FROM media_files mf
				  WHERE mf.content_id = m.content_id AND mf.missing_since IS NULL
			  )
			  AND NOT EXISTS (
				  SELECT 1 FROM user_watch_progress uwp2
				  WHERE uwp2.user_id = $1
				    AND uwp2.profile_id = $2
				    AND uwp2.media_item_id = m.content_id
			  )%s
			ORDER BY s2.series_index, LOWER(m.sort_title)
			LIMIT 1
		) next_book ON true
		ORDER BY fs.last_finished_at DESC
		LIMIT $%d`, candidateScope, argIdx)
	args = append(args, limit)

	rows, err := r.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("querying next-in-series audiobooks: %w", err)
	}
	defer rows.Close()

	var results []NextInSeriesResult
	for rows.Next() {
		var res NextInSeriesResult
		if err := rows.Scan(&res.ContentID, &res.SeriesName, &res.SeriesIndex, &res.LastFinishedAt); err != nil {
			return nil, fmt.Errorf("scanning next-in-series row: %w", err)
		}
		results = append(results, res)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating next-in-series rows: %w", err)
	}
	return results, nil
}
