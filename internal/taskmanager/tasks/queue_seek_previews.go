package tasks

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/Silo-Server/silo-server/internal/taskmanager"
	"github.com/Silo-Server/silo-server/internal/trickplay"
)

// SeekPreviewReconciler is the reconcile surface. Satisfied by
// *trickplay.Service.
type SeekPreviewReconciler interface {
	Reconcile(ctx context.Context) (trickplay.ReconcileStats, bool, error)
}

// QueueSeekPreviewsTask brings the seek preview queue in line with the
// libraries: it queues the video files of libraries that have seek previews
// turned on, and requeues previews that no longer match their file, the
// settings, or the storage.
type QueueSeekPreviewsTask struct {
	reconciler SeekPreviewReconciler
}

func NewQueueSeekPreviewsTask(reconciler SeekPreviewReconciler) *QueueSeekPreviewsTask {
	return &QueueSeekPreviewsTask{reconciler: reconciler}
}

func (t *QueueSeekPreviewsTask) Key() string  { return "queue_seek_previews" }
func (t *QueueSeekPreviewsTask) Name() string { return "Queue Seek Previews" }
func (t *QueueSeekPreviewsTask) Description() string {
	return "Queues seek-bar previews for the video files of libraries that have them turned on, and requeues previews that no longer match their file or settings."
}
func (t *QueueSeekPreviewsTask) Category() taskmanager.TaskCategory {
	return taskmanager.TaskCategoryLibrary
}
func (t *QueueSeekPreviewsTask) IsHidden() bool { return false }
func (t *QueueSeekPreviewsTask) DefaultTriggers() []taskmanager.TriggerConfig {
	return []taskmanager.TriggerConfig{
		{Type: taskmanager.TriggerTypeStartup},
		{Type: taskmanager.TriggerTypeInterval, IntervalMs: int64((15 * time.Minute) / time.Millisecond)},
	}
}

func (t *QueueSeekPreviewsTask) Execute(ctx context.Context, progress taskmanager.ProgressReporter) error {
	if t.reconciler == nil {
		progress.Report(100, "Seek previews are not configured")
		return nil
	}
	progress.Report(0, "Checking which files need seek previews")
	stats, ran, err := t.reconciler.Reconcile(ctx)
	if data, marshalErr := json.Marshal(stats); marshalErr == nil {
		progress.SetResultData(data)
	}
	if err != nil {
		return fmt.Errorf("queuing seek previews: %w", err)
	}
	if !ran {
		progress.Report(100, "Another server is already queuing seek previews")
		return nil
	}
	progress.Report(100, fmt.Sprintf("Queued %d files, requeued %d, removed %d, reclaimed %d stalled",
		stats.Added, stats.Stale, stats.Removed, stats.Reclaimed))
	return nil
}
