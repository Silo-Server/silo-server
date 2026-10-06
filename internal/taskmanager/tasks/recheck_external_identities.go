package tasks

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/Silo-Server/silo-server/internal/taskmanager"
	"github.com/jackc/pgx/v5/pgxpool"
)

// idleIdentityRechecker is the scheduled half of the external sign-in
// provider re-check; *auth.ProviderRecheck satisfies it and owns the
// selection and the outcomes.
type idleIdentityRechecker interface {
	IdleRecheckDue(ctx context.Context) (bool, error)
	RecheckIdleIdentities(ctx context.Context) (map[string]int, error)
}

// RecheckExternalIdentitiesTask asks the external sign-in provider about
// identities whose accounts hold credentials that no session refresh
// re-checks: API keys, Audiobookshelf sessions and idle login sessions
// (docs/architecture/external-sign-in.md, "Provider re-check"). Without it,
// a person the provider removed would keep an API key for good.
//
// Every API process runs the task manager and fires the same trigger, so an
// advisory lock lets one server run the pass; the others skip.
type RecheckExternalIdentitiesTask struct {
	rechecker idleIdentityRechecker
	lock      clusterLock
}

// recheckExternalIdentitiesAdvisoryLock spells "SILOIDRC".
const recheckExternalIdentitiesAdvisoryLock int64 = 0x53494C4F49445243

type recheckExternalIdentitiesResult struct {
	Checked  int            `json:"checked"`
	ByStatus map[string]int `json:"by_status"`
}

// NewRecheckExternalIdentitiesTask creates the scheduled provider re-check.
// A nil pool runs without the cluster lock.
func NewRecheckExternalIdentitiesTask(pool *pgxpool.Pool, rechecker idleIdentityRechecker) *RecheckExternalIdentitiesTask {
	t := &RecheckExternalIdentitiesTask{rechecker: rechecker}
	if pool != nil {
		t.lock = advisoryClusterLock{pool: pool, key: recheckExternalIdentitiesAdvisoryLock, name: "external sign-in re-check"}
	}
	return t
}

func (t *RecheckExternalIdentitiesTask) Key() string { return "recheck_external_identities" }
func (t *RecheckExternalIdentitiesTask) Name() string {
	return "Re-check External Sign-in Accounts"
}
func (t *RecheckExternalIdentitiesTask) Description() string {
	return "Asks the external sign-in provider about accounts that hold API keys or idle sessions, and revokes the access of accounts the provider no longer admits"
}
func (t *RecheckExternalIdentitiesTask) Category() taskmanager.TaskCategory {
	return taskmanager.TaskCategorySystem
}
func (t *RecheckExternalIdentitiesTask) IsHidden() bool { return false }

// DefaultTriggers runs hourly. Each identity is asked only once its last
// answer is older than auth.provider_recheck_interval, so the hour bounds
// how late the pass notices a due identity, not how often the provider is
// asked.
func (t *RecheckExternalIdentitiesTask) DefaultTriggers() []taskmanager.TriggerConfig {
	return []taskmanager.TriggerConfig{{Type: taskmanager.TriggerTypeInterval, IntervalMs: int64(time.Hour / time.Millisecond)}}
}

// ShouldRun skips scheduled runs while no identity is due, so a server
// without external sign-in records no hourly history.
func (t *RecheckExternalIdentitiesTask) ShouldRun(ctx context.Context) (bool, error) {
	if t == nil || t.rechecker == nil {
		return false, nil
	}
	return t.rechecker.IdleRecheckDue(ctx)
}

func (t *RecheckExternalIdentitiesTask) Execute(ctx context.Context, progress taskmanager.ProgressReporter) error {
	if t == nil || t.rechecker == nil {
		progress.Report(100, "External sign-in is not configured")
		return nil
	}
	if t.lock != nil {
		release, acquired, err := t.lock.TryAcquire(ctx)
		if err != nil {
			return fmt.Errorf("acquiring external sign-in re-check lock: %w", err)
		}
		if !acquired {
			progress.Report(100, "Another server is re-checking external sign-in accounts")
			return nil
		}
		defer release()
	}
	progress.Report(0, "Re-checking external sign-in accounts")
	counts, err := t.rechecker.RecheckIdleIdentities(ctx)
	result := recheckExternalIdentitiesResult{ByStatus: counts}
	for _, n := range counts {
		result.Checked += n
	}
	if data, marshalErr := json.Marshal(result); marshalErr == nil {
		progress.SetResultData(data)
	}
	if err != nil {
		slog.WarnContext(ctx, "external sign-in re-check failed", "component", "taskmanager", "task", t.Key(), "error", err)
		progress.Report(100, "External sign-in re-check failed")
		return err
	}
	if result.Checked > 0 {
		slog.InfoContext(ctx, "external sign-in re-check completed", "component", "taskmanager", "task", t.Key(), "checked", result.Checked, "by_status", counts)
	}
	progress.Report(100, fmt.Sprintf("Re-checked %d external sign-in accounts", result.Checked))
	return nil
}
