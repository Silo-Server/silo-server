package audiobooks

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/audiobooks/abs"
)

// ABSProgressStore implements abs.ProgressStore directly against the
// user_watch_progress table using a shared pgxpool. Using the pool directly
// (rather than the per-user-scoped PostgresUserStore) lets us query by
// (user_id, profile_id) without needing a ForUser call, which would require
// knowing the integer user_id at construction time. The ABS handlers carry
// user_id as a string and resolve it inline here.
type ABSProgressStore struct {
	Pool *pgxpool.Pool
}

var _ abs.ProgressStore = (*ABSProgressStore)(nil)

// GetProgress returns the progress row for (userID, profileID, contentID).
// Returns (nil, nil) when no row exists.
func (s *ABSProgressStore) GetProgress(ctx context.Context, userID, profileID, contentID string) (*abs.ProgressRow, error) {
	uid, err := strconv.Atoi(userID)
	if err != nil {
		return nil, fmt.Errorf("abs_progress_store: invalid user_id %q: %w", userID, err)
	}
	var row abs.ProgressRow
	var updatedAt time.Time
	var positionSeconds, durationSeconds float64
	var completed bool
	var progressPct *float64

	dbRow := s.Pool.QueryRow(ctx, `
		SELECT media_item_id, position_seconds, duration_seconds, completed,
		       CASE WHEN duration_seconds > 0 THEN position_seconds / duration_seconds ELSE 0 END AS progress_pct,
		       updated_at
		FROM user_watch_progress
		WHERE user_id = $1 AND profile_id = $2 AND media_item_id = $3`,
		uid, profileID, contentID,
	)
	err = dbRow.Scan(
		&row.ContentID, &positionSeconds, &durationSeconds, &completed,
		&progressPct, &updatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("abs_progress_store: get progress: %w", err)
	}

	row.UserID = userID
	row.ProfileID = profileID
	row.CurrentSeconds = positionSeconds
	row.DurationSeconds = durationSeconds
	row.IsFinished = completed
	if progressPct != nil {
		row.ProgressPct = *progressPct
	}
	row.UpdatedAt = updatedAt
	return &row, nil
}

// ListProgressForAudiobooks returns all progress rows for (userID, profileID)
// that join to media_items with type = 'audiobook'. Ordered by updated_at DESC,
// capped at limit rows.
func (s *ABSProgressStore) ListProgressForAudiobooks(ctx context.Context, userID, profileID string, limit int) ([]abs.ProgressRow, error) {
	uid, err := strconv.Atoi(userID)
	if err != nil {
		return nil, fmt.Errorf("abs_progress_store: invalid user_id %q: %w", userID, err)
	}
	if limit <= 0 {
		limit = 500
	}
	rows, err := s.Pool.Query(ctx, `
		SELECT wp.media_item_id,
		       wp.position_seconds,
		       wp.duration_seconds,
		       wp.completed,
		       CASE WHEN wp.duration_seconds > 0 THEN wp.position_seconds / wp.duration_seconds ELSE 0 END,
		       wp.updated_at
		FROM user_watch_progress wp
		JOIN media_items mi ON mi.content_id = wp.media_item_id
		WHERE wp.user_id = $1
		  AND wp.profile_id = $2
		  AND mi.type = 'audiobook'
		ORDER BY wp.updated_at DESC
		LIMIT $3`,
		uid, profileID, limit,
	)
	if err != nil {
		return nil, fmt.Errorf("abs_progress_store: list progress: %w", err)
	}
	defer rows.Close()

	var result []abs.ProgressRow
	for rows.Next() {
		var p abs.ProgressRow
		var updatedAt time.Time
		if err := rows.Scan(
			&p.ContentID,
			&p.CurrentSeconds,
			&p.DurationSeconds,
			&p.IsFinished,
			&p.ProgressPct,
			&updatedAt,
		); err != nil {
			return nil, fmt.Errorf("abs_progress_store: scan progress row: %w", err)
		}
		p.UserID = userID
		p.ProfileID = profileID
		p.UpdatedAt = updatedAt
		result = append(result, p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("abs_progress_store: iterate progress rows: %w", err)
	}
	return result, nil
}

// UpsertProgress inserts or updates a user_watch_progress row. All fields in
// row are written; caller is responsible for merging existing state before
// calling this (see handleSetItemProgress).
func (s *ABSProgressStore) UpsertProgress(ctx context.Context, row abs.ProgressRow) error {
	uid, err := strconv.Atoi(row.UserID)
	if err != nil {
		return fmt.Errorf("abs_progress_store: invalid user_id %q: %w", row.UserID, err)
	}
	updatedAt := row.UpdatedAt
	if updatedAt.IsZero() {
		updatedAt = time.Now().UTC()
	}
	_, err = s.Pool.Exec(ctx, `
		INSERT INTO user_watch_progress
		  (user_id, profile_id, media_item_id, position_seconds, duration_seconds, completed, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		ON CONFLICT (user_id, profile_id, media_item_id) DO UPDATE SET
		  position_seconds = EXCLUDED.position_seconds,
		  duration_seconds = EXCLUDED.duration_seconds,
		  completed        = EXCLUDED.completed,
		  updated_at       = EXCLUDED.updated_at`,
		uid, row.ProfileID, row.ContentID,
		row.CurrentSeconds, row.DurationSeconds, row.IsFinished, updatedAt,
	)
	if err != nil {
		return fmt.Errorf("abs_progress_store: upsert progress: %w", err)
	}
	return nil
}

// UpdateProgressPosition updates only the position_seconds column for an
// existing row. If no row exists this is a no-op (the session-sync path
// that calls this only needs to move the cursor, not create a progress row
// for the first time; that's done when the user explicitly sets progress).
func (s *ABSProgressStore) UpdateProgressPosition(ctx context.Context, userID, profileID, contentID string, positionSeconds float64) error {
	uid, err := strconv.Atoi(userID)
	if err != nil {
		return fmt.Errorf("abs_progress_store: invalid user_id %q: %w", userID, err)
	}
	_, err = s.Pool.Exec(ctx, `
		UPDATE user_watch_progress
		SET position_seconds = $4,
		    updated_at       = now()
		WHERE user_id = $1 AND profile_id = $2 AND media_item_id = $3`,
		uid, profileID, contentID, positionSeconds,
	)
	if err != nil {
		return fmt.Errorf("abs_progress_store: update progress position: %w", err)
	}
	return nil
}
