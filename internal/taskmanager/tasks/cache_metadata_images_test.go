package tasks

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/Silo-Server/silo-server/internal/metadata"
)

type fakeMetadataImageCacheRunner struct {
	stats       metadata.ImageCacheRunStats
	updates     []metadata.ImageCacheRunStats
	err         error
	claimLimit  int
	concurrency int
	maxRuntime  time.Duration
	drainCalls  int
	backfills   int
	workerIDs   []string
}

func (f *fakeMetadataImageCacheRunner) run(claimLimit int, concurrency int, maxRuntime time.Duration, reportProgress metadata.ImageCacheRunProgressReporter) (metadata.ImageCacheRunStats, error) {
	f.claimLimit = claimLimit
	f.concurrency = concurrency
	f.maxRuntime = maxRuntime
	for _, update := range f.updates {
		reportProgress(update)
	}
	return f.stats, f.err
}

func (f *fakeMetadataImageCacheRunner) DrainUntilIdle(_ context.Context, workerID string, claimLimit int, concurrency int, maxRuntime time.Duration, reportProgress metadata.ImageCacheRunProgressReporter) (metadata.ImageCacheRunStats, error) {
	f.drainCalls++
	f.workerIDs = append(f.workerIDs, workerID)
	return f.run(claimLimit, concurrency, maxRuntime, reportProgress)
}

func (f *fakeMetadataImageCacheRunner) RunUntilIdle(_ context.Context, workerID string, claimLimit int, concurrency int, maxRuntime time.Duration, reportProgress metadata.ImageCacheRunProgressReporter) (metadata.ImageCacheRunStats, error) {
	f.backfills++
	f.workerIDs = append(f.workerIDs, workerID)
	return f.run(claimLimit, concurrency, maxRuntime, reportProgress)
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

func TestMetadataImageTasksUseDistinctExecutionLeaseOwners(t *testing.T) {
	runner := &fakeMetadataImageCacheRunner{}
	progress := &recordingProgress{}
	cacheTask := NewCacheMetadataImagesTask(runner)
	backfillTask := NewBackfillMetadataImagesTask(runner)
	for i := 0; i < 2; i++ {
		if err := cacheTask.Execute(context.Background(), progress); err != nil {
			t.Fatalf("cache Execute() call %d error = %v", i+1, err)
		}
		if err := backfillTask.Execute(context.Background(), progress); err != nil {
			t.Fatalf("backfill Execute() call %d error = %v", i+1, err)
		}
	}
	if len(runner.workerIDs) != 4 {
		t.Fatalf("worker IDs = %#v, want four execution-scoped lease owners", runner.workerIDs)
	}
	seen := make(map[string]struct{}, len(runner.workerIDs))
	for _, workerID := range runner.workerIDs {
		if _, duplicate := seen[workerID]; duplicate {
			t.Fatalf("duplicate worker ID %q in %#v", workerID, runner.workerIDs)
		}
		seen[workerID] = struct{}{}
		suffix := workerID[strings.LastIndex(workerID, ":")+1:]
		if _, err := uuid.Parse(suffix); err != nil {
			t.Fatalf("worker ID %q has invalid UUID suffix: %v", workerID, err)
		}
	}
}
