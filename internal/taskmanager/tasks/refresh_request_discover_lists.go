package tasks

import (
	"context"
	"errors"
	"fmt"

	"github.com/Silo-Server/silo-server/internal/taskmanager"
)

// RefreshRequestDiscoverListsTask fetches the TMDB and MDBList lists the
// Requests Discover page's list rows show. Satisfied by
// *discoverrows.Refresher.
type RefreshRequestDiscoverListsTask struct {
	refresher TrendingDiscoverRefresher
}

// NewRefreshRequestDiscoverListsTask creates a new RefreshRequestDiscoverListsTask.
func NewRefreshRequestDiscoverListsTask(refresher TrendingDiscoverRefresher) *RefreshRequestDiscoverListsTask {
	return &RefreshRequestDiscoverListsTask{refresher: refresher}
}

func (t *RefreshRequestDiscoverListsTask) Key() string  { return "refresh_request_discover_lists" }
func (t *RefreshRequestDiscoverListsTask) Name() string { return "Refresh Request Discover Lists" }
func (t *RefreshRequestDiscoverListsTask) Description() string {
	return "Downloads the TMDB and MDBList lists behind the Requests Discover page's list rows"
}

func (t *RefreshRequestDiscoverListsTask) Category() taskmanager.TaskCategory {
	return taskmanager.TaskCategoryLibrary
}

func (t *RefreshRequestDiscoverListsTask) IsHidden() bool { return true }

func (t *RefreshRequestDiscoverListsTask) DefaultTriggers() []taskmanager.TriggerConfig {
	return []taskmanager.TriggerConfig{
		{Type: taskmanager.TriggerTypeStartup},
		// Lists younger than 12 hours are skipped, so most runs fetch nothing.
		{Type: taskmanager.TriggerTypeInterval, IntervalMs: 60 * 60 * 1000},
	}
}

func (t *RefreshRequestDiscoverListsTask) Execute(ctx context.Context, progress taskmanager.ProgressReporter) error {
	progress.Report(0, "Refreshing request discover lists")
	if t.refresher == nil {
		return errors.New("request discover list refresh: refresher not configured")
	}
	resultData, err := t.refresher.RunOnce(ctx)
	if resultData != nil {
		progress.SetResultData(resultData)
	}
	if err != nil {
		return fmt.Errorf("request discover list refresh: %w", err)
	}
	progress.Report(100, "Request discover list refresh complete")
	return nil
}
