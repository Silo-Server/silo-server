package tasks

import (
	"context"
	"fmt"
	"time"

	"github.com/Silo-Server/silo-server/internal/taskmanager"
)

const (
	refreshSeriesAirDatesInterval  = time.Hour
	refreshSeriesAirDatesBatchSize = 500
)

type seriesAirDateRefresher interface {
	RefreshDueSeriesAirDates(ctx context.Context, limit int) (due, updated int, err error)
}

// RefreshSeriesAirDatesTask keeps the series "latest episode air date" sort
// key current. The key depends on the current date, so it goes stale when a
// known future episode airs without any episode write; this task recomputes
// only the series whose next episode date has passed.
type RefreshSeriesAirDatesTask struct {
	refresher seriesAirDateRefresher
}

func NewRefreshSeriesAirDatesTask(refresher seriesAirDateRefresher) *RefreshSeriesAirDatesTask {
	return &RefreshSeriesAirDatesTask{refresher: refresher}
}

func (t *RefreshSeriesAirDatesTask) Key() string  { return "refresh_series_air_dates" }
func (t *RefreshSeriesAirDatesTask) Name() string { return "Refresh Series Air Dates" }
func (t *RefreshSeriesAirDatesTask) Description() string {
	return "Updates the latest-aired episode date of series whose next known episode has aired."
}
func (t *RefreshSeriesAirDatesTask) Category() taskmanager.TaskCategory {
	return taskmanager.TaskCategoryMetadata
}
func (t *RefreshSeriesAirDatesTask) IsHidden() bool { return false }
func (t *RefreshSeriesAirDatesTask) DefaultTriggers() []taskmanager.TriggerConfig {
	return []taskmanager.TriggerConfig{
		{Type: taskmanager.TriggerTypeStartup},
		{Type: taskmanager.TriggerTypeInterval, IntervalMs: int64(refreshSeriesAirDatesInterval / time.Millisecond)},
	}
}

func (t *RefreshSeriesAirDatesTask) Execute(ctx context.Context, progress taskmanager.ProgressReporter) error {
	if t == nil || t.refresher == nil {
		progress.Report(100, "Series air date refresh is not configured")
		return nil
	}
	progress.Report(0, "Refreshing series air dates")
	total := 0
	for {
		due, updated, err := t.refresher.RefreshDueSeriesAirDates(ctx, refreshSeriesAirDatesBatchSize)
		total += updated
		if err != nil {
			return fmt.Errorf("refresh series air dates (updated %d series): %w", total, err)
		}
		// A full batch that changed nothing would select the same rows again.
		if due < refreshSeriesAirDatesBatchSize || updated == 0 {
			break
		}
		progress.Report(50, fmt.Sprintf("Updated air dates for %d series", total))
	}
	progress.Report(100, fmt.Sprintf("Updated air dates for %d series", total))
	return nil
}
