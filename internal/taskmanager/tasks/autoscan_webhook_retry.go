package tasks

import (
	"context"
	"fmt"

	"github.com/Silo-Server/silo-server/internal/taskmanager"
)

const (
	autoscanWebhookRetryIntervalMs int64 = 15 * 1000
	autoscanWebhookRetryBatch            = 100
)

type AutoscanWebhookRetrier interface {
	RetryPendingWebhookDeliveries(ctx context.Context, limit int) (int, error)
}

// AutoscanWebhookRetryTask drains the durable webhook inbox independently of
// the scan-source poll cadence. It stays hidden because it is reliability
// plumbing rather than an operator-facing maintenance action.
type AutoscanWebhookRetryTask struct {
	retrier AutoscanWebhookRetrier
}

func NewAutoscanWebhookRetryTask(retrier AutoscanWebhookRetrier) *AutoscanWebhookRetryTask {
	return &AutoscanWebhookRetryTask{retrier: retrier}
}

func (t *AutoscanWebhookRetryTask) Key() string  { return "autoscan_webhook_retry" }
func (t *AutoscanWebhookRetryTask) Name() string { return "Autoscan webhook retry" }
func (t *AutoscanWebhookRetryTask) Description() string {
	return "Retry durably accepted Autoscan webhook deliveries"
}
func (t *AutoscanWebhookRetryTask) Category() taskmanager.TaskCategory {
	return taskmanager.TaskCategoryLibrary
}
func (t *AutoscanWebhookRetryTask) IsHidden() bool { return true }

func (t *AutoscanWebhookRetryTask) DefaultTriggers() []taskmanager.TriggerConfig {
	return []taskmanager.TriggerConfig{{
		Type:       taskmanager.TriggerTypeInterval,
		IntervalMs: autoscanWebhookRetryIntervalMs,
	}}
}

func (t *AutoscanWebhookRetryTask) Execute(ctx context.Context, progress taskmanager.ProgressReporter) error {
	if t.retrier == nil {
		return nil
	}
	progress.Report(0, "Checking durable webhook deliveries")
	processed, err := t.retrier.RetryPendingWebhookDeliveries(ctx, autoscanWebhookRetryBatch)
	if err != nil {
		return fmt.Errorf("retry autoscan webhook deliveries: %w", err)
	}
	progress.Report(100, fmt.Sprintf("Processed %d webhook deliveries", processed))
	return nil
}
