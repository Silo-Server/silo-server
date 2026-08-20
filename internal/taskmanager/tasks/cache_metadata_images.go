package tasks

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/google/uuid"

	"github.com/Silo-Server/silo-server/internal/metadata"
	"github.com/Silo-Server/silo-server/internal/taskmanager"
)

const (
	cacheMetadataImagesIntervalMs = int64(60 * 1000)
	// Claim only work that can start immediately. Claiming a large queue page
	// stamps one lease on every row up front; with two workers, the unstarted
	// tail could expire and be reclaimed before this execution reaches it.
	cacheMetadataImagesClaimLimit = 2
	cacheMetadataImagesWorkers    = 2
	cacheMetadataImagesMaxRuntime = 10 * time.Minute
)

type MetadataImageCacheRunner interface {
	DrainUntilIdle(ctx context.Context, workerID string, claimLimit int, concurrency int, maxRuntime time.Duration, reportProgress metadata.ImageCacheRunProgressReporter) (metadata.ImageCacheRunStats, error)
}

type MetadataImageBackfillRunner interface {
	RunUntilIdle(ctx context.Context, workerID string, claimLimit int, concurrency int, maxRuntime time.Duration, reportProgress metadata.ImageCacheRunProgressReporter) (metadata.ImageCacheRunStats, error)
}

type CacheMetadataImagesTask struct {
	runner MetadataImageCacheRunner
}

type BackfillMetadataImagesTask struct {
	runner MetadataImageBackfillRunner
}

func NewCacheMetadataImagesTask(runner MetadataImageCacheRunner) *CacheMetadataImagesTask {
	return &CacheMetadataImagesTask{runner: runner}
}

func NewBackfillMetadataImagesTask(runner MetadataImageBackfillRunner) *BackfillMetadataImagesTask {
	return &BackfillMetadataImagesTask{runner: runner}
}

func (t *CacheMetadataImagesTask) Key() string  { return "cache_metadata_images" }
func (t *CacheMetadataImagesTask) Name() string { return "Cache Metadata Images" }
func (t *CacheMetadataImagesTask) Description() string {
	return "Processes only artwork already queued by scans, refreshes, and metadata changes"
}
func (t *CacheMetadataImagesTask) Category() taskmanager.TaskCategory {
	return taskmanager.TaskCategoryMetadata
}
func (t *CacheMetadataImagesTask) IsHidden() bool { return false }

func (t *BackfillMetadataImagesTask) Key() string  { return "backfill_metadata_images" }
func (t *BackfillMetadataImagesTask) Name() string { return "Backfill Metadata Images" }
func (t *BackfillMetadataImagesTask) Description() string {
	return "Manually discovers and caches missing provider artwork across the full catalog"
}
func (t *BackfillMetadataImagesTask) Category() taskmanager.TaskCategory {
	return taskmanager.TaskCategoryMetadata
}
func (t *BackfillMetadataImagesTask) IsHidden() bool   { return false }
func (t *BackfillMetadataImagesTask) ManualOnly() bool { return true }

func (t *CacheMetadataImagesTask) DefaultTriggers() []taskmanager.TriggerConfig {
	return []taskmanager.TriggerConfig{
		{Type: taskmanager.TriggerTypeStartup},
		{Type: taskmanager.TriggerTypeInterval, IntervalMs: cacheMetadataImagesIntervalMs},
	}
}

// Backfill is deliberately manual-only. The normal cache task drains durable
// jobs created by catalog changes; only an administrator choosing this task
// may initiate a full-catalog discovery sweep.
func (t *BackfillMetadataImagesTask) DefaultTriggers() []taskmanager.TriggerConfig { return nil }

// ShouldRun fails closed for every scheduler trigger, including one an older
// installation or administrator may have persisted. TaskManager.RunTask
// bypasses this gate, preserving the explicit manual action.
func (t *BackfillMetadataImagesTask) ShouldRun(context.Context) (bool, error) {
	return false, nil
}

func (t *CacheMetadataImagesTask) Execute(ctx context.Context, progress taskmanager.ProgressReporter) error {
	if t.runner == nil {
		progress.Report(100, "Metadata image cache is not configured")
		return nil
	}
	return executeMetadataImages(ctx, progress, false, t.runner.DrainUntilIdle)
}

func (t *BackfillMetadataImagesTask) Execute(ctx context.Context, progress taskmanager.ProgressReporter) error {
	if t.runner == nil {
		progress.Report(100, "Metadata image backfill is not configured")
		return nil
	}
	return executeMetadataImages(ctx, progress, true, t.runner.RunUntilIdle)
}

type metadataImageRunFunc func(context.Context, string, int, int, time.Duration, metadata.ImageCacheRunProgressReporter) (metadata.ImageCacheRunStats, error)

func executeMetadataImages(ctx context.Context, progress taskmanager.ProgressReporter, backfill bool, run metadataImageRunFunc) error {
	hostname, _ := os.Hostname()
	if hostname == "" {
		hostname = "silo"
	}
	mode := "drain"
	maxRuntime := cacheMetadataImagesMaxRuntime
	startMessage := "Starting queued metadata image cache"
	if backfill {
		mode = "backfill"
		// A manual backfill must either reach the end of discovery or be
		// explicitly canceled. A scheduled drain is bounded because its next
		// trigger continues the durable queue; a manual-only task has no such
		// continuation and must not report a partial sweep as complete.
		maxRuntime = 0
		startMessage = "Starting full metadata image backfill"
	}
	// TaskManager prevents overlap for one task key, but drain and backfill are
	// intentionally separate tasks and may run together. Give every execution
	// a distinct lease owner so a stale worker can never finalize a job reclaimed
	// by the other task after its lease expires.
	workerID := fmt.Sprintf("%s:%s:%s", hostname, mode, uuid.NewString())
	progress.Report(0, startMessage)
	// Discovery widens the denominator mid-run, so the raw ratio can dip when a
	// sweep enqueues a fresh page. Reports are sequential, so a high-water mark
	// is enough to keep what the user sees from walking backwards.
	reportedPercent := 0.0
	stats, err := run(
		ctx,
		workerID,
		cacheMetadataImagesClaimLimit,
		cacheMetadataImagesWorkers,
		maxRuntime,
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
		operation := "caching queued metadata images"
		if backfill {
			operation = "backfilling metadata images"
		}
		return fmt.Errorf("%s: %w", operation, err)
	}
	message := fmt.Sprintf(
		"Batches %d, claimed %d, cached %d, %d %s, skipped %d, uploaded %d variants, found %d existing variants, deleted %d old successes",
		stats.Batches,
		stats.Claimed,
		stats.Succeeded,
		stats.Failed,
		imageCacheFailedAttemptLabel(stats.Failed),
		stats.Skipped,
		stats.UploadedVariants,
		stats.ExistingVariants,
		stats.DeletedSucceeded,
	)
	if backfill {
		message = fmt.Sprintf("Discovered %d existing, %s", stats.EnqueuedExisting, message)
	}
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
