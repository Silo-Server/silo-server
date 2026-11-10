package pgstore

import (
	"context"
	"fmt"

	"github.com/Silo-Server/silo-server/internal/userstore"
)

var _ userstore.ItemUserFlagStore = (*PostgresUserStore)(nil)

// itemUserFlagsSQL returns one row per flag that is set. The list branches are
// the ListFavoritesByMediaItems, ListWatchlistByMediaItems and
// ListNotInterestedByMediaItems reads. The played branch applies the
// visibility rules of ListProgressByMediaItems and ListCompletedHistoryItems
// one ID at a time. OFFSET 0 keeps each EXISTS a per-ID index probe: without
// it the planner may hash every completed row the profile has, which for a
// long watch history costs far more than the page's probes, and the probe
// keeps one plan for every profile, so a cached generic plan is safe.
var itemUserFlagsSQL = `SELECT 'favorite', media_item_id FROM user_favorites
	WHERE user_id = $1 AND profile_id = $2 AND media_item_id = ANY($3::text[])
UNION ALL
SELECT 'watchlist', media_item_id FROM user_watchlist
	WHERE user_id = $1 AND profile_id = $2 AND media_item_id = ANY($3::text[])
UNION ALL
SELECT 'not_interested', media_item_id FROM user_not_interested
	WHERE user_id = $1 AND profile_id = $2 AND media_item_id = ANY($3::text[])
UNION ALL
SELECT 'played', t.media_item_id FROM unnest($4::text[]) AS t(media_item_id)
	WHERE EXISTS (
		SELECT 1 FROM user_watch_progress wp
		WHERE wp.user_id = $1 AND wp.profile_id = $2 AND wp.media_item_id = t.media_item_id
		  AND wp.completed AND ` + visibleProgressSQL("wp") + `
		OFFSET 0
	) OR EXISTS (
		SELECT 1 FROM user_watch_history h
		WHERE h.user_id = $1 AND h.profile_id = $2 AND h.media_item_id = t.media_item_id
		  AND h.completed` + completedHistoryVisibleSQL + `
		OFFSET 0
	)`

func (s *PostgresUserStore) ItemUserFlags(ctx context.Context, profileID string, mediaItemIDs, playedIDs []string) (map[string]userstore.ItemUserFlags, bool, error) {
	result := make(map[string]userstore.ItemUserFlags)
	if len(mediaItemIDs) == 0 && len(playedIDs) == 0 {
		return result, true, nil
	}
	if mediaItemIDs == nil {
		mediaItemIDs = []string{}
	}
	if playedIDs == nil {
		playedIDs = []string{}
	}
	rows, err := s.pool.Query(ctx, itemUserFlagsSQL, s.userID, profileID, mediaItemIDs, playedIDs)
	if err != nil {
		return nil, true, fmt.Errorf("listing item user flags: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var flag, mediaItemID string
		if err := rows.Scan(&flag, &mediaItemID); err != nil {
			return nil, true, fmt.Errorf("scanning item user flag: %w", err)
		}
		flags := result[mediaItemID]
		switch flag {
		case "favorite":
			flags.Favorite = true
		case "watchlist":
			flags.InWatchlist = true
		case "not_interested":
			flags.NotInterested = true
		case "played":
			flags.Played = true
		}
		result[mediaItemID] = flags
	}
	if err := rows.Err(); err != nil {
		return nil, true, fmt.Errorf("iterating item user flags: %w", err)
	}
	return result, true, nil
}
