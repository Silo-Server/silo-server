package tasks

import (
	"context"
	"errors"
	"fmt"

	"github.com/Silo-Server/silo-server/internal/taskmanager"
)

// RefreshSeasonalListsTask downloads the MDBList lists seasonal home rows show
// now or in the next two weeks. Satisfied by *sections.SeasonalListRefresher.
type RefreshSeasonalListsTask struct {
	refresher TrendingDiscoverRefresher
}

// NewRefreshSeasonalListsTask creates a new RefreshSeasonalListsTask.
func NewRefreshSeasonalListsTask(refresher TrendingDiscoverRefresher) *RefreshSeasonalListsTask {
	return &RefreshSeasonalListsTask{refresher: refresher}
}

func (t *RefreshSeasonalListsTask) Key() string  { return "refresh_seasonal_lists" }
func (t *RefreshSeasonalListsTask) Name() string { return "Refresh Seasonal Lists" }
func (t *RefreshSeasonalListsTask) Description() string {
	return "Downloads the curated MDBList lists behind Seasonal Picks home rows ahead of each holiday"
}

func (t *RefreshSeasonalListsTask) Category() taskmanager.TaskCategory {
	return taskmanager.TaskCategoryLibrary
}

func (t *RefreshSeasonalListsTask) IsHidden() bool { return true }

func (t *RefreshSeasonalListsTask) DefaultTriggers() []taskmanager.TriggerConfig {
	return []taskmanager.TriggerConfig{
		{Type: taskmanager.TriggerTypeStartup},
		// Lists younger than a day are skipped, so most runs fetch nothing.
		{Type: taskmanager.TriggerTypeInterval, IntervalMs: 6 * 60 * 60 * 1000},
	}
}

func (t *RefreshSeasonalListsTask) Execute(ctx context.Context, progress taskmanager.ProgressReporter) error {
	progress.Report(0, "Refreshing seasonal lists")
	if t.refresher == nil {
		return errors.New("seasonal list refresh: refresher not configured")
	}
	resultData, err := t.refresher.RunOnce(ctx)
	if resultData != nil {
		progress.SetResultData(resultData)
	}
	if err != nil {
		return fmt.Errorf("seasonal list refresh: %w", err)
	}
	progress.Report(100, "Seasonal list refresh complete")
	return nil
}
