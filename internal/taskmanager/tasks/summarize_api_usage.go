package tasks

import (
	"context"
	"fmt"
	"time"

	"github.com/Silo-Server/silo-server/internal/apiusage"
	"github.com/Silo-Server/silo-server/internal/taskmanager"
)

// SummarizeAPIUsageTask keeps the admin dashboard's API clients card current:
// it summarizes recent activity log rows per sign-in session and prunes old
// summaries.
type SummarizeAPIUsageTask struct {
	store *apiusage.Store
}

func NewSummarizeAPIUsageTask(store *apiusage.Store) *SummarizeAPIUsageTask {
	return &SummarizeAPIUsageTask{store: store}
}

func (t *SummarizeAPIUsageTask) Key() string  { return "summarize_api_usage" }
func (t *SummarizeAPIUsageTask) Name() string { return "Summarize API Usage" }
func (t *SummarizeAPIUsageTask) Description() string {
	return "Counts API requests per sign-in session for the dashboard's API clients card"
}
func (t *SummarizeAPIUsageTask) Category() taskmanager.TaskCategory {
	return taskmanager.TaskCategorySystem
}
func (t *SummarizeAPIUsageTask) IsHidden() bool { return true }

func (t *SummarizeAPIUsageTask) DefaultTriggers() []taskmanager.TriggerConfig {
	return []taskmanager.TriggerConfig{
		{Type: taskmanager.TriggerTypeStartup},
		{Type: taskmanager.TriggerTypeInterval, IntervalMs: int64(15 * time.Minute / time.Millisecond)},
	}
}

func (t *SummarizeAPIUsageTask) Execute(ctx context.Context, progress taskmanager.ProgressReporter) error {
	if t.store == nil {
		progress.Report(100, "API usage summaries need the database")
		return nil
	}
	now := time.Now()
	written, err := t.store.Summarize(ctx, now)
	if err != nil {
		return err
	}
	pruned, err := t.store.Prune(ctx, now)
	if err != nil {
		return err
	}
	progress.Report(100, fmt.Sprintf("Summarized %d session-hours; pruned %d", written, pruned))
	return nil
}
