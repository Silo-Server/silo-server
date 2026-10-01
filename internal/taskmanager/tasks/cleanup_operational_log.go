package tasks

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/opslog"
	"github.com/Silo-Server/silo-server/internal/taskmanager"
)

// OperationalLogCleanupTask prunes expired operational log rows and partitions.
type OperationalLogCleanupTask struct {
	pool  *pgxpool.Pool
	store opslog.SettingsStore
	pm    opslog.PartitionManager
}

// NewOperationalLogCleanupTask creates a scheduled task for operational log retention.
func NewOperationalLogCleanupTask(
	pool *pgxpool.Pool,
	store opslog.SettingsStore,
	pm opslog.PartitionManager,
) *OperationalLogCleanupTask {
	return &OperationalLogCleanupTask{
		pool:  pool,
		store: store,
		pm:    pm,
	}
}

func (t *OperationalLogCleanupTask) Key() string  { return "cleanup_operational_log" }
func (t *OperationalLogCleanupTask) Name() string { return "Cleanup Operational Log" }
func (t *OperationalLogCleanupTask) Description() string {
	return "Prunes expired operational log rows and partitions"
}
func (t *OperationalLogCleanupTask) Category() taskmanager.TaskCategory {
	return taskmanager.TaskCategorySystem
}
func (t *OperationalLogCleanupTask) IsHidden() bool { return true }

func (t *OperationalLogCleanupTask) DefaultTriggers() []taskmanager.TriggerConfig {
	return []taskmanager.TriggerConfig{
		{Type: taskmanager.TriggerTypeStartup},
		{
			Type:       taskmanager.TriggerTypeInterval,
			IntervalMs: int64(opslog.LoadCleanupInterval(context.Background(), t.store) / time.Millisecond),
		},
	}
}

func (t *OperationalLogCleanupTask) Execute(ctx context.Context, progress taskmanager.ProgressReporter) error {
	progress.Report(0, "Pruning operational logs")
	deleted := opslog.CleanupOnce(ctx, t.pool, t.store, t.pm)
	progress.Report(100, fmt.Sprintf("Pruned %d operational log rows", deleted))
	return nil
}
