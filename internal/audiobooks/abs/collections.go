package abs

import (
	"context"
	"time"
)

// CollectionStore is the narrow slice of user_personal_collections
// (collection_type='manual') and user_personal_collection_items
// (sub_item_id=”) the collections handlers need. Implemented by
// ABSCollectionStore in internal/audiobooks/abs_collection_store.go;
// post-migration-156 it reads the unified canonical tables.
type CollectionStore interface {
	// ListUserCollections returns collections owned by (userID, profileID),
	// ordered by created_at DESC. Empty slice (never nil) when none.
	ListUserCollections(ctx context.Context, userID, profileID string) ([]Collection, error)
	// GetCollection fetches by ID without owner check (caller authorizes).
	// Returns ErrNotFound when absent.
	GetCollection(ctx context.Context, id string) (Collection, error)
	// CreateCollection inserts. ID must be set by caller (ULID).
	CreateCollection(ctx context.Context, c Collection) error
	// UpdateCollection writes name, description, is_public; bumps
	// updated_at = now(). Owner check is the caller's responsibility.
	UpdateCollection(ctx context.Context, c Collection) error
	// DeleteCollection removes the collection and (via FK CASCADE) all
	// its user_personal_collection_items. Returns nil even if no row
	// matched.
	DeleteCollection(ctx context.Context, id string) error
	// ListCollectionItems returns items ordered by added_at ASC.
	// Empty slice (never nil) when none.
	ListCollectionItems(ctx context.Context, collectionID string) ([]CollectionItem, error)
	// AddCollectionItem inserts (collectionID, libraryItemID) and bumps
	// the parent's updated_at. ON CONFLICT DO NOTHING — re-adding is a
	// silent no-op.
	AddCollectionItem(ctx context.Context, collectionID, libraryItemID string) error
	// RemoveCollectionItem deletes one row and bumps the parent's
	// updated_at. Returns nil when not present (idempotent).
	RemoveCollectionItem(ctx context.Context, collectionID, libraryItemID string) error
}

// Collection is the in-memory representation of a
// user_personal_collections row with collection_type='manual'.
type Collection struct {
	ID          string
	UserID      string
	ProfileID   string
	Name        string
	Description string
	IsPublic    bool
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// CollectionItem is the in-memory representation of a
// user_personal_collection_items row scoped to a manual collection
// (sub_item_id=”).
type CollectionItem struct {
	CollectionID  string
	LibraryItemID string
	AddedAt       time.Time
}

// collectionToABS shapes a Collection in the ABS wire format. When
// books is nil the list-shape is emitted (no "books" key); when books
// is non-nil (possibly empty) the full-shape is emitted.
//
// All seven non-books keys are always present (no omitempty),
// camelCase, with timestamps as JS-epoch milliseconds.
func collectionToABS(c Collection, books []map[string]any) map[string]any {
	out := map[string]any{
		"id":          c.ID,
		"libraryId":   VirtualLibraryID, // real ABS Collection.toOldJSON has libraryId; silo collections are cross-library user-personal
		"userId":      c.UserID,
		"name":        c.Name,
		"description": c.Description,
		"isPublic":    c.IsPublic,
		"lastUpdate":  c.UpdatedAt.UnixMilli(),
		"createdAt":   c.CreatedAt.UnixMilli(),
	}
	if books != nil {
		out["books"] = books
	}
	return out
}
