package notifications

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
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
	// PluginDestinationFilter does for plugin destinations what
	// WebhookFilter does for webhooks; nil keeps the delivery off them.
	PluginDestinationFilter func(PluginDestination) bool
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
	channels := s.operationalChannels(ctx)
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin operational dispatch tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	row, err := s.insertOperational(ctx, tx, delivery, opts, recipient, channels)
	if err != nil || row == nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit operational dispatch: %w", err)
	}
	s.dispatchOperationalRow(ctx, *row)
	return row, nil
}

// operationalChannelSettings is which channels the administrator has on,
// read before a delivery's transaction: the settings are read through the
// pool, and a caller holding a transaction must not wait for a second
// connection, which a busy pool may never give it.
type operationalChannelSettings struct {
	webhooks      bool
	webPush       bool
	pushPlatforms []string
}

func (s *System) operationalChannels(ctx context.Context) operationalChannelSettings {
	var out operationalChannelSettings
	if s.webhookRepo != nil {
		out.webhooks = s.Settings.WebhooksEnabled(ctx)
	}
	if s.webPushRepo != nil {
		out.webPush = s.Settings.WebPushEnabled(ctx)
	}
	if s.pushDeviceRepo != nil {
		out.pushPlatforms = s.Settings.EnabledPushPlatforms(ctx)
	}
	return out
}

// insertOperational is DispatchOperational's transaction: the inbox row and
// its outbox rows, written through tx, which the caller commits before
// dispatchOperationalRow. recipient is the resolved access of a delivery
// that names a catalog item, and nil otherwise; channels is
// operationalChannels, read before tx began. Everything here reads through
// tx.
func (s *System) insertOperational(ctx context.Context, tx pgx.Tx, delivery Delivery, opts OperationalDispatch, recipient *recipientAccess, channels operationalChannelSettings) (*InsertedDelivery, error) {
	if recipient == nil && delivery.SeriesID != nil && *delivery.SeriesID != "" {
		return nil, fmt.Errorf("operational delivery %s names a catalog item without the recipient's access", delivery.ID)
	}
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
	owner := profileKey{delivery.UserID, delivery.ProfileID}

	if opts.WebhookFilter != nil && s.webhookRepo != nil && channels.webhooks {
		hooksByProfile, err := s.webhookRepo.ListEnabledByProfiles(ctx, tx, []profileKey{owner})
		if err != nil {
			return nil, err
		}
		attempts := make([]DeliveryAttempt, 0, 2)
		for _, hook := range hooksByProfile[owner] {
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
	if err := s.enqueueOperationalPluginDestinations(ctx, tx, &row, opts.PluginDestinationFilter); err != nil {
		return nil, err
	}
	if s.webPushRepo != nil && channels.webPush {
		subsByProfile, err := s.webPushRepo.ListEnabledByProfiles(ctx, tx, []profileKey{owner})
		if err != nil {
			return nil, err
		}
		attempts := make([]DeliveryAttempt, 0, 2)
		for _, sub := range subsByProfile[owner] {
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
	if s.pushDeviceRepo != nil {
		if platforms := channels.pushPlatforms; len(platforms) > 0 {
			devicesByProfile, err := s.pushDeviceRepo.ListEnabledPushByProfiles(ctx, tx, []profileKey{owner}, platforms)
			if err != nil {
				return nil, err
			}
			attempts := newPushDeliveryAttempts(row.ID, devicesByProfile[owner])
			if err := s.pushDeviceRepo.EnqueuePushAttempts(ctx, tx, attempts); err != nil {
				return nil, err
			}
		}
	}
	return &row, nil
}

// enqueueOperationalPluginDestinations queues an operational delivery for
// the owner's plugin destinations that filter selects; a nil filter keeps
// it off them. It marks row when an attempt was inserted.
func (s *System) enqueueOperationalPluginDestinations(ctx context.Context, tx pgx.Tx, row *InsertedDelivery, filter func(PluginDestination) bool) error {
	if filter == nil || s.pluginDestinationSender == nil {
		return nil
	}
	owner := row.key()
	byProfile, err := s.pluginDestinationSender.enqueueable(ctx, tx, []profileKey{owner})
	if err != nil {
		return err
	}
	attempts := make([]DeliveryAttempt, 0, 2)
	for _, destination := range byProfile[owner] {
		if !filter(destination) {
			continue
		}
		attempts = append(attempts, DeliveryAttempt{
			ID:                     ulid.Make().String(),
			NotificationDeliveryID: row.ID,
			TargetID:               destination.ID,
		})
	}
	queued, err := s.pluginDestinationSender.repo.EnqueueAttempts(ctx, tx, attempts)
	row.pluginAttempts = queued[row.ID]
	return err
}

// dispatchOperationalRow hands a committed operational delivery to realtime
// and the channel dispatchers. It is best-effort: the durable inbox row
// covers websocket reconnect, and the retry workers recover the outbox rows.
func (s *System) dispatchOperationalRow(ctx context.Context, row InsertedDelivery) {
	full, err := s.Deliveries.GetRowByID(ctx, row.ID)
	if err != nil || full == nil {
		s.logger.WarnContext(ctx, "operational delivery reload failed",
			"delivery_id", row.ID, "error", err)
		return
	}
	full.pluginAttempts = row.pluginAttempts
	if err := s.dispatcher.Dispatch(ctx, *full); err != nil {
		s.logger.WarnContext(ctx, "operational delivery dispatch failed",
			"delivery_id", row.ID, "error", err)
	}
}
