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
			Backlog:   metadata.ImageCacheBacklog{Known: true, Queued: 9, Running: 1},
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
	if progress.messages[1] != "Processed 3 images across 2 batches (2 cached, 1 failed attempt, 0 skipped) · 3 of 10 in this run's backlog" || progress.percents[1] != 30 {
		t.Fatalf("live progress = %g %q", progress.percents[1], progress.messages[1])
	}
	if progress.messages[2] != "Batches 3, enqueued 5 existing, claimed 4, cached 3, 1 failed attempt, skipped 0, uploaded 7 variants, found 2 existing variants, deleted 0 old successes" || progress.percents[2] != 100 {
		t.Fatalf("final progress = %g %q", progress.percents[2], progress.messages[2])
	}
}

func TestCacheMetadataImagesPercent(t *testing.T) {
	tests := []struct {
		name  string
		stats metadata.ImageCacheRunStats
		want  float64
	}{
		{
			name:  "unknown backlog stays indeterminate",
			stats: metadata.ImageCacheRunStats{Succeeded: 3},
			want:  0,
		},
		{
			name:  "idle run does not open near complete",
			stats: metadata.ImageCacheRunStats{Backlog: metadata.ImageCacheBacklog{Known: true}},
			want:  0,
		},
		{
			name: "progress is measured against this run's backlog",
			stats: metadata.ImageCacheRunStats{
				Succeeded: 5,
				Failed:    1,
				Backlog:   metadata.ImageCacheBacklog{Known: true, Queued: 8},
			},
			want: 75,
		},
		{
			name: "a running task stays short of complete",
			stats: metadata.ImageCacheRunStats{
				Succeeded: 7,
				Failed:    1,
				Backlog:   metadata.ImageCacheBacklog{Known: true, Queued: 8},
			},
			want: 99.9,
		},
		{
			name: "work discovered mid-run does not push progress past the cap",
			stats: metadata.ImageCacheRunStats{
				Succeeded: 20,
				Backlog:   metadata.ImageCacheBacklog{Known: true, Queued: 8},
			},
			want: 99.9,
		},
		{
			// A first run samples an empty backlog because discovery has not
			// swept yet. Measuring against discovered work is what keeps the
			// backfill — the run a user most wants a number for — from
			// reporting its first completed batch as the whole job.
			name: "a first-run backfill measures against discovered work",
			stats: metadata.ImageCacheRunStats{
				Succeeded:        250,
				EnqueuedExisting: 1000,
				Backlog:          metadata.ImageCacheBacklog{Known: true},
			},
			want: 25,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := cacheMetadataImagesPercent(tt.stats); got != tt.want {
				t.Fatalf("cacheMetadataImagesPercent() = %g, want %g", got, tt.want)
			}
		})
	}
}

// TestCacheMetadataImagesPercentIsMonotonicWithinARun guards the property the
// durable-queue percentage could not hold: the backlog denominator is fixed for
// the run, so discovery adding work and retention deleting rows cannot make the
// reported number fall.
func TestCacheMetadataImagesPercentIsMonotonicWithinARun(t *testing.T) {
	backlog := metadata.ImageCacheBacklog{Known: true, Queued: 100, Running: 4}
	previous := -1.0
	for processed := 0; processed <= 300; processed++ {
		got := cacheMetadataImagesPercent(metadata.ImageCacheRunStats{
			Succeeded: processed,
			Backlog:   backlog,
		})
		if got < previous {
			t.Fatalf("percent fell from %g to %g after %d processed", previous, got, processed)
		}
		if got > 99.9 {
			t.Fatalf("percent = %g after %d processed, want a running task below 100", got, processed)
		}
		previous = got
	}
	if previous == 0 {
		t.Fatal("percent never advanced")
	}
}

// TestCacheMetadataImagesTaskProgressDoesNotFallWhenDiscoveryWidensTheRun
// covers the seam between the two halves of the progress fix: counting
// discovered work keeps a backfill meaningful, but it also lets the raw ratio
// dip when a sweep enqueues a fresh page, so what the task reports is clamped
// to a high-water mark.
func TestCacheMetadataImagesTaskProgressDoesNotFallWhenDiscoveryWidensTheRun(t *testing.T) {
	runner := &fakeMetadataImageCacheRunner{
		updates: []metadata.ImageCacheRunStats{
			{Batches: 1, Succeeded: 40, EnqueuedExisting: 100, Backlog: metadata.ImageCacheBacklog{Known: true}},
			// Discovery doubles the known work: 60/200 is a lower ratio than 40/100.
			{Batches: 2, Succeeded: 60, EnqueuedExisting: 200, Backlog: metadata.ImageCacheBacklog{Known: true}},
			{Batches: 3, Succeeded: 150, EnqueuedExisting: 200, Backlog: metadata.ImageCacheBacklog{Known: true}},
		},
	}
	task := NewCacheMetadataImagesTask(runner)
	progress := &recordingProgress{}
	if err := task.Execute(context.Background(), progress); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	for i := 1; i < len(progress.percents); i++ {
		if progress.percents[i] < progress.percents[i-1] {
			t.Fatalf("progress fell from %g to %g at report %d", progress.percents[i-1], progress.percents[i], i)
		}
	}
	if progress.percents[1] != 40 {
		t.Fatalf("first live report = %g, want 40", progress.percents[1])
	}
	if progress.percents[2] != 40 {
		t.Fatalf("widened denominator reported %g, want the 40 high-water mark held", progress.percents[2])
	}
	if progress.percents[3] != 75 {
		t.Fatalf("recovered report = %g, want 75", progress.percents[3])
	}
}
