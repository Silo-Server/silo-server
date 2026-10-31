package tasks

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/apiusage"
	"github.com/Silo-Server/silo-server/internal/taskmanager"
)

// summarizeAPIUsageAdvisoryLock spells "SILOAPIU".
const summarizeAPIUsageAdvisoryLock int64 = 0x53494C4F41504955

// SummarizeAPIUsageTask keeps the admin dashboard's API clients card current:
// it summarizes recent activity log rows per sign-in session and prunes old
// summaries. Every API process runs the task manager, so an advisory lock
// keeps one pass running across the cluster; overlapping passes would rescan
// the same activity log and upsert the same rows.
type SummarizeAPIUsageTask struct {
	store *apiusage.Store
	lock  clusterLock
}

// NewSummarizeAPIUsageTask returns the task. A nil pool runs without the
// cluster lock.
func NewSummarizeAPIUsageTask(pool *pgxpool.Pool, store *apiusage.Store) *SummarizeAPIUsageTask {
	t := &SummarizeAPIUsageTask{store: store}
	if pool != nil {
		t.lock = advisoryClusterLock{pool: pool, key: summarizeAPIUsageAdvisoryLock, name: "API usage summary"}
	}
	return t
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
	if t.lock != nil {
		release, acquired, err := t.lock.TryAcquire(ctx)
		if err != nil {
			return fmt.Errorf("acquiring API usage summary lock: %w", err)
		}
		if !acquired {
			progress.Report(100, "Another server is summarizing API usage")
			return nil
		}
		defer release()
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
