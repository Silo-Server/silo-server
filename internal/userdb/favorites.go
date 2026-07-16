package userdb

import (
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/Silo-Server/silo-server/internal/userstore"
)

// Favorite is an alias for the canonical type in userstore.
type Favorite = userstore.Favorite

// WatchlistEntry is an alias for the canonical type in userstore.
type WatchlistEntry = userstore.WatchlistEntry

// ---------- Favorites ----------

// AddFavorite adds a media item to a profile's favorites.
// If the item is already a favorite, the operation is a no-op.
func AddFavorite(db *sql.DB, profileID, mediaItemID string) error {
	_, err := AddFavoriteAt(db, profileID, mediaItemID, time.Now().UTC())
	return err
}

func AddFavoriteAt(db *sql.DB, profileID, mediaItemID string, addedAt time.Time) (bool, error) {
	result, err := db.Exec(
		`INSERT OR IGNORE INTO favorites (profile_id, media_item_id, added_at) VALUES (?, ?, ?)`,
		profileID, mediaItemID, addedAt.UTC().Format(time.RFC3339),
	)
	if err != nil {
		return false, err
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return false, err
	}
	return rows > 0, nil
}

// RemoveFavorite removes a media item from a profile's favorites.
func RemoveFavorite(db *sql.DB, profileID, mediaItemID string) error {
	_, err := db.Exec(
		`DELETE FROM favorites WHERE profile_id = ? AND media_item_id = ?`,
		profileID, mediaItemID,
	)
	return err
}

// ListFavorites returns a paginated list of favorites for a profile,
// ordered by most-recently-added first.
func ListFavorites(db *sql.DB, profileID string, limit, offset int) ([]Favorite, error) {
	rows, err := db.Query(
		`SELECT profile_id, media_item_id, added_at FROM favorites
		 WHERE profile_id = ? ORDER BY added_at DESC LIMIT ? OFFSET ?`,
		profileID, limit, offset,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var favorites []Favorite
	for rows.Next() {
		var f Favorite
		if err := rows.Scan(&f.ProfileID, &f.MediaItemID, &f.AddedAt); err != nil {
			return nil, err
		}
		favorites = append(favorites, f)
	}
	return favorites, rows.Err()
}

// IsFavorite checks whether a media item is in a profile's favorites.
func IsFavorite(db *sql.DB, profileID, mediaItemID string) (bool, error) {
	var count int
	err := db.QueryRow(
		`SELECT COUNT(*) FROM favorites WHERE profile_id = ? AND media_item_id = ?`,
		profileID, mediaItemID,
	).Scan(&count)
	return count > 0, err
}

func ListFavoritesByMediaItems(db *sql.DB, profileID string, mediaItemIDs []string) (map[string]bool, error) {
	result := make(map[string]bool, len(mediaItemIDs))
	if len(mediaItemIDs) == 0 {
		return result, nil
	}

	placeholders := make([]string, len(mediaItemIDs))
	args := make([]any, 0, len(mediaItemIDs)+1)
	args = append(args, profileID)
	for i, mediaItemID := range mediaItemIDs {
		placeholders[i] = "?"
		args = append(args, mediaItemID)
	}

	rows, err := db.Query(
		`SELECT media_item_id FROM favorites WHERE profile_id = ? AND media_item_id IN (`+strings.Join(placeholders, ",")+`)`,
		args...,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	for rows.Next() {
		var mediaItemID string
		if err := rows.Scan(&mediaItemID); err != nil {
			return nil, err
		}
		result[mediaItemID] = true
	}
	return result, rows.Err()
}

// ---------- Watchlist ----------

// AddToWatchlist adds a media item to a profile's watchlist.
// If the item is already on the watchlist, the operation is a no-op.
func AddToWatchlist(db *sql.DB, profileID, mediaItemID string) error {
	_, err := AddToWatchlistAt(db, profileID, mediaItemID, time.Now().UTC())
	return err
}

// AddToWatchlistAt adds a media item to a profile's watchlist with an explicit
// added-at timestamp (used when importing from an external provider).
func AddToWatchlistAt(db *sql.DB, profileID, mediaItemID string, addedAt time.Time) (bool, error) {
	result, err := db.Exec(
		`INSERT OR IGNORE INTO watchlist (profile_id, media_item_id, added_at) VALUES (?, ?, ?)`,
		profileID, mediaItemID, addedAt.UTC().Format(time.RFC3339),
	)
	if err != nil {
		return false, err
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return false, err
	}
	return rows > 0, nil
}

// RemoveFromWatchlist removes a media item from a profile's watchlist.
func RemoveFromWatchlist(db *sql.DB, profileID, mediaItemID string) error {
	_, err := db.Exec(
		`DELETE FROM watchlist WHERE profile_id = ? AND media_item_id = ?`,
		profileID, mediaItemID,
	)
	return err
}

// ReplaceWatchlistOrder assigns sort_index 0..N-1 to the given ids in order and
// clears sort_index on every other watchlist row for the profile. An empty
// slice clears all synced ordering.
func ReplaceWatchlistOrder(db *sql.DB, profileID string, orderedMediaItemIDs []string) error {
	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("begin watchlist order tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.Exec(
		`UPDATE watchlist SET sort_index = NULL WHERE profile_id = ? AND sort_index IS NOT NULL`,
		profileID,
	); err != nil {
		return fmt.Errorf("clear watchlist order: %w", err)
	}
	for idx, mediaItemID := range orderedMediaItemIDs {
		if _, err := tx.Exec(
			`UPDATE watchlist SET sort_index = ? WHERE profile_id = ? AND media_item_id = ?`,
			idx, profileID, mediaItemID,
		); err != nil {
			return fmt.Errorf("apply watchlist order: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit watchlist order: %w", err)
	}
	return nil
}

// ListWatchlist returns a paginated list of watchlist entries for a profile.
// Items with a synced sort_index (mirrored from a provider) come first in that
// order; locally-added items (sort_index NULL) fall back to newest-first.
func ListWatchlist(db *sql.DB, profileID string, limit, offset int) ([]WatchlistEntry, error) {
	rows, err := db.Query(
		`SELECT profile_id, media_item_id, added_at FROM watchlist
		 WHERE profile_id = ?
		 ORDER BY sort_index IS NULL, sort_index ASC, added_at DESC LIMIT ? OFFSET ?`,
		profileID, limit, offset,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var entries []WatchlistEntry
	for rows.Next() {
		var w WatchlistEntry
		if err := rows.Scan(&w.ProfileID, &w.MediaItemID, &w.AddedAt); err != nil {
			return nil, err
		}
		entries = append(entries, w)
	}
	return entries, rows.Err()
}

// InWatchlist checks whether a media item is on a profile's watchlist.
func InWatchlist(db *sql.DB, profileID, mediaItemID string) (bool, error) {
	var count int
	err := db.QueryRow(
		`SELECT COUNT(*) FROM watchlist WHERE profile_id = ? AND media_item_id = ?`,
		profileID, mediaItemID,
	).Scan(&count)
	return count > 0, err
}

func ListWatchlistByMediaItems(db *sql.DB, profileID string, mediaItemIDs []string) (map[string]bool, error) {
	result := make(map[string]bool, len(mediaItemIDs))
	if len(mediaItemIDs) == 0 {
		return result, nil
	}

	placeholders := make([]string, len(mediaItemIDs))
	args := make([]any, 0, len(mediaItemIDs)+1)
	args = append(args, profileID)
	for i, mediaItemID := range mediaItemIDs {
		placeholders[i] = "?"
		args = append(args, mediaItemID)
	}

	rows, err := db.Query(
		`SELECT media_item_id FROM watchlist WHERE profile_id = ? AND media_item_id IN (`+strings.Join(placeholders, ",")+`)`,
		args...,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	for rows.Next() {
		var mediaItemID string
		if err := rows.Scan(&mediaItemID); err != nil {
			return nil, err
		}
		result[mediaItemID] = true
	}
	return result, rows.Err()
}
