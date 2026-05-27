package audiobooks

import (
	"context"
	"errors"
	"fmt"
	"strconv"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/audiobooks/abs"
)

// ABSCollectionStore implements abs.CollectionStore against the canonical
// user_personal_collections + user_personal_collection_items tables
// (migration 156). Manual ABS collections live in user_personal_collections
// with collection_type = 'manual'; their member books are rows in
// user_personal_collection_items with sub_item_id = '' (the empty string
// distinguishes whole-item membership from playlist podcast-episode rows).
//
// abs.Collection.IsPublic maps to user_personal_collections.is_shared.
// profile_id is a text column (NOT NULL DEFAULT '') in the canonical
// schema, so the empty string stands in for "primary profile".
type ABSCollectionStore struct {
	Pool *pgxpool.Pool
}

// Compile-time assertion.
var _ abs.CollectionStore = (*ABSCollectionStore)(nil)

// absCollectionTypeManual is the discriminator value for ABS manual
// collections in the canonical user_personal_collections table.
const absCollectionTypeManual = "manual"

func (s *ABSCollectionStore) ListUserCollections(ctx context.Context, userID, profileID string) ([]abs.Collection, error) {
	uid, err := strconv.Atoi(userID)
	if err != nil {
		return nil, fmt.Errorf("abs_collection_store: invalid user id %q: %w", userID, err)
	}
	rows, err := s.Pool.Query(ctx, `
		SELECT id, user_id, profile_id, name, description, is_shared, created_at, updated_at
		FROM user_personal_collections
		WHERE collection_type = $3
		  AND user_id = $1
		  AND profile_id = $2
		ORDER BY created_at DESC`,
		uid, profileID, absCollectionTypeManual,
	)
	if err != nil {
		return nil, fmt.Errorf("abs_collection_store: list: %w", err)
	}
	defer rows.Close()
	out := make([]abs.Collection, 0)
	for rows.Next() {
		var c abs.Collection
		var uidScan int
		if err := rows.Scan(&c.ID, &uidScan, &c.ProfileID, &c.Name, &c.Description, &c.IsPublic, &c.CreatedAt, &c.UpdatedAt); err != nil {
			return nil, fmt.Errorf("abs_collection_store: list scan: %w", err)
		}
		c.UserID = strconv.Itoa(uidScan)
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("abs_collection_store: list rows: %w", err)
	}
	return out, nil
}

func (s *ABSCollectionStore) GetCollection(ctx context.Context, id string) (abs.Collection, error) {
	var c abs.Collection
	var uidScan int
	row := s.Pool.QueryRow(ctx, `
		SELECT id, user_id, profile_id, name, description, is_shared, created_at, updated_at
		FROM user_personal_collections
		WHERE id = $1 AND collection_type = $2`,
		id, absCollectionTypeManual,
	)
	if err := row.Scan(&c.ID, &uidScan, &c.ProfileID, &c.Name, &c.Description, &c.IsPublic, &c.CreatedAt, &c.UpdatedAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return abs.Collection{}, abs.ErrNotFound
		}
		return abs.Collection{}, fmt.Errorf("abs_collection_store: get: %w", err)
	}
	c.UserID = strconv.Itoa(uidScan)
	return c, nil
}

func (s *ABSCollectionStore) CreateCollection(ctx context.Context, c abs.Collection) error {
	uid, err := strconv.Atoi(c.UserID)
	if err != nil {
		return fmt.Errorf("abs_collection_store: invalid user id %q: %w", c.UserID, err)
	}
	if _, err := s.Pool.Exec(ctx, `
		INSERT INTO user_personal_collections
		    (id, user_id, profile_id, creator_profile_id, name, description,
		     collection_type, is_shared, query_definition, created_at, updated_at)
		VALUES ($1, $2, $3, $3, $4, $5, $6, $7, '{}'::jsonb, now(), now())`,
		c.ID, uid, c.ProfileID, c.Name, c.Description, absCollectionTypeManual, c.IsPublic,
	); err != nil {
		return fmt.Errorf("abs_collection_store: create: %w", err)
	}
	return nil
}

func (s *ABSCollectionStore) UpdateCollection(ctx context.Context, c abs.Collection) error {
	if _, err := s.Pool.Exec(ctx, `
		UPDATE user_personal_collections
		   SET name = $2, description = $3, is_shared = $4, updated_at = now()
		 WHERE id = $1 AND collection_type = $5`,
		c.ID, c.Name, c.Description, c.IsPublic, absCollectionTypeManual,
	); err != nil {
		return fmt.Errorf("abs_collection_store: update: %w", err)
	}
	return nil
}

func (s *ABSCollectionStore) DeleteCollection(ctx context.Context, id string) error {
	// user_personal_collection_items has no FK to user_personal_collections,
	// so cascade is not automatic — drop items first, then the parent.
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("abs_collection_store: begin tx: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	if _, err := tx.Exec(ctx,
		`DELETE FROM user_personal_collection_items WHERE collection_id = $1`,
		id,
	); err != nil {
		return fmt.Errorf("abs_collection_store: delete-items: %w", err)
	}
	if _, err := tx.Exec(ctx,
		`DELETE FROM user_personal_collections WHERE id = $1 AND collection_type = $2`,
		id, absCollectionTypeManual,
	); err != nil {
		return fmt.Errorf("abs_collection_store: delete: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("abs_collection_store: commit: %w", err)
	}
	return nil
}

func (s *ABSCollectionStore) ListCollectionItems(ctx context.Context, collectionID string) ([]abs.CollectionItem, error) {
	rows, err := s.Pool.Query(ctx, `
		SELECT media_item_id, added_at
		FROM user_personal_collection_items
		WHERE collection_id = $1 AND sub_item_id = ''
		ORDER BY position ASC, added_at ASC`,
		collectionID,
	)
	if err != nil {
		return nil, fmt.Errorf("abs_collection_store: list-items: %w", err)
	}
	defer rows.Close()
	out := make([]abs.CollectionItem, 0)
	for rows.Next() {
		var it abs.CollectionItem
		if err := rows.Scan(&it.LibraryItemID, &it.AddedAt); err != nil {
			return nil, fmt.Errorf("abs_collection_store: list-items scan: %w", err)
		}
		it.CollectionID = collectionID
		out = append(out, it)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("abs_collection_store: list-items rows: %w", err)
	}
	return out, nil
}

func (s *ABSCollectionStore) AddCollectionItem(ctx context.Context, collectionID, libraryItemID string) error {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("abs_collection_store: begin tx: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	// user_personal_collection_items.user_id is NOT NULL and has no
	// default; the canonical schema requires the inserter to repeat the
	// owning user_id from the parent. INSERT ... SELECT preserves the
	// silent-no-op semantics of the ABS interface when the parent is
	// missing (zero rows selected → zero rows inserted) and lets the
	// PK ON CONFLICT keep re-adds idempotent.
	if _, err := tx.Exec(ctx, `
		INSERT INTO user_personal_collection_items
		    (user_id, collection_id, media_item_id, sub_item_id, position, added_at)
		SELECT c.user_id, c.id, $2, '',
		       COALESCE((
		           SELECT MAX(i.position) + 1
		           FROM user_personal_collection_items i
		           WHERE i.collection_id = c.id
		       ), 0),
		       now()
		FROM user_personal_collections c
		WHERE c.id = $1 AND c.collection_type = $3
		ON CONFLICT (user_id, collection_id, media_item_id) DO NOTHING`,
		collectionID, libraryItemID, absCollectionTypeManual,
	); err != nil {
		return fmt.Errorf("abs_collection_store: add-item: %w", err)
	}
	if _, err := tx.Exec(ctx,
		`UPDATE user_personal_collections SET updated_at = now() WHERE id = $1 AND collection_type = $2`,
		collectionID, absCollectionTypeManual,
	); err != nil {
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
	defer tx.Rollback(ctx) //nolint:errcheck
	if _, err := tx.Exec(ctx,
		`DELETE FROM user_personal_collection_items
		 WHERE collection_id = $1 AND media_item_id = $2 AND sub_item_id = ''`,
		collectionID, libraryItemID,
	); err != nil {
		return fmt.Errorf("abs_collection_store: remove-item: %w", err)
	}
	if _, err := tx.Exec(ctx,
		`UPDATE user_personal_collections SET updated_at = now() WHERE id = $1 AND collection_type = $2`,
		collectionID, absCollectionTypeManual,
	); err != nil {
		return fmt.Errorf("abs_collection_store: bump-parent: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("abs_collection_store: commit: %w", err)
	}
	return nil
}
