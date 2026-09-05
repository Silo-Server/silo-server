package userdb

import (
	"context"
	"encoding/json"
	"time"
)

// SetJellycompatProgress applies an explicit user edit rather than a replayed
// import, so an old LastPlayedDate does not suppress the requested state.
func (s *SQLiteUserStore) SetJellycompatProgress(ctx context.Context, profileID, itemID string, position, duration float64, completed bool, date time.Time) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO watch_progress
 (profile_id,media_item_id,position_seconds,duration_seconds,completed,updated_at,event_at)
 SELECT ?,?,?,?,?,MAX(strftime('%Y-%m-%dT%H:%M:%fZ','now'),COALESCE((SELECT strftime('%Y-%m-%dT%H:%M:%fZ',hidden_before,'+1 second') FROM hidden_history_items WHERE profile_id=? AND media_item_id=?),'')),? WHERE true ON CONFLICT(profile_id,media_item_id) DO UPDATE SET
 position_seconds=excluded.position_seconds,duration_seconds=excluded.duration_seconds,
 completed=excluded.completed,updated_at=excluded.updated_at,event_at=excluded.event_at`,
		profileID, itemID, position, duration, completed, profileID, itemID, date.UTC().Format(time.RFC3339Nano))
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
