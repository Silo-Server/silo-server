package catalog

import (
	"context"
	"fmt"
)

// Per-profile id-set queries backing the calendar presets. All run on the same
// pool as the base calendar query. The watched-series rollup intentionally
// mirrors recommendations.GetPopularItems (COALESCE(e.series_id, wp.media_item_id))
// so "engaged with a series" means the same thing everywhere; it is a one-line
// SQL expression, not worth a cross-package extraction.
const (
	followedItemIDsQuery = `
SELECT media_item_id FROM user_favorites WHERE user_id = $1 AND profile_id = $2
UNION
SELECT media_item_id FROM user_watchlist WHERE user_id = $1 AND profile_id = $2
UNION
SELECT DISTINCT COALESCE(e.series_id, wp.media_item_id)
FROM   user_watch_progress wp
LEFT JOIN episodes e ON e.content_id = wp.media_item_id
WHERE  wp.user_id = $1 AND wp.profile_id = $2`

	favoriteItemIDsQuery  = `SELECT media_item_id FROM user_favorites WHERE user_id = $1 AND profile_id = $2`
	watchlistItemIDsQuery = `SELECT media_item_id FROM user_watchlist WHERE user_id = $1 AND profile_id = $2`

	watchedItemIDsQuery = `
SELECT media_item_id
FROM   user_watch_progress
WHERE  user_id = $1 AND profile_id = $2 AND completed = true AND media_item_id = ANY($3)`
)

// ListFollowedItemIDs returns the profile's followed set: favorited ∪ watchlisted ∪
// any series/movie they have watch progress on.
func (r *CalendarRepository) ListFollowedItemIDs(ctx context.Context, userID int, profileID string) ([]string, error) {
	return r.queryIDs(ctx, followedItemIDsQuery, userID, profileID)
}

// ListFavoriteItemIDs returns the profile's favorited content ids.
func (r *CalendarRepository) ListFavoriteItemIDs(ctx context.Context, userID int, profileID string) ([]string, error) {
	return r.queryIDs(ctx, favoriteItemIDsQuery, userID, profileID)
}

// ListWatchlistItemIDs returns the profile's watchlisted content ids.
func (r *CalendarRepository) ListWatchlistItemIDs(ctx context.Context, userID int, profileID string) ([]string, error) {
	return r.queryIDs(ctx, watchlistItemIDsQuery, userID, profileID)
}

func (r *CalendarRepository) queryIDs(ctx context.Context, query string, userID int, profileID string) ([]string, error) {
	rows, err := r.pool.Query(ctx, query, userID, profileID)
	if err != nil {
		return nil, fmt.Errorf("calendar personal query: %w", err)
	}
	defer rows.Close()

	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scanning personal id: %w", err)
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// ListWatchedItemIDs returns the subset of contentIDs the profile has completed,
// as a set. An empty input returns an empty set without querying.
func (r *CalendarRepository) ListWatchedItemIDs(ctx context.Context, userID int, profileID string, contentIDs []string) (map[string]bool, error) {
	watched := make(map[string]bool, len(contentIDs))
	if len(contentIDs) == 0 {
		return watched, nil
	}
	rows, err := r.pool.Query(ctx, watchedItemIDsQuery, userID, profileID, contentIDs)
	if err != nil {
		return nil, fmt.Errorf("calendar watched query: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scanning watched id: %w", err)
		}
		watched[id] = true
	}
	return watched, rows.Err()
}
