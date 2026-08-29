package tasks

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/Silo-Server/silo-server/internal/metadata"
	"github.com/Silo-Server/silo-server/internal/taskmanager"
)

type ArtworkRevisionGCRunner interface {
	Run(ctx context.Context) (metadata.ArtworkRevisionGCStats, error)
}

// ArtworkTempFileSweeper removes abandoned temporary files from a filesystem
// artwork store. Crash debris under the store root is invisible to the catalog
// but occupies bytes forever unless something sweeps it; this task is that
// something. Satisfied by *artworkstore.FilesystemStore; nil on S3 installs,
// which have no temp files to sweep.
type ArtworkTempFileSweeper interface {
	CleanTempFiles(ctx context.Context, olderThan time.Duration) (int, error)
}

type CleanupArtworkRevisionsTask struct {
	runner  ArtworkRevisionGCRunner
	sweeper ArtworkTempFileSweeper
}

func NewCleanupArtworkRevisionsTask(runner ArtworkRevisionGCRunner, sweeper ArtworkTempFileSweeper) *CleanupArtworkRevisionsTask {
	return &CleanupArtworkRevisionsTask{runner: runner, sweeper: sweeper}
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
	tempSummary := ""
	if t.sweeper != nil {
		progress.Report(95, "Sweeping abandoned temporary artwork files")
		removed, sweepErr := t.sweeper.CleanTempFiles(ctx, 0)
		if sweepErr != nil {
			// Revision GC succeeded; debris that survives one sweep is retried
			// on the next run, so report rather than fail.
			slog.WarnContext(ctx, "artwork temp-file sweep reported errors",
				"component", "tasks", "removed", removed, "error", sweepErr)
		}
		tempSummary = fmt.Sprintf(", swept %d temp files", removed)
	}
	progress.Report(100, fmt.Sprintf(
		"Processed %d revisions: deleted %d, retained %d referenced, scheduled %d retries%s",
		stats.Claimed, stats.Deleted, stats.Referenced, stats.Retried, tempSummary,
	))
	return nil
}
