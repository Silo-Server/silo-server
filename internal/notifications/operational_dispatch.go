package notifications

import (
	"context"
	"fmt"
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
// away (the partial unique indexes make operational notices idempotent), or
// when it names a catalog item (SeriesID) the recipient may not open now.
//
// Targets are the recipient profile's on the recipient's account: profile ids
// repeat across accounts (every account from before profiles has one named
// "default"), and a request.fulfilled for one account's "default" profile must
// not reach another account's devices or webhooks.
func (s *System) DispatchOperational(ctx context.Context, delivery Delivery, opts OperationalDispatch) (*InsertedDelivery, error) {
	if s == nil {
		return nil, nil
	}
	var recipient *recipientAccess
	if delivery.SeriesID != nil && *delivery.SeriesID != "" {
		// The resolver reads through the pool, so resolve before the
		// transaction holds a connection; canOpen below reuses the result.
		recipient = newRecipientAccess(s.scopes)
		if _, err := recipient.scope(ctx, delivery.UserID, delivery.ProfileID); err != nil {
			return nil, err
		}
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin operational dispatch tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if recipient != nil {
		allowed, err := recipient.canOpen(ctx, tx, delivery.UserID, delivery.ProfileID, *delivery.SeriesID, 0)
		if err != nil {
			return nil, err
		}
		if !allowed {
			return nil, nil
		}
	}

	inserted, err := s.Deliveries.BulkInsert(ctx, tx, []Delivery{delivery})
	if err != nil {
		return nil, err
	}
	if len(inserted) == 0 {
		return nil, nil
	}
	row := inserted[0]
	var webhookAllowed func(InsertedDelivery, Webhook) bool
	if opts.WebhookFilter != nil {
		webhookAllowed = func(_ InsertedDelivery, hook Webhook) bool { return opts.WebhookFilter(hook) }
	}
	if err := s.outbox.enqueue(ctx, tx, inserted, inserted, webhookAllowed); err != nil {
		return nil, err
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
