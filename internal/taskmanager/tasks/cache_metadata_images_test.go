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

func TestImageCacheWorkerCount(t *testing.T) {
	const gib = int64(1) << 30
	cases := []struct {
		configured int
		numCPU     int
		memory     int64
		want       int
	}{
		{numCPU: 0, memory: 0, want: 1}, // defensive floor; GOMAXPROCS never reports < 1
		{numCPU: 1, memory: 0, want: 1},
		{numCPU: 4, memory: 0, want: 4}, // one encode per core: vips runs single-threaded
		{numCPU: 12, memory: 0, want: 12},
		{numCPU: 48, memory: 0, want: 48},
		{numCPU: 64, memory: 0, want: 48}, // cap: more cores must not monopolize providers
		// A many-core container with a small memory limit is bounded by
		// memory, never below the original pool of 2 — a sub-1GiB deployment
		// already ran 2 workers before this change, so 2 is the floor, not 1.
		{numCPU: 16, memory: 256 << 20, want: 2},
		{numCPU: 16, memory: 512 << 20, want: 2},
		{numCPU: 16, memory: 1 * gib, want: 2},
		{numCPU: 16, memory: 2 * gib, want: 4},
		{numCPU: 16, memory: 8 * gib, want: 16},
		{numCPU: 16, memory: 64 * gib, want: 16},
		// Plenty of memory but few cores: CPU stays the binding cap.
		{numCPU: 2, memory: 64 * gib, want: 2},
		// An explicit setting replaces the CPU sizing, above or below it, and
		// may go all the way down to 1; the memory cap still applies.
		{configured: 3, numCPU: 16, memory: 0, want: 3},
		{configured: 64, numCPU: 4, memory: 0, want: 64},
		{configured: 1, numCPU: 16, memory: 0, want: 1},
		{configured: 1, numCPU: 16, memory: 256 << 20, want: 1},
		{configured: 32, numCPU: 16, memory: 2 * gib, want: 4},
	}
	for _, tc := range cases {
		if got := imageCacheWorkerCount(tc.configured, tc.numCPU, tc.memory); got != tc.want {
			t.Errorf("imageCacheWorkerCount(%d, %d, %d) = %d, want %d", tc.configured, tc.numCPU, tc.memory, got, tc.want)
		}
	}
	// A claimed page is stamped with one lease up front, so it must fully
	// drain before the lease expires or another worker reclaims the tail.
	// The job timeout cannot preempt the synchronous decode/encode segment,
	// so the timeout-based drain must leave real headroom under the lease
	// for that bounded overshoot — at least one extra timeout per job.
	drain := time.Duration(cacheMetadataImagesClaimPerWorker) * metadata.ImageCacheJobTimeout
	overshootBudget := time.Duration(cacheMetadataImagesClaimPerWorker) * metadata.ImageCacheJobTimeout / 2
	if drain+overshootBudget > metadata.ImageCacheLeaseDuration {
		t.Errorf("page drain %s plus overshoot budget %s must stay within the %s claim lease", drain, overshootBudget, metadata.ImageCacheLeaseDuration)
	}
	if got := cacheMetadataImagesClaimLimit(7); got != cacheMetadataImagesClaimPerWorker*7 {
		t.Errorf("claim limit = %d, want %d per worker (%d)", got, cacheMetadataImagesClaimPerWorker, cacheMetadataImagesClaimPerWorker*7)
	}
	// The admin setting flows through to the next run.
	SetImageWorkers(3)
	defer SetImageWorkers(0)
	if got := cacheMetadataImagesWorkers(); got != 3 && detectImageCacheMemoryBytes() >= 3*imageCacheWorkerMemoryBudget {
		t.Errorf("configured workers = %d, want 3", got)
	}
}

// Conflicting memory bounds resolve to the smallest positive one: GOMEMLIMIT
// can be set looser than the cgroup limit and the cgroup limit can sit above
// host memory, so no single source can be preferred outright.
func TestTightestMemoryLimit(t *testing.T) {
	const gib = int64(1) << 30
	cases := []struct {
		limits []int64
		want   int64
	}{
		{limits: []int64{4 * gib, 2 * gib, 8 * gib}, want: 2 * gib}, // smallest wins
		{limits: []int64{0, 2 * gib, 0}, want: 2 * gib},             // zeros mean "no bound", not zero bytes
		{limits: []int64{0, 0, 0}, want: 0},                         // nothing detectable
		{limits: []int64{6 * gib}, want: 6 * gib},
	}
	for _, tc := range cases {
		if got := tightestMemoryLimit(tc.limits...); got != tc.want {
			t.Errorf("tightestMemoryLimit(%v) = %d, want %d", tc.limits, got, tc.want)
		}
	}
}
