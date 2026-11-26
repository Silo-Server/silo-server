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
// unspent, the IDs of used grants past their life, and expired app codes
// and sign-in links;
// *auth.KeyChallenges satisfies it.
type expiredKeyChallengeDeleter interface {
	DeleteExpiredChallenges(ctx context.Context) (int, error)
}

// expiredPendingKeyRevoker revokes device keys that waited for approval
// longer than they may; *auth.AppKeyRepository satisfies it.
type expiredPendingKeyRevoker interface {
	RevokeExpiredPendingKeys(ctx context.Context) (int, error)
}

// AppKeyChallengeCleanupTask deletes app key challenges nobody spent, the
// records of used grants once their grant has expired and app codes past
// their ten minutes and sign-in links past their 24 hours, and revokes device
// keys that waited for an administrator's approval past their 24 hours.
// Spending a challenge deletes it; nothing else removes one that was never
// used. A waiting key is revoked, not deleted, so its public key stays
// enrolled once.
type AppKeyChallengeCleanupTask struct {
	challenges  expiredKeyChallengeDeleter
	pendingKeys expiredPendingKeyRevoker
}

type appKeyChallengeCleanupResult struct {
	Deleted            int `json:"deleted"`
	RevokedPendingKeys int `json:"revoked_pending_keys"`
}

// NewAppKeyChallengeCleanupTask creates the retention step for app key
// challenges and expired waiting keys.
func NewAppKeyChallengeCleanupTask(challenges expiredKeyChallengeDeleter, pendingKeys expiredPendingKeyRevoker) *AppKeyChallengeCleanupTask {
	return &AppKeyChallengeCleanupTask{challenges: challenges, pendingKeys: pendingKeys}
}

func (t *AppKeyChallengeCleanupTask) Key() string  { return "cleanup_app_key_challenges" }
func (t *AppKeyChallengeCleanupTask) Name() string { return "Cleanup Expired App Key Challenges" }
func (t *AppKeyChallengeCleanupTask) Description() string {
	return "Deletes app key sign-in challenges that expired unused and records of used grants that have expired, and revokes devices that waited too long for approval"
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
	result := appKeyChallengeCleanupResult{Deleted: deleted}
	if err == nil && t.pendingKeys != nil {
		result.RevokedPendingKeys, err = t.pendingKeys.RevokeExpiredPendingKeys(ctx)
	}
	data, _ := json.Marshal(result)
	progress.SetResultData(data)
	if err != nil {
		slog.WarnContext(ctx, "app key challenge cleanup failed", "component", "taskmanager", "task", t.Key(), "error", err)
		progress.Report(100, "App key challenge cleanup failed")
		return err
	}
	if deleted > 0 || result.RevokedPendingKeys > 0 {
		slog.InfoContext(ctx, "app key challenge cleanup completed", "component", "taskmanager", "task", t.Key(),
			"deleted", deleted, "revoked_pending_keys", result.RevokedPendingKeys)
	}
	progress.Report(100, fmt.Sprintf("Deleted %d expired app key challenges, revoked %d expired waiting devices", deleted, result.RevokedPendingKeys))
	return nil
}
