package userstore

import (
	"context"
	"fmt"
	"log/slog"
)

// CollectionSortPreferenceCleaner removes cross-user viewer preferences when
// a shared library collection is deleted. Library collections live in the
// central catalog while preferences may live in either shared PostgreSQL or
// per-user SQLite, so database foreign keys cannot provide this cleanup.
type CollectionSortPreferenceCleaner struct {
	users  UserLister
	stores UserStoreProvider
	logger *slog.Logger
}

// NewCollectionSortPreferenceCleaner wires the cross-user cleanup sweep.
func NewCollectionSortPreferenceCleaner(users UserLister, stores UserStoreProvider) *CollectionSortPreferenceCleaner {
	return &CollectionSortPreferenceCleaner{
		users:  users,
		stores: stores,
		logger: slog.Default().With("component", "userstore.collection_sort_cleanup"),
	}
}

// DeleteForCollection clears every profile's preference for one collection.
// Failures are logged per user and do not undo the owning collection delete;
// stale rows are unreachable because preference writes and reads both require
// the collection to remain accessible.
func (c *CollectionSortPreferenceCleaner) DeleteForCollection(ctx context.Context, kind, collectionID string) {
	if c == nil || c.users == nil || c.stores == nil {
		return
	}
	forEachUserStore(ctx, c.users, c.stores, c.logger, "collection sort cleanup", []any{"kind", kind, "collection_id", collectionID}, func(userID int, store UserStore) error {
		profiles, err := store.ListProfiles(ctx)
		if err != nil {
			return fmt.Errorf("list profiles: %w", err)
		}
		for _, profile := range profiles {
			if err := store.ClearCollectionSortPreference(ctx, profile.ID, kind, collectionID); err != nil {
				c.logger.WarnContext(ctx, "collection sort cleanup: delete failed", "kind", kind, "collection_id", collectionID, "user_id", userID, "profile_id", profile.ID, "error", err)
			}
		}
		return nil
	})
}
