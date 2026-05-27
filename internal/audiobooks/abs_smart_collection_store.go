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

// ABSSmartCollectionStore implements abs.SmartCollectionStore against the
// canonical user_personal_collections table (migration 156). ABS smart
// collections live in user_personal_collections with collection_type =
// 'smart'; the rule DSL is stored in the query_definition jsonb column
// (formerly abs_smart_collections.query_def). Smart collections have no
// membership rows — items are materialised at request time by the
// smartcoll engine, so user_personal_collection_items is unused for
// collection_type = 'smart'.
//
// abs.SmartCollection.IsPublic maps to user_personal_collections.is_shared.
// profile_id is a text column (NOT NULL DEFAULT '') in the canonical
// schema, so the empty string stands in for "primary profile".
//
// abs.SmartCollection.Color and abs.SmartCollection.IsPinned have no
// canonical columns (deferred per spec §6); reads always return the zero
// value and writes ignore them.
type ABSSmartCollectionStore struct {
	Pool *pgxpool.Pool
}

var _ abs.SmartCollectionStore = (*ABSSmartCollectionStore)(nil)

// absCollectionTypeSmart is the discriminator value for ABS smart
// collections in the canonical user_personal_collections table.
const absCollectionTypeSmart = "smart"

func (s *ABSSmartCollectionStore) ListUserSmartCollections(ctx context.Context, userID, profileID string) ([]abs.SmartCollection, error) {
	uid, err := strconv.Atoi(userID)
	if err != nil {
		return nil, fmt.Errorf("abs_smart_collection_store: invalid user id %q: %w", userID, err)
	}
	rows, err := s.Pool.Query(ctx, `
		SELECT id, user_id, profile_id, name, description, is_shared, query_definition, created_at, updated_at
		FROM user_personal_collections
		WHERE collection_type = $3
		  AND user_id = $1
		  AND profile_id = $2
		ORDER BY created_at DESC`,
		uid, profileID, absCollectionTypeSmart,
	)
	if err != nil {
		return nil, fmt.Errorf("abs_smart_collection_store: list: %w", err)
	}
	defer rows.Close()
	out := make([]abs.SmartCollection, 0)
	for rows.Next() {
		var c abs.SmartCollection
		var uidScan int
		if err := rows.Scan(&c.ID, &uidScan, &c.ProfileID, &c.Name, &c.Description, &c.IsPublic, &c.QueryDef, &c.CreatedAt, &c.UpdatedAt); err != nil {
			return nil, fmt.Errorf("abs_smart_collection_store: list scan: %w", err)
		}
		c.UserID = strconv.Itoa(uidScan)
		// Color / IsPinned have no canonical columns — always zero.
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("abs_smart_collection_store: list rows: %w", err)
	}
	return out, nil
}

func (s *ABSSmartCollectionStore) GetSmartCollection(ctx context.Context, id string) (abs.SmartCollection, error) {
	var c abs.SmartCollection
	var uidScan int
	row := s.Pool.QueryRow(ctx, `
		SELECT id, user_id, profile_id, name, description, is_shared, query_definition, created_at, updated_at
		FROM user_personal_collections
		WHERE id = $1 AND collection_type = $2`,
		id, absCollectionTypeSmart,
	)
	if err := row.Scan(&c.ID, &uidScan, &c.ProfileID, &c.Name, &c.Description, &c.IsPublic, &c.QueryDef, &c.CreatedAt, &c.UpdatedAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return abs.SmartCollection{}, abs.ErrNotFound
		}
		return abs.SmartCollection{}, fmt.Errorf("abs_smart_collection_store: get: %w", err)
	}
	c.UserID = strconv.Itoa(uidScan)
	// Color / IsPinned have no canonical columns — always zero.
	return c, nil
}

func (s *ABSSmartCollectionStore) CreateSmartCollection(ctx context.Context, c abs.SmartCollection) error {
	uid, err := strconv.Atoi(c.UserID)
	if err != nil {
		return fmt.Errorf("abs_smart_collection_store: invalid user id %q: %w", c.UserID, err)
	}
	// Color / IsPinned are intentionally not persisted (no canonical columns).
	if _, err := s.Pool.Exec(ctx, `
		INSERT INTO user_personal_collections
		    (id, user_id, profile_id, creator_profile_id, name, description,
		     collection_type, is_shared, query_definition, created_at, updated_at)
		VALUES ($1, $2, $3, $3, $4, $5, $6, $7, $8::jsonb, now(), now())`,
		c.ID, uid, c.ProfileID, c.Name, c.Description, absCollectionTypeSmart, c.IsPublic, c.QueryDef,
	); err != nil {
		return fmt.Errorf("abs_smart_collection_store: create: %w", err)
	}
	return nil
}

func (s *ABSSmartCollectionStore) UpdateSmartCollection(ctx context.Context, c abs.SmartCollection) error {
	// Color / IsPinned are intentionally not persisted (no canonical columns).
	if _, err := s.Pool.Exec(ctx, `
		UPDATE user_personal_collections
		   SET name = $2, description = $3, is_shared = $4, query_definition = $5::jsonb, updated_at = now()
		 WHERE id = $1 AND collection_type = $6`,
		c.ID, c.Name, c.Description, c.IsPublic, c.QueryDef, absCollectionTypeSmart,
	); err != nil {
		return fmt.Errorf("abs_smart_collection_store: update: %w", err)
	}
	return nil
}

func (s *ABSSmartCollectionStore) DeleteSmartCollection(ctx context.Context, id string) error {
	// Smart collections have no membership rows; no tx needed.
	if _, err := s.Pool.Exec(ctx,
		`DELETE FROM user_personal_collections WHERE id = $1 AND collection_type = $2`,
		id, absCollectionTypeSmart,
	); err != nil {
		return fmt.Errorf("abs_smart_collection_store: delete: %w", err)
	}
	return nil
}
