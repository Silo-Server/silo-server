package tasks

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/Silo-Server/silo-server/internal/taskmanager"
)

// expiredOAuthFlowDeleter removes expired OAuth sign-in flow rows;
// *auth.PGOAuthStore satisfies it and owns the thresholds.
type expiredOAuthFlowDeleter interface {
	DeleteExpiredFlows(ctx context.Context) (int, error)
}

// OAuthFlowCleanupTask deletes expired OAuth sign-in flows, completion
// codes past their reuse window, link tickets, pending links and parked
// native starts. A flow abandoned at the provider is never consumed, so
// nothing else removes it.
type OAuthFlowCleanupTask struct {
	requests expiredOAuthFlowDeleter
}

type oauthFlowCleanupResult struct {
	Deleted int `json:"deleted"`
}

// NewOAuthFlowCleanupTask creates the retention step for OAuth sign-in
// flows.
func NewOAuthFlowCleanupTask(requests expiredOAuthFlowDeleter) *OAuthFlowCleanupTask {
	return &OAuthFlowCleanupTask{requests: requests}
}

func (t *OAuthFlowCleanupTask) Key() string  { return "cleanup_oauth_flows" }
func (t *OAuthFlowCleanupTask) Name() string { return "Cleanup Expired OAuth Sign-in Flows" }
func (t *OAuthFlowCleanupTask) Description() string {
	return "Deletes OAuth sign-in flows long past their expiry"
}
func (t *OAuthFlowCleanupTask) Category() taskmanager.TaskCategory {
	return taskmanager.TaskCategorySystem
}
func (t *OAuthFlowCleanupTask) IsHidden() bool { return false }

// DefaultTriggers matches the neighboring retention steps: once at startup,
// then daily.
func (t *OAuthFlowCleanupTask) DefaultTriggers() []taskmanager.TriggerConfig {
	return []taskmanager.TriggerConfig{
		{Type: taskmanager.TriggerTypeStartup},
		{Type: taskmanager.TriggerTypeInterval, IntervalMs: int64((24 * time.Hour) / time.Millisecond)},
	}
}

func (t *OAuthFlowCleanupTask) Execute(ctx context.Context, progress taskmanager.ProgressReporter) error {
	if t == nil || t.requests == nil {
		progress.Report(100, "OAuth sign-in flow cleanup is not configured")
		return nil
	}
	progress.Report(0, "Deleting expired OAuth sign-in flows")
	deleted, err := t.requests.DeleteExpiredFlows(ctx)
	if data, marshalErr := json.Marshal(oauthFlowCleanupResult{Deleted: deleted}); marshalErr == nil {
		progress.SetResultData(data)
	}
	if err != nil {
		slog.WarnContext(ctx, "oauth sign-in flow cleanup failed", "component", "taskmanager", "task", t.Key(), "error", err)
		progress.Report(100, "OAuth sign-in flow cleanup failed")
		return err
	}
	if deleted > 0 {
		slog.InfoContext(ctx, "oauth sign-in flow cleanup completed", "component", "taskmanager", "task", t.Key(), "deleted", deleted)
	}
	progress.Report(100, fmt.Sprintf("Deleted %d expired OAuth sign-in flows", deleted))
	return nil
}
