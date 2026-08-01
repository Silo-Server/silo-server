package tasks

import (
	"context"
	"fmt"
	"time"

	"github.com/Silo-Server/silo-server/internal/taskmanager"
)

const (
	reconcileVirtualEpisodesInterval = 24 * time.Hour
	reconcileVirtualEpisodesLimit    = 1000
)

type VirtualEpisodeReconciler interface {
	ReconcileReleasedCollectionVirtualEpisodes(ctx context.Context, limit int) (int, error)
}

type ReconcileVirtualEpisodesTask struct {
	reconciler VirtualEpisodeReconciler
}

func NewReconcileVirtualEpisodesTask(reconciler VirtualEpisodeReconciler) *ReconcileVirtualEpisodesTask {
	return &ReconcileVirtualEpisodesTask{reconciler: reconciler}
}

func (t *ReconcileVirtualEpisodesTask) Key() string  { return "reconcile_virtual_episodes" }
func (t *ReconcileVirtualEpisodesTask) Name() string { return "Reconcile virtual episodes" }
func (t *ReconcileVirtualEpisodesTask) Description() string {
	return "Activates released zero-storage episodes without resolving provider streams"
}
func (t *ReconcileVirtualEpisodesTask) Category() taskmanager.TaskCategory {
	return taskmanager.TaskCategoryLibrary
}
func (t *ReconcileVirtualEpisodesTask) IsHidden() bool { return false }

func (t *ReconcileVirtualEpisodesTask) DefaultTriggers() []taskmanager.TriggerConfig {
	return []taskmanager.TriggerConfig{{
		Type:       taskmanager.TriggerTypeInterval,
		IntervalMs: int64(reconcileVirtualEpisodesInterval / time.Millisecond),
	}}
}

func (t *ReconcileVirtualEpisodesTask) Execute(ctx context.Context, progress taskmanager.ProgressReporter) error {
	progress.Report(0, "Finding newly released virtual episodes")
	if t.reconciler == nil {
		progress.Report(100, "Virtual episode reconciliation unavailable")
		return nil
	}
	reconciled, err := t.reconciler.ReconcileReleasedCollectionVirtualEpisodes(ctx, reconcileVirtualEpisodesLimit)
	if err != nil {
		return fmt.Errorf("reconcile virtual episodes: %w", err)
	}
	progress.Report(100, fmt.Sprintf("Reconciled %d virtual series", reconciled))
	return nil
}
