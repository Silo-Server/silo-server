package notifications

import (
	"context"
	"fmt"

	"github.com/oklog/ulid/v2"
)

// OperationalDispatch describes how one operational delivery (a non-fanout
// notice such as webhook.auto_disabled or request.fulfilled) reaches the
// per-target channels. WebhookFilter selects which of the profile's enabled
// webhooks receive it; nil means the type must not reach webhooks at all
// (e.g. the auto-disable notice's loop guard). Web push has no per-type
// filter: profile-level gating happens before dispatch.
type OperationalDispatch struct {
	WebhookFilter func(Webhook) bool
}

// DispatchOperational durably creates one operational delivery. The inbox row
// and the per-target webhook / web push / Apple push outbox rows commit in a
// single transaction — a crash afterwards delays channel sends instead of dropping
// them, because the retry workers recover pending outbox rows — then realtime
// and channel dispatch run post-commit. Returns nil when the delivery deduped
// away (the partial unique indexes make operational notices idempotent).
func (s *System) DispatchOperational(ctx context.Context, delivery Delivery, opts OperationalDispatch) (*InsertedDelivery, error) {
	if s == nil {
		return nil, nil
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin operational dispatch tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	inserted, err := s.Deliveries.BulkInsert(ctx, tx, []Delivery{delivery})
	if err != nil {
		return nil, err
	}
	if len(inserted) == 0 {
		return nil, nil
	}
	row := inserted[0]

	if opts.WebhookFilter != nil && s.webhookRepo != nil && s.Settings.WebhooksEnabled(ctx) {
		hooksByProfile, err := s.webhookRepo.ListEnabledByProfiles(ctx, tx, []string{delivery.ProfileID})
		if err != nil {
			return nil, err
		}
		attempts := make([]DeliveryAttempt, 0, 2)
		for _, hook := range hooksByProfile[delivery.ProfileID] {
			if !opts.WebhookFilter(hook) {
				continue
			}
			attempts = append(attempts, DeliveryAttempt{
				ID:                     ulid.Make().String(),
				NotificationDeliveryID: row.ID,
				TargetID:               hook.ID,
			})
		}
		if err := s.webhookRepo.EnqueueAttempts(ctx, tx, attempts); err != nil {
			return nil, err
		}
	}
	if s.webPushRepo != nil && s.Settings.WebPushEnabled(ctx) {
		subsByProfile, err := s.webPushRepo.ListEnabledByProfiles(ctx, tx, []string{delivery.ProfileID})
		if err != nil {
			return nil, err
		}
		attempts := make([]DeliveryAttempt, 0, 2)
		for _, sub := range subsByProfile[delivery.ProfileID] {
			attempts = append(attempts, DeliveryAttempt{
				ID:                     ulid.Make().String(),
				NotificationDeliveryID: row.ID,
				TargetID:               sub.ID,
			})
		}
		if err := s.webPushRepo.EnqueueAttempts(ctx, tx, attempts); err != nil {
			return nil, err
		}
	}
	if s.pushDeviceRepo != nil && s.Settings.ApplePushDeliveryEnabled(ctx) {
		devicesByProfile, err := s.pushDeviceRepo.ListEnabledAppleByProfiles(ctx, tx, []string{delivery.ProfileID})
		if err != nil {
			return nil, err
		}
		attempts := newPushDeliveryAttempts(row.ID, devicesByProfile[delivery.ProfileID])
		if err := s.pushDeviceRepo.EnqueuePushAttempts(ctx, tx, attempts); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit operational dispatch: %w", err)
	}

	// Post-commit dispatch is best-effort: the durable inbox row covers
	// websocket reconnect, and the retry workers recover the outbox rows.
	full, err := s.Deliveries.GetRowByID(ctx, row.ID)
	if err != nil || full == nil {
		s.logger.WarnContext(ctx, "operational delivery reload failed",
			"delivery_id", row.ID, "error", err)
		return &row, nil
	}
	if err := s.dispatcher.Dispatch(ctx, *full); err != nil {
		s.logger.WarnContext(ctx, "operational delivery dispatch failed",
			"delivery_id", row.ID, "error", err)
	}
	return &row, nil
}
