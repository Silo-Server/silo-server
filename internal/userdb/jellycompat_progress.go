package userdb

import (
	"context"
	"database/sql"
	"encoding/json"
	"time"

	"github.com/Silo-Server/silo-server/internal/userstore"
)

func setJellycompatProgress(ctx context.Context, tx *sql.Tx, profileID, itemID string, position, duration float64, completed bool, date time.Time) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO watch_progress
 (profile_id,media_item_id,position_seconds,duration_seconds,completed,updated_at,event_at)
 SELECT ?,?,?,?,?,MAX(strftime('%Y-%m-%dT%H:%M:%fZ','now'),COALESCE((SELECT strftime('%Y-%m-%dT%H:%M:%fZ',hidden_before,'+1 second') FROM hidden_history_items WHERE profile_id=? AND media_item_id=?),'')),? WHERE true ON CONFLICT(profile_id,media_item_id) DO UPDATE SET
 position_seconds=excluded.position_seconds,duration_seconds=excluded.duration_seconds,
 completed=excluded.completed,updated_at=excluded.updated_at,event_at=excluded.event_at`,
		profileID, itemID, position, duration, completed, profileID, itemID, nil)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, "UPDATE watch_progress SET event_at = ? WHERE profile_id = ? AND media_item_id = ?", date.UTC().Format(time.RFC3339Nano), profileID, itemID)
	return err
}

func (s *SQLiteUserStore) ListJellycompatProgressDates(ctx context.Context, profileID string, ids []string) (map[string]string, error) {
	encoded, err := json.Marshal(ids)
	if err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT media_item_id,event_at FROM watch_progress WHERE profile_id=? AND media_item_id IN (SELECT value FROM json_each(?))`, profileID, string(encoded))
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	dates := make(map[string]string)
	for rows.Next() {
		var id, date string
		if err := rows.Scan(&id, &date); err != nil {
			return nil, err
		}
		dates[id] = date
	}
	return dates, rows.Err()
}

func (s *SQLiteUserStore) ApplyJellycompatProgress(ctx context.Context, profileID string, edit userstore.JellycompatProgressEdit) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if edit.ClearHistory {
		if err := removeHistoryItems(ctx, tx, profileID, []string{edit.MediaItemID}, time.Now().UTC()); err != nil {
			return err
		}
	}
	if edit.History != nil {
		entry := *edit.History
		entry.ProfileID, entry.MediaItemID = profileID, edit.MediaItemID
		if _, err := addVisibleHistory(ctx, tx, entry); err != nil {
			return err
		}
	}
	if err := setJellycompatProgress(ctx, tx, profileID, edit.MediaItemID, edit.PositionSeconds, edit.DurationSeconds, edit.Completed, edit.EventAt); err != nil {
		return err
	}
	return tx.Commit()
}
