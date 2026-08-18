package tasks

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/metadata"
	"github.com/Silo-Server/silo-server/internal/taskmanager"
)

type fakeMetadataImageCacheRunner struct {
	stats       metadata.ImageCacheRunStats
	updates     []metadata.ImageCacheRunStats
	err         error
	claimLimit  int
	concurrency int
	maxRuntime  time.Duration
}

func (f *fakeMetadataImageCacheRunner) RunUntilIdle(_ context.Context, _ string, claimLimit int, concurrency int, maxRuntime time.Duration, reportProgress metadata.ImageCacheRunProgressReporter) (metadata.ImageCacheRunStats, error) {
	f.claimLimit = claimLimit
	f.concurrency = concurrency
	f.maxRuntime = maxRuntime
	for _, update := range f.updates {
		reportProgress(update)
	}
	return f.stats, f.err
}

type recordingProgress struct {
	percents []float64
	messages []string
}

func (r *recordingProgress) Report(percent float64, message string) {
	r.percents = append(r.percents, percent)
	r.messages = append(r.messages, message)
}

func (r *recordingProgress) SetResultData(json.RawMessage) {}

func TestCacheMetadataImagesTaskProperties(t *testing.T) {
	task := NewCacheMetadataImagesTask(&fakeMetadataImageCacheRunner{})
	if task.Key() != "cache_metadata_images" {
		t.Fatalf("Key() = %q", task.Key())
	}
	if task.Category() != taskmanager.TaskCategoryMetadata {
		t.Fatalf("Category() = %q", task.Category())
	}
	if len(task.DefaultTriggers()) != 2 {
		t.Fatalf("DefaultTriggers count = %d, want 2", len(task.DefaultTriggers()))
	}
}

func TestCacheMetadataImagesTaskReportsStats(t *testing.T) {
	runner := &fakeMetadataImageCacheRunner{
		updates: []metadata.ImageCacheRunStats{{
			Batches:   2,
			Claimed:   3,
			Succeeded: 2,
			Failed:    1,
			Queue: metadata.ImageCacheQueueProgress{
				Known: true, Total: 10, Succeeded: 6, Failed: 1, Queued: 2, Running: 1,
			},
		}},
		stats: metadata.ImageCacheRunStats{
			Batches:          3,
			EnqueuedExisting: 5,
			Claimed:          4,
			Succeeded:        3,
			Failed:           1,
			UploadedVariants: 7,
			ExistingVariants: 2,
		},
	}
	task := NewCacheMetadataImagesTask(runner)
	progress := &recordingProgress{}
	if err := task.Execute(context.Background(), progress); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if runner.claimLimit != 1000 {
		t.Fatalf("claimLimit = %d, want 1000", runner.claimLimit)
	}
	if runner.concurrency != 2 {
		t.Fatalf("concurrency = %d, want 2", runner.concurrency)
	}
	if runner.maxRuntime != 10*time.Minute {
		t.Fatalf("maxRuntime = %s, want 10m", runner.maxRuntime)
	}
	if len(progress.messages) != 3 {
		t.Fatalf("progress reports = %d, want 3", len(progress.messages))
	}
	if progress.messages[0] != "Starting metadata image cache" || progress.percents[0] != 0 {
		t.Fatalf("initial progress = %g %q", progress.percents[0], progress.messages[0])
	}
	if progress.messages[1] != "Processed 3 images across 2 batches (2 cached, 1 failed attempt, 0 skipped) · Overall 7/10 complete (1 terminal failure)" || progress.percents[1] != 70 {
		t.Fatalf("live progress = %g %q", progress.percents[1], progress.messages[1])
	}
	if progress.messages[2] != "Batches 3, enqueued 5 existing, claimed 4, cached 3, 1 failed attempt, skipped 0, uploaded 7 variants, found 2 existing variants, deleted 0 old successes" || progress.percents[2] != 100 {
		t.Fatalf("final progress = %g %q", progress.percents[2], progress.messages[2])
	}
}

func TestCacheMetadataImagesPercent(t *testing.T) {
	tests := []struct {
		name  string
		queue metadata.ImageCacheQueueProgress
		want  float64
	}{
		{name: "unknown", queue: metadata.ImageCacheQueueProgress{}, want: 0},
		{name: "known empty", queue: metadata.ImageCacheQueueProgress{Known: true}, want: 0},
		{name: "partial", queue: metadata.ImageCacheQueueProgress{Known: true, Total: 8, Succeeded: 5, Failed: 1}, want: 75},
		{name: "running completion is capped", queue: metadata.ImageCacheQueueProgress{Known: true, Total: 8, Succeeded: 7, Failed: 1}, want: 99.9},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := cacheMetadataImagesPercent(tt.queue); got != tt.want {
				t.Fatalf("cacheMetadataImagesPercent() = %g, want %g", got, tt.want)
			}
		})
	}
}
