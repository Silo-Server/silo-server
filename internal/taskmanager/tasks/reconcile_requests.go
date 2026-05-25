package tasks

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/Silo-Server/silo-server/internal/requests"
	"github.com/Silo-Server/silo-server/internal/taskmanager"
)

type RequestReconciler interface {
	ReconcileRequests(ctx context.Context, limit int) (requests.ReconcileResult, error)
}

type ReconcileRequestsTask struct {
	reconciler RequestReconciler
	limit      int
}

func NewReconcileRequestsTask(reconciler RequestReconciler, limit int) *ReconcileRequestsTask {
	if limit <= 0 {
		limit = 100
	}
	return &ReconcileRequestsTask{reconciler: reconciler, limit: limit}
}

func (t *ReconcileRequestsTask) Key() string  { return "reconcile_requests" }
func (t *ReconcileRequestsTask) Name() string { return "Reconcile Requests" }
func (t *ReconcileRequestsTask) Description() string {
	return "Checks approved and active media requests against Radarr, Sonarr, and the Silo catalog"
}
func (t *ReconcileRequestsTask) Category() taskmanager.TaskCategory {
	return taskmanager.TaskCategoryLibrary
}
func (t *ReconcileRequestsTask) IsHidden() bool { return false }

func (t *ReconcileRequestsTask) DefaultTriggers() []taskmanager.TriggerConfig {
	return []taskmanager.TriggerConfig{
		{Type: taskmanager.TriggerTypeInterval, IntervalMs: 5 * 60 * 1000},
	}
}

func (t *ReconcileRequestsTask) Execute(ctx context.Context, progress taskmanager.ProgressReporter) error {
	progress.Report(0, "Reconciling media requests")
	if t.reconciler == nil {
		progress.Report(100, "Request reconciliation unavailable")
		return nil
	}
	result, err := t.reconciler.ReconcileRequests(ctx, t.limit)
	if err != nil {
		return fmt.Errorf("reconcile media requests: %w", err)
	}
	if data, err := json.Marshal(result); err == nil {
		progress.SetResultData(data)
	}
	progress.Report(100, "Request reconciliation complete")
	return nil
}
