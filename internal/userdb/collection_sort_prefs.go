package userdb

import (
	"database/sql"
	"fmt"

	"github.com/Silo-Server/silo-server/internal/userstore"
)

// CollectionSortPreference is an alias for the canonical type in userstore.
type CollectionSortPreference = userstore.CollectionSortPreference

// SetCollectionSortPreference creates or replaces a profile's sort override for
// a collection. Timestamps should be ISO 8601 UTC strings.
func SetCollectionSortPreference(db *sql.DB, pref CollectionSortPreference) error {
	if pref.UpdatedAt == "" {
		pref.UpdatedAt = nowUTC()
	}
	_, err := db.Exec(`
		INSERT INTO collection_sort_preferences (
			profile_id, collection_kind, collection_id, sort_field, sort_order, updated_at
		) VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(profile_id, collection_kind, collection_id) DO UPDATE SET
			sort_field = excluded.sort_field,
			sort_order = excluded.sort_order,
			updated_at = excluded.updated_at`,
		pref.ProfileID,
		pref.CollectionKind,
		pref.CollectionID,
		pref.SortField,
		pref.SortOrder,
		pref.UpdatedAt,
	)
	if err != nil {
		return fmt.Errorf("setting collection sort preference for profile %q collection %q/%q: %w",
			pref.ProfileID, pref.CollectionKind, pref.CollectionID, err)
	}
	return nil
}

// GetCollectionSortPreference returns nil (not an error) when the profile has
// never changed this collection's sort. A preference with an empty SortField
// means the viewer explicitly chose the collection's own source order.
func GetCollectionSortPreference(db *sql.DB, profileID, collectionKind, collectionID string) (*CollectionSortPreference, error) {
	var pref CollectionSortPreference
	err := db.QueryRow(`
		SELECT profile_id, collection_kind, collection_id, sort_field, sort_order, updated_at
		FROM collection_sort_preferences
		WHERE profile_id = ? AND collection_kind = ? AND collection_id = ?`,
		profileID, collectionKind, collectionID,
	).Scan(
		&pref.ProfileID,
		&pref.CollectionKind,
		&pref.CollectionID,
		&pref.SortField,
		&pref.SortOrder,
		&pref.UpdatedAt,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("getting collection sort preference for profile %q collection %q/%q: %w",
			profileID, collectionKind, collectionID, err)
	}
	return &pref, nil
}

// ClearCollectionSortPreference drops the override so the collection falls back
// to its creator-configured default.
func ClearCollectionSortPreference(db *sql.DB, profileID, collectionKind, collectionID string) error {
	_, err := db.Exec(`
		DELETE FROM collection_sort_preferences
		WHERE profile_id = ? AND collection_kind = ? AND collection_id = ?`,
		profileID, collectionKind, collectionID,
	)
	if err != nil {
		return fmt.Errorf("clearing collection sort preference for profile %q collection %q/%q: %w",
			profileID, collectionKind, collectionID, err)
	}
	return nil
}
