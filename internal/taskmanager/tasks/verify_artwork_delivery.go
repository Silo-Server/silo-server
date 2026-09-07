package tasks

import (
	"context"
	"fmt"
	"time"

	"github.com/Silo-Server/silo-server/internal/metadata"
	"github.com/Silo-Server/silo-server/internal/taskmanager"
)

var _ taskmanager.Task = (*VerifyArtworkDeliveryTask)(nil)

type VerifyArtworkDeliveryTask struct {
	store   *metadata.ArtworkDeliveryStore
	checker metadata.ArtworkDeliveryChecker
}

func NewVerifyArtworkDeliveryTask(store *metadata.ArtworkDeliveryStore, checker metadata.ArtworkDeliveryChecker) *VerifyArtworkDeliveryTask {
	return &VerifyArtworkDeliveryTask{store: store, checker: checker}
}
func (t *VerifyArtworkDeliveryTask) Key() string  { return "verify_artwork_delivery" }
func (t *VerifyArtworkDeliveryTask) Name() string { return "Verify Artwork Delivery" }
func (t *VerifyArtworkDeliveryTask) Description() string {
	return "Checks published artwork through storage and client delivery URLs in bounded batches, and schedules repair for missing storage objects."
}
func (t *VerifyArtworkDeliveryTask) Category() taskmanager.TaskCategory {
	return taskmanager.TaskCategoryMetadata
}
func (t *VerifyArtworkDeliveryTask) IsHidden() bool { return false }

func (t *VerifyArtworkDeliveryTask) DefaultTriggers() []taskmanager.TriggerConfig {
	return []taskmanager.TriggerConfig{
		{Type: taskmanager.TriggerTypeStartup},
		{Type: taskmanager.TriggerTypeInterval, IntervalMs: int64(time.Minute / time.Millisecond)},
	}
}
func (t *VerifyArtworkDeliveryTask) Execute(ctx context.Context, progress taskmanager.ProgressReporter) error {
	stats, err := t.store.Reconcile(ctx, t.checker)
	if err != nil {
		return err
	}
	progress.Report(100, fmt.Sprintf("Checked %d revisions: %d missing objects, %d probe errors", stats.Checked, stats.Missing, stats.Errors))
	return nil
}
