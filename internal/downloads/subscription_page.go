package downloads

import (
	"context"
	"fmt"
	"time"
)

// ListSubscriptionsPage bounds device registry reads before decoding rows. The
// creation tuple stays stable when a subscription's options or active flag change.
func (s *Service) ListSubscriptionsPage(ctx context.Context, userID int, profileID, deviceID string, after *RegistryPosition, limit int) ([]*Subscription, error) {
	if s.subRepo == nil {
		return nil, ErrSubscriptionsUnavailable
	}
	if profileID == "" || deviceID == "" {
		return nil, ErrProfileRequired
	}
	return s.subRepo.ListPage(ctx, userID, profileID, deviceID, after, limit)
}

func (r *SubscriptionRepository) ListPage(ctx context.Context, userID int, profileID, deviceID string, after *RegistryPosition, limit int) ([]*Subscription, error) {
	if limit < 1 || limit > 101 {
		return nil, fmt.Errorf("subscription page limit must be 1 to 101")
	}
	var at *time.Time
	id := ""
	if after != nil {
		at = &after.CreatedAt
		id = after.ID
	}
	rows, err := r.pool.Query(ctx, `SELECT `+subscriptionColumns+` FROM download_subscriptions
 WHERE user_id=$1 AND profile_id=$2 AND device_id=$3
 AND ($4::timestamptz IS NULL OR (created_at,id)<($4,$5))
 ORDER BY created_at DESC,id DESC LIMIT $6`, userID, profileID, deviceID, at, id, limit)
	if err != nil {
		return nil, fmt.Errorf("paging subscriptions: %w", err)
	}
	defer rows.Close()
	return scanSubscriptions(rows)
}
