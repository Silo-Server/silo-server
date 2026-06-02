package tasks

import (
	"context"
	"fmt"

	"github.com/Silo-Server/silo-server/internal/taskmanager"
)

type AutoscanPoller interface {
	PollOnce(ctx context.Context) error
}

type AutoscanPollTask struct {
	poller          AutoscanPoller
	intervalMinutes int
}

func NewAutoscanPollTask(poller AutoscanPoller, intervalMinutes int) *AutoscanPollTask {
	if intervalMinutes <= 0 {
		intervalMinutes = 10
	}
	return &AutoscanPollTask{poller: poller, intervalMinutes: intervalMinutes}
}

func (t *AutoscanPollTask) Key() string  { return "autoscan_poll" }
func (t *AutoscanPollTask) Name() string { return "Autoscan Poll" }
func (t *AutoscanPollTask) Description() string {
	return "Polls autoscan-enabled Radarr/Sonarr instances for imported files and scans the affected folders"
}
func (t *AutoscanPollTask) Category() taskmanager.TaskCategory {
	return taskmanager.TaskCategoryLibrary
}
func (t *AutoscanPollTask) IsHidden() bool { return false }

func (t *AutoscanPollTask) DefaultTriggers() []taskmanager.TriggerConfig {
	return []taskmanager.TriggerConfig{
		{Type: taskmanager.TriggerTypeInterval, IntervalMs: int64(t.intervalMinutes) * 60 * 1000},
	}
}

func (t *AutoscanPollTask) Execute(ctx context.Context, progress taskmanager.ProgressReporter) error {
	progress.Report(0, "Polling arr instances")
	if t.poller == nil {
		progress.Report(100, "Autoscan unavailable")
		return nil
	}
	if err := t.poller.PollOnce(ctx); err != nil {
		return fmt.Errorf("autoscan poll: %w", err)
	}
	progress.Report(100, "Autoscan poll complete")
	return nil
}
