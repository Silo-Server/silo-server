package audiobooks

import (
	"context"
	"fmt"
	"strconv"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/audiobooks/abs"
)

// ABSCollectionStore implements abs.CollectionStore against the
// abs_user_collections + abs_collection_items tables (migrations
// 149 + 150). One row per collection in the parent table; one row
// per (collection_id, library_item_id) in the items table.
type ABSCollectionStore struct {
	Pool *pgxpool.Pool
}

// Compile-time assertion.
var _ abs.CollectionStore = (*ABSCollectionStore)(nil)

func (s *ABSCollectionStore) ListUserCollections(ctx context.Context, userID, profileID string) ([]abs.Collection, error) {
	uid, err := strconv.Atoi(userID)
	if err != nil {
		return nil, fmt.Errorf("abs_collection_store: invalid user id %q: %w", userID, err)
	}
	rows, err := s.Pool.Query(ctx, `
		SELECT id, user_id, profile_id, name, description, is_public, created_at, updated_at
		FROM abs_user_collections
		WHERE user_id = $1
		  AND COALESCE(profile_id, '00000000-0000-0000-0000-000000000000'::uuid)
		      = COALESCE($2::uuid, '00000000-0000-0000-0000-000000000000'::uuid)
		ORDER BY created_at DESC`,
		uid, profileArg(profileID),
	)
	if err != nil {
		return nil, fmt.Errorf("abs_collection_store: list: %w", err)
	}
	defer rows.Close()
	out := make([]abs.Collection, 0)
	for rows.Next() {
		var c abs.Collection
		var uidScan int
		var profileScan *string
		if err := rows.Scan(&c.ID, &uidScan, &profileScan, &c.Name, &c.Description, &c.IsPublic, &c.CreatedAt, &c.UpdatedAt); err != nil {
			return nil, fmt.Errorf("abs_collection_store: list scan: %w", err)
		}
		c.UserID = strconv.Itoa(uidScan)
		if profileScan != nil {
			c.ProfileID = *profileScan
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (s *ABSCollectionStore) GetCollection(ctx context.Context, id string) (abs.Collection, error) {
	var c abs.Collection
	var uidScan int
	var profileScan *string
	row := s.Pool.QueryRow(ctx, `
		SELECT id, user_id, profile_id, name, description, is_public, created_at, updated_at
		FROM abs_user_collections WHERE id = $1`, id)
	if err := row.Scan(&c.ID, &uidScan, &profileScan, &c.Name, &c.Description, &c.IsPublic, &c.CreatedAt, &c.UpdatedAt); err != nil {
		if err.Error() == "no rows in result set" {
			return abs.Collection{}, abs.ErrNotFound
		}
		return abs.Collection{}, fmt.Errorf("abs_collection_store: get: %w", err)
	}
	c.UserID = strconv.Itoa(uidScan)
	if profileScan != nil {
		c.ProfileID = *profileScan
	}
	return c, nil
}

func (s *ABSCollectionStore) CreateCollection(ctx context.Context, c abs.Collection) error {
	uid, err := strconv.Atoi(c.UserID)
	if err != nil {
		return fmt.Errorf("abs_collection_store: invalid user id %q: %w", c.UserID, err)
	}
	if _, err := s.Pool.Exec(ctx, `
		INSERT INTO abs_user_collections (id, user_id, profile_id, name, description, is_public)
		VALUES ($1, $2, $3::uuid, $4, $5, $6)`,
		c.ID, uid, profileArg(c.ProfileID), c.Name, c.Description, c.IsPublic,
	); err != nil {
		return fmt.Errorf("abs_collection_store: create: %w", err)
	}
	return nil
}

func (s *ABSCollectionStore) UpdateCollection(ctx context.Context, c abs.Collection) error {
	if _, err := s.Pool.Exec(ctx, `
		UPDATE abs_user_collections
		   SET name = $2, description = $3, is_public = $4, updated_at = now()
		 WHERE id = $1`,
		c.ID, c.Name, c.Description, c.IsPublic,
	); err != nil {
		return fmt.Errorf("abs_collection_store: update: %w", err)
	}
	return nil
}

func (s *ABSCollectionStore) DeleteCollection(ctx context.Context, id string) error {
	if _, err := s.Pool.Exec(ctx, `DELETE FROM abs_user_collections WHERE id = $1`, id); err != nil {
		return fmt.Errorf("abs_collection_store: delete: %w", err)
	}
	return nil
}

func (s *ABSCollectionStore) ListCollectionItems(ctx context.Context, collectionID string) ([]abs.CollectionItem, error) {
	rows, err := s.Pool.Query(ctx, `
		SELECT collection_id, library_item_id, added_at
		FROM abs_collection_items
		WHERE collection_id = $1
		ORDER BY added_at ASC`, collectionID)
	if err != nil {
		return nil, fmt.Errorf("abs_collection_store: list-items: %w", err)
	}
	defer rows.Close()
	out := make([]abs.CollectionItem, 0)
	for rows.Next() {
		var it abs.CollectionItem
		if err := rows.Scan(&it.CollectionID, &it.LibraryItemID, &it.AddedAt); err != nil {
			return nil, fmt.Errorf("abs_collection_store: list-items scan: %w", err)
		}
		out = append(out, it)
	}
	return out, rows.Err()
}

func (s *ABSCollectionStore) AddCollectionItem(ctx context.Context, collectionID, libraryItemID string) error {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("abs_collection_store: begin tx: %w", err)
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `
		INSERT INTO abs_collection_items (collection_id, library_item_id)
		VALUES ($1, $2)
		ON CONFLICT (collection_id, library_item_id) DO NOTHING`,
		collectionID, libraryItemID,
	); err != nil {
		return fmt.Errorf("abs_collection_store: add-item: %w", err)
	}
	if _, err := tx.Exec(ctx, `UPDATE abs_user_collections SET updated_at = now() WHERE id = $1`, collectionID); err != nil {
		return fmt.Errorf("abs_collection_store: bump-parent: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("abs_collection_store: commit: %w", err)
	}
	return nil
}

func (s *ABSCollectionStore) RemoveCollectionItem(ctx context.Context, collectionID, libraryItemID string) error {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("abs_collection_store: begin tx: %w", err)
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `DELETE FROM abs_collection_items WHERE collection_id = $1 AND library_item_id = $2`,
		collectionID, libraryItemID,
	); err != nil {
		return fmt.Errorf("abs_collection_store: remove-item: %w", err)
	}
	if _, err := tx.Exec(ctx, `UPDATE abs_user_collections SET updated_at = now() WHERE id = $1`, collectionID); err != nil {
		return fmt.Errorf("abs_collection_store: bump-parent: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("abs_collection_store: commit: %w", err)
	}
	return nil
}
