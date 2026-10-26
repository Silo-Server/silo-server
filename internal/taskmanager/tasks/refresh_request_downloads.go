package tasks

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/Silo-Server/silo-server/internal/requests"
	"github.com/Silo-Server/silo-server/internal/taskmanager"
	"github.com/jackc/pgx/v5/pgxpool"
)

type RequestDownloadRefresher interface {
	RefreshDownloads(ctx context.Context, limit int, budget time.Duration) (requests.DownloadRefreshResult, error)
	HasDownloadsToRefresh(ctx context.Context) (bool, error)
}

// RefreshRequestDownloadsTask refreshes the status and download progress of
// downloading request targets whose router plugin reports progress, between
// reconcile passes. It tries the request target write lock without waiting,
// so one server runs each pass, and it skips while a reconcile pass holds the
// lock anywhere in the cluster: the two never write the same target at once,
// and a reconcile pass waits for a refresh pass instead of skipping. Each pass
// runs within requestDownloadRefreshBudget, so that wait always ends.
type RefreshRequestDownloadsTask struct {
	refresher RequestDownloadRefresher
	limit     int
	lock      clusterLock
}

// NewRefreshRequestDownloadsTask constructs the task. A nil pool runs without
// the cluster lock.
func NewRefreshRequestDownloadsTask(refresher RequestDownloadRefresher, limit int, pool *pgxpool.Pool) *RefreshRequestDownloadsTask {
	if limit <= 0 {
		limit = 200
	}
	t := &RefreshRequestDownloadsTask{refresher: refresher, limit: limit}
	if pool != nil {
		t.lock = advisoryClusterLock{pool: pool, key: requestTargetWriteAdvisoryLock, name: "request download refresh"}
	}
	return t
}

func (t *RefreshRequestDownloadsTask) Key() string  { return "refresh_request_downloads" }
func (t *RefreshRequestDownloadsTask) Name() string { return "Refresh Request Downloads" }
func (t *RefreshRequestDownloadsTask) Description() string {
	return "Refreshes the download progress of media requests that Radarr, Sonarr, or another request plugin is downloading"
}
func (t *RefreshRequestDownloadsTask) Category() taskmanager.TaskCategory {
	return taskmanager.TaskCategoryLibrary
}
func (t *RefreshRequestDownloadsTask) IsHidden() bool { return true }

func (t *RefreshRequestDownloadsTask) DefaultTriggers() []taskmanager.TriggerConfig {
	return []taskmanager.TriggerConfig{
		{Type: taskmanager.TriggerTypeInterval, IntervalMs: 60 * 1000},
	}
}

// ShouldRun skips a scheduled run while no downloading target has progress to
// refresh, so an idle server records no run every minute. The reconcile pass
// finds a download's first progress.
func (t *RefreshRequestDownloadsTask) ShouldRun(ctx context.Context) (bool, error) {
	if t == nil || t.refresher == nil {
		return false, nil
	}
	return t.refresher.HasDownloadsToRefresh(ctx)
}

func (t *RefreshRequestDownloadsTask) Execute(ctx context.Context, progress taskmanager.ProgressReporter) error {
	progress.Report(0, "Refreshing request downloads")
	if t.refresher == nil {
		progress.Report(100, "Request download refresh unavailable")
		return nil
	}
	if t.lock != nil {
		release, acquired, err := t.lock.TryAcquire(ctx)
		if err != nil {
			return fmt.Errorf("acquiring request target write lock: %w", err)
		}
		if !acquired {
			progress.Report(100, "A request reconcile or download refresh pass is already running")
			return nil
		}
		defer release()
	}
	result, err := t.refresher.RefreshDownloads(ctx, t.limit, requestDownloadRefreshBudget)
	if err != nil {
		return fmt.Errorf("refresh request downloads: %w", err)
	}
	if data, err := json.Marshal(result); err == nil {
		progress.SetResultData(data)
	}
	progress.Report(100, "Request download refresh complete")
	return nil
}
