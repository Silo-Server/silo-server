package userdb

import (
	"database/sql"
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
	allowedProfiles := normalizeAllowedProfiles(input.CreatorProfileID, input.AllowedProfileIDs, input.IsShared)

	tx, err := db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	_, err = tx.Exec(
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
	for _, profileID := range allowedProfiles {
		if _, err := tx.Exec(
			`INSERT INTO personal_collection_profiles (collection_id, profile_id) VALUES (?, ?)`,
			id, profileID,
		); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}

	return &Collection{
		ID:                id,
		ProfileID:         input.CreatorProfileID,
		CreatorProfileID:  input.CreatorProfileID,
		Name:              input.Name,
		CollectionType:    input.CollectionType,
		IsShared:          input.IsShared,
		AllowedProfileIDs: allowedProfiles,
		QueryDefinition:   input.QueryDefinition,
		SortConfig:        input.SortConfig,
		CreatedAt:         now,
		UpdatedAt:         now,
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
	if err != nil {
		return nil, err
	}
	c.IsShared = isShared
	c.AllowedProfileIDs, err = listCollectionProfiles(db, id)
	if err != nil {
		return nil, err
	}
	return &c, nil
}

// ListCollections returns all collections for a given profile, ordered by creation date.
func ListCollections(db *sql.DB, profileID string) ([]Collection, error) {
	rows, err := db.Query(
		`SELECT pc.id, pc.profile_id, pc.creator_profile_id, pc.name, pc.collection_type, pc.is_shared,
		        pc.query_definition, pc.sort_config, pc.created_at, pc.updated_at
		 FROM personal_collections pc
		 JOIN personal_collection_profiles pcp ON pcp.collection_id = pc.id
		 WHERE pcp.profile_id = ? ORDER BY pc.created_at ASC`,
		profileID,
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
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := attachCollectionProfiles(db, profileID, collections); err != nil {
		return nil, err
	}
	return collections, nil
}

// attachCollectionProfiles fills AllowedProfileIDs for every collection with a
// single batched query, avoiding the N+1 round trip a per-collection lookup
// would create.
func attachCollectionProfiles(db *sql.DB, profileID string, collections []Collection) error {
	if len(collections) == 0 {
		return nil
	}
	rows, err := db.Query(
		`SELECT allowed.collection_id, allowed.profile_id
		 FROM personal_collection_profiles AS visible
		 JOIN personal_collection_profiles AS allowed
		   ON allowed.collection_id = visible.collection_id
		 WHERE visible.profile_id = ?
		 ORDER BY allowed.profile_id ASC`,
		profileID,
	)
	if err != nil {
		return err
	}
	defer rows.Close()

	byCollection := make(map[string][]string, len(collections))
	for rows.Next() {
		var collectionID, profileID string
		if err := rows.Scan(&collectionID, &profileID); err != nil {
			return err
		}
		byCollection[collectionID] = append(byCollection[collectionID], profileID)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for i := range collections {
		collections[i].AllowedProfileIDs = byCollection[collections[i].ID]
	}
	return nil
}

// UpdateCollection renames a collection and updates its updated_at timestamp.
func UpdateCollection(db *sql.DB, input userstore.UpdateCollectionInput) error {
	var creatorProfileID string
	if err := db.QueryRow(`SELECT creator_profile_id FROM personal_collections WHERE id = ?`, input.ID).Scan(&creatorProfileID); err != nil {
		return err
	}
	if creatorProfileID != input.RequestProfileID {
		return fmt.Errorf("only the creator can update this collection")
	}

	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

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
	if input.AllowedProfileIDs != nil || input.IsShared != nil {
		isShared := false
		if input.IsShared != nil {
			isShared = *input.IsShared
		} else {
			if err := tx.QueryRow(`SELECT is_shared FROM personal_collections WHERE id = ?`, input.ID).Scan(&isShared); err != nil {
				return err
			}
		}
		allowed := []string{}
		if input.AllowedProfileIDs != nil {
			allowed = *input.AllowedProfileIDs
		} else {
			allowed, err = listCollectionProfilesTx(tx, input.ID)
			if err != nil {
				return err
			}
		}
		allowed = normalizeAllowedProfiles(creatorProfileID, allowed, isShared)
		if _, err := tx.Exec(`DELETE FROM personal_collection_profiles WHERE collection_id = ?`, input.ID); err != nil {
			return err
		}
		for _, profileID := range allowed {
			if _, err := tx.Exec(`INSERT INTO personal_collection_profiles (collection_id, profile_id) VALUES (?, ?)`, input.ID, profileID); err != nil {
				return err
			}
		}
	}

	return tx.Commit()
}

// DeleteCollection removes a collection and all of its items.
func DeleteCollection(db *sql.DB, id string) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.Exec(`DELETE FROM personal_collection_items WHERE collection_id = ?`, id); err != nil {
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

func listCollectionProfiles(db *sql.DB, collectionID string) ([]string, error) {
	rows, err := db.Query(
		`SELECT profile_id FROM personal_collection_profiles WHERE collection_id = ? ORDER BY profile_id ASC`,
		collectionID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanCollectionProfiles(rows)
}

func listCollectionProfilesTx(tx *sql.Tx, collectionID string) ([]string, error) {
	rows, err := tx.Query(
		`SELECT profile_id FROM personal_collection_profiles WHERE collection_id = ? ORDER BY profile_id ASC`,
		collectionID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanCollectionProfiles(rows)
}

func scanCollectionProfiles(rows *sql.Rows) ([]string, error) {
	var profiles []string
	for rows.Next() {
		var profileID string
		if err := rows.Scan(&profileID); err != nil {
			return nil, err
		}
		profiles = append(profiles, profileID)
	}
	return profiles, rows.Err()
}

func normalizeAllowedProfiles(creatorProfileID string, allowedProfiles []string, isShared bool) []string {
	if !isShared {
		return []string{creatorProfileID}
	}
	seen := map[string]struct{}{creatorProfileID: {}}
	normalized := []string{creatorProfileID}
	for _, profileID := range allowedProfiles {
		if profileID == "" {
			continue
		}
		if _, ok := seen[profileID]; ok {
			continue
		}
		seen[profileID] = struct{}{}
		normalized = append(normalized, profileID)
	}
	return normalized
}
