package tasks

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"runtime"
	"runtime/debug"
	"time"

	"github.com/google/uuid"

	"github.com/Silo-Server/silo-server/internal/metadata"
	"github.com/Silo-Server/silo-server/internal/nodemetrics"
	"github.com/Silo-Server/silo-server/internal/taskmanager"
)

const (
	cacheMetadataImagesIntervalMs = int64(60 * 1000)
	cacheMetadataImagesMaxRuntime = 10 * time.Minute
)

// imageCacheWorkerMemoryBudget is the memory reserved per concurrent image
// job when sizing the pool against a detected memory bound. A job can hold
// the compressed download (capped at 25 MiB in imagecache), the libvips
// working set for the variant ladder, and — today — a full Go-heap decode of
// the original for thumbhash, which for a large provider poster reaches the
// low hundreds of MiB. 512 MiB per worker keeps even a burst of worst-case
// originals from consuming the whole budget, since the baseline server also
// lives inside it.
const imageCacheWorkerMemoryBudget = 512 << 20

// imageCacheWorkerCount sizes the image-cache worker pool for the host. Each
// job downloads an original (30s timeout in imagecache) and runs a libvips
// WEBP encode ladder, so the work is a CPU/network mix: 4× the scheduler's
// CPU count keeps cores busy while other workers wait on downloads, and the
// cap keeps a many-core server from monopolizing provider connections. The
// previous fixed pool of 2 measured roughly 60 images/minute on a ~600k-item
// library; 48 workers measured roughly 2,900/minute over a 60-minute window
// (RXWatcher/silo-server@3b377f5c2).
//
// memoryBytes, when positive, is the tightest detected memory bound for this
// process (GOMEMLIMIT, cgroup limit, or system memory) and caps the pool at
// one worker per imageCacheWorkerMemoryBudget so a many-core container with a
// small memory limit cannot be OOM-killed by concurrent decodes.
//
// The floor of 2 is the pool size this task shipped with, and it deliberately
// overrides the per-worker budget below 2×imageCacheWorkerMemoryBudget: a
// sub-1GiB deployment ran 2 workers before this sizing existed, so the memory
// cap never reduces such a host below its long-standing baseline. The budget
// is a sizing heuristic for how far to scale up, not a reservation.
func imageCacheWorkerCount(numCPU int, memoryBytes int64) int {
	workers := min(48, 4*max(numCPU, 1))
	if memoryBytes > 0 {
		workers = min(workers, max(int(memoryBytes/imageCacheWorkerMemoryBudget), 2))
	}
	return workers
}

// detectImageCacheMemoryBytes returns the tightest memory bound the process
// can see, or 0 when none is detectable (macOS dev boxes, bare Linux without
// cgroups). Every source is consulted and the smallest wins, because none
// implies the others: GOMEMLIMIT can be set looser than a cgroup limit, and
// the effective cgroup limit — own cgroup, ancestors, and root, so a systemd
// MemoryMax= or an inherited pod/slice limit binds, not just a namespaced
// container's root files — can sit above or below host memory.
func detectImageCacheMemoryBytes() int64 {
	goLimit := int64(0)
	if limit := debug.SetMemoryLimit(-1); limit < math.MaxInt64 {
		goLimit = limit
	}
	hostTotal, _ := nodemetrics.ReadMeminfoTotalBytes("/proc/meminfo")
	return tightestMemoryLimit(goLimit, nodemetrics.EffectiveMemoryLimitBytes(), hostTotal)
}

// tightestMemoryLimit returns the smallest positive limit, or 0 when none is.
func tightestMemoryLimit(limits ...int64) int64 {
	tightest := int64(0)
	for _, limit := range limits {
		if limit > 0 && (tightest == 0 || limit < tightest) {
			tightest = limit
		}
	}
	return tightest
}

var cacheMetadataImagesWorkers = imageCacheWorkerCount(runtime.GOMAXPROCS(0), detectImageCacheMemoryBytes())

// cacheMetadataImagesClaimPerWorker sizes the queue page stamped with one
// lease up front. processClaimedJobs dispatches the page through a semaphore,
// so a page larger than the worker count keeps the pool saturated instead of
// waiting on every straggler in a worker-sized batch before the next page can
// be claimed.
//
// The page must drain inside metadata.ImageCacheLeaseDuration (15 minutes) or
// another worker reclaims the unstarted tail and duplicates it. Every job runs
// under metadata.ImageCacheJobTimeout (2 minutes), but that context cannot
// preempt the synchronous decode/encode segment (imageutil.Thumbhash,
// GenerateVariants take no context) — it only stops the job at the next
// context-aware step. That segment operates on inputs capped at 25 MiB
// (imagecache's download limit), so its overshoot is bounded by CPU speed,
// not by the network; 4 jobs per worker budgets the worst chain at
// 4 × (timeout + overshoot), which stays inside the lease with a generous
// overshoot allowance of nearly two minutes per job.
// TestImageCacheWorkerCount asserts the timeout part of this arithmetic
// against the exported constants.
const cacheMetadataImagesClaimPerWorker = 4

var cacheMetadataImagesClaimLimit = cacheMetadataImagesClaimPerWorker * cacheMetadataImagesWorkers

type MetadataImageCacheRunner interface {
	DrainUntilIdle(ctx context.Context, workerID string, claimLimit int, concurrency int, maxRuntime time.Duration, reportProgress metadata.ImageCacheRunProgressReporter) (metadata.ImageCacheRunStats, error)
}

// MetadataImageLadderBackfillRunner regenerates already-cached artwork against
// the current variant ladder. It is optional on the drain runner: a deployment
// whose runner does not implement it just never runs the one-shot pass.
type MetadataImageLadderBackfillRunner interface {
	RunLadderBackfill(ctx context.Context, workerID string, claimLimit int, concurrency int, maxRuntime time.Duration, reportProgress metadata.ImageCacheRunProgressReporter) (metadata.ImageCacheRunStats, bool, error)
}

// MetadataImageLadderBackfillState records which ladder version this deployment
// has finished backfilling and when a pass last ran. Satisfied by
// *metadata.ImageLadderBackfillStateRepository.
type MetadataImageLadderBackfillState interface {
	Get(ctx context.Context) (metadata.ImageLadderBackfillState, error)
	MarkAttempt(ctx context.Context) error
	ConfirmBackfilled(ctx context.Context, version int) (bool, error)
}

// ladderBackfillScanInterval paces the sweep. It cannot simply run on every
// scheduler tick: completion is measured against the artwork itself, so a
// deployment holding an image that can never be regenerated — a provider that
// 404s for good, a sidecar deleted off disk — never reaches "done", and its
// remainder check would otherwise scan the catalog once a minute forever.
const ladderBackfillScanInterval = 15 * time.Minute

type MetadataImageBackfillRunner interface {
	RunUntilIdle(ctx context.Context, workerID string, claimLimit int, concurrency int, maxRuntime time.Duration, reportProgress metadata.ImageCacheRunProgressReporter) (metadata.ImageCacheRunStats, error)
}

type CacheMetadataImagesTask struct {
	runner       MetadataImageCacheRunner
	ladderState  MetadataImageLadderBackfillState
	ladderTarget int
}

// SetLadderBackfill arms the one-shot artwork ladder backfill. Without it the
// task drains the queue exactly as before.
func (t *CacheMetadataImagesTask) SetLadderBackfill(state MetadataImageLadderBackfillState, targetVersion int) {
	t.ladderState = state
	t.ladderTarget = targetVersion
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

// drainPhaseCeiling is where the queue drain tops out when a ladder pass will
// follow it in the same execution. The two phases own disjoint, ascending
// stretches of the bar so it advances once from 0 to 100 rather than reaching
// 100 and restarting.
const drainPhaseCeiling = 50.0

func (t *CacheMetadataImagesTask) Execute(ctx context.Context, progress taskmanager.ProgressReporter) error {
	if t.runner == nil {
		progress.Report(100, "Metadata image cache is not configured")
		return nil
	}

	// Resolved before the drain so the drain knows whether it owns the whole bar
	// or only the first half of it.
	backfiller := t.pendingLadderBackfill(ctx, progress)

	drainProgress := progress
	if backfiller != nil {
		drainProgress = phaseProgress{inner: progress, start: 0, end: drainPhaseCeiling}
	}
	if err := executeMetadataImages(ctx, drainProgress, false, t.runner.DrainUntilIdle); err != nil {
		return err
	}
	if backfiller != nil {
		t.runLadderBackfill(ctx, phaseProgress{inner: progress, start: drainPhaseCeiling, end: 100}, backfiller)
	}
	return nil
}

// pendingLadderBackfill reports the ladder pass this execution should run, or
// nil when there is none: a runner without ladder support, no recorded state, or
// the current ladder version already backfilled.
func (t *CacheMetadataImagesTask) pendingLadderBackfill(ctx context.Context, progress taskmanager.ProgressReporter) MetadataImageLadderBackfillRunner {
	backfiller, ok := t.runner.(MetadataImageLadderBackfillRunner)
	if !ok || t.ladderState == nil || t.ladderTarget <= 0 {
		return nil
	}
	state, err := t.ladderState.Get(ctx)
	if err != nil {
		progress.Report(0, fmt.Sprintf("Artwork ladder backfill state unavailable: %v", err))
		return nil
	}
	if state.BackfilledVersion >= t.ladderTarget {
		return nil
	}
	if !state.LastAttemptAt.IsZero() && time.Since(state.LastAttemptAt) < ladderBackfillScanInterval {
		return nil
	}
	// Written before the pass, not after, so a crash mid-sweep still paces the
	// next one instead of letting every restart re-scan immediately.
	if err := t.ladderState.MarkAttempt(ctx); err != nil {
		progress.Report(0, fmt.Sprintf("Artwork ladder backfill could not be scheduled: %v", err))
		return nil
	}
	return backfiller
}

// phaseProgress maps one phase's own 0-100 reporting onto a stretch of the
// task's overall bar, so an execution that runs two phases advances the bar
// monotonically instead of each phase restarting it at zero.
type phaseProgress struct {
	inner taskmanager.ProgressReporter
	start float64
	end   float64
}

func (p phaseProgress) Report(percent float64, message string) {
	percent = min(max(percent, 0), 100)
	p.inner.Report(p.start+(p.end-p.start)*percent/100, message)
}

func (p phaseProgress) SetResultData(data json.RawMessage) { p.inner.SetResultData(data) }

// runLadderBackfill regenerates artwork cached under an older variant ladder,
// once per ladder version, after the ordinary queue is drained. Draining first
// keeps scan- and refresh-driven work — the artwork a user is waiting on — ahead
// of a sweep over images that already display, just narrower than requested.
//
// Failures are reported and swallowed: the drain above is this task's job, and
// an incomplete pass simply resumes on the next run. Only a pass that reached
// the end records the version.
func (t *CacheMetadataImagesTask) runLadderBackfill(
	ctx context.Context,
	progress taskmanager.ProgressReporter,
	backfiller MetadataImageLadderBackfillRunner,
) {
	hostname, _ := os.Hostname()
	if hostname == "" {
		hostname = "silo"
	}
	workerID := fmt.Sprintf("%s:ladder:%s", hostname, uuid.NewString())
	progress.Report(0, "Regenerating cached artwork for the current image size ladder")
	reportedPercent := 0.0

	stats, complete, err := backfiller.RunLadderBackfill(
		ctx,
		workerID,
		cacheMetadataImagesClaimLimit,
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
		progress.Report(100, fmt.Sprintf("Artwork ladder backfill interrupted after %d images: %v", stats.Succeeded, err))
		return
	}
	if !complete {
		progress.Report(100, fmt.Sprintf("Artwork ladder backfill in progress: %d regenerated so far", stats.Succeeded))
		return
	}
	confirmed, err := t.ladderState.ConfirmBackfilled(ctx, t.ladderTarget)
	if err != nil {
		progress.Report(100, fmt.Sprintf("Artwork ladder backfill finished but could not be recorded: %v", err))
		return
	}
	if !confirmed {
		progress.Report(100, "Artwork ladder backfill found new work during final confirmation; it will resume on the next run")
		return
	}
	progress.Report(100, fmt.Sprintf("Artwork ladder backfill complete: %d images regenerated", stats.Succeeded))
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
