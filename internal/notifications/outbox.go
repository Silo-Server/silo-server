package notifications

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/oklog/ulid/v2"
)

// channelOutbox enqueues the per-target attempt rows (webhooks, web push,
// mobile push) for newly inserted deliveries inside the transaction that
// inserted them, so a crash after commit delays a send instead of dropping
// it: the retry sweeps recover pending rows. A nil repository leaves its
// channel out. Fanout and operational dispatch share it so a channel cannot
// be wired into one path and forgotten in the other.
type channelOutbox struct {
	settings    *Settings
	webhooks    *WebhookRepository
	webPush     *WebPushRepository
	pushDevices *PushDeviceRepository
}

// enqueue writes the attempt rows for inserted. webhookAllowed decides, per
// delivery and enabled webhook, whether the webhook receives it; nil keeps
// the deliveries off webhooks entirely. Web push and mobile push receive the
// pushable deliveries, a subset of inserted, with no per-target filter:
// profile preferences already gated delivery creation.
func (o channelOutbox) enqueue(ctx context.Context, tx pgx.Tx, inserted, pushable []InsertedDelivery, webhookAllowed func(InsertedDelivery, Webhook) bool) error {
	if err := o.enqueueWebhooks(ctx, tx, inserted, webhookAllowed); err != nil {
		return err
	}
	if err := o.enqueueWebPush(ctx, tx, pushable); err != nil {
		return err
	}
	return o.enqueueMobilePush(ctx, tx, pushable)
}

func (o channelOutbox) enqueueWebhooks(ctx context.Context, tx pgx.Tx, inserted []InsertedDelivery, allowed func(InsertedDelivery, Webhook) bool) error {
	if len(inserted) == 0 || allowed == nil || o.webhooks == nil || !o.settings.WebhooksEnabled(ctx) {
		return nil
	}
	hooksByProfile, err := o.webhooks.ListEnabledByProfiles(ctx, tx, insertedProfileKeys(inserted))
	if err != nil {
		return err
	}
	attempts := make([]DeliveryAttempt, 0, len(inserted))
	for _, row := range inserted {
		for _, hook := range hooksByProfile[row.key()] {
			if allowed(row, hook) {
				attempts = append(attempts, newDeliveryAttempt(row.ID, hook.ID))
			}
		}
	}
	return o.webhooks.EnqueueAttempts(ctx, tx, attempts)
}

func (o channelOutbox) enqueueWebPush(ctx context.Context, tx pgx.Tx, inserted []InsertedDelivery) error {
	if len(inserted) == 0 || o.webPush == nil || !o.settings.WebPushEnabled(ctx) {
		return nil
	}
	subsByProfile, err := o.webPush.ListEnabledByProfiles(ctx, tx, insertedProfileKeys(inserted))
	if err != nil {
		return err
	}
	attempts := make([]DeliveryAttempt, 0, len(inserted))
	for _, row := range inserted {
		for _, sub := range subsByProfile[row.key()] {
			attempts = append(attempts, newDeliveryAttempt(row.ID, sub.ID))
		}
	}
	return o.webPush.EnqueueAttempts(ctx, tx, attempts)
}

func (o channelOutbox) enqueueMobilePush(ctx context.Context, tx pgx.Tx, inserted []InsertedDelivery) error {
	if len(inserted) == 0 || o.pushDevices == nil {
		return nil
	}
	platforms := o.settings.EnabledPushPlatforms(ctx)
	if len(platforms) == 0 {
		return nil
	}
	devicesByProfile, err := o.pushDevices.ListEnabledPushByProfiles(ctx, tx, insertedProfileKeys(inserted), platforms)
	if err != nil {
		return err
	}
	attempts := make([]PushDeliveryAttempt, 0, len(inserted))
	for _, row := range inserted {
		attempts = append(attempts, newPushDeliveryAttempts(row.ID, devicesByProfile[row.key()])...)
	}
	return o.pushDevices.EnqueuePushAttempts(ctx, tx, attempts)
}

func newDeliveryAttempt(deliveryID, targetID string) DeliveryAttempt {
	return DeliveryAttempt{ID: ulid.Make().String(), NotificationDeliveryID: deliveryID, TargetID: targetID}
}

// insertedProfileKeys lists the distinct account/profile pairs of the
// inserted deliveries. Channel targets are looked up by both: profile IDs
// repeat across accounts, and one account's delivery must not reach another
// account's devices or webhooks.
func insertedProfileKeys(inserted []InsertedDelivery) []profileKey {
	seen := make(map[profileKey]struct{}, len(inserted))
	keys := make([]profileKey, 0, len(inserted))
	for _, row := range inserted {
		key := row.key()
		if _, ok := seen[key]; !ok {
			seen[key] = struct{}{}
			keys = append(keys, key)
		}
	}
	return keys
}
