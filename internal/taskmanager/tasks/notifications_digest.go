package tasks

import (
	"context"
	"fmt"
	"time"

	"github.com/Silo-Server/silo-server/internal/taskmanager"
)

// NotificationDigester sends the daily digest to opted-in users.
type NotificationDigester interface {
	RunDailyDigest(ctx context.Context, since time.Time) error
}

// NotificationsDigestTask sends daily digest notifications on a daily schedule.
type NotificationsDigestTask struct {
	digester NotificationDigester
}

// NewNotificationsDigestTask creates a digest task backed by digester.
func NewNotificationsDigestTask(digester NotificationDigester) *NotificationsDigestTask {
	return &NotificationsDigestTask{digester: digester}
}

func (t *NotificationsDigestTask) Key() string  { return "notifications_digest" }
func (t *NotificationsDigestTask) Name() string { return "Send notification digests" }
func (t *NotificationsDigestTask) Description() string {
	return "Sends a daily digest notification summarizing new catalog additions to opted-in users"
}
func (t *NotificationsDigestTask) Category() taskmanager.TaskCategory {
	return taskmanager.TaskCategorySystem
}
func (t *NotificationsDigestTask) IsHidden() bool { return false }

func (t *NotificationsDigestTask) DefaultTriggers() []taskmanager.TriggerConfig {
	return []taskmanager.TriggerConfig{
		{Type: taskmanager.TriggerTypeDaily, TimeOfDay: "08:00"},
	}
}

func (t *NotificationsDigestTask) Execute(ctx context.Context, progress taskmanager.ProgressReporter) error {
	progress.Report(0, "Sending notification digests")
	if t.digester == nil {
		progress.Report(100, "Notification digest unavailable")
		return nil
	}
	since := time.Now().Add(-24 * time.Hour)
	if err := t.digester.RunDailyDigest(ctx, since); err != nil {
		return fmt.Errorf("notifications digest: %w", err)
	}
	progress.Report(100, "Notification digests sent")
	return nil
}
