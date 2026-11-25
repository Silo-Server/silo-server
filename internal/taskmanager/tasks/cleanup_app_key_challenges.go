package tasks

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/Silo-Server/silo-server/internal/taskmanager"
)

// expiredKeyChallengeDeleter removes app key challenges that expired
// unspent and the IDs of used grants past their life; *auth.KeyChallenges
// satisfies it.
type expiredKeyChallengeDeleter interface {
	DeleteExpiredChallenges(ctx context.Context) (int, error)
}

// AppKeyChallengeCleanupTask deletes app key challenges nobody spent, and the
// records of used grants once their grant has expired. Spending a challenge
// deletes it; nothing else removes one that was never used.
type AppKeyChallengeCleanupTask struct {
	challenges expiredKeyChallengeDeleter
}

type appKeyChallengeCleanupResult struct {
	Deleted int `json:"deleted"`
}

// NewAppKeyChallengeCleanupTask creates the retention step for app key
// challenges.
func NewAppKeyChallengeCleanupTask(challenges expiredKeyChallengeDeleter) *AppKeyChallengeCleanupTask {
	return &AppKeyChallengeCleanupTask{challenges: challenges}
}

func (t *AppKeyChallengeCleanupTask) Key() string  { return "cleanup_app_key_challenges" }
func (t *AppKeyChallengeCleanupTask) Name() string { return "Cleanup Expired App Key Challenges" }
func (t *AppKeyChallengeCleanupTask) Description() string {
	return "Deletes app key sign-in challenges that expired unused, and records of used grants that have expired"
}
func (t *AppKeyChallengeCleanupTask) Category() taskmanager.TaskCategory {
	return taskmanager.TaskCategorySystem
}
func (t *AppKeyChallengeCleanupTask) IsHidden() bool { return false }

// DefaultTriggers runs once at startup, then hourly: anyone may ask for a
// challenge, so unspent ones pile up faster than the daily retention steps
// would clear them.
func (t *AppKeyChallengeCleanupTask) DefaultTriggers() []taskmanager.TriggerConfig {
	return []taskmanager.TriggerConfig{
		{Type: taskmanager.TriggerTypeStartup},
		{Type: taskmanager.TriggerTypeInterval, IntervalMs: int64(time.Hour / time.Millisecond)},
	}
}

func (t *AppKeyChallengeCleanupTask) Execute(ctx context.Context, progress taskmanager.ProgressReporter) error {
	if t == nil || t.challenges == nil {
		progress.Report(100, "App key challenge cleanup is not configured")
		return nil
	}
	progress.Report(0, "Deleting expired app key challenges")
	deleted, err := t.challenges.DeleteExpiredChallenges(ctx)
	data, _ := json.Marshal(appKeyChallengeCleanupResult{Deleted: deleted})
	progress.SetResultData(data)
	if err != nil {
		slog.WarnContext(ctx, "app key challenge cleanup failed", "component", "taskmanager", "task", t.Key(), "error", err)
		progress.Report(100, "App key challenge cleanup failed")
		return err
	}
	if deleted > 0 {
		slog.InfoContext(ctx, "app key challenge cleanup completed", "component", "taskmanager", "task", t.Key(), "deleted", deleted)
	}
	progress.Report(100, fmt.Sprintf("Deleted %d expired app key challenges", deleted))
	return nil
}
