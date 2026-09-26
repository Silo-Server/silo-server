package tasks

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/Silo-Server/silo-server/internal/taskmanager"
)

// expiredSessionDeleter removes login sessions whose expires_at has passed;
// *auth.SessionRepository satisfies it.
type expiredSessionDeleter interface {
	DeleteExpired(ctx context.Context) (int, error)
}

// AuthSessionCleanupTask deletes expired login sessions. Nothing else removes
// them: logout and revocation only set revoked_at, and refresh extends
// expires_at, so without this job the auth_sessions table grows with every
// login for the life of the deployment.
type AuthSessionCleanupTask struct {
	sessions expiredSessionDeleter
}

type authSessionCleanupResult struct {
	Deleted int `json:"deleted"`
}

// NewAuthSessionCleanupTask creates the scheduled task for login-session retention.
func NewAuthSessionCleanupTask(sessions expiredSessionDeleter) *AuthSessionCleanupTask {
	return &AuthSessionCleanupTask{sessions: sessions}
}

func (t *AuthSessionCleanupTask) Key() string  { return "cleanup_auth_sessions" }
func (t *AuthSessionCleanupTask) Name() string { return "Cleanup Expired Login Sessions" }
func (t *AuthSessionCleanupTask) Description() string {
	return "Deletes login sessions whose expiry has passed, revoked or not"
}
func (t *AuthSessionCleanupTask) Category() taskmanager.TaskCategory {
	return taskmanager.TaskCategorySystem
}
func (t *AuthSessionCleanupTask) IsHidden() bool { return false }

// DefaultTriggers matches the neighboring retention jobs: once at startup,
// then daily.
func (t *AuthSessionCleanupTask) DefaultTriggers() []taskmanager.TriggerConfig {
	return []taskmanager.TriggerConfig{
		{Type: taskmanager.TriggerTypeStartup},
		{Type: taskmanager.TriggerTypeInterval, IntervalMs: int64((24 * time.Hour) / time.Millisecond)},
	}
}

func (t *AuthSessionCleanupTask) Execute(ctx context.Context, progress taskmanager.ProgressReporter) error {
	if t == nil || t.sessions == nil {
		progress.Report(100, "Login session cleanup is not configured")
		return nil
	}
	progress.Report(0, "Deleting expired login sessions")
	deleted, err := t.sessions.DeleteExpired(ctx)
	if data, marshalErr := json.Marshal(authSessionCleanupResult{Deleted: deleted}); marshalErr == nil {
		progress.SetResultData(data)
	}
	if err != nil {
		slog.WarnContext(ctx, "login session cleanup failed", "component", "taskmanager", "task", t.Key(), "error", err)
		progress.Report(100, "Login session cleanup failed")
		return err
	}
	if deleted > 0 {
		slog.InfoContext(ctx, "login session cleanup completed", "component", "taskmanager", "task", t.Key(), "deleted", deleted)
	}
	progress.Report(100, fmt.Sprintf("Deleted %d expired login sessions", deleted))
	return nil
}
