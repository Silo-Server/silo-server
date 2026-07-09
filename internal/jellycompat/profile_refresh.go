package jellycompat

import (
	"context"
	"log/slog"
)

type profileStaler interface {
	MarkProfileStale(ctx context.Context, userID int, profileID string) error
}

type profileRefreshRequester interface {
	RequestProfileRefresh(ctx context.Context, userID int, profileID string)
}

func triggerProfileRefresh(ctx context.Context, staler profileStaler, requester profileRefreshRequester, userID int, profileID string) {
	if staler != nil {
		if err := staler.MarkProfileStale(ctx, userID, profileID); err != nil {
			slog.WarnContext(ctx, "failed to mark profile stale", "component", "jellycompat", "user_id", userID, "profile_id", profileID, "error", err)
		}
	}
	if requester != nil {
		requester.RequestProfileRefresh(ctx, userID, profileID)
	}
}
