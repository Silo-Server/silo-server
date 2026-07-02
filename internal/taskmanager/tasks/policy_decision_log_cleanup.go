package tasks

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/policy"
	"github.com/Silo-Server/silo-server/internal/taskmanager"
)

// PolicyDecisionLogCleanupTask prunes expired policy decision log rows and partitions.
type PolicyDecisionLogCleanupTask struct {
	pool  *pgxpool.Pool
	store policy.DecisionLogSettingsStore
	pm    policy.DecisionLogPartitionManager
}

// NewPolicyDecisionLogCleanupTask creates a scheduled task for policy decision log retention.
func NewPolicyDecisionLogCleanupTask(
	pool *pgxpool.Pool,
	store policy.DecisionLogSettingsStore,
	pm policy.DecisionLogPartitionManager,
) *PolicyDecisionLogCleanupTask {
	return &PolicyDecisionLogCleanupTask{
		pool:  pool,
		store: store,
		pm:    pm,
	}
}

func (t *PolicyDecisionLogCleanupTask) Key() string  { return "cleanup_policy_decision_log" }
func (t *PolicyDecisionLogCleanupTask) Name() string { return "Cleanup Policy Decision Log" }
func (t *PolicyDecisionLogCleanupTask) Description() string {
	return "Prunes expired policy decision log rows and partitions"
}
func (t *PolicyDecisionLogCleanupTask) Category() taskmanager.TaskCategory {
	return taskmanager.TaskCategorySystem
}
func (t *PolicyDecisionLogCleanupTask) IsHidden() bool { return false }

func (t *PolicyDecisionLogCleanupTask) DefaultTriggers() []taskmanager.TriggerConfig {
	return []taskmanager.TriggerConfig{
		{Type: taskmanager.TriggerTypeStartup},
		{Type: taskmanager.TriggerTypeInterval, IntervalMs: int64((24 * time.Hour) / time.Millisecond)},
	}
}

func (t *PolicyDecisionLogCleanupTask) Execute(ctx context.Context, progress taskmanager.ProgressReporter) error {
	progress.Report(0, "Pruning policy decision logs")
	deleted, err := policy.CleanupDecisionLogsOnce(ctx, t.pool, t.store, t.pm)
	if err != nil {
		return fmt.Errorf("policy decision log cleanup (deleted %d rows): %w", deleted, err)
	}
	progress.Report(100, fmt.Sprintf("Pruned %d policy decision log rows", deleted))
	return nil
}
