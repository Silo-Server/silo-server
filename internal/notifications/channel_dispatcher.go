package notifications

import (
	"context"
	"log/slog"
	"sync"
	"time"
)

// channelDispatcher is the shared dispatch core behind the per-channel
// Dispatcher implementations (webhooks, web push, Apple push). dispatch never
// blocks the fanout loop on destination I/O: it hands the delivery ID to a
// bounded worker pool that claims the delivery's pending outbox attempts and
// sends them. A full queue simply drops the hand-off — the durable pending
// rows are picked up by the retry sweep, so delivery is delayed, never lost.
type channelDispatcher[A any] struct {
	channel      string // log label, e.g. "web push"
	queue        chan string
	logger       *slog.Logger
	claimPending func(ctx context.Context, deliveryID string) ([]A, error)
	process      func(ctx context.Context, attempt A)

	// Optional integrated retry sweep: when claimDue is set, run also drains
	// due retries and recovers stale pending rows, skipping ticks while
	// enabled reports the channel off. Webhooks instead run the standalone
	// WebhookRetryWorker, whose lifecycle the system manages separately.
	enabled    func(ctx context.Context) bool
	claimDue   func(ctx context.Context, limit int) ([]A, error)
	claimLimit int
}

// dispatch queues the delivery's attempts for immediate send without blocking.
func (d *channelDispatcher[A]) dispatch(deliveryID string) {
	select {
	case d.queue <- deliveryID:
	default:
		d.logger.Warn("dispatch queue full; deferring to retry worker",
			"channel", d.channel, "delivery_id", deliveryID)
	}
}

// run consumes the dispatch queue with a bounded worker pool (plus the retry
// sweep when configured) until ctx is canceled. One slow destination cannot
// block other deliveries.
func (d *channelDispatcher[A]) run(ctx context.Context) {
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
	if d.claimDue != nil {
		wg.Add(1)
		go func() {
			defer wg.Done()
			runRetrySweep(ctx, d.channel, d.logger, d.enabled, d.claimDue, d.claimLimit, d.process)
		}()
	}
	wg.Wait()
}

func (d *channelDispatcher[A]) processDelivery(ctx context.Context, deliveryID string) {
	attempts, err := d.claimPending(ctx, deliveryID)
	if err != nil {
		if ctx.Err() == nil {
			d.logger.WarnContext(ctx, "attempt claim failed", "channel", d.channel, "delivery_id", deliveryID, "error", err)
		}
		return
	}
	for _, attempt := range attempts {
		if ctx.Err() != nil {
			return
		}
		d.process(ctx, attempt)
	}
}

// runRetrySweep polls for due attempts until ctx is canceled, draining every
// due batch each tick. Shared by the integrated dispatcher sweeps and the
// standalone webhook retry worker.
func runRetrySweep[A any](
	ctx context.Context,
	channel string,
	logger *slog.Logger,
	enabled func(context.Context) bool,
	claim func(context.Context, int) ([]A, error),
	limit int,
	process func(context.Context, A),
) {
	ticker := time.NewTicker(webhookRetryInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		if !enabled(ctx) {
			continue
		}
		for {
			attempts, err := claim(ctx, limit)
			if err != nil {
				if ctx.Err() == nil {
					logger.WarnContext(ctx, "retry claim failed", "channel", channel, "error", err)
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
				process(ctx, attempt)
			}
		}
	}
}
