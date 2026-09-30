package tasks

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/Silo-Server/silo-server/internal/blobgc"
	"github.com/Silo-Server/silo-server/internal/taskmanager"
)

// orphanedMediaImagesMaxPages bounds each namespace's listing in one run, at
// a thousand objects a page.
const orphanedMediaImagesMaxPages = 5000

// OrphanedMediaImagesSweeper is the sweep surface. Satisfied by
// *blobgc.Sweeper.
type OrphanedMediaImagesSweeper interface {
	Sweep(ctx context.Context, maxPages int) (blobgc.SweepStats, error)
}

// SweepOrphanedMediaImagesTask finds images generated from media files whose
// rows are gone without their images having been queued for deletion, and
// queues them for Clean Removed Media Images.
type SweepOrphanedMediaImagesTask struct {
	sweeper OrphanedMediaImagesSweeper
}

func NewSweepOrphanedMediaImagesTask(sweeper OrphanedMediaImagesSweeper) *SweepOrphanedMediaImagesTask {
	return &SweepOrphanedMediaImagesTask{sweeper: sweeper}
}

func (t *SweepOrphanedMediaImagesTask) Key() string  { return "sweep_orphaned_media_images" }
func (t *SweepOrphanedMediaImagesTask) Name() string { return "Sweep Orphaned Media Images" }
func (t *SweepOrphanedMediaImagesTask) Description() string {
	return "Finds stored chapter thumbnails and seek previews that nothing references any more and schedules them for deletion."
}
func (t *SweepOrphanedMediaImagesTask) Category() taskmanager.TaskCategory {
	return taskmanager.TaskCategoryMetadata
}
func (t *SweepOrphanedMediaImagesTask) IsHidden() bool { return false }
func (t *SweepOrphanedMediaImagesTask) DefaultTriggers() []taskmanager.TriggerConfig {
	// Weekly: removals are queued as they happen, so the sweep only catches
	// what slipped past the queue.
	return []taskmanager.TriggerConfig{
		{Type: taskmanager.TriggerTypeInterval, IntervalMs: int64((7 * 24 * time.Hour) / time.Millisecond)},
	}
}

func (t *SweepOrphanedMediaImagesTask) Execute(ctx context.Context, progress taskmanager.ProgressReporter) error {
	if t.sweeper == nil {
		progress.Report(100, "Media image sweep is not configured")
		return nil
	}
	progress.Report(0, "Listing stored media images")
	stats, err := t.sweeper.Sweep(ctx, orphanedMediaImagesMaxPages)
	if data, marshalErr := json.Marshal(stats); marshalErr == nil {
		progress.SetResultData(data)
	}
	if err != nil {
		return fmt.Errorf("sweeping media images: %w", err)
	}
	if stats.Skipped {
		progress.Report(100, "Another server is already sweeping media images")
		return nil
	}
	var queued, objects int
	anomaly := false
	for _, ns := range stats.Namespaces {
		queued += ns.Queued
		objects += ns.Objects
		anomaly = anomaly || ns.StoppedOnAnomaly
	}
	message := fmt.Sprintf("Listed %d objects; scheduled %d orphaned prefixes for deletion", objects, queued)
	if anomaly {
		message += "; stopped in a namespace where most images looked orphaned"
	}
	progress.Report(100, message)
	return nil
}
