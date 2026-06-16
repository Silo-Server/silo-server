package pgstore

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/Silo-Server/silo-server/internal/userstore"
)

// --- Hidden recommendations ("not interested") ---

// AddHiddenRecommendation marks an item as "not interested" for the profile.
// It is idempotent: re-hiding an already-hidden item is a no-op.
func (s *PostgresUserStore) AddHiddenRecommendation(ctx context.Context, profileID, mediaItemID string) error {
	_, err := s.pool.Exec(ctx,
		`INSERT INTO user_hidden_recommendations (user_id, profile_id, media_item_id, hidden_at)
		 VALUES ($1, $2, $3, $4)
		 ON CONFLICT DO NOTHING`,
		s.userID, profileID, mediaItemID, time.Now().UTC(),
	)
	return err
}

// RemoveHiddenRecommendation un-hides an item so it can be recommended again.
func (s *PostgresUserStore) RemoveHiddenRecommendation(ctx context.Context, profileID, mediaItemID string) error {
	_, err := s.pool.Exec(ctx,
		`DELETE FROM user_hidden_recommendations WHERE user_id = $1 AND profile_id = $2 AND media_item_id = $3`,
		s.userID, profileID, mediaItemID,
	)
	return err
}

// ListHiddenRecommendations returns the profile's hidden items, most recently
// hidden first, paginated by limit/offset.
func (s *PostgresUserStore) ListHiddenRecommendations(ctx context.Context, profileID string, limit, offset int) ([]userstore.HiddenRecommendation, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT profile_id, media_item_id, hidden_at FROM user_hidden_recommendations
		 WHERE user_id = $1 AND profile_id = $2 ORDER BY hidden_at DESC LIMIT $3 OFFSET $4`,
		s.userID, profileID, limit, offset,
	)
	if err != nil {
		return nil, fmt.Errorf("listing hidden recommendations: %w", err)
	}
	defer rows.Close()

	var hidden []userstore.HiddenRecommendation
	for rows.Next() {
		var h userstore.HiddenRecommendation
		var hiddenAt time.Time
		if err := rows.Scan(&h.ProfileID, &h.MediaItemID, &hiddenAt); err != nil {
			return nil, fmt.Errorf("scanning hidden recommendation row: %w", err)
		}
		h.HiddenAt = timeToString(hiddenAt)
		hidden = append(hidden, h)
	}
	return hidden, rows.Err()
}

// IsHiddenRecommendation reports whether the profile has hidden the item.
func (s *PostgresUserStore) IsHiddenRecommendation(ctx context.Context, profileID, mediaItemID string) (bool, error) {
	var count int
	err := s.pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM user_hidden_recommendations WHERE user_id = $1 AND profile_id = $2 AND media_item_id = $3`,
		s.userID, profileID, mediaItemID,
	).Scan(&count)
	return count > 0, err
}

// ListHiddenRecommendationsByMediaItems returns, for the given item IDs, which
// ones the profile has hidden (keyed by media item ID, value true).
func (s *PostgresUserStore) ListHiddenRecommendationsByMediaItems(ctx context.Context, profileID string, mediaItemIDs []string) (map[string]bool, error) {
	result := make(map[string]bool, len(mediaItemIDs))
	if len(mediaItemIDs) == 0 {
		return result, nil
	}

	placeholders := make([]string, len(mediaItemIDs))
	args := make([]any, 0, len(mediaItemIDs)+2)
	args = append(args, s.userID, profileID)
	for i, mediaItemID := range mediaItemIDs {
		placeholders[i] = fmt.Sprintf("$%d", i+3)
		args = append(args, mediaItemID)
	}

	rows, err := s.pool.Query(ctx,
		`SELECT media_item_id FROM user_hidden_recommendations
		 WHERE user_id = $1 AND profile_id = $2 AND media_item_id IN (`+strings.Join(placeholders, ",")+`)`,
		args...,
	)
	if err != nil {
		return nil, fmt.Errorf("listing hidden recommendations by media items: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var mediaItemID string
		if err := rows.Scan(&mediaItemID); err != nil {
			return nil, fmt.Errorf("scanning hidden recommendation row: %w", err)
		}
		result[mediaItemID] = true
	}
	return result, rows.Err()
}

// HiddenRecommendationIDSet returns the full set of media item IDs the profile
// has hidden, used to exclude them from recommendation and discovery surfaces.
func (s *PostgresUserStore) HiddenRecommendationIDSet(ctx context.Context, profileID string) (map[string]struct{}, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT media_item_id FROM user_hidden_recommendations WHERE user_id = $1 AND profile_id = $2`,
		s.userID, profileID,
	)
	if err != nil {
		return nil, fmt.Errorf("listing hidden recommendation id set: %w", err)
	}
	defer rows.Close()

	result := make(map[string]struct{})
	for rows.Next() {
		var mediaItemID string
		if err := rows.Scan(&mediaItemID); err != nil {
			return nil, fmt.Errorf("scanning hidden recommendation row: %w", err)
		}
		result[mediaItemID] = struct{}{}
	}
	return result, rows.Err()
}
