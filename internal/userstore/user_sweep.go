package userstore

import (
	"context"
	"log/slog"
	"sort"
)

// forEachUserStore runs fn on every account's store in user ID order, for
// work that has to reach every account because its data may live in shared
// PostgreSQL or per-user SQLite. A failure is logged with the sweep name and
// attrs and skips that account; the sweep stops when ctx ends.
func forEachUserStore(ctx context.Context, users UserLister, stores UserStoreProvider, logger *slog.Logger, sweep string, attrs []any, fn func(userID int, store UserStore) error) {
	attrs = append([]any{"sweep", sweep}, attrs...)
	list, err := users.List(ctx)
	if err != nil {
		logger.WarnContext(ctx, "user sweep: list users failed", append(attrs, "error", err)...)
		return
	}
	sort.Slice(list, func(i, j int) bool { return list[i].ID < list[j].ID })
	for _, user := range list {
		if ctx.Err() != nil {
			return
		}
		store, err := stores.ForUser(ctx, user.ID)
		if err != nil {
			logger.WarnContext(ctx, "user sweep: open user store failed", append(attrs, "user_id", user.ID, "error", err)...)
			continue
		}
		if err := fn(user.ID, store); err != nil {
			logger.WarnContext(ctx, "user sweep: account failed", append(attrs, "user_id", user.ID, "error", err)...)
		}
	}
}
