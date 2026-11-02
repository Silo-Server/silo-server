package pgstore

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/Silo-Server/silo-server/internal/userstore"
)

// --- Not interested ---

func (s *PostgresUserStore) MarkNotInterested(ctx context.Context, profileID, mediaItemID string) (bool, error) {
	tag, err := s.pool.Exec(ctx,
		`INSERT INTO user_not_interested (user_id, profile_id, media_item_id, added_at)
		 VALUES ($1, $2, $3, $4)
		 ON CONFLICT DO NOTHING`,
		s.userID, profileID, mediaItemID, time.Now().UTC(),
	)
	if err != nil {
		return false, fmt.Errorf("marking not interested: %w", err)
	}
	return tag.RowsAffected() > 0, nil
}

func (s *PostgresUserStore) UnmarkNotInterested(ctx context.Context, profileID, mediaItemID string) (bool, error) {
	tag, err := s.pool.Exec(ctx,
		`DELETE FROM user_not_interested WHERE user_id = $1 AND profile_id = $2 AND media_item_id = $3`,
		s.userID, profileID, mediaItemID,
	)
	if err != nil {
		return false, fmt.Errorf("unmarking not interested: %w", err)
	}
	return tag.RowsAffected() > 0, nil
}

func (s *PostgresUserStore) GetNotInterested(ctx context.Context, profileID, mediaItemID string) (*userstore.NotInterestedEntry, error) {
	var e userstore.NotInterestedEntry
	var addedAt time.Time
	err := s.pool.QueryRow(ctx,
		`SELECT profile_id, media_item_id, added_at FROM user_not_interested
		 WHERE user_id = $1 AND profile_id = $2 AND media_item_id = $3`,
		s.userID, profileID, mediaItemID,
	).Scan(&e.ProfileID, &e.MediaItemID, &addedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("getting not interested mark: %w", err)
	}
	e.AddedAt = timeToString(addedAt)
	return &e, nil
}

// ListNotInterestedPage pages by keyset over (added_at DESC, media_item_id
// DESC); see ListFavoritesPage for the precision rule.
func (s *PostgresUserStore) ListNotInterestedPage(ctx context.Context, profileID string, after *userstore.ListKey, limit int) ([]userstore.NotInterestedEntry, error) {
	query, args, err := s.listKeysetQuery(`SELECT profile_id, media_item_id, added_at FROM user_not_interested`, profileID, after, limit)
	if err != nil {
		return nil, err
	}
	rows, err := s.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("listing not interested page: %w", err)
	}
	defer rows.Close()

	var entries []userstore.NotInterestedEntry
	for rows.Next() {
		var e userstore.NotInterestedEntry
		var addedAt time.Time
		if err := rows.Scan(&e.ProfileID, &e.MediaItemID, &addedAt); err != nil {
			return nil, fmt.Errorf("scanning not interested row: %w", err)
		}
		e.AddedAt = addedAt.UTC().Format(time.RFC3339Nano)
		entries = append(entries, e)
	}
	return entries, rows.Err()
}

func (s *PostgresUserStore) ListNotInterestedByMediaItems(ctx context.Context, profileID string, mediaItemIDs []string) (map[string]bool, error) {
	result := make(map[string]bool, len(mediaItemIDs))
	if len(mediaItemIDs) == 0 {
		return result, nil
	}
	rows, err := s.pool.Query(ctx,
		`SELECT media_item_id FROM user_not_interested
		 WHERE user_id = $1 AND profile_id = $2 AND media_item_id = ANY($3)`,
		s.userID, profileID, mediaItemIDs,
	)
	if err != nil {
		return nil, fmt.Errorf("listing not interested by media items: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var mediaItemID string
		if err := rows.Scan(&mediaItemID); err != nil {
			return nil, fmt.Errorf("scanning not interested row: %w", err)
		}
		result[mediaItemID] = true
	}
	return result, rows.Err()
}
