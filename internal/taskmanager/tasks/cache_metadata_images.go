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
	// Discovery widens the denominator mid-run, so the raw ratio can dip when a
	// sweep enqueues a fresh page. Reports are sequential, so a high-water mark
	// is enough to keep what the user sees from walking backwards.
	reportedPercent := 0.0
	stats, err := t.runner.RunUntilIdle(
		ctx,
		hostname,
		cacheMetadataImagesBatchSize,
		cacheMetadataImagesWorkers,
		cacheMetadataImagesMaxRuntime,
		func(update metadata.ImageCacheRunStats) {
			percent := cacheMetadataImagesPercent(update)
			if percent < reportedPercent {
				percent = reportedPercent
			}
			reportedPercent = percent
			progress.Report(percent, formatCacheMetadataImagesProgress(update))
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
	processed := stats.Processed()
	message := fmt.Sprintf(
		"Processed %d images across %d batches (%d cached, %d %s, %d skipped)",
		processed,
		stats.Batches,
		stats.Succeeded,
		stats.Failed,
		imageCacheFailedAttemptLabel(stats.Failed),
		stats.Skipped,
	)
	if total := cacheMetadataImagesRunTotal(stats); total > 0 {
		message += fmt.Sprintf(" · %d of %d in this run's backlog", processed, total)
	}
	return message
}

func imageCacheFailedAttemptLabel(count int) string {
	if count == 1 {
		return "failed attempt"
	}
	return "failed attempts"
}

// cacheMetadataImagesRunTotal is the denominator for this execution's progress:
// the backlog sampled when the run started, plus the work discovery has
// enqueued since, widened again if the run has somehow processed more than
// both. Reporting against the run rather than the whole durable queue keeps a
// steady-state server from opening at ~100%; counting discovered work keeps a
// first-run backfill — which samples an empty backlog because discovery has not
// swept yet — from reporting its first completed batch as the whole job.
func cacheMetadataImagesRunTotal(stats metadata.ImageCacheRunStats) int64 {
	if !stats.Backlog.Known {
		return 0
	}
	total := stats.Backlog.Outstanding() + int64(stats.EnqueuedExisting)
	if processed := int64(stats.Processed()); processed > total {
		total = processed
	}
	return total
}

func cacheMetadataImagesPercent(stats metadata.ImageCacheRunStats) float64 {
	total := cacheMetadataImagesRunTotal(stats)
	if total <= 0 {
		return 0
	}
	processed := int64(stats.Processed())
	if processed <= 0 {
		return 0
	}
	percent := float64(processed) * 100 / float64(total)
	// Execute reports the authoritative 100% after the runner returns. Keep a
	// still-running task below 100% while it may still claim or discover work,
	// and short enough of it that one-decimal rounding cannot read as complete.
	if percent >= 99.9 {
		return 99.9
	}
	return percent
}
