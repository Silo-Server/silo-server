package userstore

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
)

// SettingValuesCleaner removes canonical library- and series-scoped setting
// values when their owning entity is deleted. The schema deliberately declares
// no foreign keys on library_id or series_id — libraries and series live in
// the shared catalog while values live per user — so the owning delete paths
// must call this, or removed content leaves orphaned preferences behind and a
// returning series identity resurrects stale values.
type SettingValuesCleaner struct {
	users  UserLister
	stores UserStoreProvider
	logger *slog.Logger
}

// NewSettingValuesCleaner wires the cleaner.
func NewSettingValuesCleaner(users UserLister, stores UserStoreProvider) *SettingValuesCleaner {
	return &SettingValuesCleaner{
		users:  users,
		stores: stores,
		logger: slog.Default().With("component", "userstore.setting_values_cleanup"),
	}
}

// DeleteForLibrary removes every user's profile_library values for one
// library. A user whose store fails is logged and skipped rather than failing
// the caller's delete: the library is already gone, and an orphaned row's only
// cost is dead weight until a retry — while aborting the delete job for it
// would leave far more state behind.
func (c *SettingValuesCleaner) DeleteForLibrary(ctx context.Context, libraryID int) int64 {
	return c.sweep(ctx, fmt.Sprintf("library %d", libraryID),
		func(ctx context.Context, store UserStore) (int64, error) {
			return store.DeleteSettingValuesForLibrary(ctx, libraryID)
		})
}

// DeleteForSeries removes every user's profile_series values for one series.
func (c *SettingValuesCleaner) DeleteForSeries(ctx context.Context, seriesID string) int64 {
	return c.sweep(ctx, "series "+seriesID,
		func(ctx context.Context, store UserStore) (int64, error) {
			return store.DeleteSettingValuesForSeries(ctx, seriesID)
		})
}

func (c *SettingValuesCleaner) sweep(
	ctx context.Context,
	subject string,
	del func(context.Context, UserStore) (int64, error),
) int64 {
	if c == nil || c.users == nil || c.stores == nil {
		return 0
	}
	users, err := c.users.List(ctx)
	if err != nil {
		c.logger.WarnContext(ctx, "settings cleanup: list users failed",
			"subject", subject, "error", err)
		return 0
	}
	sort.Slice(users, func(i, j int) bool { return users[i].ID < users[j].ID })

	var deleted int64
	for _, user := range users {
		if ctx.Err() != nil {
			return deleted
		}
		store, err := c.stores.ForUser(ctx, user.ID)
		if err != nil {
			c.logger.WarnContext(ctx, "settings cleanup: open user store failed",
				"subject", subject, "user_id", user.ID, "error", err)
			continue
		}
		n, err := del(ctx, store)
		if err != nil {
			c.logger.WarnContext(ctx, "settings cleanup: delete failed",
				"subject", subject, "user_id", user.ID, "error", err)
			continue
		}
		deleted += n
	}
	if deleted > 0 {
		c.logger.InfoContext(ctx, "settings cleanup removed orphaned values",
			"subject", subject, "rows", deleted)
	}
	return deleted
}
