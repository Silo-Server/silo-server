package abs

import (
	"context"
	"time"
)

// BookmarkStore is the narrow slice of the abs_bookmarks table the
// bookmarks handlers need. Implemented by ABSBookmarkStore in
// internal/audiobooks/abs_bookmark_store.go.
type BookmarkStore interface {
	// List returns all bookmarks for (user, profile, item) ordered by
	// time ASC. Returns an empty slice (never nil) when none exist.
	List(ctx context.Context, userID, profileID, itemID string) ([]Bookmark, error)
	// Upsert inserts a bookmark or updates the title at the exact
	// (user, profile, item, time) tuple. ID is generated on insert and
	// preserved on update. Returns the resulting row.
	Upsert(ctx context.Context, userID, profileID, itemID string, timeSeconds float64, title string) (Bookmark, error)
	// Delete removes the bookmark at (user, profile, item, time).
	// Returns nil when no row matched — DELETE is idempotent (a UX
	// convenience, not a 404 surface). See spec §6.
	Delete(ctx context.Context, userID, profileID, itemID string, timeSeconds float64) error
	// CountByUser returns a map of library_item_id -> bookmark count
	// for the given (user, profile). Empty map (never nil) when none.
	// Used by the smart-collection items evaluator to hydrate the
	// `bookmark_count` personalized rule in one SQL pass.
	CountByUser(ctx context.Context, userID, profileID string) (map[string]int, error)
}

// Bookmark is the in-memory representation of an abs_bookmarks row as
// the handlers use it. Intentionally narrow — only the fields the wire
// format cares about.
type Bookmark struct {
	ID            string  // ULID
	LibraryItemID string
	Time          float64 // fractional seconds
	Title         string
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

// bookmarkToABS shapes a Bookmark into the ABS wire format the Android
// and iOS clients expect. All six keys are always present (no
// omitempty), camelCase, with timestamps as JS-epoch milliseconds.
func bookmarkToABS(b Bookmark) map[string]any {
	return map[string]any{
		"id":            b.ID,
		"libraryItemId": b.LibraryItemID,
		"time":          b.Time,
		"title":         b.Title,
		"createdAt":     b.CreatedAt.UnixMilli(),
		"updatedAt":     b.UpdatedAt.UnixMilli(),
	}
}
