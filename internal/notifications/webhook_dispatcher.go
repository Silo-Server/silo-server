package notifications

import (
	"context"
	"log/slog"
	"sync"
	"time"
)

const (
	webhookDispatchWorkers = 16
	webhookDispatchQueue   = 256
	webhookRetryInterval   = 30 * time.Second
	webhookRetryClaimLimit = 50
)

// WebhookDispatcher implements the channel Dispatcher interface for outbound
// webhooks. Dispatch never blocks the fanout loop on destination HTTP: it
// hands the delivery ID to a bounded worker pool that claims the delivery's
// pending outbox attempts and sends them. A full queue simply drops the
// hand-off — the durable `pending` rows are picked up by the retry worker's
// outbox recovery sweep, so delivery is delayed, never lost.
type WebhookDispatcher struct {
	sender *webhookSender
	queue  chan string
	logger *slog.Logger
}

func newWebhookDispatcher(sender *webhookSender) *WebhookDispatcher {
	return &WebhookDispatcher{
		sender: sender,
		queue:  make(chan string, webhookDispatchQueue),
		logger: slog.Default().With("component", "notifications.webhooks.dispatch"),
	}
}

// Dispatch queues the delivery's webhook attempts for immediate send.
func (d *WebhookDispatcher) Dispatch(_ context.Context, delivery DeliveryRow) error {
	if d == nil {
		return nil
	}
	if delivery.Type == DeliveryTypeWebhookAutoDisabled {
		// Type deny list: an auto-disable notice must never re-dispatch as a
		// webhook, or a broken webhook would loop forever.
		return nil
	}
	select {
	case d.queue <- delivery.ID:
	default:
		d.logger.Warn("webhook dispatch queue full; deferring to retry worker",
			"delivery_id", delivery.ID)
	}
	return nil
}

// Run consumes the dispatch queue with a bounded worker pool until ctx is
// canceled. One slow destination cannot block other deliveries.
func (d *WebhookDispatcher) Run(ctx context.Context) {
	var wg sync.WaitGroup
	for range webhookDispatchWorkers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-ctx.Done():
					return
				case deliveryID := <-d.queue:
					d.processDelivery(ctx, deliveryID)
				}
			}
		}()
	}
	wg.Wait()
}

func (d *WebhookDispatcher) processDelivery(ctx context.Context, deliveryID string) {
	attempts, err := d.sender.webhooks.ClaimPendingForDelivery(ctx, deliveryID)
	if err != nil {
		if ctx.Err() == nil {
			d.logger.Warn("webhook attempt claim failed", "delivery_id", deliveryID, "error", err)
		}
		return
	}
	for _, attempt := range attempts {
		if ctx.Err() != nil {
			return
		}
		d.sender.processAttempt(ctx, attempt)
	}
}

// WebhookRetryWorker drains due retries and recovers stale pending outbox
// rows whose post-commit dispatch never ran (process crash between the fanout
// commit and dispatch).
type WebhookRetryWorker struct {
	sender *webhookSender
	logger *slog.Logger
}

func newWebhookRetryWorker(sender *webhookSender) *WebhookRetryWorker {
	return &WebhookRetryWorker{
		sender: sender,
		logger: slog.Default().With("component", "notifications.webhooks.retry"),
	}
}

// Run polls for due attempts until ctx is canceled.
func (w *WebhookRetryWorker) Run(ctx context.Context) {
	ticker := time.NewTicker(webhookRetryInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		if !w.sender.settings.WebhooksEnabled(ctx) {
			continue
		}
		for {
			attempts, err := w.sender.webhooks.ClaimDue(ctx, webhookRetryClaimLimit)
			if err != nil {
				if ctx.Err() == nil {
					w.logger.Warn("webhook retry claim failed", "error", err)
				}
				break
			}
			if len(attempts) == 0 {
				break
			}
			for _, attempt := range attempts {
				if ctx.Err() != nil {
					return
				}
				w.sender.processAttempt(ctx, attempt)
			}
		}
	}
}
