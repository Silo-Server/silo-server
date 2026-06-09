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
// Library scoping and access filtering are applied by the caller when
// resolving content IDs to items (mirrors NextUpRepository.ListNextUp).
func (r *AudiobookNextRepository) ListNextInSeries(ctx context.Context, q NextInSeriesQuery) ([]NextInSeriesResult, error) {
	if r == nil || r.pool == nil || q.UserID <= 0 || q.ProfileID == "" {
		return nil, nil
	}

	limit := q.Limit
	if limit <= 0 {
		limit = 20
	}

	const query = `
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
			  )
			ORDER BY s2.series_index, LOWER(m.sort_title)
			LIMIT 1
		) next_book ON true
		ORDER BY fs.last_finished_at DESC
		LIMIT $3`

	rows, err := r.pool.Query(ctx, query, q.UserID, q.ProfileID, limit)
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
