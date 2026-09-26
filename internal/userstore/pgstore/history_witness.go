package pgstore

import (
	"context"
	"encoding/json"
	"fmt"
)

// LatestHistoryIDs selects a witness before any raw-page cursor is applied.
// Group membership is bounded by the candidate cards and their catalog episodes.
func (s *PostgresUserStore) LatestHistoryIDs(ctx context.Context, profileID string, groups map[string][]string) (map[string]string, error) {
	result := make(map[string]string, len(groups))
	if len(groups) == 0 {
		return result, nil
	}
	payload, err := json.Marshal(groups)
	if err != nil {
		return nil, err
	}
	rows, err := s.pool.Query(ctx, `
 SELECT DISTINCT ON (g.key) g.key, latest.id
 FROM jsonb_each($3::jsonb) g
 CROSS JOIN LATERAL jsonb_array_elements_text(g.value) member(media_item_id)
 CROSS JOIN LATERAL (
  SELECT h.id, date_trunc('second', h.watched_at AT TIME ZONE 'UTC') AS watched_second
  FROM user_watch_history h
  WHERE h.user_id = $1 AND h.profile_id = $2 AND h.media_item_id = member.media_item_id
   AND h.watched_at > COALESCE((
    SELECT hidden.hidden_before FROM user_history_hidden_items hidden
    WHERE hidden.user_id = h.user_id AND hidden.profile_id = h.profile_id
     AND hidden.media_item_id = h.media_item_id
   ), '-infinity'::timestamptz)
  ORDER BY date_trunc('second', h.watched_at AT TIME ZONE 'UTC') DESC, h.id DESC
  LIMIT 1
 ) latest
 ORDER BY g.key, latest.watched_second DESC, latest.id DESC`, s.userID, profileID, payload)
	if err != nil {
		return nil, fmt.Errorf("querying latest history witnesses: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var displayID, historyID string
		if err := rows.Scan(&displayID, &historyID); err != nil {
			return nil, err
		}
		result[displayID] = historyID
	}
	return result, rows.Err()
}
