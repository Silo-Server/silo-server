package tasks

import (
	"context"
	"fmt"
	"time"

	"github.com/Silo-Server/silo-server/internal/metadata"
	"github.com/Silo-Server/silo-server/internal/taskmanager"
)

type ArtworkRevisionGCRunner interface {
	Run(ctx context.Context) (metadata.ArtworkRevisionGCStats, error)
}

type CleanupArtworkRevisionsTask struct {
	runner ArtworkRevisionGCRunner
}

func NewCleanupArtworkRevisionsTask(runner ArtworkRevisionGCRunner) *CleanupArtworkRevisionsTask {
	return &CleanupArtworkRevisionsTask{runner: runner}
}

func (t *CleanupArtworkRevisionsTask) Key() string  { return "cleanup_artwork_revisions" }
func (t *CleanupArtworkRevisionsTask) Name() string { return "Clean Artwork Revisions" }
func (t *CleanupArtworkRevisionsTask) Description() string {
	return "Deletes unpublished or displaced immutable artwork revisions after a grace period when no catalog record references them."
}
func (t *CleanupArtworkRevisionsTask) Category() taskmanager.TaskCategory {
	return taskmanager.TaskCategoryMetadata
}
func (t *CleanupArtworkRevisionsTask) IsHidden() bool { return false }
func (t *CleanupArtworkRevisionsTask) DefaultTriggers() []taskmanager.TriggerConfig {
	return []taskmanager.TriggerConfig{
		{Type: taskmanager.TriggerTypeStartup},
		{Type: taskmanager.TriggerTypeInterval, IntervalMs: int64(time.Hour / time.Millisecond)},
	}
}

func (t *CleanupArtworkRevisionsTask) Execute(ctx context.Context, progress taskmanager.ProgressReporter) error {
	if t == nil || t.runner == nil {
		progress.Report(100, "Artwork revision cleanup is not configured")
		return nil
	}
	progress.Report(0, "Checking displaced artwork revisions")
	stats, err := t.runner.Run(ctx)
	progress.SetResultData(stats.JSON())
	if err != nil {
		return fmt.Errorf("cleaning artwork revisions: %w", err)
	}
	progress.Report(100, fmt.Sprintf(
		"Processed %d revisions: deleted %d, retained %d referenced, scheduled %d retries",
		stats.Claimed, stats.Deleted, stats.Referenced, stats.Retried,
	))
	return nil
}
