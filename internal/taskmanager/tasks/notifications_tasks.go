package tasks

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/Silo-Server/silo-server/internal/notifications"
	"github.com/Silo-Server/silo-server/internal/taskmanager"
)

// SeedEpisodeAvailabilityTask inserts episode_availability rows for every
// currently playable episode without creating release events, then writes the
// per-library seed markers. Running it is what allows release events to flow
// for libraries that predate the notifications feature; rerunning is a cheap
// idempotent repair pass.
type SeedEpisodeAvailabilityTask struct {
	system *notifications.System
}

// NewSeedEpisodeAvailabilityTask creates the seeding task.
func NewSeedEpisodeAvailabilityTask(system *notifications.System) *SeedEpisodeAvailabilityTask {
	return &SeedEpisodeAvailabilityTask{system: system}
}

func (t *SeedEpisodeAvailabilityTask) Key() string  { return "seed_episode_availability" }
func (t *SeedEpisodeAvailabilityTask) Name() string { return "Seed Episode Availability" }
func (t *SeedEpisodeAvailabilityTask) Description() string {
	return "Records the existing episode back-catalog as already-released so new-episode notifications only fire for episodes that arrive afterwards."
}
func (t *SeedEpisodeAvailabilityTask) Category() taskmanager.TaskCategory {
	return taskmanager.TaskCategorySystem
}
func (t *SeedEpisodeAvailabilityTask) IsHidden() bool { return true }

func (t *SeedEpisodeAvailabilityTask) DefaultTriggers() []taskmanager.TriggerConfig {
	return []taskmanager.TriggerConfig{{Type: taskmanager.TriggerTypeStartup}}
}

func (t *SeedEpisodeAvailabilityTask) Execute(ctx context.Context, progress taskmanager.ProgressReporter) error {
	if t == nil || t.system == nil {
		progress.Report(100, "Notifications are not configured")
		return nil
	}
	progress.Report(0, "Seeding episode availability")
	if err := t.system.SeedAvailability(ctx, func(percent int, message string) {
		progress.Report(float64(percent), message)
	}); err != nil {
		return fmt.Errorf("seeding episode availability: %w", err)
	}
	progress.Report(100, "Episode availability seeded")
	return nil
}

// RebuildReleaseInterestTask rebuilds profile_series_interest from favorites,
// watchlist, and watch progress. It is the rollout backfill and the periodic
// drift-repair pass; recomputes share the same code as live updates.
type RebuildReleaseInterestTask struct {
	system *notifications.System
}

// NewRebuildReleaseInterestTask creates the interest rebuild task.
func NewRebuildReleaseInterestTask(system *notifications.System) *RebuildReleaseInterestTask {
	return &RebuildReleaseInterestTask{system: system}
}

func (t *RebuildReleaseInterestTask) Key() string  { return "rebuild_release_interest" }
func (t *RebuildReleaseInterestTask) Name() string { return "Rebuild Notification Interest" }
func (t *RebuildReleaseInterestTask) Description() string {
	return "Recomputes which profiles care about which series (favorites, watchlist, watch progress) for new-episode notifications."
}
func (t *RebuildReleaseInterestTask) Category() taskmanager.TaskCategory {
	return taskmanager.TaskCategorySystem
}
func (t *RebuildReleaseInterestTask) IsHidden() bool { return true }

func (t *RebuildReleaseInterestTask) DefaultTriggers() []taskmanager.TriggerConfig {
	return []taskmanager.TriggerConfig{
		{Type: taskmanager.TriggerTypeStartup},
		{Type: taskmanager.TriggerTypeDaily, TimeOfDay: "04:30"},
	}
}

func (t *RebuildReleaseInterestTask) Execute(ctx context.Context, progress taskmanager.ProgressReporter) error {
	if t == nil || t.system == nil {
		progress.Report(100, "Notifications are not configured")
		return nil
	}
	progress.Report(0, "Rebuilding profile series interest")
	if err := t.system.RebuildInterest(ctx, func(percent int, message string) {
		progress.Report(float64(percent), message)
	}); err != nil {
		return fmt.Errorf("rebuilding notification interest: %w", err)
	}
	progress.Report(100, "Notification interest rebuilt")
	return nil
}

// NotificationsRetentionTask applies the notification retention policy: read
// inbox rows past the read window, unread rows past the unread window,
// processed release events past the debug window, and inert interest rows.
type NotificationsRetentionTask struct {
	system *notifications.System
}

// NewNotificationsRetentionTask creates the retention task.
func NewNotificationsRetentionTask(system *notifications.System) *NotificationsRetentionTask {
	return &NotificationsRetentionTask{system: system}
}

func (t *NotificationsRetentionTask) Key() string  { return "notifications_retention" }
func (t *NotificationsRetentionTask) Name() string { return "Clean Up Notifications" }
func (t *NotificationsRetentionTask) Description() string {
	return "Prunes old notifications and processed release events according to the retention settings."
}
func (t *NotificationsRetentionTask) Category() taskmanager.TaskCategory {
	return taskmanager.TaskCategorySystem
}
func (t *NotificationsRetentionTask) IsHidden() bool { return false }

func (t *NotificationsRetentionTask) DefaultTriggers() []taskmanager.TriggerConfig {
	return []taskmanager.TriggerConfig{{Type: taskmanager.TriggerTypeDaily, TimeOfDay: "05:00"}}
}

func (t *NotificationsRetentionTask) Execute(ctx context.Context, progress taskmanager.ProgressReporter) error {
	if t == nil || t.system == nil {
		progress.Report(100, "Notifications are not configured")
		return nil
	}
	progress.Report(0, "Applying notification retention policy")
	stats, err := t.system.RunRetention(ctx)
	if err != nil {
		return fmt.Errorf("notification retention: %w", err)
	}
	if data, err := json.Marshal(stats); err == nil {
		progress.SetResultData(data)
	}
	progress.Report(100, fmt.Sprintf(
		"Removed %d notifications, %d release events, %d inert interest rows",
		stats.DeliveriesDeleted, stats.EventsDeleted, stats.InterestPruned))
	return nil
}
