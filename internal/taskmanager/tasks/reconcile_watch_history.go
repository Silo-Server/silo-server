package tasks

import (
	"context"
	"fmt"
	"time"

	"github.com/Silo-Server/silo-server/internal/taskmanager"
	"github.com/Silo-Server/silo-server/internal/watchstate"
)

// ReconcileWatchHistoryTask re-resolves orphaned watch history rows against
// the current media catalog using the stable identities stored at record time.
type ReconcileWatchHistoryTask struct {
	reconciler *watchstate.HistoryReconciler
}

// NewReconcileWatchHistoryTask creates a scheduled task for watch-history reconciliation.
func NewReconcileWatchHistoryTask(reconciler *watchstate.HistoryReconciler) *ReconcileWatchHistoryTask {
	return &ReconcileWatchHistoryTask{reconciler: reconciler}
}

func (t *ReconcileWatchHistoryTask) Key() string  { return "reconcile_watch_history" }
func (t *ReconcileWatchHistoryTask) Name() string { return "Reconcile Watch History" }
func (t *ReconcileWatchHistoryTask) Description() string {
	return "Re-resolves orphaned watch history rows against the current media catalog using stored stable identities."
}
func (t *ReconcileWatchHistoryTask) Category() taskmanager.TaskCategory {
	return taskmanager.TaskCategorySystem
}
func (t *ReconcileWatchHistoryTask) IsHidden() bool { return true }

func (t *ReconcileWatchHistoryTask) DefaultTriggers() []taskmanager.TriggerConfig {
	return []taskmanager.TriggerConfig{
		{
			Type:       taskmanager.TriggerTypeInterval,
			IntervalMs: int64(30 * 24 * time.Hour / time.Millisecond),
		},
	}
}

func (t *ReconcileWatchHistoryTask) Execute(ctx context.Context, progress taskmanager.ProgressReporter) error {
	progress.Report(0, "Scanning for orphaned history rows")
	stats, err := t.reconciler.Run(ctx)
	progress.Report(100, fmt.Sprintf("Reconciled %d/%d orphans (%d unresolvable, %d errors)",
		stats.Resolved, stats.OrphansFound, stats.Unresolvable, stats.Errors))
	return err
}
