package userdb

import (
	"database/sql"
	"errors"
	"strings"
	"time"

	"github.com/Silo-Server/silo-server/internal/userstore"
)

// ---------- Not interested ----------

// MarkNotInterested records a profile's Not interested mark on a media item
// and reports whether it was new.
func MarkNotInterested(db *sql.DB, profileID, mediaItemID string) (bool, error) {
	result, err := db.Exec(
		`INSERT OR IGNORE INTO not_interested (profile_id, media_item_id, added_at) VALUES (?, ?, ?)`,
		profileID, mediaItemID, time.Now().UTC().Format(time.RFC3339),
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

// UnmarkNotInterested removes a profile's mark and reports whether it was
// there.
func UnmarkNotInterested(db *sql.DB, profileID, mediaItemID string) (bool, error) {
	result, err := db.Exec(
		`DELETE FROM not_interested WHERE profile_id = ? AND media_item_id = ?`,
		profileID, mediaItemID,
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

// GetNotInterested answers a profile's mark on a media item, nil when the
// item is not marked.
func GetNotInterested(db *sql.DB, profileID, mediaItemID string) (*userstore.NotInterestedEntry, error) {
	var e userstore.NotInterestedEntry
	err := db.QueryRow(
		`SELECT profile_id, media_item_id, added_at FROM not_interested WHERE profile_id = ? AND media_item_id = ?`,
		profileID, mediaItemID,
	).Scan(&e.ProfileID, &e.MediaItemID, &e.AddedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &e, nil
}

// ListNotInterestedPage pages by keyset like ListFavoritesPage.
func ListNotInterestedPage(db *sql.DB, profileID string, after *userstore.ListKey, limit int) ([]userstore.NotInterestedEntry, error) {
	query, args := listKeysetQuery(`SELECT profile_id, media_item_id, added_at FROM not_interested`, profileID, after, limit)
	rows, err := db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var entries []userstore.NotInterestedEntry
	for rows.Next() {
		var e userstore.NotInterestedEntry
		if err := rows.Scan(&e.ProfileID, &e.MediaItemID, &e.AddedAt); err != nil {
			return nil, err
		}
		entries = append(entries, e)
	}
	return entries, rows.Err()
}

// ListNotInterestedByMediaItems reports which of mediaItemIDs the profile
// marked.
func ListNotInterestedByMediaItems(db *sql.DB, profileID string, mediaItemIDs []string) (map[string]bool, error) {
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
		`SELECT media_item_id FROM not_interested WHERE profile_id = ? AND media_item_id IN (`+strings.Join(placeholders, ",")+`)`,
		args...,
	)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var mediaItemID string
		if err := rows.Scan(&mediaItemID); err != nil {
			return nil, err
		}
		result[mediaItemID] = true
	}
	return result, rows.Err()
}
