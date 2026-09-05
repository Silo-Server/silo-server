package pgstore

import (
	"context"
	"time"
)

// SetJellycompatProgress applies an explicit user edit, including historical
// dates. Import tombstones must not suppress a new interactive edit.
// A transaction-local marker tells the stamp trigger that an unchanged event_at
// is still explicitly supplied. The write timestamp and synced_seq advance.
func (s *PostgresUserStore) SetJellycompatProgress(ctx context.Context, profileID, itemID string, position, duration float64, completed bool, date time.Time) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, "SELECT set_config('silo.explicit_progress_event_time', 'on', true)"); err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO user_watch_progress
 (user_id, profile_id, media_item_id, position_seconds, duration_seconds, completed, updated_at, event_at)
 SELECT $1,$2,$3,$4,$5,$6,GREATEST(clock_timestamp(), COALESCE((SELECT hidden_before + interval '1 second' FROM user_history_hidden_items WHERE user_id=$1 AND profile_id=$2 AND media_item_id=$3), clock_timestamp())),$7
 ON CONFLICT(user_id,profile_id,media_item_id) DO UPDATE SET
 position_seconds=excluded.position_seconds, duration_seconds=excluded.duration_seconds,
 completed=excluded.completed, updated_at=excluded.updated_at, event_at=excluded.event_at`,
		s.userID, profileID, itemID, position, duration, completed, date.UTC())
	if err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *PostgresUserStore) ListJellycompatProgressDates(ctx context.Context, profileID string, ids []string) (map[string]string, error) {
	rows, err := s.pool.Query(ctx, `SELECT media_item_id, event_at FROM user_watch_progress WHERE user_id=$1 AND profile_id=$2 AND media_item_id=ANY($3)`, s.userID, profileID, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	dates := make(map[string]string)
	for rows.Next() {
		var id string
		var date time.Time
		if err := rows.Scan(&id, &date); err != nil {
			return nil, err
		}
		dates[id] = date.UTC().Format(time.RFC3339Nano)
	}
	return dates, rows.Err()
}
