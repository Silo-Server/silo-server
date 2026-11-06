package notifications

import (
	"context"
	"log/slog"
)

// PluginDestinationDispatcher sends a delivery's plugin destination
// attempts after the delivery commits, and sweeps due retries and stranded
// pending rows, on the shared channelDispatcher core.
type PluginDestinationDispatcher struct {
	core channelDispatcher[DeliveryAttempt]
}

func newPluginDestinationDispatcher(sender *pluginDestinationSender) *PluginDestinationDispatcher {
	return &PluginDestinationDispatcher{core: channelDispatcher[DeliveryAttempt]{
		channel:      "plugin destination",
		queue:        make(chan string, webhookDispatchQueue),
		logger:       slog.Default().With("component", "notifications.plugin_destinations.dispatch"),
		claimPending: sender.repo.ClaimPendingForDelivery,
		process:      sender.processAttempt,
		// Plugins may not be wired yet at startup; sweeping then would only
		// claim attempts to leave them for the next lease.
		enabled:    func(context.Context) bool { return sender.host() != nil },
		claimDue:   sender.repo.ClaimDue,
		claimLimit: pluginDestinationClaimLimit,
	}}
}

// Dispatch queues the delivery's plugin destination attempts. Auto-disable
// notices never get attempts, so they never arrive here with any to send.
func (d *PluginDestinationDispatcher) Dispatch(_ context.Context, delivery DeliveryRow) error {
	if d == nil || delivery.Type == DeliveryTypePluginDestinationAutoDisabled || delivery.Type == DeliveryTypeWebhookAutoDisabled {
		return nil
	}
	d.core.dispatch(delivery.ID)
	return nil
}

// Run sends queued deliveries and sweeps retries until ctx is canceled.
func (d *PluginDestinationDispatcher) Run(ctx context.Context) {
	d.core.run(ctx)
}
