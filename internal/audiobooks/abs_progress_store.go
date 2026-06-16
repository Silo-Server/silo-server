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

	// Completed rows store position_seconds = 0 (no resume point), so the
	// percentage must come from the completed flag, not the position.
	dbRow := s.Pool.QueryRow(ctx, `
		SELECT media_item_id, position_seconds, duration_seconds, completed,
		       CASE WHEN completed THEN 1.0
		            WHEN duration_seconds > 0 THEN position_seconds / duration_seconds
		            ELSE 0 END AS progress_pct,
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
		       CASE WHEN wp.completed THEN 1.0
		            WHEN wp.duration_seconds > 0 THEN wp.position_seconds / wp.duration_seconds
		            ELSE 0 END,
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

// UpsertProgress inserts or updates a user_watch_progress row. Conflict
// updates merge monotonically so concurrent writes cannot un-finish an item or
// rewind progress with a stale position.
func (s *ABSProgressStore) UpsertProgress(ctx context.Context, row abs.ProgressRow) error {
	uid, err := strconv.Atoi(row.UserID)
	if err != nil {
		return fmt.Errorf("abs_progress_store: invalid user_id %q: %w", row.UserID, err)
	}
	updatedAt := row.UpdatedAt
	if updatedAt.IsZero() {
		updatedAt = time.Now().UTC()
	}
	// Completed rows hold no resume point (position_seconds = 0) — the same
	// invariant as the userstore write paths — so finished books can't leak
	// into position-based continue-watching/listening queries. A later
	// non-finished report (re-listen) moves position forward from 0 while
	// the completed latch stays.
	position := row.CurrentSeconds
	if row.IsFinished {
		position = 0
	}
	_, err = s.Pool.Exec(ctx, `
		INSERT INTO user_watch_progress
		  (user_id, profile_id, media_item_id, position_seconds, duration_seconds, completed, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		ON CONFLICT (user_id, profile_id, media_item_id) DO UPDATE SET
		  position_seconds = CASE WHEN EXCLUDED.completed THEN 0
		      ELSE GREATEST(user_watch_progress.position_seconds, EXCLUDED.position_seconds) END,
		  duration_seconds = GREATEST(user_watch_progress.duration_seconds, EXCLUDED.duration_seconds),
		  completed        = user_watch_progress.completed OR EXCLUDED.completed,
		  updated_at       = GREATEST(user_watch_progress.updated_at, EXCLUDED.updated_at)`,
		uid, row.ProfileID, row.ContentID,
		position, row.DurationSeconds, row.IsFinished, updatedAt,
	)
	if err != nil {
		return fmt.Errorf("abs_progress_store: upsert progress: %w", err)
	}
	return nil
}

// UpdateProgressPosition advances the resume cursor from a session-sync tick.
// It is monotonic and finish-preserving: position only moves forward
// (GREATEST) and a completed row is never moved or un-finished. Without the
// GREATEST guard an out-of-order tick or a second device rewound the saved
// position. It deliberately stays UPDATE-only (no row created when none
// exists): the row is created by the explicit progress-report path, so a stray
// sync tick can't resurrect progress the user just cleared, nor create a
// zero-duration row.
func (s *ABSProgressStore) UpdateProgressPosition(ctx context.Context, userID, profileID, contentID string, positionSeconds float64) error {
	uid, err := strconv.Atoi(userID)
	if err != nil {
		return fmt.Errorf("abs_progress_store: invalid user_id %q: %w", userID, err)
	}
	_, err = s.Pool.Exec(ctx, `
		UPDATE user_watch_progress
		SET position_seconds = GREATEST(position_seconds, $4),
		    updated_at       = now()
		WHERE user_id = $1 AND profile_id = $2 AND media_item_id = $3
		  AND NOT completed`,
		uid, profileID, contentID, positionSeconds,
	)
	if err != nil {
		return fmt.Errorf("abs_progress_store: update progress position: %w", err)
	}
	return nil
}

// DeleteProgress removes the progress row entirely. Idempotent on missing-row.
// Used by the ABS "Reset Progress" / clear-progress affordance.
func (s *ABSProgressStore) DeleteProgress(ctx context.Context, userID, profileID, contentID string) error {
	uid, err := strconv.Atoi(userID)
	if err != nil {
		return fmt.Errorf("abs_progress_store: invalid user id %q: %w", userID, err)
	}
	if _, err := s.Pool.Exec(ctx, `
		DELETE FROM user_watch_progress
		WHERE user_id = $1 AND profile_id = $2 AND media_item_id = $3`,
		uid, profileID, contentID,
	); err != nil {
		return fmt.Errorf("abs_progress_store: delete progress: %w", err)
	}
	return nil
}

// SetHideFromContinue sets the hide_from_continue flag for the given
// progress row. Idempotent on missing-row.
func (s *ABSProgressStore) SetHideFromContinue(ctx context.Context, userID, profileID, contentID string, hide bool) error {
	uid, err := strconv.Atoi(userID)
	if err != nil {
		return fmt.Errorf("abs_progress_store: invalid user id %q: %w", userID, err)
	}
	if _, err := s.Pool.Exec(ctx, `
		UPDATE user_watch_progress
		SET hide_from_continue = $4
		WHERE user_id = $1 AND profile_id = $2 AND media_item_id = $3`,
		uid, profileID, contentID, hide,
	); err != nil {
		return fmt.Errorf("abs_progress_store: set hide_from_continue: %w", err)
	}
	return nil
}
