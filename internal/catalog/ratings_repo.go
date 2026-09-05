package catalog

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// UserRating represents a user's rating for a media item.
type UserRating struct {
	UserID      int       `json:"user_id"`
	ProfileID   string    `json:"profile_id"`
	MediaItemID string    `json:"media_item_id"`
	Rating      int       `json:"rating"`
	RatedAt     time.Time `json:"rated_at"`
}

// RatingKey is the keyset position ListPage resumes after: the (RatedAt,
// MediaItemID) of the last row a caller already holds, copied verbatim from
// a UserRating the page returned. media_item_id is unique per profile, so
// with the fixed ORDER BY rated_at DESC, media_item_id DESC the pair orders
// every row totally.
type RatingKey struct {
	RatedAt     time.Time
	MediaItemID string
}

// RatingsRepo provides access to the user_ratings table.
type RatingsRepo struct {
	pool *pgxpool.Pool
}

// NewRatingsRepo creates a new RatingsRepo.
func NewRatingsRepo(pool *pgxpool.Pool) *RatingsRepo {
	return &RatingsRepo{pool: pool}
}

// Set creates or updates a user's rating for an item.
func (r *RatingsRepo) Set(ctx context.Context, userID int, profileID, mediaItemID string, rating int) error {
	_, err := r.pool.Exec(ctx, `
		INSERT INTO user_ratings (user_id, profile_id, media_item_id, rating, rated_at)
		VALUES ($1, $2, $3, $4, NOW())
		ON CONFLICT (user_id, profile_id, media_item_id)
		DO UPDATE SET rating = EXCLUDED.rating, rated_at = EXCLUDED.rated_at`,
		userID, profileID, mediaItemID, rating,
	)
	if err != nil {
		return fmt.Errorf("set rating: %w", err)
	}
	return nil
}

// Get retrieves a user's rating for an item. Returns nil if not rated.
func (r *RatingsRepo) Get(ctx context.Context, userID int, profileID, mediaItemID string) (*UserRating, error) {
	var ur UserRating
	err := r.pool.QueryRow(ctx, `
		SELECT user_id, profile_id, media_item_id, rating, rated_at
		FROM user_ratings
		WHERE user_id = $1 AND profile_id = $2 AND media_item_id = $3`,
		userID, profileID, mediaItemID,
	).Scan(&ur.UserID, &ur.ProfileID, &ur.MediaItemID, &ur.Rating, &ur.RatedAt)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get rating: %w", err)
	}
	return &ur, nil
}

// Delete removes a user's rating for an item.
func (r *RatingsRepo) Delete(ctx context.Context, userID int, profileID, mediaItemID string) error {
	_, err := r.pool.Exec(ctx, `
		DELETE FROM user_ratings
		WHERE user_id = $1 AND profile_id = $2 AND media_item_id = $3`,
		userID, profileID, mediaItemID,
	)
	if err != nil {
		return fmt.Errorf("delete rating: %w", err)
	}
	return nil
}

// List returns all ratings for a user+profile with pagination.
// ListPage is the keyset form of List: ordered by (rated_at DESC,
// media_item_id DESC), at most limit rows strictly after the key in that
// order (nil = from the most recently rated row). rated_at keeps the
// column's own precision, so a key read back from a row compares equal to
// the stored value.
func (r *RatingsRepo) ListPage(ctx context.Context, userID int, profileID string, after *RatingKey, limit int) ([]UserRating, error) {
	args := []any{userID, profileID}
	query := `
		SELECT user_id, profile_id, media_item_id, rating, rated_at
		FROM user_ratings
		WHERE user_id = $1 AND profile_id = $2`
	if after != nil {
		args = append(args, after.RatedAt, after.MediaItemID)
		query += fmt.Sprintf(`
		  AND (rated_at, media_item_id) < ($%d::timestamptz, $%d)`, len(args)-1, len(args))
	}
	args = append(args, limit)
	query += fmt.Sprintf(`
		ORDER BY rated_at DESC, media_item_id DESC
		LIMIT $%d`, len(args))
	rows, err := r.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list ratings page: %w", err)
	}
	defer rows.Close()

	var ratings []UserRating
	for rows.Next() {
		var ur UserRating
		if err := rows.Scan(&ur.UserID, &ur.ProfileID, &ur.MediaItemID, &ur.Rating, &ur.RatedAt); err != nil {
			return nil, fmt.Errorf("scan rating: %w", err)
		}
		ratings = append(ratings, ur)
	}
	return ratings, rows.Err()
}

func (r *RatingsRepo) List(ctx context.Context, userID int, profileID string, limit, offset int) ([]UserRating, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT user_id, profile_id, media_item_id, rating, rated_at
		FROM user_ratings
		WHERE user_id = $1 AND profile_id = $2
		ORDER BY rated_at DESC
		LIMIT $3 OFFSET $4`,
		userID, profileID, limit, offset,
	)
	if err != nil {
		return nil, fmt.Errorf("list ratings: %w", err)
	}
	defer rows.Close()

	var ratings []UserRating
	for rows.Next() {
		var ur UserRating
		if err := rows.Scan(&ur.UserID, &ur.ProfileID, &ur.MediaItemID, &ur.Rating, &ur.RatedAt); err != nil {
			return nil, fmt.Errorf("scan rating: %w", err)
		}
		ratings = append(ratings, ur)
	}
	return ratings, rows.Err()
}

// ListForItems returns ratings for a specific set of item IDs (used by recommendation filtering).
func (r *RatingsRepo) ListForItems(ctx context.Context, userID int, profileID string, itemIDs []string) (map[string]int, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT media_item_id, rating
		FROM user_ratings
		WHERE user_id = $1 AND profile_id = $2 AND media_item_id = ANY($3)`,
		userID, profileID, itemIDs,
	)
	if err != nil {
		return nil, fmt.Errorf("list ratings for items: %w", err)
	}
	defer rows.Close()

	result := make(map[string]int, len(itemIDs))
	for rows.Next() {
		var itemID string
		var rating int
		if err := rows.Scan(&itemID, &rating); err != nil {
			return nil, fmt.Errorf("scan rating: %w", err)
		}
		result[itemID] = rating
	}
	return result, rows.Err()
}
