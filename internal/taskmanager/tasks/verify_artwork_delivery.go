package tasks

import (
	"context"
	"fmt"
	"time"

	"github.com/Silo-Server/silo-server/internal/metadata"
	"github.com/Silo-Server/silo-server/internal/taskmanager"
)

var _ taskmanager.Task = (*VerifyArtworkDeliveryTask)(nil)

type ArtworkDeliveryReconciler interface {
	Reconcile(context.Context, metadata.ArtworkDeliveryChecker) (metadata.ArtworkDeliveryStats, error)
}

type VerifyArtworkDeliveryTask struct {
	store   ArtworkDeliveryReconciler
	checker metadata.ArtworkDeliveryChecker
}

func NewVerifyArtworkDeliveryTask(store ArtworkDeliveryReconciler, checker metadata.ArtworkDeliveryChecker) *VerifyArtworkDeliveryTask {
	return &VerifyArtworkDeliveryTask{store: store, checker: checker}
}
func (t *VerifyArtworkDeliveryTask) Key() string  { return "verify_artwork_delivery" }
func (t *VerifyArtworkDeliveryTask) Name() string { return "Verify Artwork Delivery" }
func (t *VerifyArtworkDeliveryTask) Description() string {
	return "Checks published artwork through storage and client delivery URLs, newly published artwork first, and schedules repair for missing storage objects."
}
func (t *VerifyArtworkDeliveryTask) Category() taskmanager.TaskCategory {
	return taskmanager.TaskCategoryMetadata
}
func (t *VerifyArtworkDeliveryTask) IsHidden() bool { return true }

func (t *VerifyArtworkDeliveryTask) DefaultTriggers() []taskmanager.TriggerConfig {
	return []taskmanager.TriggerConfig{
		{Type: taskmanager.TriggerTypeStartup},
		{Type: taskmanager.TriggerTypeInterval, IntervalMs: int64(time.Minute / time.Millisecond)},
	}
}
func (t *VerifyArtworkDeliveryTask) Execute(ctx context.Context, progress taskmanager.ProgressReporter) error {
	stats, err := t.store.Reconcile(ctx, t.checker)
	progress.SetResultData(stats.JSON())
	if err != nil {
		return fmt.Errorf("verifying artwork delivery: %w", err)
	}
	if stats.Checked > 0 && stats.Errors == stats.Checked {
		return fmt.Errorf("every artwork delivery probe failed (%d revisions): %s", stats.Errors, stats.LastError)
	}
	progress.Report(100, fmt.Sprintf(
		"Checked %d revisions (%d newly published): %d incomplete, %d probe errors, %d overdue",
		stats.Checked, stats.Pending, stats.Incomplete, stats.Errors, stats.Overdue,
	))
	return nil
}
