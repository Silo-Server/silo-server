package userdb

import (
	"database/sql"
	"strings"

	"github.com/Silo-Server/silo-server/internal/userstore"
)

// HiddenRecommendation is an alias for the canonical type in userstore.
type HiddenRecommendation = userstore.HiddenRecommendation

// ---------- Hidden recommendations ("not interested") ----------

// AddHiddenRecommendation marks a media item as "not interested" for a profile.
// If the item is already hidden, the operation is a no-op.
func AddHiddenRecommendation(db *sql.DB, profileID, mediaItemID string) error {
	_, err := db.Exec(
		`INSERT OR IGNORE INTO hidden_recommendations (profile_id, media_item_id, hidden_at) VALUES (?, ?, ?)`,
		profileID, mediaItemID, nowUTC(),
	)
	return err
}

// RemoveHiddenRecommendation un-hides a media item for a profile.
func RemoveHiddenRecommendation(db *sql.DB, profileID, mediaItemID string) error {
	_, err := db.Exec(
		`DELETE FROM hidden_recommendations WHERE profile_id = ? AND media_item_id = ?`,
		profileID, mediaItemID,
	)
	return err
}

// ListHiddenRecommendations returns a paginated list of hidden items for a
// profile, ordered by most-recently-hidden first.
func ListHiddenRecommendations(db *sql.DB, profileID string, limit, offset int) ([]HiddenRecommendation, error) {
	rows, err := db.Query(
		`SELECT profile_id, media_item_id, hidden_at FROM hidden_recommendations
		 WHERE profile_id = ? ORDER BY hidden_at DESC LIMIT ? OFFSET ?`,
		profileID, limit, offset,
	)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var hidden []HiddenRecommendation
	for rows.Next() {
		var h HiddenRecommendation
		if err := rows.Scan(&h.ProfileID, &h.MediaItemID, &h.HiddenAt); err != nil {
			return nil, err
		}
		hidden = append(hidden, h)
	}
	return hidden, rows.Err()
}

// IsHiddenRecommendation checks whether a media item is hidden for a profile.
func IsHiddenRecommendation(db *sql.DB, profileID, mediaItemID string) (bool, error) {
	var count int
	err := db.QueryRow(
		`SELECT COUNT(*) FROM hidden_recommendations WHERE profile_id = ? AND media_item_id = ?`,
		profileID, mediaItemID,
	).Scan(&count)
	return count > 0, err
}

func ListHiddenRecommendationsByMediaItems(db *sql.DB, profileID string, mediaItemIDs []string) (map[string]bool, error) {
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
		`SELECT media_item_id FROM hidden_recommendations WHERE profile_id = ? AND media_item_id IN (`+strings.Join(placeholders, ",")+`)`,
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

func HiddenRecommendationIDSet(db *sql.DB, profileID string) (map[string]struct{}, error) {
	rows, err := db.Query(
		`SELECT media_item_id FROM hidden_recommendations WHERE profile_id = ?`,
		profileID,
	)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	result := make(map[string]struct{})
	for rows.Next() {
		var mediaItemID string
		if err := rows.Scan(&mediaItemID); err != nil {
			return nil, err
		}
		result[mediaItemID] = struct{}{}
	}
	return result, rows.Err()
}
