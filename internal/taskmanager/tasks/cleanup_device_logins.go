package tasks

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/Silo-Server/silo-server/internal/taskmanager"
)

// expiredDeviceLoginDeleter removes device sign-in requests long past their
// expiry; *auth.DeviceLoginRetention satisfies it and owns the threshold.
type expiredDeviceLoginDeleter interface {
	DeleteExpired(ctx context.Context) (int, error)
}

// DeviceLoginCleanupTask deletes old device sign-in requests. Nothing else
// removes them, and user codes are unique across every stored request, so
// without it the code space slowly fills.
type DeviceLoginCleanupTask struct {
	requests expiredDeviceLoginDeleter
}

type deviceLoginCleanupResult struct {
	Deleted int `json:"deleted"`
}

// NewDeviceLoginCleanupTask creates the retention step for device sign-in
// requests.
func NewDeviceLoginCleanupTask(requests expiredDeviceLoginDeleter) *DeviceLoginCleanupTask {
	return &DeviceLoginCleanupTask{requests: requests}
}

func (t *DeviceLoginCleanupTask) Key() string  { return "cleanup_device_logins" }
func (t *DeviceLoginCleanupTask) Name() string { return "Cleanup Expired Device Sign-in Requests" }
func (t *DeviceLoginCleanupTask) Description() string {
	return "Deletes device sign-in requests long past their expiry"
}
func (t *DeviceLoginCleanupTask) Category() taskmanager.TaskCategory {
	return taskmanager.TaskCategorySystem
}
func (t *DeviceLoginCleanupTask) IsHidden() bool { return false }

// DefaultTriggers matches the neighboring retention steps: once at startup,
// then daily.
func (t *DeviceLoginCleanupTask) DefaultTriggers() []taskmanager.TriggerConfig {
	return []taskmanager.TriggerConfig{
		{Type: taskmanager.TriggerTypeStartup},
		{Type: taskmanager.TriggerTypeInterval, IntervalMs: int64((24 * time.Hour) / time.Millisecond)},
	}
}

func (t *DeviceLoginCleanupTask) Execute(ctx context.Context, progress taskmanager.ProgressReporter) error {
	if t == nil || t.requests == nil {
		progress.Report(100, "Device sign-in request cleanup is not configured")
		return nil
	}
	progress.Report(0, "Deleting expired device sign-in requests")
	deleted, err := t.requests.DeleteExpired(ctx)
	if data, marshalErr := json.Marshal(deviceLoginCleanupResult{Deleted: deleted}); marshalErr == nil {
		progress.SetResultData(data)
	}
	if err != nil {
		slog.WarnContext(ctx, "device sign-in request cleanup failed", "component", "taskmanager", "task", t.Key(), "error", err)
		progress.Report(100, "Device sign-in request cleanup failed")
		return err
	}
	if deleted > 0 {
		slog.InfoContext(ctx, "device sign-in request cleanup completed", "component", "taskmanager", "task", t.Key(), "deleted", deleted)
	}
	progress.Report(100, fmt.Sprintf("Deleted %d expired device sign-in requests", deleted))
	return nil
}
