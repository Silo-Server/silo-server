package tasks

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/Silo-Server/silo-server/internal/taskmanager"
)

// NotificationPurger deletes old notifications rows.
type NotificationPurger interface {
	PurgeOld(ctx context.Context, dismissedBefore, allBefore time.Time) (int64, error)
}

// PushPurger deletes terminal push deliveries older than a cutoff.
type PushPurger interface {
	PurgeTerminal(ctx context.Context, before time.Time) (int64, error)
}

// NotificationsRetentionTask removes old notification rows on a daily schedule.
type NotificationsRetentionTask struct {
	store      NotificationPurger
	pushPurger PushPurger
}

// NewNotificationsRetentionTask creates a retention task backed by store.
// pushPurger may be nil, in which case push delivery purging is skipped.
func NewNotificationsRetentionTask(store NotificationPurger, pushPurger PushPurger) *NotificationsRetentionTask {
	return &NotificationsRetentionTask{store: store, pushPurger: pushPurger}
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
	pushDeleted := int64(0)
	if t.pushPurger != nil {
		if n, err := t.pushPurger.PurgeTerminal(ctx, time.Now().AddDate(0, 0, -7)); err != nil {
			slog.WarnContext(ctx, "push: purge terminal deliveries failed", "error", err)
		} else if n > 0 {
			slog.InfoContext(ctx, "push: purged terminal deliveries", "count", n)
			pushDeleted = n
		}
	}
	progress.Report(100, fmt.Sprintf("Purged %d old notification rows, %d push deliveries", deleted, pushDeleted))
	return nil
}
