package handlers

import (
	"context"
	"log/slog"
)

// ProfileStaler marks a user's taste profile as stale so it gets refreshed.
type ProfileStaler interface {
	MarkProfileStale(ctx context.Context, userID int, profileID string) error
}

// ProfileRefreshRequester enqueues an asynchronous profile-scoped recommendation refresh.
type ProfileRefreshRequester interface {
	RequestProfileRefresh(ctx context.Context, userID int, profileID string)
}

func triggerProfileRefresh(ctx context.Context, staler ProfileStaler, requester ProfileRefreshRequester, userID int, profileID string) {
	if staler != nil {
		if err := staler.MarkProfileStale(ctx, userID, profileID); err != nil {
			slog.WarnContext(ctx, "failed to mark profile stale", "component", "api", "user_id", userID, "profile_id", profileID, "error", err)
		}
	}
	if requester != nil {
		requester.RequestProfileRefresh(ctx, userID, profileID)
	}
}
