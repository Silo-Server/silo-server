package userdb

import (
	"context"
	"encoding/json"
	"fmt"
)

func (s *SQLiteUserStore) LatestHistoryIDs(ctx context.Context, profileID string, groups map[string][]string) (map[string]string, error) {
	result := make(map[string]string, len(groups))
	if len(groups) == 0 {
		return result, nil
	}
	payload, err := json.Marshal(groups)
	if err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `
 SELECT display_id, id FROM (
  SELECT g.key AS display_id, h.id,
   ROW_NUMBER() OVER (PARTITION BY g.key ORDER BY h.watched_at DESC, h.id DESC) AS rank
  FROM json_each(?) g
  JOIN json_each(g.value) member
  JOIN watch_history h ON h.id = (
   SELECT candidate.id FROM watch_history candidate
   WHERE candidate.profile_id = ? AND candidate.media_item_id = member.value
    AND candidate.watched_at > COALESCE((
     SELECT hidden.hidden_before FROM hidden_history_items hidden
     WHERE hidden.profile_id = candidate.profile_id AND hidden.media_item_id = candidate.media_item_id
    ), '')
   ORDER BY candidate.watched_at DESC, candidate.id DESC
   LIMIT 1
  )
 ) WHERE rank = 1`, string(payload), profileID)
	if err != nil {
		return nil, fmt.Errorf("querying latest history witnesses: %w", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var displayID, historyID string
		if err := rows.Scan(&displayID, &historyID); err != nil {
			return nil, err
		}
		result[displayID] = historyID
	}
	return result, rows.Err()
}
