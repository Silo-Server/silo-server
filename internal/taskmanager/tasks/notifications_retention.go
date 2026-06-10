package tasks

import (
	"context"
	"fmt"
	"time"

	"github.com/Silo-Server/silo-server/internal/taskmanager"
)

// NotificationPurger deletes old notifications rows.
type NotificationPurger interface {
	PurgeOld(ctx context.Context, dismissedBefore, allBefore time.Time) (int64, error)
}

// NotificationsRetentionTask removes old notification rows on a daily schedule.
type NotificationsRetentionTask struct {
	store NotificationPurger
}

// NewNotificationsRetentionTask creates a retention task backed by store.
func NewNotificationsRetentionTask(store NotificationPurger) *NotificationsRetentionTask {
	return &NotificationsRetentionTask{store: store}
}

func (t *NotificationsRetentionTask) Key() string  { return "notifications_retention" }
func (t *NotificationsRetentionTask) Name() string { return "Purge old notifications" }
func (t *NotificationsRetentionTask) Description() string {
	return "Removes dismissed and expired notification rows to keep the table lean"
}
func (t *NotificationsRetentionTask) Category() taskmanager.TaskCategory {
	return taskmanager.TaskCategorySystem
}
func (t *NotificationsRetentionTask) IsHidden() bool { return false }

func (t *NotificationsRetentionTask) DefaultTriggers() []taskmanager.TriggerConfig {
	return []taskmanager.TriggerConfig{
		{Type: taskmanager.TriggerTypeInterval, IntervalMs: 24 * 60 * 60 * 1000},
	}
}

func (t *NotificationsRetentionTask) Execute(ctx context.Context, progress taskmanager.ProgressReporter) error {
	progress.Report(0, "Purging old notifications")
	if t.store == nil {
		progress.Report(100, "Notification retention unavailable")
		return nil
	}
	now := time.Now()
	deleted, err := t.store.PurgeOld(ctx, now.Add(-30*24*time.Hour), now.Add(-90*24*time.Hour))
	if err != nil {
		return fmt.Errorf("notifications retention: %w", err)
	}
	progress.Report(100, fmt.Sprintf("Purged %d old notification rows", deleted))
	return nil
}
