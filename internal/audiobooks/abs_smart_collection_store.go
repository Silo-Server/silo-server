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

// ABSSmartCollectionStore implements abs.SmartCollectionStore against
// the abs_smart_collections table (migration 153).
type ABSSmartCollectionStore struct {
	Pool *pgxpool.Pool
}

var _ abs.SmartCollectionStore = (*ABSSmartCollectionStore)(nil)

func (s *ABSSmartCollectionStore) ListUserSmartCollections(ctx context.Context, userID, profileID string) ([]abs.SmartCollection, error) {
	uid, err := strconv.Atoi(userID)
	if err != nil {
		return nil, fmt.Errorf("abs_smart_collection_store: invalid user id %q: %w", userID, err)
	}
	rows, err := s.Pool.Query(ctx, `
		SELECT id, user_id, profile_id, name, description, color, is_public, is_pinned, query_def, created_at, updated_at
		FROM abs_smart_collections
		WHERE user_id = $1
		  AND COALESCE(profile_id, '00000000-0000-0000-0000-000000000000'::uuid)
		      = COALESCE($2::uuid, '00000000-0000-0000-0000-000000000000'::uuid)
		ORDER BY created_at DESC`,
		uid, profileArg(profileID),
	)
	if err != nil {
		return nil, fmt.Errorf("abs_smart_collection_store: list: %w", err)
	}
	defer rows.Close()
	out := make([]abs.SmartCollection, 0)
	for rows.Next() {
		var c abs.SmartCollection
		var uidScan int
		var profileScan *string
		if err := rows.Scan(&c.ID, &uidScan, &profileScan, &c.Name, &c.Description, &c.Color, &c.IsPublic, &c.IsPinned, &c.QueryDef, &c.CreatedAt, &c.UpdatedAt); err != nil {
			return nil, fmt.Errorf("abs_smart_collection_store: list scan: %w", err)
		}
		c.UserID = strconv.Itoa(uidScan)
		if profileScan != nil {
			c.ProfileID = *profileScan
		}
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
	var profileScan *string
	row := s.Pool.QueryRow(ctx, `
		SELECT id, user_id, profile_id, name, description, color, is_public, is_pinned, query_def, created_at, updated_at
		FROM abs_smart_collections WHERE id = $1`, id)
	if err := row.Scan(&c.ID, &uidScan, &profileScan, &c.Name, &c.Description, &c.Color, &c.IsPublic, &c.IsPinned, &c.QueryDef, &c.CreatedAt, &c.UpdatedAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return abs.SmartCollection{}, abs.ErrNotFound
		}
		return abs.SmartCollection{}, fmt.Errorf("abs_smart_collection_store: get: %w", err)
	}
	c.UserID = strconv.Itoa(uidScan)
	if profileScan != nil {
		c.ProfileID = *profileScan
	}
	return c, nil
}

func (s *ABSSmartCollectionStore) CreateSmartCollection(ctx context.Context, c abs.SmartCollection) error {
	uid, err := strconv.Atoi(c.UserID)
	if err != nil {
		return fmt.Errorf("abs_smart_collection_store: invalid user id %q: %w", c.UserID, err)
	}
	if _, err := s.Pool.Exec(ctx, `
		INSERT INTO abs_smart_collections (id, user_id, profile_id, name, description, color, is_public, is_pinned, query_def)
		VALUES ($1, $2, $3::uuid, $4, $5, $6, $7, $8, $9::jsonb)`,
		c.ID, uid, profileArg(c.ProfileID), c.Name, c.Description, c.Color, c.IsPublic, c.IsPinned, c.QueryDef,
	); err != nil {
		return fmt.Errorf("abs_smart_collection_store: create: %w", err)
	}
	return nil
}

func (s *ABSSmartCollectionStore) UpdateSmartCollection(ctx context.Context, c abs.SmartCollection) error {
	if _, err := s.Pool.Exec(ctx, `
		UPDATE abs_smart_collections
		   SET name = $2, description = $3, color = $4, is_public = $5, is_pinned = $6, query_def = $7::jsonb, updated_at = now()
		 WHERE id = $1`,
		c.ID, c.Name, c.Description, c.Color, c.IsPublic, c.IsPinned, c.QueryDef,
	); err != nil {
		return fmt.Errorf("abs_smart_collection_store: update: %w", err)
	}
	return nil
}

func (s *ABSSmartCollectionStore) DeleteSmartCollection(ctx context.Context, id string) error {
	if _, err := s.Pool.Exec(ctx, `DELETE FROM abs_smart_collections WHERE id = $1`, id); err != nil {
		return fmt.Errorf("abs_smart_collection_store: delete: %w", err)
	}
	return nil
}
