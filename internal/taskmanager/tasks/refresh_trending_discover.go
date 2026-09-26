package tasks

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/Silo-Server/silo-server/internal/taskmanager"
)

// TrendingDiscoverRefresher runs a single pass of the trending refresh.
// Satisfied by *sections.TrendingRefresher.
type TrendingDiscoverRefresher interface {
	RunOnce(ctx context.Context) (json.RawMessage, error)
}

// RefreshTrendingDiscoverTask refreshes the persisted external-trending
// snapshots used by trending_discover home sections and the calendar's
// Trending preset.
type RefreshTrendingDiscoverTask struct {
	refresher TrendingDiscoverRefresher
}

// NewRefreshTrendingDiscoverTask creates a new RefreshTrendingDiscoverTask.
func NewRefreshTrendingDiscoverTask(refresher TrendingDiscoverRefresher) *RefreshTrendingDiscoverTask {
	return &RefreshTrendingDiscoverTask{refresher: refresher}
}

func (t *RefreshTrendingDiscoverTask) Key() string  { return "refresh_trending_discover" }
func (t *RefreshTrendingDiscoverTask) Name() string { return "Refresh Trending Discover" }
func (t *RefreshTrendingDiscoverTask) Description() string {
	return "Refreshes the persisted external trending lists (TMDB/Trakt) for Trending Discover home sections and the calendar's Trending view"
}

func (t *RefreshTrendingDiscoverTask) Category() taskmanager.TaskCategory {
	return taskmanager.TaskCategoryLibrary
}

func (t *RefreshTrendingDiscoverTask) IsHidden() bool { return true }

func (t *RefreshTrendingDiscoverTask) DefaultTriggers() []taskmanager.TriggerConfig {
	return []taskmanager.TriggerConfig{
		{Type: taskmanager.TriggerTypeStartup},
		{Type: taskmanager.TriggerTypeInterval, IntervalMs: 60 * 60 * 1000}, // hourly
	}
}

func (t *RefreshTrendingDiscoverTask) Execute(ctx context.Context, progress taskmanager.ProgressReporter) error {
	progress.Report(0, "Refreshing trending discover")

	if t.refresher == nil {
		return errors.New("trending discover refresh: refresher not configured")
	}

	// RunOnce can return a summary alongside an error: a failed section
	// listing still refreshes the calendar feed.
	resultData, err := t.refresher.RunOnce(ctx)
	if resultData != nil {
		progress.SetResultData(resultData)
	}
	if err != nil {
		return fmt.Errorf("trending discover refresh: %w", err)
	}

	progress.Report(100, "Trending discover refresh complete")
	return nil
}
