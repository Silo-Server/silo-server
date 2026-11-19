package notifications

import (
	"context"
	"fmt"
	"time"
)

// RetentionStats reports what a retention pass removed.
type RetentionStats struct {
	DeliveriesDeleted  int64
	EventsDeleted      int64
	StaleEventsDeleted int64
	InterestPruned     int64
	// WatchlistPromotionsDeleted counts promotion records past the fanout
	// staleness horizon.
	WatchlistPromotionsDeleted int64
	WebhookAttemptsDeleted     int64
	// PluginDestinationAttemptsDeleted counts plugin destination attempts
	// past their retention.
	PluginDestinationAttemptsDeleted int64
	WebPushAttemptsDeleted           int64
	WebPushSubsDeleted               int64
	// UnboundWebPushSubsDeleted counts subscriptions with no recorded login
	// whose account has no live login and no API key.
	UnboundWebPushSubsDeleted int64
	PushRegistrationsDeleted  int64
	// UnboundPushRegistrationsDeleted counts registrations from before
	// login_session_id whose device has no live login.
	UnboundPushRegistrationsDeleted int64
	DiscordLinkStatesDeleted        int64
	// Email verification outbox: payloads dropped from terminal rows whose
	// link expired, and receipt rows deleted past the retention window.
	EmailVerificationPayloadsRetired int64
	EmailVerificationReceiptsDeleted int64
}

// RunRetention applies the retention policy: read deliveries past the read
// window, unread past the unread window, processed release events past the
// debug window, unprocessed release events and watchlist promotion records
// past the fanout staleness horizon, inert interest rows, web push subscriptions whose login session ended or, for
// one with no recorded login, whose account has no live one, and push
// registrations whose login ended or, for one with no recorded login, whose
// device has no live one.
func (s *System) RunRetention(ctx context.Context) (RetentionStats, error) {
	var stats RetentionStats
	now := time.Now().UTC()
	readCutoff := now.AddDate(0, 0, -s.Settings.ReadRetentionDays(ctx))
	unreadCutoff := now.AddDate(0, 0, -s.Settings.UnreadRetentionDays(ctx))
	eventCutoff := now.AddDate(0, 0, -s.Settings.EventRetentionDays(ctx))

	deleted, err := s.Deliveries.DeleteOld(ctx, readCutoff, unreadCutoff, now.Add(-s.Settings.MaxEventAge(ctx)))
	if err != nil {
		return stats, fmt.Errorf("prune deliveries: %w", err)
	}
	stats.DeliveriesDeleted = deleted

	events, err := s.Releases.DeleteProcessedBefore(ctx, eventCutoff)
	if err != nil {
		return stats, fmt.Errorf("prune release events: %w", err)
	}
	stats.EventsDeleted = events

	// Unprocessed events accumulate without bound when fanout is disabled
	// while availability detection keeps emitting; the fanout worker would
	// suppress them as stale anyway, so retention reclaims them directly.
	staleEvents, err := s.Releases.DeleteUnprocessedBefore(ctx, now.Add(-s.Settings.MaxEventAge(ctx)))
	if err != nil {
		return stats, fmt.Errorf("prune stale release events: %w", err)
	}
	stats.StaleEventsDeleted = staleEvents

	// Watchlist promotion records past the same horizon can no longer make
	// an arrival (watchlist.Arrivals.PrunePromotions).
	if s.Fanout != nil && s.Fanout.watchlist != nil && s.Fanout.pool != nil {
		promotions, err := s.Fanout.watchlist.PrunePromotions(ctx, s.Fanout.pool, now.Add(-s.Settings.MaxEventAge(ctx)))
		if err != nil {
			return stats, fmt.Errorf("prune watchlist promotions: %w", err)
		}
		stats.WatchlistPromotionsDeleted = promotions
	}

	pruned, err := s.Interests.PruneInert(ctx)
	if err != nil {
		return stats, fmt.Errorf("prune interest rows: %w", err)
	}
	stats.InterestPruned = pruned

	if s.webhookRepo != nil {
		attempts, err := s.webhookRepo.DeleteOldAttempts(ctx, now)
		if err != nil {
			return stats, fmt.Errorf("prune webhook attempts: %w", err)
		}
		stats.WebhookAttemptsDeleted = attempts
	}
	if stats.PluginDestinationAttemptsDeleted, err = s.prunePluginDestinationAttempts(ctx, now); err != nil {
		return stats, err
	}
	if s.webPushRepo != nil {
		attempts, err := s.webPushRepo.DeleteOldAttempts(ctx, now)
		if err != nil {
			return stats, fmt.Errorf("prune web push attempts: %w", err)
		}
		stats.WebPushAttemptsDeleted = attempts
		subscriptions, err := s.webPushRepo.DeleteEndedLoginSubscriptions(ctx)
		if err != nil {
			return stats, fmt.Errorf("prune web push subscriptions of ended logins: %w", err)
		}
		stats.WebPushSubsDeleted = subscriptions
		unboundSubscriptions, err := s.webPushRepo.DeleteSubscriptionsWithNoLogin(ctx)
		if err != nil {
			return stats, fmt.Errorf("prune unbound web push subscriptions: %w", err)
		}
		stats.UnboundWebPushSubsDeleted = unboundSubscriptions
	}
	if s.pushDeviceRepo != nil {
		registrations, err := s.pushDeviceRepo.DeleteEndedLoginRegistrations(ctx)
		if err != nil {
			return stats, fmt.Errorf("prune push registrations of ended logins: %w", err)
		}
		stats.PushRegistrationsDeleted = registrations
		unbound, err := s.pushDeviceRepo.DeleteRegistrationsWithNoLogin(ctx)
		if err != nil {
			return stats, fmt.Errorf("prune unbound push registrations: %w", err)
		}
		stats.UnboundPushRegistrationsDeleted = unbound
	}
	if s.DiscordPrefs != nil {
		states, err := s.DiscordPrefs.DeleteExpiredLinkStates(ctx)
		if err != nil {
			return stats, fmt.Errorf("prune discord link states: %w", err)
		}
		stats.DiscordLinkStatesDeleted = states
	}
	if s.EmailPrefs != nil {
		payloads, rows, err := s.EmailPrefs.RetireVerificationDispatch(ctx, now)
		if err != nil {
			return stats, fmt.Errorf("prune email verification outbox: %w", err)
		}
		stats.EmailVerificationPayloadsRetired = payloads
		stats.EmailVerificationReceiptsDeleted = rows
	}
	return stats, nil
}
