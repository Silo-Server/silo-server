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
	cacheMetadataImagesWorkers    = 12
	cacheMetadataImagesMaxRuntime = 10 * time.Minute
)

type MetadataImageCacheRunner interface {
	RunUntilIdle(ctx context.Context, workerID string, claimLimit int, concurrency int, maxRuntime time.Duration) (metadata.ImageCacheRunStats, error)
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
	stats, err := t.runner.RunUntilIdle(ctx, hostname, cacheMetadataImagesBatchSize, cacheMetadataImagesWorkers, cacheMetadataImagesMaxRuntime)
	if err != nil {
		return fmt.Errorf("caching metadata images: %w", err)
	}
	message := fmt.Sprintf(
		"Batches %d, enqueued %d existing, claimed %d, cached %d, failed %d, skipped %d, deleted %d old successes",
		stats.Batches,
		stats.EnqueuedExisting,
		stats.Claimed,
		stats.Succeeded,
		stats.Failed,
		stats.Skipped,
		stats.DeletedSucceeded,
	)
	if stats.RuntimeLimited {
		message += ", runtime budget reached"
	}
	progress.Report(100, message)
	return nil
}
