package audiobooks

import (
	"context"
	"fmt"
	"strconv"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/oklog/ulid/v2"

	"github.com/Silo-Server/silo-server/internal/audiobooks/abs"
)

// ABSBookmarkStore implements abs.BookmarkStore against the
// abs_bookmarks table (migration 148). One row per
// (user, profile, item, time) — uniqueness is enforced by the
// abs_bookmarks_user_profile_item_time_uniq index, with the
// COALESCE-to-sentinel-UUID trick collapsing NULL profile_id into a
// single bucket per user.
type ABSBookmarkStore struct {
	Pool *pgxpool.Pool
}

// Compile-time assertion that ABSBookmarkStore satisfies the
// abs.BookmarkStore contract. Catches signature drift at build time.
var _ abs.BookmarkStore = (*ABSBookmarkStore)(nil)

// profileArg returns the value to bind for the profile_id column.
// pgx interprets a (*string)(nil) as SQL NULL, which is exactly what
// the schema wants for primary-profile rows.
func profileArg(profileID string) any {
	if profileID == "" {
		return nil
	}
	return profileID
}

// List returns all bookmarks for (user, profile, item) ordered by
// time_seconds ASC. Empty slice (never nil) when none exist.
func (s *ABSBookmarkStore) List(ctx context.Context, userID, profileID, itemID string) ([]abs.Bookmark, error) {
	uid, err := strconv.Atoi(userID)
	if err != nil {
		return nil, fmt.Errorf("abs_bookmark_store: invalid user id %q: %w", userID, err)
	}
	rows, err := s.Pool.Query(ctx, `
		SELECT id, library_item_id, time_seconds, title, created_at, updated_at
		FROM abs_bookmarks
		WHERE user_id = $1
		  AND COALESCE(profile_id, '00000000-0000-0000-0000-000000000000'::uuid)
		      = COALESCE($2::uuid, '00000000-0000-0000-0000-000000000000'::uuid)
		  AND library_item_id = $3
		ORDER BY time_seconds ASC`,
		uid, profileArg(profileID), itemID,
	)
	if err != nil {
		return nil, fmt.Errorf("abs_bookmark_store: list: %w", err)
	}
	defer rows.Close()
	out := make([]abs.Bookmark, 0)
	for rows.Next() {
		var b abs.Bookmark
		if err := rows.Scan(&b.ID, &b.LibraryItemID, &b.Time, &b.Title, &b.CreatedAt, &b.UpdatedAt); err != nil {
			return nil, fmt.Errorf("abs_bookmark_store: list scan: %w", err)
		}
		out = append(out, b)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("abs_bookmark_store: list rows: %w", err)
	}
	return out, nil
}

// Upsert inserts a new bookmark or updates the title at the exact
// (user, profile, item, time) tuple. ID is generated on insert and
// preserved on update.
func (s *ABSBookmarkStore) Upsert(ctx context.Context, userID, profileID, itemID string, timeSeconds float64, title string) (abs.Bookmark, error) {
	uid, err := strconv.Atoi(userID)
	if err != nil {
		return abs.Bookmark{}, fmt.Errorf("abs_bookmark_store: invalid user id %q: %w", userID, err)
	}
	id := ulid.Make().String()
	var out abs.Bookmark
	row := s.Pool.QueryRow(ctx, `
		INSERT INTO abs_bookmarks
		  (id, user_id, profile_id, library_item_id, time_seconds, title)
		VALUES ($1, $2, $3::uuid, $4, $5, $6)
		ON CONFLICT (
		    user_id,
		    COALESCE(profile_id, '00000000-0000-0000-0000-000000000000'::uuid),
		    library_item_id,
		    time_seconds
		) DO UPDATE
		   SET title = EXCLUDED.title,
		       updated_at = now()
		RETURNING id, library_item_id, time_seconds, title, created_at, updated_at`,
		id, uid, profileArg(profileID), itemID, timeSeconds, title,
	)
	if err := row.Scan(&out.ID, &out.LibraryItemID, &out.Time, &out.Title, &out.CreatedAt, &out.UpdatedAt); err != nil {
		return abs.Bookmark{}, fmt.Errorf("abs_bookmark_store: upsert: %w", err)
	}
	return out, nil
}

// Delete removes the bookmark at (user, profile, item, time).
// Returns nil when no row matched — DELETE is idempotent per
// the BookmarkStore contract.
func (s *ABSBookmarkStore) Delete(ctx context.Context, userID, profileID, itemID string, timeSeconds float64) error {
	uid, err := strconv.Atoi(userID)
	if err != nil {
		return fmt.Errorf("abs_bookmark_store: invalid user id %q: %w", userID, err)
	}
	if _, err := s.Pool.Exec(ctx, `
		DELETE FROM abs_bookmarks
		WHERE user_id = $1
		  AND COALESCE(profile_id, '00000000-0000-0000-0000-000000000000'::uuid)
		      = COALESCE($2::uuid, '00000000-0000-0000-0000-000000000000'::uuid)
		  AND library_item_id = $3
		  AND time_seconds = $4`,
		uid, profileArg(profileID), itemID, timeSeconds,
	); err != nil {
		return fmt.Errorf("abs_bookmark_store: delete: %w", err)
	}
	return nil
}

// CountByUser returns a map of library_item_id -> bookmark count for
// the given (user, profile). One SQL query; used by the
// smart-collection items evaluator for batch hydration.
func (s *ABSBookmarkStore) CountByUser(ctx context.Context, userID, profileID string) (map[string]int, error) {
	uid, err := strconv.Atoi(userID)
	if err != nil {
		return nil, fmt.Errorf("abs_bookmark_store: invalid user id %q: %w", userID, err)
	}
	rows, err := s.Pool.Query(ctx, `
		SELECT library_item_id, COUNT(*)
		FROM abs_bookmarks
		WHERE user_id = $1
		  AND COALESCE(profile_id, '00000000-0000-0000-0000-000000000000'::uuid)
		      = COALESCE($2::uuid, '00000000-0000-0000-0000-000000000000'::uuid)
		GROUP BY library_item_id`,
		uid, profileArg(profileID),
	)
	if err != nil {
		return nil, fmt.Errorf("abs_bookmark_store: count-by-user: %w", err)
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var itemID string
		var count int
		if err := rows.Scan(&itemID, &count); err != nil {
			return nil, fmt.Errorf("abs_bookmark_store: count-by-user scan: %w", err)
		}
		out[itemID] = count
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("abs_bookmark_store: count-by-user rows: %w", err)
	}
	return out, nil
}
