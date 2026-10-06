package adminjob

import (
	"context"
	"encoding/json"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/blobstore"
	"github.com/Silo-Server/silo-server/internal/models"
)

func queueImageCacheCleanupJob(t *testing.T, r *Repository, prefixes []string) *models.AdminJob {
	t.Helper()
	job, err := r.Create(t.Context(), CreateJobInput{
		JobType:         JobTypeImageCacheCleanup,
		CreatedByUserID: lifecycleUser(t, r, "image-cache-cleanup"),
		RequestPayload:  ImageCacheCleanupRequest{LibraryID: 1, LibraryName: "Books", Prefixes: prefixes},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = r.pool.Exec(context.Background(), "DELETE FROM admin_jobs WHERE id=$1", job.ID) })
	return job
}

func imageCacheCleanupRunner(r *Repository, store blobstore.Store, slice time.Duration) *Runner {
	runner := NewRunner(NewRepository(r.pool), nil, nil, nil, nil, nil, NewImageCacheCleanupExecutor(store), nil, nil)
	runner.imageCacheCleanupSlice = slice
	return runner
}

func imageCacheCleanupResultOf(t *testing.T, job *models.AdminJob) ImageCacheCleanupResult {
	t.Helper()
	var result ImageCacheCleanupResult
	if err := json.Unmarshal(job.ResultPayload, &result); err != nil {
		t.Fatalf("result payload %s: %v", job.ResultPayload, err)
	}
	return result
}

// A cleanup that outlasts its slice records its position and goes back to the
// queue. A catalog job queued meanwhile is claimed before the cleanup resumes,
// and the resumed claim continues at the interrupted prefix.
func TestImageCacheCleanupYieldsToCatalogJobsAndResumes(t *testing.T) {
	r := lifecycleRepo(t)
	prefixes := cleanupPrefixes(8)
	job := queueImageCacheCleanupJob(t, r, prefixes)
	store := &cleanupStore{deleteFn: func(ctx context.Context, call int, _ string) (int, error) {
		if call == 3 { // the first attempt at prefix 3 outlasts the slice
			<-ctx.Done()
			return 0, ctx.Err()
		}
		return 1, nil
	}}
	runner := imageCacheCleanupRunner(r, store, 2*time.Second)

	runner.runNext()
	yielded, err := r.GetByID(t.Context(), job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if yielded.Status != StatusQueued || yielded.ProgressCurrent != 3 || yielded.ProgressTotal != 8 {
		t.Fatalf("after first slice: status=%s progress=%d/%d, want queued 3/8", yielded.Status, yielded.ProgressCurrent, yielded.ProgressTotal)
	}
	if got := imageCacheCleanupResultOf(t, yielded); got.DeletedPrefixes != 3 {
		t.Fatalf("checkpointed result %+v, want 3 deleted prefixes", got)
	}

	export, err := r.Create(t.Context(), CreateJobInput{JobType: JobTypeCatalogExport, CreatedByUserID: yielded.CreatedByUserID})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = r.pool.Exec(context.Background(), "DELETE FROM admin_jobs WHERE id=$1", export.ID) })
	runner.runNext()
	// The runner has no artifact store, so the export fails as soon as it is
	// claimed. Reaching a terminal state shows it was claimed first.
	if claimed, err := r.GetByID(t.Context(), export.ID); err != nil || claimed.Status != StatusFailed {
		t.Fatalf("catalog export after the yield: %v %+v", err, claimed)
	}
	if waiting, err := r.GetByID(t.Context(), job.ID); err != nil || waiting.Status != StatusQueued || waiting.ProgressCurrent != 3 {
		t.Fatalf("cleanup while the export ran: %v %+v", err, waiting)
	}

	runner.runNext()
	finished, err := r.GetByID(t.Context(), job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if finished.Status != StatusCompleted || finished.ProgressCurrent != 8 || finished.ProgressTotal != 8 {
		t.Fatalf("after resume: status=%s progress=%d/%d, want completed 8/8", finished.Status, finished.ProgressCurrent, finished.ProgressTotal)
	}
	if got := imageCacheCleanupResultOf(t, finished); got.DeletedPrefixes != 8 || got.DeletedS3Objects != 8 || got.LibraryName != "Books" {
		t.Fatalf("final result %+v, want 8 prefixes and 8 objects across both claims", got)
	}
	want := append(append([]string(nil), prefixes[:4]...), prefixes[3:]...)
	if calls := store.attempted(); !reflect.DeepEqual(calls, want) {
		t.Fatalf("attempted %v, want %v", calls, want)
	}
}

func stallUntilDone(ctx context.Context) (int, error) {
	<-ctx.Done()
	return 0, ctx.Err()
}

// A cleanup whose deletes keep hanging yields a bounded number of times and
// then fails, rather than requeueing the same prefix forever.
func TestImageCacheCleanupFailsAfterConsecutiveStalledClaims(t *testing.T) {
	r := lifecycleRepo(t)
	prefixes := cleanupPrefixes(3)
	job := queueImageCacheCleanupJob(t, r, prefixes)
	store := &cleanupStore{deleteFn: func(ctx context.Context, _ int, _ string) (int, error) { return stallUntilDone(ctx) }}
	runner := imageCacheCleanupRunner(r, store, 100*time.Millisecond)

	for claim := 1; claim < imageCacheCleanupMaxStalledClaims; claim++ {
		runner.runNext()
		queued, err := r.GetByID(t.Context(), job.ID)
		if err != nil {
			t.Fatal(err)
		}
		if queued.Status != StatusQueued || queued.ProgressCurrent != 0 || imageCacheCleanupResultOf(t, queued).StalledClaims != claim {
			t.Fatalf("after stalled claim %d: status=%s progress=%d result=%s", claim, queued.Status, queued.ProgressCurrent, queued.ResultPayload)
		}
	}
	runner.runNext()
	failed, err := r.GetByID(t.Context(), job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if failed.Status != StatusFailed || !strings.Contains(failed.ErrorMessage, "no cached image deleted") {
		t.Fatalf("status=%s error=%q, want failed with no progress", failed.Status, failed.ErrorMessage)
	}
	want := slices.Repeat(prefixes[:1], imageCacheCleanupMaxStalledClaims)
	if calls := store.attempted(); !reflect.DeepEqual(calls, want) {
		t.Fatalf("attempted %v, want only the first prefix once per claim", calls)
	}
}

// Stalls separated by progress do not add up: a long cleanup can stall
// several times over its life and still finish.
func TestImageCacheCleanupProgressResetsStalledClaims(t *testing.T) {
	const most = imageCacheCleanupMaxStalledClaims - 1
	r := lifecycleRepo(t)
	job := queueImageCacheCleanupJob(t, r, cleanupPrefixes(3))
	// Prefix 0 stalls for most claims and then finishes; prefix 1 then stalls
	// most times. Its first stall usually shares the claim that finished
	// prefix 0, but a slow progress write can push it into a claim of its
	// own, so neither prefix stalls enough alone to fail the job. Together
	// they would, unless progress resets the count.
	store := &cleanupStore{deleteFn: func(ctx context.Context, call int, _ string) (int, error) {
		if call < most || (call > most && call <= 2*most) {
			return stallUntilDone(ctx)
		}
		return 1, nil
	}}
	runner := imageCacheCleanupRunner(r, store, 150*time.Millisecond)

	var current *models.AdminJob
	for claims := 0; claims < 3*imageCacheCleanupMaxStalledClaims; claims++ {
		runner.runNext()
		var err error
		if current, err = r.GetByID(t.Context(), job.ID); err != nil {
			t.Fatal(err)
		}
		if current.Status != StatusQueued {
			break
		}
	}
	if current.Status != StatusCompleted {
		t.Fatalf("status=%s error=%q, want completed", current.Status, current.ErrorMessage)
	}
	if got := imageCacheCleanupResultOf(t, current); got.DeletedPrefixes != 3 || got.StalledClaims != 0 {
		t.Fatalf("final result %+v, want 3 prefixes and no stalled claims", got)
	}
}

// A prefix too large to delete within one slice keeps losing objects each
// claim. That is progress, so the job does not fail as stalled.
func TestImageCacheCleanupFinishesAPrefixThatOutlastsSlices(t *testing.T) {
	r := lifecycleRepo(t)
	job := queueImageCacheCleanupJob(t, r, cleanupPrefixes(3))
	claims := imageCacheCleanupMaxStalledClaims + 1
	store := &cleanupStore{deleteFn: func(ctx context.Context, call int, _ string) (int, error) {
		if call < claims { // prefix 0 is cut off after deleting one object
			<-ctx.Done()
			return 1, nil
		}
		return 1, nil
	}}
	runner := imageCacheCleanupRunner(r, store, 100*time.Millisecond)

	var current *models.AdminJob
	for claim := 0; claim <= claims; claim++ {
		runner.runNext()
		var err error
		if current, err = r.GetByID(t.Context(), job.ID); err != nil {
			t.Fatal(err)
		}
		if current.Status != StatusQueued {
			break
		}
	}
	if current.Status != StatusCompleted {
		t.Fatalf("status=%s error=%q, want completed", current.Status, current.ErrorMessage)
	}
	if got := imageCacheCleanupResultOf(t, current); got.DeletedPrefixes != 3 || got.DeletedS3Objects != claims+3 || got.StalledClaims != 0 {
		t.Fatalf("final result %+v, want 3 prefixes, %d objects and no stalled claims", got, claims+3)
	}
}

// A worker that stopped heartbeating partway through a large library leaves
// its progress on the row. Stale recovery requeues the job, and the next claim
// finishes the library from that position instead of starting over.
func TestRequeuedImageCacheCleanupFinishesALargeLibrary(t *testing.T) {
	const total, recorded = 189_821, 100_000
	r := lifecycleRepo(t)
	prefixes := cleanupPrefixes(total)
	job := queueImageCacheCleanupJob(t, r, prefixes)
	claimed, err := r.ClaimNextQueued(t.Context(), JobTypeImageCacheCleanup)
	if err != nil || claimed == nil || claimed.ID != job.ID {
		t.Fatalf("claim %v %+v", err, claimed)
	}
	earlier := ImageCacheCleanupResult{LibraryID: 1, LibraryName: "Books", DeletedPrefixes: recorded, DeletedS3Objects: recorded}
	if err := r.withClaim(claimed).UpdateProgressResult(t.Context(), job.ID, recorded, total, "Cleaning cached images", earlier); err != nil {
		t.Fatal(err)
	}
	if requeued, err := r.RequeueStaleRunning(t.Context(), time.Now().Add(time.Minute)); err != nil || requeued == 0 {
		t.Fatalf("stale requeue %d %v", requeued, err)
	}

	store := &cleanupStore{}
	runner := imageCacheCleanupRunner(r, store, time.Second)
	var current *models.AdminJob
	for claims := 0; claims < 100; claims++ {
		runner.runNext()
		if current, err = r.GetByID(t.Context(), job.ID); err != nil {
			t.Fatal(err)
		}
		if current.Status != StatusQueued {
			break
		}
	}
	if current.Status != StatusCompleted || current.ProgressCurrent != total {
		t.Fatalf("status=%s progress=%d/%d, want completed %d/%d", current.Status, current.ProgressCurrent, current.ProgressTotal, total, total)
	}
	if got := imageCacheCleanupResultOf(t, current); got.DeletedPrefixes != total || got.DeletedS3Objects != total {
		t.Fatalf("final result %+v, want %d prefixes and objects across all claims", got, total)
	}
	calls := store.attempted()
	if len(calls) == 0 || calls[0] != prefixes[recorded] {
		t.Fatalf("resumed at %v, want %s", calls[:min(len(calls), 1)], prefixes[recorded])
	}
	seen := make(map[string]bool, len(calls))
	for _, prefix := range calls {
		seen[prefix] = true
	}
	for i, prefix := range prefixes {
		if seen[prefix] != (i >= recorded) {
			t.Fatalf("prefix %d attempted=%v, want %v", i, seen[prefix], i >= recorded)
		}
	}
}

type finishingRefresh struct{ started chan struct{} }

func (e finishingRefresh) Execute(ctx context.Context, _ LibraryRefreshRequest, _ func(int, int, string)) (*LibraryRefreshResult, error) {
	close(e.started)
	<-ctx.Done()
	return &LibraryRefreshResult{LibraryID: 1}, nil
}

// An executor can finish after its context has ended. Its outcome must still
// be recorded: a write through the ended context fails, leaves the row
// running, and stale recovery would run the job again.
func TestJobOutcomeIsRecordedAfterItsContextEnds(t *testing.T) {
	r := lifecycleRepo(t)
	job, err := r.Create(t.Context(), CreateJobInput{
		JobType:         JobTypeLibraryRefresh,
		CreatedByUserID: lifecycleUser(t, r, "outcome-after-context"),
		RequestPayload:  LibraryRefreshRequest{LibraryID: 1},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = r.pool.Exec(context.Background(), "DELETE FROM admin_jobs WHERE id=$1", job.ID) })
	executor := finishingRefresh{started: make(chan struct{})}
	worker := NewRunner(NewRepository(r.pool), nil, nil, nil, executor, nil, nil, nil, nil)
	done := make(chan struct{})
	go func() { worker.runNext(); close(done) }()
	select {
	case <-executor.started:
	case <-time.After(5 * time.Second):
		t.Fatal("worker did not start")
	}
	// The v1 running-job cancel path: it cancels the context through the
	// registry without marking the row.
	if !worker.cancelRegistry.Cancel(job.ID) {
		t.Fatal("running refresh is not registered for cancellation")
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("worker did not finish")
	}
	terminal, err := r.GetByID(t.Context(), job.ID)
	if err != nil || terminal.Status != StatusCompleted {
		t.Fatalf("outcome %v %+v, want completed", err, terminal)
	}
}
