package tasks

import (
	"context"
	"fmt"

	"github.com/Silo-Server/silo-server/internal/taskmanager"
)

type AutoscanPoller interface {
	PollOnce(ctx context.Context) error
}

// defaultAutoscanPollIntervalMs is the fallback poll cadence (10 minutes) used
// when no positive interval is supplied.
const defaultAutoscanPollIntervalMs int64 = 10 * 60 * 1000

type AutoscanPollTask struct {
	poller     AutoscanPoller
	intervalMs int64
}

// NewAutoscanPollTask builds the poll task with an interval expressed in
// MILLISECONDS. This matches the reschedule path (HandleUpdateSettings seeds
// seconds*1000 ms), so the startup-seeded schedule and a settings-driven
// reschedule agree for sub-minute and non-60-multiple intervals — previously the
// startup path integer-divided seconds by 60 (minutes) and diverged.
func NewAutoscanPollTask(poller AutoscanPoller, intervalMs int64) *AutoscanPollTask {
	if intervalMs <= 0 {
		intervalMs = defaultAutoscanPollIntervalMs
	}
	return &AutoscanPollTask{poller: poller, intervalMs: intervalMs}
}

func (t *AutoscanPollTask) Key() string  { return "autoscan_poll" }
func (t *AutoscanPollTask) Name() string { return "Autoscan poll" }
func (t *AutoscanPollTask) Description() string {
	return "Poll installed scan-source providers for changes and enqueue rescans"
}
func (t *AutoscanPollTask) Category() taskmanager.TaskCategory {
	return taskmanager.TaskCategoryLibrary
}
func (t *AutoscanPollTask) IsHidden() bool { return false }

func (t *AutoscanPollTask) DefaultTriggers() []taskmanager.TriggerConfig {
	return []taskmanager.TriggerConfig{
		{Type: taskmanager.TriggerTypeInterval, IntervalMs: t.intervalMs},
	}
}

func (t *AutoscanPollTask) Execute(ctx context.Context, progress taskmanager.ProgressReporter) error {
	progress.Report(0, "Polling scan-source providers")
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
