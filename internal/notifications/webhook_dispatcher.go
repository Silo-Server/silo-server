package notifications

import (
	"context"
	"log/slog"
	"time"
)

const (
	webhookDispatchWorkers = 16
	webhookDispatchQueue   = 256
	webhookRetryInterval   = 30 * time.Second
	webhookRetryClaimLimit = 50
)

// WebhookDispatcher implements the channel Dispatcher interface for outbound
// webhooks on top of the shared channelDispatcher core. Retry/recovery runs in
// the standalone WebhookRetryWorker.
type WebhookDispatcher struct {
	core channelDispatcher[DeliveryAttempt]
}

func newWebhookDispatcher(sender *webhookSender) *WebhookDispatcher {
	return &WebhookDispatcher{core: channelDispatcher[DeliveryAttempt]{
		channel:      "webhook",
		queue:        make(chan string, webhookDispatchQueue),
		logger:       slog.Default().With("component", "notifications.webhooks.dispatch"),
		claimPending: sender.webhooks.ClaimPendingForDelivery,
		process:      sender.processAttempt,
	}}
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
	d.core.dispatch(delivery.ID)
	return nil
}

// Run consumes the dispatch queue with a bounded worker pool until ctx is
// canceled. One slow destination cannot block other deliveries.
func (d *WebhookDispatcher) Run(ctx context.Context) {
	d.core.run(ctx)
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
	runRetrySweep(ctx, "webhook", w.logger,
		w.sender.settings.WebhooksEnabled, w.sender.webhooks.ClaimDue,
		webhookRetryClaimLimit, w.sender.processAttempt)
}
