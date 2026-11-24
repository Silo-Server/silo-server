package catalog

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// UserRating represents a user's rating for a media item.
type UserRating struct {
	UserID      int    `json:"user_id"`
	ProfileID   string `json:"profile_id"`
	MediaItemID string `json:"media_item_id"`
	// Score is the rating from 1 to 10 (see MaxRatingScore).
	Score   int       `json:"score"`
	RatedAt time.Time `json:"rated_at"`
}

// Stars answers the rating in whole stars, rounding half up (7 is 4 stars),
// the scale the recommendation weights are defined on.
func (r UserRating) Stars() int { return (r.Score + 1) / 2 }

// MaxRatingScore is the top of the scale personal ratings are stored on:
// integer scores from 1 to 10, shown as five stars with half stars.
const MaxRatingScore = 10

// ScoreFromStars converts whole stars (1 to 5), the scale clients used
// before scores, to a score.
func ScoreFromStars(stars int) int { return stars * 2 }

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
func (r *RatingsRepo) Set(ctx context.Context, userID int, profileID, mediaItemID string, score int) error {
	_, err := r.pool.Exec(ctx, `
		INSERT INTO user_ratings (user_id, profile_id, media_item_id, rating, rated_at)
		VALUES ($1, $2, $3, $4, NOW())
		ON CONFLICT (user_id, profile_id, media_item_id)
		DO UPDATE SET rating = EXCLUDED.rating, rated_at = EXCLUDED.rated_at`,
		userID, profileID, mediaItemID, score,
	)
	if err != nil {
		return fmt.Errorf("set rating: %w", err)
	}
	return nil
}

// ObservedRating is the rating and rating time a caller read earlier, used to
// detect a change made since. A zero Score means the item was unrated.
type ObservedRating struct {
	Score   int
	RatedAt time.Time
}

// SetIfUnchanged writes a rating observed elsewhere (a watch provider) only if
// the item's rating is still the observed one. Every local write stamps
// rated_at, so comparing it too catches a change back to the same value.
// ratedAt is the time the new rating was made. It reports whether the write
// applied; false means a concurrent change won, and the caller should re-read
// before retrying.
func (r *RatingsRepo) SetIfUnchanged(ctx context.Context, userID int, profileID, mediaItemID string, observed ObservedRating, score int, ratedAt time.Time) (bool, error) {
	var query string
	args := []any{userID, profileID, mediaItemID, score, ratedAt}
	if observed.Score == 0 {
		query = `
			INSERT INTO user_ratings (user_id, profile_id, media_item_id, rating, rated_at)
			VALUES ($1, $2, $3, $4, $5)
			ON CONFLICT (user_id, profile_id, media_item_id) DO NOTHING`
	} else {
		query = `
			UPDATE user_ratings SET rating = $4, rated_at = $5
			WHERE user_id = $1 AND profile_id = $2 AND media_item_id = $3 AND rating = $6 AND rated_at = $7`
		args = append(args, observed.Score, observed.RatedAt)
	}
	tag, err := r.pool.Exec(ctx, query, args...)
	if err != nil {
		return false, fmt.Errorf("set rating if unchanged: %w", err)
	}
	return tag.RowsAffected() == 1, nil
}

// DeleteIfUnchanged removes a rating only if it is still the observed one. It
// reports whether a row was deleted.
func (r *RatingsRepo) DeleteIfUnchanged(ctx context.Context, userID int, profileID, mediaItemID string, observed ObservedRating) (bool, error) {
	tag, err := r.pool.Exec(ctx, `
		DELETE FROM user_ratings
		WHERE user_id = $1 AND profile_id = $2 AND media_item_id = $3 AND rating = $4 AND rated_at = $5`,
		userID, profileID, mediaItemID, observed.Score, observed.RatedAt,
	)
	if err != nil {
		return false, fmt.Errorf("delete rating if unchanged: %w", err)
	}
	return tag.RowsAffected() == 1, nil
}

// Get retrieves a user's rating for an item. Returns nil if not rated.
func (r *RatingsRepo) Get(ctx context.Context, userID int, profileID, mediaItemID string) (*UserRating, error) {
	ur, err := scanUserRating(r.pool.QueryRow(ctx, userRatingSQL, userID, profileID, mediaItemID))
	if err != nil {
		return nil, fmt.Errorf("get rating: %w", err)
	}
	return ur, nil
}

// QueueUserRating queues Get's read on b, for a caller batching it with
// other reads of the same database. Closing the sent batch's results without
// error sets *rating, nil when the item is not rated.
func QueueUserRating(b *pgx.Batch, userID int, profileID, mediaItemID string, rating **UserRating) {
	b.Queue(userRatingSQL, userID, profileID, mediaItemID).QueryRow(func(row pgx.Row) error {
		ur, err := scanUserRating(row)
		*rating = ur
		return err
	})
}

const userRatingSQL = `
		SELECT user_id, profile_id, media_item_id, rating, rated_at
		FROM user_ratings
		WHERE user_id = $1 AND profile_id = $2 AND media_item_id = $3`

// scanUserRating reads the row userRatingSQL selects, nil when there is none.
func scanUserRating(row pgx.Row) (*UserRating, error) {
	var ur UserRating
	err := row.Scan(&ur.UserID, &ur.ProfileID, &ur.MediaItemID, &ur.Score, &ur.RatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
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
// ListPage is the keyset form of List for a viewer: the profile's ratings of
// items the access filter admits, ordered by (rated_at DESC, media_item_id
// DESC), at most limit rows strictly after the key in that order (nil = from
// the most recently rated row). The filter is applied in the page query with
// the predicates EnsureAccessibleIDs uses, so a page of visible ratings is one
// index-ordered read however many hidden ratings come before it, and a rating
// of an item the catalog no longer has is left out. rated_at keeps the
// column's own precision, so a key read back from a row compares equal to the
// stored value.
func (r *RatingsRepo) ListPage(ctx context.Context, userID int, profileID string, filter AccessFilter, after *RatingKey, limit int) ([]UserRating, error) {
	// An empty (non-nil) allowlist means "no libraries allowed".
	if filter.AllowedLibraryIDs != nil && len(filter.AllowedLibraryIDs) == 0 {
		return nil, nil
	}
	query, args := ratingsPageSQL(userID, profileID, filter, after, limit)
	rows, err := r.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list ratings page: %w", err)
	}
	defer rows.Close()

	return collectUserRatings(rows)
}

func ratingsPageSQL(userID int, profileID string, filter AccessFilter, after *RatingKey, limit int) (string, []any) {
	conditions := []string{"ur.user_id = $1", "ur.profile_id = $2"}
	args := []any{userID, profileID}
	argIdx := 3
	if after != nil {
		conditions = append(conditions, fmt.Sprintf("(ur.rated_at, ur.media_item_id) < ($%d::timestamptz, $%d)", argIdx, argIdx+1))
		args = append(args, after.RatedAt, after.MediaItemID)
		argIdx += 2
	}
	appendLibraryAccessConditions("mi.content_id", filter, &conditions, &args, &argIdx)
	applyAccessFilter("mi", AccessFilter{MaturityLimits: filter.MaturityLimits, ExcludedMediaTypes: filter.ExcludedMediaTypes}, &conditions, &args, &argIdx)
	args = append(args, limit)
	return fmt.Sprintf(`
		SELECT ur.user_id, ur.profile_id, ur.media_item_id, ur.rating, ur.rated_at
		FROM user_ratings ur
		JOIN media_items mi ON mi.content_id = ur.media_item_id
		WHERE %s
		ORDER BY ur.rated_at DESC, ur.media_item_id DESC
		LIMIT $%d`, strings.Join(conditions, " AND "), argIdx), args
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

	return collectUserRatings(rows)
}

// ListAll returns every rating of a profile in one query, so the caller reads a
// single consistent snapshot.
func (r *RatingsRepo) ListAll(ctx context.Context, userID int, profileID string) ([]UserRating, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT user_id, profile_id, media_item_id, rating, rated_at
		FROM user_ratings
		WHERE user_id = $1 AND profile_id = $2
		ORDER BY media_item_id`,
		userID, profileID,
	)
	if err != nil {
		return nil, fmt.Errorf("list all ratings: %w", err)
	}
	defer rows.Close()

	return collectUserRatings(rows)
}

// ListRatedAtLeast returns at most limit of the profile's ratings scored
// minScore or more of items whose media type is one of mediaTypes, most
// recently rated first. Both filters apply before the limit, so newer low
// ratings, or ratings of other types such as episodes, cannot push an older
// high rating out of the window.
func (r *RatingsRepo) ListRatedAtLeast(ctx context.Context, userID int, profileID string, minScore int, mediaTypes []string, limit int) ([]UserRating, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT ur.user_id, ur.profile_id, ur.media_item_id, ur.rating, ur.rated_at
		FROM user_ratings ur
		JOIN media_items mi ON mi.content_id = ur.media_item_id
		WHERE ur.user_id = $1 AND ur.profile_id = $2 AND ur.rating >= $3 AND mi.type = ANY($4)
		ORDER BY ur.rated_at DESC, ur.media_item_id DESC
		LIMIT $5`,
		userID, profileID, minScore, mediaTypes, limit,
	)
	if err != nil {
		return nil, fmt.Errorf("list ratings at least %d: %w", minScore, err)
	}
	defer rows.Close()
	return collectUserRatings(rows)
}

// collectUserRatings reads every user_ratings row of rows, which select
// user_id, profile_id, media_item_id, rating and rated_at in that order.
func collectUserRatings(rows pgx.Rows) ([]UserRating, error) {
	var ratings []UserRating
	for rows.Next() {
		var ur UserRating
		if err := rows.Scan(&ur.UserID, &ur.ProfileID, &ur.MediaItemID, &ur.Score, &ur.RatedAt); err != nil {
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
