package tasks

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/Silo-Server/silo-server/internal/autoscan"
	"github.com/Silo-Server/silo-server/internal/taskmanager"
)

// Batch geometry is a compile-time property of the delete strategy, not an
// operator knob. Events can carry a change log of up to 50 paths, and deleting
// one clears the event link on its scan runs, so batches stay small; one run
// still clears a year of 10-minute polls from three sources.
const (
	autoscanEventCleanupBatchSize  = 1000
	autoscanEventCleanupMaxBatches = 200
)

// autoscanEventPruner deletes a bounded amount of autoscan event history;
// *autoscan.Repository satisfies it.
type autoscanEventPruner interface {
	PruneEvents(ctx context.Context, cutoff time.Time, batchSize, maxBatches int) (autoscan.EventPruneResult, error)
}

// AutoscanEventCleanupTask prunes old autoscan events. Every poll of every
// enabled source and every webhook delivery writes one, and nothing else
// removes them.
type AutoscanEventCleanupTask struct {
	pruner autoscanEventPruner
	store  autoscan.SettingsStore
}

// NewAutoscanEventCleanupTask creates the retention step for autoscan events.
func NewAutoscanEventCleanupTask(pruner autoscanEventPruner, store autoscan.SettingsStore) *AutoscanEventCleanupTask {
	return &AutoscanEventCleanupTask{pruner: pruner, store: store}
}

func (t *AutoscanEventCleanupTask) Key() string  { return "cleanup_autoscan_events" }
func (t *AutoscanEventCleanupTask) Name() string { return "Cleanup Autoscan Events" }
func (t *AutoscanEventCleanupTask) Description() string {
	return "Prunes old autoscan poll and webhook events from the Activity history"
}
func (t *AutoscanEventCleanupTask) Category() taskmanager.TaskCategory {
	return taskmanager.TaskCategorySystem
}
func (t *AutoscanEventCleanupTask) IsHidden() bool { return false }

// DefaultTriggers matches the neighboring retention steps: once at startup,
// then daily.
func (t *AutoscanEventCleanupTask) DefaultTriggers() []taskmanager.TriggerConfig {
	return []taskmanager.TriggerConfig{
		{Type: taskmanager.TriggerTypeStartup},
		{Type: taskmanager.TriggerTypeInterval, IntervalMs: int64((24 * time.Hour) / time.Millisecond)},
	}
}

func (t *AutoscanEventCleanupTask) Execute(ctx context.Context, progress taskmanager.ProgressReporter) error {
	if t == nil || t.pruner == nil {
		progress.Report(100, "Autoscan event cleanup is not configured")
		return nil
	}
	progress.Report(0, "Pruning autoscan events")
	days, err := autoscan.LoadEventsRetentionDays(ctx, t.store)
	if err != nil {
		slog.WarnContext(ctx, "autoscan event cleanup skipped; retention setting unreadable", "component", "taskmanager", "task", t.Key(), "error", err)
		progress.Report(100, "Autoscan event cleanup skipped: the retention setting could not be read")
		return err
	}
	cutoff := time.Now().UTC().AddDate(0, 0, -days)
	result, err := t.pruner.PruneEvents(ctx, cutoff, autoscanEventCleanupBatchSize, autoscanEventCleanupMaxBatches)
	if data, marshalErr := json.Marshal(result); marshalErr == nil {
		progress.SetResultData(data)
	}
	if err != nil {
		slog.WarnContext(ctx, "autoscan event cleanup failed", "component", "taskmanager", "task", t.Key(), "deleted", result.Deleted, "error", err)
		progress.Report(100, fmt.Sprintf("Autoscan event cleanup failed after deleting %d events", result.Deleted))
		return err
	}
	if result.LimitReached {
		slog.WarnContext(ctx, "autoscan event cleanup reached per-run limit",
			"component", "taskmanager", "task", t.Key(), "deleted", result.Deleted)
		progress.Report(100, fmt.Sprintf(
			"Pruned %d autoscan events; remaining history will be pruned on the next run",
			result.Deleted,
		))
		return nil
	}
	if result.Deleted > 0 {
		slog.InfoContext(ctx, "autoscan event cleanup completed",
			"component", "taskmanager", "task", t.Key(), "deleted", result.Deleted, "retention_days", days)
	}
	progress.Report(100, fmt.Sprintf("Pruned %d autoscan events", result.Deleted))
	return nil
}
