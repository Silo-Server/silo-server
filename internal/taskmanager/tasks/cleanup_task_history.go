package tasks

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/Silo-Server/silo-server/internal/taskmanager"
)

// Batch geometry is a compile-time property of the delete strategy, not an
// operator knob: it bounds how much of the table a single run may rewrite.
const (
	taskHistoryCleanupBatchSize  = 10000
	taskHistoryCleanupMaxBatches = 100
)

// taskHistoryPruner deletes a bounded amount of task execution history.
type taskHistoryPruner interface {
	Prune(
		ctx context.Context,
		keepPerTask int,
		cutoff time.Time,
		batchSize int,
		maxBatches int,
	) (taskmanager.HistoryPruneResult, error)
}

// TaskHistoryCleanupTask prunes old task execution history.
type TaskHistoryCleanupTask struct {
	pruner taskHistoryPruner
	store  taskmanager.SettingsStore
}

// NewTaskHistoryCleanupTask creates a scheduled task for task history retention.
func NewTaskHistoryCleanupTask(pruner taskHistoryPruner, store taskmanager.SettingsStore) *TaskHistoryCleanupTask {
	return &TaskHistoryCleanupTask{pruner: pruner, store: store}
}

func (t *TaskHistoryCleanupTask) Key() string  { return "cleanup_task_history" }
func (t *TaskHistoryCleanupTask) Name() string { return "Cleanup Task History" }
func (t *TaskHistoryCleanupTask) Description() string {
	return "Prunes old background task execution history"
}
func (t *TaskHistoryCleanupTask) Category() taskmanager.TaskCategory {
	return taskmanager.TaskCategorySystem
}
func (t *TaskHistoryCleanupTask) IsHidden() bool { return false }

func (t *TaskHistoryCleanupTask) DefaultTriggers() []taskmanager.TriggerConfig {
	return []taskmanager.TriggerConfig{
		{Type: taskmanager.TriggerTypeStartup},
		{Type: taskmanager.TriggerTypeInterval, IntervalMs: int64((24 * time.Hour) / time.Millisecond)},
	}
}

func (t *TaskHistoryCleanupTask) Execute(ctx context.Context, progress taskmanager.ProgressReporter) error {
	progress.Report(0, "Pruning task execution history")
	retention := taskmanager.LoadHistoryRetention(ctx, t.store)
	cutoff := time.Now().UTC().AddDate(0, 0, -retention.RetentionDays)
	result, err := t.pruner.Prune(
		ctx,
		retention.KeepPerTask,
		cutoff,
		taskHistoryCleanupBatchSize,
		taskHistoryCleanupMaxBatches,
	)
	if data, marshalErr := json.Marshal(result); marshalErr == nil {
		progress.SetResultData(data)
	}
	if err != nil {
		slog.WarnContext(ctx, "task history cleanup failed", "component", "taskmanager", "task", t.Key(), "deleted", result.Deleted, "error", err)
		progress.Report(100, fmt.Sprintf("Task history cleanup failed after deleting %d executions", result.Deleted))
		return err
	}
	if result.Skipped {
		slog.InfoContext(ctx, "task history cleanup skipped; another node holds the cleanup lock",
			"component", "taskmanager", "task", t.Key())
		progress.Report(100, "Task history cleanup is already running on another node")
		return nil
	}
	if result.LimitReached {
		slog.WarnContext(ctx, "task history cleanup reached per-run limit",
			"component", "taskmanager", "task", t.Key(), "deleted", result.Deleted)
		progress.Report(100, fmt.Sprintf(
			"Pruned %d task executions; remaining history will be pruned on the next run",
			result.Deleted,
		))
		return nil
	}
	if result.Deleted > 0 {
		slog.InfoContext(ctx, "task history cleanup completed",
			"component", "taskmanager", "task", t.Key(), "deleted", result.Deleted,
			"retention_days", retention.RetentionDays, "keep_per_task", retention.KeepPerTask)
	}
	progress.Report(100, fmt.Sprintf("Pruned %d task executions", result.Deleted))
	return nil
}
