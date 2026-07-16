package pgstore

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/Silo-Server/silo-server/internal/userstore"
)

// --- Favorites ---

func (s *PostgresUserStore) AddFavorite(ctx context.Context, profileID, mediaItemID string) error {
	_, err := s.AddFavoriteAt(ctx, profileID, mediaItemID, time.Now().UTC())
	return err
}

func (s *PostgresUserStore) AddFavoriteAt(ctx context.Context, profileID, mediaItemID string, addedAt time.Time) (bool, error) {
	tag, err := s.pool.Exec(ctx,
		`INSERT INTO user_favorites (user_id, profile_id, media_item_id, added_at)
		 VALUES ($1, $2, $3, $4)
		 ON CONFLICT DO NOTHING`,
		s.userID, profileID, mediaItemID, addedAt.UTC(),
	)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() > 0, nil
}

func (s *PostgresUserStore) RemoveFavorite(ctx context.Context, profileID, mediaItemID string) error {
	_, err := s.pool.Exec(ctx,
		`DELETE FROM user_favorites WHERE user_id = $1 AND profile_id = $2 AND media_item_id = $3`,
		s.userID, profileID, mediaItemID,
	)
	return err
}

func (s *PostgresUserStore) ListFavorites(ctx context.Context, profileID string, limit, offset int) ([]userstore.Favorite, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT profile_id, media_item_id, added_at FROM user_favorites
		 WHERE user_id = $1 AND profile_id = $2 ORDER BY added_at DESC LIMIT $3 OFFSET $4`,
		s.userID, profileID, limit, offset,
	)
	if err != nil {
		return nil, fmt.Errorf("listing favorites: %w", err)
	}
	defer rows.Close()

	var favorites []userstore.Favorite
	for rows.Next() {
		var f userstore.Favorite
		var addedAt time.Time
		if err := rows.Scan(&f.ProfileID, &f.MediaItemID, &addedAt); err != nil {
			return nil, fmt.Errorf("scanning favorite row: %w", err)
		}
		f.AddedAt = timeToString(addedAt)
		favorites = append(favorites, f)
	}
	return favorites, rows.Err()
}

func (s *PostgresUserStore) IsFavorite(ctx context.Context, profileID, mediaItemID string) (bool, error) {
	var count int
	err := s.pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM user_favorites WHERE user_id = $1 AND profile_id = $2 AND media_item_id = $3`,
		s.userID, profileID, mediaItemID,
	).Scan(&count)
	return count > 0, err
}

func (s *PostgresUserStore) ListFavoritesByMediaItems(ctx context.Context, profileID string, mediaItemIDs []string) (map[string]bool, error) {
	result := make(map[string]bool, len(mediaItemIDs))
	if len(mediaItemIDs) == 0 {
		return result, nil
	}

	placeholders := make([]string, len(mediaItemIDs))
	args := make([]any, 0, len(mediaItemIDs)+2)
	args = append(args, s.userID, profileID)
	for i, mediaItemID := range mediaItemIDs {
		placeholders[i] = fmt.Sprintf("$%d", i+3)
		args = append(args, mediaItemID)
	}

	rows, err := s.pool.Query(ctx,
		`SELECT media_item_id FROM user_favorites
		 WHERE user_id = $1 AND profile_id = $2 AND media_item_id IN (`+strings.Join(placeholders, ",")+`)`,
		args...,
	)
	if err != nil {
		return nil, fmt.Errorf("listing favorites by media items: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var mediaItemID string
		if err := rows.Scan(&mediaItemID); err != nil {
			return nil, fmt.Errorf("scanning favorite row: %w", err)
		}
		result[mediaItemID] = true
	}
	return result, rows.Err()
}

// --- Watchlist ---

func (s *PostgresUserStore) AddToWatchlist(ctx context.Context, profileID, mediaItemID string) error {
	_, err := s.AddToWatchlistAt(ctx, profileID, mediaItemID, time.Now().UTC())
	return err
}

func (s *PostgresUserStore) AddToWatchlistAt(ctx context.Context, profileID, mediaItemID string, addedAt time.Time) (bool, error) {
	tag, err := s.pool.Exec(ctx,
		`INSERT INTO user_watchlist (user_id, profile_id, media_item_id, added_at)
		 VALUES ($1, $2, $3, $4)
		 ON CONFLICT DO NOTHING`,
		s.userID, profileID, mediaItemID, addedAt.UTC(),
	)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() > 0, nil
}

func (s *PostgresUserStore) RemoveFromWatchlist(ctx context.Context, profileID, mediaItemID string) error {
	_, err := s.pool.Exec(ctx,
		`DELETE FROM user_watchlist WHERE user_id = $1 AND profile_id = $2 AND media_item_id = $3`,
		s.userID, profileID, mediaItemID,
	)
	return err
}

func (s *PostgresUserStore) ListWatchlist(ctx context.Context, profileID string, limit, offset int) ([]userstore.WatchlistEntry, error) {
	// Items with a synced sort_index (mirrored from a provider) come first in
	// that order; locally-added items (sort_index NULL) fall back to newest-first.
	rows, err := s.pool.Query(ctx,
		`SELECT profile_id, media_item_id, added_at FROM user_watchlist
		 WHERE user_id = $1 AND profile_id = $2
		 ORDER BY sort_index ASC NULLS LAST, added_at DESC LIMIT $3 OFFSET $4`,
		s.userID, profileID, limit, offset,
	)
	if err != nil {
		return nil, fmt.Errorf("listing watchlist: %w", err)
	}
	defer rows.Close()

	var entries []userstore.WatchlistEntry
	for rows.Next() {
		var w userstore.WatchlistEntry
		var addedAt time.Time
		if err := rows.Scan(&w.ProfileID, &w.MediaItemID, &addedAt); err != nil {
			return nil, fmt.Errorf("scanning watchlist row: %w", err)
		}
		w.AddedAt = timeToString(addedAt)
		entries = append(entries, w)
	}
	return entries, rows.Err()
}

func (s *PostgresUserStore) InWatchlist(ctx context.Context, profileID, mediaItemID string) (bool, error) {
	var count int
	err := s.pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM user_watchlist WHERE user_id = $1 AND profile_id = $2 AND media_item_id = $3`,
		s.userID, profileID, mediaItemID,
	).Scan(&count)
	return count > 0, err
}

// ReplaceWatchlistOrder mirrors a provider's watchlist order: the given media
// item ids get sort_index 0..N-1 in order, and every other watchlist row for
// the profile is reset to NULL (falling back to added_at ordering). Pass an
// empty slice to clear all synced ordering.
func (s *PostgresUserStore) ReplaceWatchlistOrder(ctx context.Context, profileID string, orderedMediaItemIDs []string) error {
	if len(orderedMediaItemIDs) == 0 {
		// Clear all synced ordering — revert to added_at ordering.
		if _, err := s.pool.Exec(ctx,
			`UPDATE user_watchlist SET sort_index = NULL
			 WHERE user_id = $1 AND profile_id = $2 AND sort_index IS NOT NULL`,
			s.userID, profileID,
		); err != nil {
			return fmt.Errorf("clear watchlist order: %w", err)
		}
		return nil
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin watchlist order tx: %w", err)
	}
	defer tx.Rollback(ctx)

	if _, err := tx.Exec(ctx,
		`UPDATE user_watchlist SET sort_index = NULL
		 WHERE user_id = $1 AND profile_id = $2 AND sort_index IS NOT NULL
		   AND NOT (media_item_id = ANY($3))`,
		s.userID, profileID, orderedMediaItemIDs,
	); err != nil {
		return fmt.Errorf("clear stale watchlist order: %w", err)
	}

	if _, err := tx.Exec(ctx,
		`UPDATE user_watchlist w
		 SET sort_index = v.idx
		 FROM (
		   SELECT unnest($3::text[]) AS media_item_id,
		          generate_subscripts($3::text[], 1) - 1 AS idx
		 ) v
		 WHERE w.user_id = $1 AND w.profile_id = $2 AND w.media_item_id = v.media_item_id`,
		s.userID, profileID, orderedMediaItemIDs,
	); err != nil {
		return fmt.Errorf("apply watchlist order: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit watchlist order: %w", err)
	}
	return nil
}

func (s *PostgresUserStore) RemoveWatchedFromWatchlist(ctx context.Context, profileID string) (bool, error) {
	var enabled bool
	err := s.pool.QueryRow(ctx,
		`SELECT remove_watched_from_watchlist FROM user_profiles WHERE user_id = $1 AND id = $2`,
		s.userID, profileID,
	).Scan(&enabled)
	if errors.Is(err, pgx.ErrNoRows) {
		// Default-on when the profile row is unexpectedly absent.
		return true, nil
	}
	if err != nil {
		return true, fmt.Errorf("get remove_watched_from_watchlist: %w", err)
	}
	return enabled, nil
}

func (s *PostgresUserStore) ListWatchlistByMediaItems(ctx context.Context, profileID string, mediaItemIDs []string) (map[string]bool, error) {
	result := make(map[string]bool, len(mediaItemIDs))
	if len(mediaItemIDs) == 0 {
		return result, nil
	}

	placeholders := make([]string, len(mediaItemIDs))
	args := make([]any, 0, len(mediaItemIDs)+2)
	args = append(args, s.userID, profileID)
	for i, mediaItemID := range mediaItemIDs {
		placeholders[i] = fmt.Sprintf("$%d", i+3)
		args = append(args, mediaItemID)
	}

	rows, err := s.pool.Query(ctx,
		`SELECT media_item_id FROM user_watchlist
		 WHERE user_id = $1 AND profile_id = $2 AND media_item_id IN (`+strings.Join(placeholders, ",")+`)`,
		args...,
	)
	if err != nil {
		return nil, fmt.Errorf("listing watchlist by media items: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var mediaItemID string
		if err := rows.Scan(&mediaItemID); err != nil {
			return nil, fmt.Errorf("scanning watchlist row: %w", err)
		}
		result[mediaItemID] = true
	}
	return result, rows.Err()
}
