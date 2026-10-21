package tasks

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/Silo-Server/silo-server/internal/blobgc"
	"github.com/Silo-Server/silo-server/internal/taskmanager"
)

// removedMediaImagesPerRun bounds one run. A run that stops at the bound
// leaves the rest due, and the next hourly run continues.
const removedMediaImagesPerRun = 5000

// RemovedMediaImagesCollector is the collector surface. Satisfied by
// *blobgc.Collector.
type RemovedMediaImagesCollector interface {
	Collect(ctx context.Context, limit int) (blobgc.CollectStats, error)
}

// CleanupRemovedMediaImagesTask deletes the images generated from media
// files that were removed from the catalog, once their grace period ends.
type CleanupRemovedMediaImagesTask struct {
	collector RemovedMediaImagesCollector
}

func NewCleanupRemovedMediaImagesTask(collector RemovedMediaImagesCollector) *CleanupRemovedMediaImagesTask {
	return &CleanupRemovedMediaImagesTask{collector: collector}
}

func (t *CleanupRemovedMediaImagesTask) Key() string  { return "cleanup_removed_media_images" }
func (t *CleanupRemovedMediaImagesTask) Name() string { return "Clean Removed Media Images" }
func (t *CleanupRemovedMediaImagesTask) Description() string {
	return "Deletes the chapter thumbnails and seek previews of media files removed from the catalog, and seek previews that were replaced, after a grace period."
}
func (t *CleanupRemovedMediaImagesTask) Category() taskmanager.TaskCategory {
	return taskmanager.TaskCategoryMetadata
}
func (t *CleanupRemovedMediaImagesTask) IsHidden() bool { return false }
func (t *CleanupRemovedMediaImagesTask) DefaultTriggers() []taskmanager.TriggerConfig {
	return []taskmanager.TriggerConfig{
		{Type: taskmanager.TriggerTypeStartup},
		{Type: taskmanager.TriggerTypeInterval, IntervalMs: int64(time.Hour / time.Millisecond)},
	}
}

func (t *CleanupRemovedMediaImagesTask) Execute(ctx context.Context, progress taskmanager.ProgressReporter) error {
	if t.collector == nil {
		progress.Report(100, "Media image cleanup is not configured")
		return nil
	}
	progress.Report(0, "Deleting images of removed media files")
	stats, err := t.collector.Collect(ctx, removedMediaImagesPerRun)
	if data, marshalErr := json.Marshal(stats); marshalErr == nil {
		progress.SetResultData(data)
	}
	if err != nil {
		return fmt.Errorf("cleaning removed media images: %w", err)
	}
	progress.Report(100, fmt.Sprintf("Deleted %d objects under %d prefixes; kept %d referenced, %d to retry",
		stats.Objects, stats.Deleted, stats.Kept, stats.Retried))
	return nil
}
