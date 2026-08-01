package tasks

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/Silo-Server/silo-server/internal/taskmanager"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

// SettingMutationSweeper is the per-user retention sweep the task drives.
// Satisfied by *userstore.SettingMutationSweeper.
type SettingMutationSweeper interface {
	Sweep(ctx context.Context, progress func(percent int, message string)) (userstore.SettingMutationSweepStats, error)
}

// SettingMutationsRetentionTask deletes expired setting-mutation idempotency
// receipts across every user store. The mutation endpoint guarantees a
// mutation_id is replayable for at least 30 days; expires_at is not
// self-enforcing, so this daily sweep is what actually bounds the table.
type SettingMutationsRetentionTask struct {
	sweeper SettingMutationSweeper
}

// NewSettingMutationsRetentionTask creates the retention task.
func NewSettingMutationsRetentionTask(sweeper SettingMutationSweeper) *SettingMutationsRetentionTask {
	return &SettingMutationsRetentionTask{sweeper: sweeper}
}

func (t *SettingMutationsRetentionTask) Key() string  { return "setting_mutations_retention" }
func (t *SettingMutationsRetentionTask) Name() string { return "Clean Up Setting Mutation Receipts" }
func (t *SettingMutationsRetentionTask) Description() string {
	return "Deletes expired settings-mutation idempotency receipts from every user's store."
}
func (t *SettingMutationsRetentionTask) Category() taskmanager.TaskCategory {
	return taskmanager.TaskCategorySystem
}
func (t *SettingMutationsRetentionTask) IsHidden() bool { return true }

func (t *SettingMutationsRetentionTask) DefaultTriggers() []taskmanager.TriggerConfig {
	return []taskmanager.TriggerConfig{{Type: taskmanager.TriggerTypeDaily, TimeOfDay: "05:00"}}
}

func (t *SettingMutationsRetentionTask) Execute(ctx context.Context, progress taskmanager.ProgressReporter) error {
	if t == nil || t.sweeper == nil {
		progress.Report(100, "Setting mutation sweep is not configured")
		return nil
	}
	progress.Report(0, "Sweeping expired setting mutation receipts")
	stats, err := t.sweeper.Sweep(ctx, func(percent int, message string) {
		progress.Report(float64(percent), message)
	})
	if err != nil {
		return fmt.Errorf("setting mutation retention: %w", err)
	}
	if data, err := json.Marshal(stats); err == nil {
		progress.SetResultData(data)
	}
	progress.Report(100, fmt.Sprintf(
		"Removed %d expired receipts across %d users (%d users failed)",
		stats.ReceiptsDeleted, stats.UsersSwept, stats.UsersFailed))
	return nil
}
