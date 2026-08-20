package pgstore

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/Silo-Server/silo-server/internal/userstore"
)

func (s *PostgresUserStore) SetCollectionSortPreference(ctx context.Context, pref userstore.CollectionSortPreference) error {
	if pref.UpdatedAt == "" {
		pref.UpdatedAt = nowUTC()
	}
	_, err := s.pool.Exec(ctx, `
		INSERT INTO user_collection_sort_preferences (
			user_id, profile_id, collection_kind, collection_id, sort_field, sort_order, updated_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7)
		ON CONFLICT(user_id, profile_id, collection_kind, collection_id) DO UPDATE SET
			sort_field = excluded.sort_field,
			sort_order = excluded.sort_order,
			updated_at = excluded.updated_at`,
		s.userID,
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

// GetCollectionSortPreference returns (nil, nil) when the profile has never
// changed this collection's sort. A returned preference with an empty SortField
// means the viewer explicitly chose the collection's own source order.
func (s *PostgresUserStore) GetCollectionSortPreference(ctx context.Context, profileID, collectionKind, collectionID string) (*userstore.CollectionSortPreference, error) {
	var pref userstore.CollectionSortPreference
	var updatedAt time.Time
	err := s.pool.QueryRow(ctx, `
		SELECT profile_id, collection_kind, collection_id, sort_field, sort_order, updated_at
		FROM user_collection_sort_preferences
		WHERE user_id = $1 AND profile_id = $2 AND collection_kind = $3 AND collection_id = $4`,
		s.userID, profileID, collectionKind, collectionID,
	).Scan(
		&pref.ProfileID,
		&pref.CollectionKind,
		&pref.CollectionID,
		&pref.SortField,
		&pref.SortOrder,
		&updatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("getting collection sort preference for profile %q collection %q/%q: %w",
			profileID, collectionKind, collectionID, err)
	}
	pref.UpdatedAt = updatedAt.UTC().Format(time.RFC3339)
	return &pref, nil
}

// ClearCollectionSortPreference drops the override so the collection falls back
// to its creator-configured default. Distinct from storing an empty SortField,
// which pins the viewer to source order.
func (s *PostgresUserStore) ClearCollectionSortPreference(ctx context.Context, profileID, collectionKind, collectionID string) error {
	_, err := s.pool.Exec(ctx, `
		DELETE FROM user_collection_sort_preferences
		WHERE user_id = $1 AND profile_id = $2 AND collection_kind = $3 AND collection_id = $4`,
		s.userID, profileID, collectionKind, collectionID,
	)
	if err != nil {
		return fmt.Errorf("clearing collection sort preference for profile %q collection %q/%q: %w",
			profileID, collectionKind, collectionID, err)
	}
	return nil
}
