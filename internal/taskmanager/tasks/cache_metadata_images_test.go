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
	err         error
	claimLimit  int
	concurrency int
	maxRuntime  time.Duration
}

func (f *fakeMetadataImageCacheRunner) RunUntilIdle(_ context.Context, _ string, claimLimit int, concurrency int, maxRuntime time.Duration) (metadata.ImageCacheRunStats, error) {
	f.claimLimit = claimLimit
	f.concurrency = concurrency
	f.maxRuntime = maxRuntime
	return f.stats, f.err
}

type recordingProgress struct {
	message string
}

func (r *recordingProgress) Report(_ float64, message string) {
	r.message = message
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
		stats: metadata.ImageCacheRunStats{
			Batches:          3,
			EnqueuedExisting: 5,
			Claimed:          4,
			Succeeded:        3,
			Failed:           1,
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
	if runner.concurrency != 12 {
		t.Fatalf("concurrency = %d, want 12", runner.concurrency)
	}
	if runner.maxRuntime != 10*time.Minute {
		t.Fatalf("maxRuntime = %s, want 10m", runner.maxRuntime)
	}
	if progress.message != "Batches 3, enqueued 5 existing, claimed 4, cached 3, failed 1, skipped 0, deleted 0 old successes" {
		t.Fatalf("progress message = %q", progress.message)
	}
}
