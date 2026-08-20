package userstore

import (
	"context"
	"log/slog"
	"sort"
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
	users, err := c.users.List(ctx)
	if err != nil {
		c.logger.WarnContext(ctx, "collection sort cleanup: list users failed", "kind", kind, "collection_id", collectionID, "error", err)
		return
	}
	sort.Slice(users, func(i, j int) bool { return users[i].ID < users[j].ID })
	for _, user := range users {
		if ctx.Err() != nil {
			return
		}
		store, err := c.stores.ForUser(ctx, user.ID)
		if err != nil {
			c.logger.WarnContext(ctx, "collection sort cleanup: open user store failed", "kind", kind, "collection_id", collectionID, "user_id", user.ID, "error", err)
			continue
		}
		profiles, err := store.ListProfiles(ctx)
		if err != nil {
			c.logger.WarnContext(ctx, "collection sort cleanup: list profiles failed", "kind", kind, "collection_id", collectionID, "user_id", user.ID, "error", err)
			continue
		}
		for _, profile := range profiles {
			if err := store.ClearCollectionSortPreference(ctx, profile.ID, kind, collectionID); err != nil {
				c.logger.WarnContext(ctx, "collection sort cleanup: delete failed", "kind", kind, "collection_id", collectionID, "user_id", user.ID, "profile_id", profile.ID, "error", err)
			}
		}
	}
}
