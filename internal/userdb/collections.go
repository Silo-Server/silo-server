package userdb

import (
	"database/sql"
	"errors"
	"fmt"

	"github.com/Silo-Server/silo-server/internal/userstore"
)

// Collection is an alias for the canonical type in userstore.
type Collection = userstore.Collection

// CollectionItem is an alias for the canonical type in userstore.
type CollectionItem = userstore.CollectionItem

// CreateCollection creates a new personal collection with a generated UUID.
func CreateCollection(db *sql.DB, input userstore.CreateCollectionInput) (*Collection, error) {
	id := generateUUID()
	now := nowUTC()

	if input.CollectionType == "" {
		input.CollectionType = "manual"
	}
	if input.QueryDefinition == "" {
		input.QueryDefinition = "{}"
	}
	if input.SortConfig == "" {
		input.SortConfig = "{}"
	}
	_, err := db.Exec(
		`INSERT INTO personal_collections (
			id, profile_id, creator_profile_id, name, collection_type, is_shared,
			query_definition, sort_config, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		id, input.CreatorProfileID, input.CreatorProfileID, input.Name, input.CollectionType, input.IsShared,
		input.QueryDefinition, input.SortConfig, now, now,
	)
	if err != nil {
		return nil, err
	}

	return &Collection{
		ID:               id,
		ProfileID:        input.CreatorProfileID,
		CreatorProfileID: input.CreatorProfileID,
		Name:             input.Name,
		CollectionType:   input.CollectionType,
		IsShared:         input.IsShared,
		QueryDefinition:  input.QueryDefinition,
		SortConfig:       input.SortConfig,
		CreatedAt:        now,
		UpdatedAt:        now,
	}, nil
}

// GetCollection retrieves a collection by its ID.
func GetCollection(db *sql.DB, id string) (*Collection, error) {
	var c Collection
	var isShared bool
	err := db.QueryRow(
		`SELECT id, profile_id, creator_profile_id, name, collection_type, is_shared, query_definition, sort_config, created_at, updated_at
		 FROM personal_collections WHERE id = ?`,
		id,
	).Scan(&c.ID, &c.ProfileID, &c.CreatorProfileID, &c.Name, &c.CollectionType, &isShared, &c.QueryDefinition, &c.SortConfig, &c.CreatedAt, &c.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("collection %s: %w", id, userstore.ErrCollectionNotFound)
	}
	if err != nil {
		return nil, err
	}
	c.IsShared = isShared
	return &c, nil
}

// ListCollections returns the collections profileID may see: its own, then
// other profiles' shared collections grouped by creator, each by creation date.
func ListCollections(db *sql.DB, profileID string) ([]Collection, error) {
	rows, err := db.Query(
		`SELECT id, profile_id, creator_profile_id, name, collection_type, is_shared,
		        query_definition, sort_config, created_at, updated_at
		 FROM personal_collections
		 WHERE creator_profile_id = ? OR is_shared
		 ORDER BY creator_profile_id = ? DESC, creator_profile_id ASC, created_at ASC, id ASC`,
		profileID, profileID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var collections []Collection
	for rows.Next() {
		var c Collection
		if err := rows.Scan(&c.ID, &c.ProfileID, &c.CreatorProfileID, &c.Name, &c.CollectionType, &c.IsShared, &c.QueryDefinition, &c.SortConfig, &c.CreatedAt, &c.UpdatedAt); err != nil {
			return nil, err
		}
		collections = append(collections, c)
	}
	return collections, rows.Err()
}

// UpdateCollection renames a collection and updates its updated_at timestamp.
func UpdateCollection(db *sql.DB, input userstore.UpdateCollectionInput) error {
	if input.SourceConfigPatch != nil {
		return fmt.Errorf("user collection imports are not supported on the SQLite user store")
	}

	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := checkSQLiteCollectionRevision(tx, input.ID, input.ExpectedRevision); err != nil {
		return err
	}

	var creatorProfileID string
	if err := tx.QueryRow(`SELECT creator_profile_id FROM personal_collections WHERE id = ?`, input.ID).Scan(&creatorProfileID); err != nil {
		return err
	}
	if creatorProfileID != input.RequestProfileID {
		return fmt.Errorf("only the creator can update this collection")
	}

	now := nowUTC()
	if input.Name != nil {
		if _, err := tx.Exec(`UPDATE personal_collections SET name = ?, updated_at = ? WHERE id = ?`, *input.Name, now, input.ID); err != nil {
			return err
		}
	}
	if input.IsShared != nil {
		if _, err := tx.Exec(`UPDATE personal_collections SET is_shared = ?, updated_at = ? WHERE id = ?`, *input.IsShared, now, input.ID); err != nil {
			return err
		}
	}
	if input.QueryDefinition != nil {
		if _, err := tx.Exec(`UPDATE personal_collections SET query_definition = ?, updated_at = ? WHERE id = ?`, *input.QueryDefinition, now, input.ID); err != nil {
			return err
		}
	}
	if input.SortConfig != nil {
		if _, err := tx.Exec(`UPDATE personal_collections SET sort_config = ?, updated_at = ? WHERE id = ?`, *input.SortConfig, now, input.ID); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// DeleteCollection removes a collection and all of its items.
func DeleteCollection(db *sql.DB, id string) error { return deleteCollection(db, id, nil) }
func deleteCollection(db *sql.DB, id string, expected *int64) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := checkSQLiteCollectionRevision(tx, id, expected); err != nil {
		return err
	}

	if _, err := tx.Exec(`DELETE FROM personal_collection_items WHERE collection_id = ?`, id); err != nil {
		return err
	}
	if _, err := tx.Exec(`
		DELETE FROM collection_sort_preferences
		WHERE collection_kind = 'user' AND collection_id = ?`, id); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM personal_collections WHERE id = ?`, id); err != nil {
		return err
	}

	return tx.Commit()
}

// AddCollectionItem adds a media item to a collection at the given position.
// If the item already exists in the collection, the operation is a no-op.
func AddCollectionItem(db *sql.DB, collectionID, mediaItemID string, position int) error {
	_, err := db.Exec(
		`INSERT OR IGNORE INTO personal_collection_items (collection_id, media_item_id, position, added_at) VALUES (?, ?, ?, ?)`,
		collectionID, mediaItemID, position, nowUTC(),
	)
	return err
}

// RemoveCollectionItem removes a media item from a collection.
func RemoveCollectionItem(db *sql.DB, collectionID, mediaItemID string) error {
	_, err := db.Exec(
		`DELETE FROM personal_collection_items WHERE collection_id = ? AND media_item_id = ?`,
		collectionID, mediaItemID,
	)
	return err
}

// ListCollectionItems returns all items in a collection, ordered by position ascending.
func ListCollectionItems(db *sql.DB, collectionID string) ([]CollectionItem, error) {
	rows, err := db.Query(
		`SELECT collection_id, media_item_id, position, added_at FROM personal_collection_items
		 WHERE collection_id = ? ORDER BY position ASC`,
		collectionID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var items []CollectionItem
	for rows.Next() {
		var ci CollectionItem
		if err := rows.Scan(&ci.CollectionID, &ci.MediaItemID, &ci.Position, &ci.AddedAt); err != nil {
			return nil, err
		}
		items = append(items, ci)
	}
	return items, rows.Err()
}
