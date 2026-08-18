package tasks

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/Silo-Server/silo-server/internal/metadata"
	"github.com/Silo-Server/silo-server/internal/taskmanager"
)

const (
	cacheMetadataImagesIntervalMs = int64(60 * 1000)
	cacheMetadataImagesBatchSize  = 1000
	cacheMetadataImagesWorkers    = 2
	cacheMetadataImagesMaxRuntime = 10 * time.Minute
)

type MetadataImageCacheRunner interface {
	RunUntilIdle(ctx context.Context, workerID string, claimLimit int, concurrency int, maxRuntime time.Duration, reportProgress metadata.ImageCacheRunProgressReporter) (metadata.ImageCacheRunStats, error)
}

type CacheMetadataImagesTask struct {
	runner MetadataImageCacheRunner
}

func NewCacheMetadataImagesTask(runner MetadataImageCacheRunner) *CacheMetadataImagesTask {
	return &CacheMetadataImagesTask{runner: runner}
}

func (t *CacheMetadataImagesTask) Key() string  { return "cache_metadata_images" }
func (t *CacheMetadataImagesTask) Name() string { return "Cache Metadata Images" }
func (t *CacheMetadataImagesTask) Description() string {
	return "Caches provider metadata artwork into object storage"
}
func (t *CacheMetadataImagesTask) Category() taskmanager.TaskCategory {
	return taskmanager.TaskCategoryMetadata
}
func (t *CacheMetadataImagesTask) IsHidden() bool { return false }

func (t *CacheMetadataImagesTask) DefaultTriggers() []taskmanager.TriggerConfig {
	return []taskmanager.TriggerConfig{
		{Type: taskmanager.TriggerTypeStartup},
		{Type: taskmanager.TriggerTypeInterval, IntervalMs: cacheMetadataImagesIntervalMs},
	}
}

func (t *CacheMetadataImagesTask) Execute(ctx context.Context, progress taskmanager.ProgressReporter) error {
	if t.runner == nil {
		progress.Report(100, "Metadata image cache is not configured")
		return nil
	}
	hostname, _ := os.Hostname()
	if hostname == "" {
		hostname = "silo"
	}
	progress.Report(0, "Starting metadata image cache")
	stats, err := t.runner.RunUntilIdle(
		ctx,
		hostname,
		cacheMetadataImagesBatchSize,
		cacheMetadataImagesWorkers,
		cacheMetadataImagesMaxRuntime,
		func(update metadata.ImageCacheRunStats) {
			progress.Report(cacheMetadataImagesPercent(update.Queue), formatCacheMetadataImagesProgress(update))
		},
	)
	if err != nil {
		return fmt.Errorf("caching metadata images: %w", err)
	}
	message := fmt.Sprintf(
		"Batches %d, enqueued %d existing, claimed %d, cached %d, %d %s, skipped %d, uploaded %d variants, found %d existing variants, deleted %d old successes",
		stats.Batches,
		stats.EnqueuedExisting,
		stats.Claimed,
		stats.Succeeded,
		stats.Failed,
		imageCacheFailedAttemptLabel(stats.Failed),
		stats.Skipped,
		stats.UploadedVariants,
		stats.ExistingVariants,
		stats.DeletedSucceeded,
	)
	if stats.RuntimeLimited {
		message += ", runtime budget reached"
	}
	progress.Report(100, message)
	return nil
}

func formatCacheMetadataImagesProgress(stats metadata.ImageCacheRunStats) string {
	processed := stats.Succeeded + stats.Failed + stats.Skipped
	message := fmt.Sprintf(
		"Processed %d images across %d batches (%d cached, %d %s, %d skipped)",
		processed,
		stats.Batches,
		stats.Succeeded,
		stats.Failed,
		imageCacheFailedAttemptLabel(stats.Failed),
		stats.Skipped,
	)
	if stats.Queue.Known && stats.Queue.Total > 0 {
		failureLabel := "terminal failures"
		if stats.Queue.Failed == 1 {
			failureLabel = "terminal failure"
		}
		message += fmt.Sprintf(
			" · Overall %d/%d complete (%d %s)",
			stats.Queue.Completed(),
			stats.Queue.Total,
			stats.Queue.Failed,
			failureLabel,
		)
	}
	return message
}

func imageCacheFailedAttemptLabel(count int) string {
	if count == 1 {
		return "failed attempt"
	}
	return "failed attempts"
}

func cacheMetadataImagesPercent(queue metadata.ImageCacheQueueProgress) float64 {
	if !queue.Known || queue.Total <= 0 {
		return 0
	}
	percent := float64(queue.Completed()) * 100 / float64(queue.Total)
	// Execute reports the authoritative 100% after the runner returns. Keep a
	// still-running task below 100% while it may be performing discovery.
	if percent >= 100 {
		return 99.9
	}
	if percent < 0 {
		return 0
	}
	return percent
}
