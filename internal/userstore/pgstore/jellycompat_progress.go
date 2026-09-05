package pgstore

import (
	"context"
	"time"

	"github.com/Silo-Server/silo-server/internal/userstore"
	"github.com/jackc/pgx/v5"
)

func (s *PostgresUserStore) setJellycompatProgress(ctx context.Context, tx pgx.Tx, profileID, itemID string, position, duration float64, completed bool, date time.Time) error {
	if _, err := tx.Exec(ctx, "SELECT set_config('silo.explicit_progress_event_time', 'on', true)"); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `INSERT INTO user_watch_progress
 (user_id, profile_id, media_item_id, position_seconds, duration_seconds, completed, updated_at, event_at)
 SELECT $1,$2,$3,$4,$5,$6,GREATEST(clock_timestamp(), COALESCE((SELECT hidden_before + interval '1 second' FROM user_history_hidden_items WHERE user_id=$1 AND profile_id=$2 AND media_item_id=$3), clock_timestamp())),$7
 ON CONFLICT(user_id,profile_id,media_item_id) DO UPDATE SET
 position_seconds=excluded.position_seconds, duration_seconds=excluded.duration_seconds,
 completed=excluded.completed, updated_at=excluded.updated_at, event_at=excluded.event_at`,
		s.userID, profileID, itemID, position, duration, completed, date.UTC())
	if err != nil {
		return err
	}
	return nil
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

func (s *PostgresUserStore) ApplyJellycompatProgress(ctx context.Context, profileID string, edit userstore.JellycompatProgressEdit) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if edit.ClearHistory {
		if err := s.removeHistoryItems(ctx, tx, profileID, []string{edit.MediaItemID}, time.Now().UTC()); err != nil {
			return err
		}
	}
	if edit.History != nil {
		entry := *edit.History
		entry.ProfileID, entry.MediaItemID = profileID, edit.MediaItemID
		if _, err := s.addVisibleHistory(ctx, tx, entry); err != nil {
			return err
		}
	}
	if err := s.setJellycompatProgress(ctx, tx, profileID, edit.MediaItemID, edit.PositionSeconds, edit.DurationSeconds, edit.Completed, edit.EventAt); err != nil {
		return err
	}
	if edit.IsFavorite != nil {
		if *edit.IsFavorite {
			_, err = tx.Exec(ctx, `INSERT INTO user_favorites (user_id, profile_id, media_item_id, added_at) VALUES ($1, $2, $3, $4) ON CONFLICT DO NOTHING`, s.userID, profileID, edit.MediaItemID, time.Now().UTC())
		} else {
			_, err = tx.Exec(ctx, `DELETE FROM user_favorites WHERE user_id = $1 AND profile_id = $2 AND media_item_id = $3`, s.userID, profileID, edit.MediaItemID)
		}
		if err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}
