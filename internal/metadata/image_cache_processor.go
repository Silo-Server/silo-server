package metadata

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"

	"github.com/Silo-Server/silo-server/internal/models"
)

// maxLocalImageSourceBytes caps local sidecar artwork reads at 8 MiB,
// matching the NFO provider's discovery guard.
const maxLocalImageSourceBytes = 8 << 20

const (
	immediateImageCacheClaimLimit  = 16
	immediateImageCacheConcurrency = 3
	// Discovery is a read/enqueue page, not a processing lease. Keep it large
	// even though processing now claims only jobs that can start immediately.
	imageCacheDiscoveryBatchSize = 1000
	// Waiting for a background worker to release a job polls with backoff and
	// gives up after immediateImageCacheIdleTimeout without progress. The
	// worker's own lease runs for imageCacheLeaseDuration, and its pod can die
	// while holding it, so an interactive refresh must not wait that long.
	immediateImageCacheMinPoll     = 100 * time.Millisecond
	immediateImageCacheMaxPoll     = 2 * time.Second
	immediateImageCacheIdleTimeout = 30 * time.Second
)

// ErrTargetArtworkPending reports that some of a refreshed target's artwork was
// still held by a background worker when the interactive wait gave up. The
// metadata refresh itself is complete and the artwork finishes on the normal
// queue, so callers should surface this as a warning, not a failure.
var ErrTargetArtworkPending = errors.New("artwork caching is still running in the background")

// ErrImageCachingDisabled prevents an explicit manual backfill from being
// recorded as successfully complete when metadata image caching is disabled.
var ErrImageCachingDisabled = errors.New("metadata image caching is disabled")

type ImageCacheJobClaimer interface {
	ClaimDue(ctx context.Context, workerID string, limit int) ([]*models.MetadataImageCacheJob, error)
	MarkSucceeded(ctx context.Context, id int64, lockedBy string) error
	MarkFailed(ctx context.Context, id int64, attemptCount int, lockedBy string, errText string) error
	RequeueClaimed(ctx context.Context, ids []int64, workerID string) error
	CurrentTargetSourcePath(ctx context.Context, job *models.MetadataImageCacheJob) (string, error)
	EnqueueExistingProviderArtwork(ctx context.Context, limit int) (int, error)
	DeleteSucceededBefore(ctx context.Context, before time.Time, limit int) (int, error)
}

type targetImageCacheJobStore interface {
	retryTargetNow(ctx context.Context, targetContentID string) error
	claimDueForTarget(ctx context.Context, workerID, targetContentID string, limit int) ([]*models.MetadataImageCacheJob, error)
	targetHasRunningJobs(ctx context.Context, targetContentID string) (bool, error)
}

// imageCacheTargetCachedPathReader is optionally implemented by the job store
// (ImageCacheJobRepository does) to expose the target's currently stored
// cached path for the local-artwork unchanged-skip and stale-prefix cleanup.
type imageCacheTargetCachedPathReader interface {
	CurrentTargetCachedPath(ctx context.Context, job *models.MetadataImageCacheJob) (string, error)
}

// imageCacheBacklogReader is optional so lightweight processor fakes and
// alternate stores do not need a database-backed count implementation.
type imageCacheBacklogReader interface {
	GetBacklog(ctx context.Context) (ImageCacheBacklog, error)
}

// LibraryRootResolver reports the media folder root paths a piece of content
// belongs to (media_folders.Paths via media_item_libraries). The processor
// confines local file:// sources to these roots before reading them.
type LibraryRootResolver interface {
	LibraryRootsForContent(ctx context.Context, contentID string) ([]string, error)
}

// ImagePrefixDeleter deletes cached image objects under a key prefix; used to
// sweep the previous hashed local/ prefix after a successful re-cache.
type ImagePrefixDeleter interface {
	DeletePrefix(ctx context.Context, bucket, prefix string) (int, error)
	Bucket() string
}

type SeasonArtworkUpdater interface {
	UpdateArtworkIfSourceMatches(ctx context.Context, contentID, sourcePath, cachedPath, thumbhash string) (bool, error)
}

type EpisodeStillUpdater interface {
	UpdateStillIfSourceMatches(ctx context.Context, contentID, sourcePath, cachedPath, thumbhash string) (bool, error)
}

type ItemArtworkUpdater interface {
	UpdateArtworkIfSourceMatches(ctx context.Context, contentID, imageType, sourcePath, cachedPath, thumbhash string) (bool, error)
}

type ItemLocalizationArtworkUpdater interface {
	UpdateArtworkIfSourceMatches(ctx context.Context, contentID, language, imageType, sourcePath, cachedPath, thumbhash string) (bool, error)
}

type SeasonLocalizationArtworkUpdater interface {
	UpdateArtworkIfSourceMatches(ctx context.Context, contentID, language, sourcePath, cachedPath, thumbhash string) (bool, error)
}

type PersonPhotoUpdater interface {
	UpdatePhotoIfSourceMatches(ctx context.Context, personID int64, sourcePath, cachedPath, thumbhash string) (bool, error)
}

type ImageCacheProcessorTargets struct {
	Items               ItemArtworkUpdater
	Seasons             SeasonArtworkUpdater
	Episodes            EpisodeStillUpdater
	ItemLocalizations   ItemLocalizationArtworkUpdater
	SeasonLocalizations SeasonLocalizationArtworkUpdater
	People              PersonPhotoUpdater
}

type ImageCacheProcessor struct {
	jobs     ImageCacheJobClaimer
	cacher   ImageCacher
	resolver interface {
		ResolveImageURL(ctx context.Context, path string, variant string) string
	}
	targets ImageCacheProcessorTargets
	logger  *slog.Logger

	// Local file:// artwork support. libraryRoots confines reads to the
	// owning library's roots; prefixDeleter sweeps stale hashed prefixes.
	// The processor host must mount the libraries, like the metadata worker.
	libraryRoots  LibraryRootResolver
	prefixDeleter ImagePrefixDeleter

	enabled atomic.Bool

	// idleWaitTimeout bounds how long CacheTargetArtwork waits on jobs held by
	// a background worker. Zero means immediateImageCacheIdleTimeout.
	idleWaitTimeout time.Duration

	// runGate serializes the scheduled queue drain and explicit full backfill.
	// They are separate TaskManager tasks, so TaskManager's per-key guard cannot
	// prevent them from racing each other through the shared durable queue. A
	// channel gate keeps waiting cancellable, unlike a sync.Mutex.
	runGate chan struct{}
}

// SetLibraryRootResolver wires the folder repository used to confine local
// file:// sources to their owning library's roots. Without it, local jobs
// fail (and retry on the normal backoff) rather than reading arbitrary paths.
func (p *ImageCacheProcessor) SetLibraryRootResolver(resolver LibraryRootResolver) {
	if p == nil {
		return
	}
	p.libraryRoots = resolver
}

// SetImagePrefixDeleter wires the object store used to delete the previous
// hashed local/ prefix after a successful local re-cache. Optional: without
// it, stale prefixes are left behind (item deletion still sweeps them).
func (p *ImageCacheProcessor) SetImagePrefixDeleter(deleter ImagePrefixDeleter) {
	if p == nil {
		return
	}
	p.prefixDeleter = deleter
}

// SetEnabled toggles background caching. When disabled the processor begins no
// new discovery, claims, or uploads; a job already in flight may finish. This
// honors metadata.cache_images so merely configuring object storage does not
// download the whole catalog.
func (p *ImageCacheProcessor) SetEnabled(enabled bool) {
	if p == nil {
		return
	}
	p.enabled.Store(enabled)
}

func NewImageCacheProcessor(
	jobs ImageCacheJobClaimer,
	cacher ImageCacher,
	resolver interface {
		ResolveImageURL(ctx context.Context, path string, variant string) string
	},
	seasons SeasonArtworkUpdater,
	episodes EpisodeStillUpdater,
) *ImageCacheProcessor {
	return NewImageCacheProcessorWithTargets(jobs, cacher, resolver, ImageCacheProcessorTargets{
		Seasons:  seasons,
		Episodes: episodes,
	})
}

func NewImageCacheProcessorWithTargets(
	jobs ImageCacheJobClaimer,
	cacher ImageCacher,
	resolver interface {
		ResolveImageURL(ctx context.Context, path string, variant string) string
	},
	targets ImageCacheProcessorTargets,
) *ImageCacheProcessor {
	p := &ImageCacheProcessor{
		jobs:            jobs,
		cacher:          cacher,
		resolver:        resolver,
		targets:         targets,
		logger:          slog.Default(),
		idleWaitTimeout: immediateImageCacheIdleTimeout,
		runGate:         make(chan struct{}, 1),
	}
	p.runGate <- struct{}{}
	// Default to enabled; callers gate on metadata.cache_images via SetEnabled.
	p.enabled.Store(true)
	return p
}

type ImageCacheRunStats struct {
	Batches          int
	EnqueuedExisting int
	Claimed          int
	Succeeded        int
	Failed           int
	Skipped          int
	DeletedSucceeded int
	UploadedVariants int
	ExistingVariants int
	RuntimeLimited   bool

	// Backlog is the outstanding queue sampled once, when this run started, and
	// never moves during the run. It forms this execution's progress denominator
	// together with EnqueuedExisting, so retention deleting old rows underneath
	// the run cannot move reported progress.
	Backlog ImageCacheBacklog
}

// Processed reports how many jobs this run has finished with, in any terminal
// way.
func (s ImageCacheRunStats) Processed() int {
	return s.Succeeded + s.Failed + s.Skipped
}

// ImageCacheRunProgressReporter receives cumulative stats for the current
// execution after each queue or discovery batch.
type ImageCacheRunProgressReporter func(ImageCacheRunStats)

func (s *ImageCacheRunStats) add(other ImageCacheRunStats) {
	s.EnqueuedExisting += other.EnqueuedExisting
	s.Claimed += other.Claimed
	s.Succeeded += other.Succeeded
	s.Failed += other.Failed
	s.Skipped += other.Skipped
	s.DeletedSucceeded += other.DeletedSucceeded
	s.UploadedVariants += other.UploadedVariants
	s.ExistingVariants += other.ExistingVariants
}

// RunOnce claims and processes one batch of already-queued jobs. It does not
// run catalog discovery; callers choose between queue-only draining and an
// explicit full-catalog backfill.
func (p *ImageCacheProcessor) RunOnce(ctx context.Context, workerID string, claimLimit int, concurrency int) (ImageCacheRunStats, error) {
	var stats ImageCacheRunStats
	if p == nil || p.jobs == nil || p.cacher == nil || !p.enabled.Load() {
		return stats, nil
	}
	if claimLimit <= 0 {
		claimLimit = 100
	}
	if concurrency <= 0 {
		concurrency = 4
	}

	jobs, err := p.jobs.ClaimDue(ctx, workerID, claimLimit)
	if err != nil {
		return stats, err
	}
	if len(jobs) == 0 {
		p.cleanupSucceeded(ctx, &stats)
		return stats, nil
	}
	stats = p.processClaimedJobs(ctx, workerID, jobs, concurrency)
	p.cleanupSucceeded(ctx, &stats)
	return stats, nil
}

// ArtworkCachingEnabled reports whether the processor would actually cache
// anything right now. Callers use it to decide whether to advertise an artwork
// step at all: the processor is wired whenever object storage is configured,
// but metadata.cache_images can still have it turned off.
func (p *ImageCacheProcessor) ArtworkCachingEnabled() bool {
	return p != nil && p.jobs != nil && p.cacher != nil && p.enabled.Load()
}

// CacheTargetArtwork processes only the artwork queued for a manually
// refreshed item, and for a series also the artwork of its seasons, episodes,
// and localizations, which are queued under their own content IDs. It waits a
// bounded while for jobs a background worker already holds so the interactive
// refresh does not report success before the cached paths have been updated;
// past that it returns ErrTargetArtworkPending and lets the queue finish.
func (p *ImageCacheProcessor) CacheTargetArtwork(ctx context.Context, targetContentID string) error {
	if !p.ArtworkCachingEnabled() {
		return nil
	}
	targetContentID = strings.TrimSpace(targetContentID)
	if targetContentID == "" {
		return fmt.Errorf("target content ID is required")
	}
	store, ok := p.jobs.(targetImageCacheJobStore)
	if !ok {
		return fmt.Errorf("metadata image cache does not support targeted processing")
	}

	// Once, up front: the user asked for this item, so artwork deferred by an
	// earlier failure becomes due now. Repeating it on every poll would clear
	// backoff that background workers set on sibling jobs and would turn the
	// wait below into a write-heavy hot loop.
	if err := store.retryTargetNow(ctx, targetContentID); err != nil {
		return err
	}

	idleTimeout := p.idleWaitTimeout
	if idleTimeout <= 0 {
		idleTimeout = immediateImageCacheIdleTimeout
	}
	workerID := uuid.NewString()
	poll := immediateImageCacheMinPoll
	deadline := time.Now().Add(idleTimeout)
	failed := 0
	for {
		jobs, err := store.claimDueForTarget(ctx, workerID, targetContentID, immediateImageCacheClaimLimit)
		if err != nil {
			return err
		}
		if len(jobs) > 0 {
			stats := p.processClaimedJobs(ctx, workerID, jobs, immediateImageCacheConcurrency)
			// Failures are collected rather than returned: the remaining
			// artwork still gets cached, and a failed job carries its own
			// backoff so it is not reclaimed by the loop below.
			failed += stats.Failed
			poll = immediateImageCacheMinPoll
			deadline = time.Now().Add(idleTimeout)
			continue
		}

		running, err := store.targetHasRunningJobs(ctx, targetContentID)
		if err != nil {
			return err
		}
		if !running {
			break
		}
		if !time.Now().Before(deadline) {
			if failed > 0 {
				return fmt.Errorf("%d refreshed artwork image(s) failed to cache, and %w", failed, ErrTargetArtworkPending)
			}
			return ErrTargetArtworkPending
		}
		timer := time.NewTimer(poll)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
		poll = min(2*poll, immediateImageCacheMaxPoll)
	}
	if failed > 0 {
		return fmt.Errorf("%d refreshed artwork image(s) failed to cache", failed)
	}
	return nil
}

func (p *ImageCacheProcessor) processClaimedJobs(ctx context.Context, workerID string, jobs []*models.MetadataImageCacheJob, concurrency int) ImageCacheRunStats {
	stats := ImageCacheRunStats{Claimed: len(jobs)}
	if len(jobs) == 0 {
		return stats
	}
	if concurrency <= 0 {
		concurrency = 4
	}

	sem := make(chan struct{}, concurrency)
	var wg sync.WaitGroup
	var mu sync.Mutex
	var unstarted []int64
loop:
	for i, job := range jobs {
		if !p.enabled.Load() {
			for _, rem := range jobs[i:] {
				unstarted = append(unstarted, rem.ID)
			}
			break loop
		}
		// Acquire the semaphore before spawning so cancellation is observed here
		// rather than inside a goroutine that already holds a claimed job. Jobs we
		// never start are requeued below instead of being left locked until the
		// lease expires.
		select {
		case sem <- struct{}{}:
		case <-ctx.Done():
			for _, rem := range jobs[i:] {
				unstarted = append(unstarted, rem.ID)
			}
			break loop
		}
		if !p.enabled.Load() {
			<-sem
			for _, rem := range jobs[i:] {
				unstarted = append(unstarted, rem.ID)
			}
			break loop
		}
		wg.Add(1)
		go func(job *models.MetadataImageCacheJob) {
			defer wg.Done()
			defer func() { <-sem }()
			result := p.processOne(ctx, job)
			mu.Lock()
			switch result.outcome {
			case "succeeded":
				stats.Succeeded++
			case "skipped":
				stats.Skipped++
			default:
				stats.Failed++
			}
			stats.UploadedVariants += result.uploadedVariants
			stats.ExistingVariants += result.existingVariants
			mu.Unlock()
		}(job)
	}
	wg.Wait()

	if len(unstarted) > 0 {
		requeueCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		if err := p.jobs.RequeueClaimed(requeueCtx, unstarted, workerID); err != nil {
			p.logger.WarnContext(ctx, "metadata image cache: failed to requeue unstarted jobs", "count", len(unstarted), "error", err)
		}
		cancel()
	}

	return stats
}

// DrainUntilIdle processes only jobs that have already been queued by scans,
// metadata refreshes, or explicit artwork changes. It never performs the
// full-catalog discovery sweep, so it is safe for startup and interval tasks:
// an idle server stays idle instead of turning every scheduler tick into a
// library-wide backfill.
func (p *ImageCacheProcessor) DrainUntilIdle(ctx context.Context, workerID string, claimLimit int, concurrency int, maxRuntime time.Duration, reportProgress ImageCacheRunProgressReporter) (ImageCacheRunStats, error) {
	return p.runUntilIdle(ctx, workerID, claimLimit, concurrency, maxRuntime, false, reportProgress)
}

// RunUntilIdle drains the queue and explicitly discovers uncached provider
// artwork across the catalog. This is the manual backfill path; scheduled
// cache processing must use DrainUntilIdle.
func (p *ImageCacheProcessor) RunUntilIdle(ctx context.Context, workerID string, claimLimit int, concurrency int, maxRuntime time.Duration, reportProgress ImageCacheRunProgressReporter) (ImageCacheRunStats, error) {
	return p.runUntilIdle(ctx, workerID, claimLimit, concurrency, maxRuntime, true, reportProgress)
}

func (p *ImageCacheProcessor) runUntilIdle(ctx context.Context, workerID string, claimLimit int, concurrency int, maxRuntime time.Duration, discover bool, reportProgress ImageCacheRunProgressReporter) (ImageCacheRunStats, error) {
	var total ImageCacheRunStats
	if p == nil {
		return total, nil
	}
	select {
	case <-ctx.Done():
		return total, ctx.Err()
	case <-p.runGate:
	}
	defer func() { p.runGate <- struct{}{} }()
	if p.jobs == nil || p.cacher == nil {
		return total, nil
	}
	if !p.enabled.Load() {
		if discover {
			return total, ErrImageCachingDisabled
		}
		return total, nil
	}
	total.Backlog = p.sampleBacklog(ctx)
	reportImageCacheRunProgress(reportProgress, total)

	// A positive runtime bounds scheduled draining; zero or negative means the
	// explicit manual backfill keeps going until the catalog is exhausted or its
	// context is canceled.
	limited := maxRuntime > 0
	deadline := time.Time{}
	if limited {
		deadline = time.Now().Add(maxRuntime)
	}
	for {
		if err := ctx.Err(); err != nil {
			return total, err
		}
		if !p.enabled.Load() {
			if discover {
				return total, ErrImageCachingDisabled
			}
			return total, nil
		}
		if limited && !time.Now().Before(deadline) {
			total.RuntimeLimited = true
			return total, nil
		}

		stats, err := p.RunOnce(ctx, workerID, claimLimit, concurrency)
		total.Batches++
		total.add(stats)
		reportImageCacheRunProgress(reportProgress, total)
		if err != nil {
			return total, err
		}
		// SetEnabled may change while a claimed batch is in flight. Re-check
		// before looping or discovering so disabling caching cannot turn an
		// unbounded manual backfill into a rapid enqueue-only catalog sweep.
		if !p.enabled.Load() {
			if discover {
				return total, ErrImageCachingDisabled
			}
			return total, nil
		}
		if stats.Claimed > 0 {
			// Keep draining the queue before spending a full-table sweep.
			continue
		}
		if !discover {
			return total, nil
		}
		enqueued, err := p.jobs.EnqueueExistingProviderArtwork(ctx, imageCacheDiscoveryBatchSize)
		if err != nil {
			return total, err
		}
		total.EnqueuedExisting += enqueued
		reportImageCacheRunProgress(reportProgress, total)
		if enqueued == 0 {
			return total, nil
		}
	}
}

// sampleBacklog counts the outstanding queue once per run. It is deliberately
// not called per batch: the count is the run's fixed progress denominator, and
// repeating it would put an aggregate over the whole queue on the hot path.
func (p *ImageCacheProcessor) sampleBacklog(ctx context.Context) ImageCacheBacklog {
	reader, ok := p.jobs.(imageCacheBacklogReader)
	if !ok {
		return ImageCacheBacklog{}
	}
	backlog, err := reader.GetBacklog(ctx)
	if err != nil {
		p.logger.WarnContext(ctx, "metadata image cache: failed to count queue backlog", "error", err)
		return ImageCacheBacklog{}
	}
	return backlog
}

func reportImageCacheRunProgress(reportProgress ImageCacheRunProgressReporter, stats ImageCacheRunStats) {
	if reportProgress == nil {
		return
	}
	reportProgress(stats)
}

func (p *ImageCacheProcessor) cleanupSucceeded(ctx context.Context, stats *ImageCacheRunStats) {
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	deleted, err := p.jobs.DeleteSucceededBefore(cleanupCtx, time.Now().Add(-30*24*time.Hour), 1000)
	if err != nil {
		p.logger.WarnContext(ctx, "metadata image cache: failed to delete old succeeded jobs", "error", err)
	} else {
		stats.DeletedSucceeded = deleted
	}
}

func terminalJobContext(parent context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(parent), 10*time.Second)
}

func (p *ImageCacheProcessor) markFailed(parent context.Context, job *models.MetadataImageCacheJob, errText string) {
	writeCtx, cancel := terminalJobContext(parent)
	defer cancel()
	if err := p.jobs.MarkFailed(writeCtx, job.ID, job.AttemptCount, job.LockedBy, errText); err != nil {
		p.logger.WarnContext(parent, "metadata image cache: failed to mark job failed", "job_id", job.ID, "error", err)
	}
}

func (p *ImageCacheProcessor) markSucceeded(parent context.Context, job *models.MetadataImageCacheJob) {
	writeCtx, cancel := terminalJobContext(parent)
	defer cancel()
	if err := p.jobs.MarkSucceeded(writeCtx, job.ID, job.LockedBy); err != nil {
		p.logger.WarnContext(parent, "metadata image cache: failed to mark job succeeded", "job_id", job.ID, "error", err)
	}
}

type imageCacheProcessResult struct {
	outcome          string
	uploadedVariants int
	existingVariants int
}

func (p *ImageCacheProcessor) processOne(ctx context.Context, job *models.MetadataImageCacheJob) imageCacheProcessResult {
	if job == nil {
		return imageCacheProcessResult{outcome: "skipped"}
	}
	imageType, err := imageCacheJobImageType(job.ImageType)
	if err != nil {
		p.markFailed(ctx, job, err.Error())
		return imageCacheProcessResult{outcome: "failed"}
	}

	// Confirm the target still references this job's source before doing the
	// download and immutable upload. The conditional update below remains the
	// final concurrency guard; this early check avoids work for an obsolete job.
	current, err := p.jobs.CurrentTargetSourcePath(ctx, job)
	if err != nil {
		p.markFailed(ctx, job, err.Error())
		return imageCacheProcessResult{outcome: "failed"}
	}
	if current != job.SourcePath {
		p.markSucceeded(ctx, job)
		return imageCacheProcessResult{outcome: "skipped"}
	}

	if isLocalImageSourcePath(job.SourcePath) {
		return p.processLocalOne(ctx, job, imageType)
	}

	downloadURL := job.SourcePath
	if isProviderImagePath(downloadURL) {
		if p.resolver == nil {
			p.markFailed(ctx, job, "missing image resolver")
			return imageCacheProcessResult{outcome: "failed"}
		}
		downloadURL = p.resolver.ResolveImageURL(ctx, job.SourcePath, "original")
		if downloadURL == "" {
			p.markFailed(ctx, job, imageCacheEmptyResolvedURLError)
			return imageCacheProcessResult{outcome: "failed"}
		}
	}

	result, err := p.cacher.CacheImage(ctx, CacheImageRequest{
		SourceURL:     downloadURL,
		ProviderID:    job.ProviderID,
		ContentType:   job.ContentType,
		ContentID:     job.ProviderContentID,
		ImageType:     imageType,
		SeasonNumber:  job.SeasonNumber,
		EpisodeNumber: job.EpisodeNumber,
		Language:      job.TargetLanguage,
	})
	if err != nil {
		p.markFailed(ctx, job, err.Error())
		return imageCacheProcessResult{outcome: "failed"}
	}

	if result == nil {
		p.markFailed(ctx, job, "image cache returned no result")
		return imageCacheProcessResult{outcome: "failed"}
	}
	processResult := imageCacheProcessResult{
		uploadedVariants: result.UploadedVariants,
		existingVariants: result.ExistingVariants,
	}
	cachedPath := CachedImageOriginalPath(result)
	if cachedPath == "" {
		p.markFailed(ctx, job, "image cache returned empty stored path")
		processResult.outcome = "failed"
		return processResult
	}
	p.finishJobWithTargetUpdate(ctx, job, cachedPath, result.Thumbhash, &processResult)
	return processResult
}

// finishJobWithTargetUpdate persists the cached path/thumbhash to the job's
// target row and marks the job terminal, setting processResult.outcome.
func (p *ImageCacheProcessor) finishJobWithTargetUpdate(ctx context.Context, job *models.MetadataImageCacheJob, cachedPath, thumbhash string, processResult *imageCacheProcessResult) {
	updated, err := p.updateTargetArtwork(ctx, job, cachedPath, thumbhash)
	if err != nil {
		p.markFailed(ctx, job, err.Error())
		processResult.outcome = "failed"
		return
	}
	p.markSucceeded(ctx, job)
	if updated {
		processResult.outcome = "succeeded"
	} else {
		processResult.outcome = "skipped"
	}
}

func (p *ImageCacheProcessor) updateTargetArtwork(ctx context.Context, job *models.MetadataImageCacheJob, cachedPath, thumbhash string) (bool, error) {
	switch job.TargetType {
	case ImageCacheTargetItem:
		if p.targets.Items == nil {
			return false, errors.New("missing item updater")
		}
		return p.targets.Items.UpdateArtworkIfSourceMatches(ctx, job.TargetContentID, job.ImageType, job.SourcePath, cachedPath, thumbhash)
	case ImageCacheTargetItemLocalization:
		if p.targets.ItemLocalizations == nil {
			return false, errors.New("missing item localization updater")
		}
		return p.targets.ItemLocalizations.UpdateArtworkIfSourceMatches(ctx, job.TargetContentID, job.TargetLanguage, job.ImageType, job.SourcePath, cachedPath, thumbhash)
	case ImageCacheTargetSeason:
		if p.targets.Seasons == nil {
			return false, errors.New("missing season updater")
		}
		return p.targets.Seasons.UpdateArtworkIfSourceMatches(ctx, job.TargetContentID, job.SourcePath, cachedPath, thumbhash)
	case ImageCacheTargetSeasonLocalization:
		if p.targets.SeasonLocalizations == nil {
			return false, errors.New("missing season localization updater")
		}
		return p.targets.SeasonLocalizations.UpdateArtworkIfSourceMatches(ctx, job.TargetContentID, job.TargetLanguage, job.SourcePath, cachedPath, thumbhash)
	case ImageCacheTargetEpisode:
		if p.targets.Episodes == nil {
			return false, errors.New("missing episode updater")
		}
		return p.targets.Episodes.UpdateStillIfSourceMatches(ctx, job.TargetContentID, job.SourcePath, cachedPath, thumbhash)
	case ImageCacheTargetPerson:
		if p.targets.People == nil {
			return false, errors.New("missing person updater")
		}
		personID, parseErr := strconv.ParseInt(job.TargetContentID, 10, 64)
		if parseErr != nil {
			return false, fmt.Errorf("invalid person image cache target %q: %w", job.TargetContentID, parseErr)
		}
		return p.targets.People.UpdatePhotoIfSourceMatches(ctx, personID, job.SourcePath, cachedPath, thumbhash)
	default:
		return false, fmt.Errorf("unknown image cache target type %q", job.TargetType)
	}
}

// processLocalOne caches a local sidecar image (file:// source). The file is
// confined to the owning library's roots by a lexical check on the logical path
// followed by a symlink-resolving re-check (both path and roots are resolved, so
// scanner-recorded logical paths under symlinked roots stay valid while an
// intermediate directory symlink escaping a root is rejected), then read through
// an opened handle with fstat re-checks and pushed to S3 under a content-hashed
// local/ key.
func (p *ImageCacheProcessor) processLocalOne(ctx context.Context, job *models.MetadataImageCacheJob, imageType ImageType) imageCacheProcessResult {
	byteCacher, ok := p.cacher.(ImageByteCacher)
	if !ok {
		p.markFailed(ctx, job, "image cacher does not support local artwork")
		return imageCacheProcessResult{outcome: "failed"}
	}
	if p.libraryRoots == nil {
		p.markFailed(ctx, job, "missing library root resolver for local artwork")
		return imageCacheProcessResult{outcome: "failed"}
	}

	localPath := filepath.Clean(strings.TrimSpace(job.SourcePath)[len("file://"):])
	rootsContentID := firstNonEmpty(job.SeriesID, job.TargetContentID)
	roots, err := p.libraryRoots.LibraryRootsForContent(ctx, rootsContentID)
	if err != nil {
		p.markFailed(ctx, job, fmt.Sprintf("resolving library roots: %v", err))
		return imageCacheProcessResult{outcome: "failed"}
	}
	if !localImagePathWithinRoots(localPath, roots) {
		p.markFailed(ctx, job, "local image path outside library roots: "+localPath)
		return imageCacheProcessResult{outcome: "failed"}
	}
	// The lexical check above cannot see through symlinks. Resolve the path and
	// roots and re-confine so an intermediate directory symlink planted inside a
	// root (e.g. a link pointing out of the library) cannot pull an out-of-root
	// file into the cache. EvalSymlinks resolves both sides, so a legitimately
	// symlinked root still matches. This is a confinement GATE only: the read
	// below still uses the logical path so readLocalImageFile's Lstat keeps
	// rejecting a symlinked leaf. A not-yet-existent path (ErrNotExist) falls
	// through to the reader, which classifies it as the stable "missing" failure.
	if _, err := localImagePathResolvedWithinRoots(localPath, roots); err != nil && !errors.Is(err, fs.ErrNotExist) {
		p.markFailed(ctx, job, "local image path outside library roots: "+localPath)
		return imageCacheProcessResult{outcome: "failed"}
	}

	data, err := readLocalImageFile(localPath)
	if err != nil {
		p.markFailed(ctx, job, err.Error())
		return imageCacheProcessResult{outcome: "failed"}
	}

	// The target's current cached path drives the unchanged-skip (same hash →
	// same key → nothing to sweep) and the stale-prefix cleanup below.
	previousCachedPath := ""
	if reader, ok := p.jobs.(imageCacheTargetCachedPathReader); ok {
		previousCachedPath, err = reader.CurrentTargetCachedPath(ctx, job)
		if err != nil {
			p.logger.WarnContext(ctx, "metadata image cache: failed to read current cached path", "job_id", job.ID, "error", err)
			previousCachedPath = ""
		}
	}

	digest := sha256.Sum256(data)
	result, err := byteCacher.CacheImageBytes(ctx, data, CacheImageRequest{
		SourceURL:        job.SourcePath,
		ProviderID:       imageCacheLocalProviderID,
		ContentType:      job.ContentType,
		ContentID:        job.ProviderContentID,
		ImageType:        imageType,
		SeasonNumber:     job.SeasonNumber,
		EpisodeNumber:    job.EpisodeNumber,
		Language:         job.TargetLanguage,
		KeyDiscriminator: hex.EncodeToString(digest[:4]),
	})
	if err != nil {
		p.markFailed(ctx, job, err.Error())
		return imageCacheProcessResult{outcome: "failed"}
	}
	if result == nil {
		p.markFailed(ctx, job, "image cache returned no result")
		return imageCacheProcessResult{outcome: "failed"}
	}
	processResult := imageCacheProcessResult{
		uploadedVariants: result.UploadedVariants,
		existingVariants: result.ExistingVariants,
	}
	cachedPath := CachedImageOriginalPath(result)
	if cachedPath == "" {
		p.markFailed(ctx, job, "image cache returned empty stored path")
		processResult.outcome = "failed"
		return processResult
	}
	p.finishJobWithTargetUpdate(ctx, job, cachedPath, result.Thumbhash, &processResult)
	if processResult.outcome == "succeeded" {
		p.deleteStaleLocalPrefix(ctx, previousCachedPath, cachedPath)
	}
	return processResult
}

// deleteStaleLocalPrefix removes the previous hashed local/ image prefix
// after a re-cache stored the artwork under a different key.
func (p *ImageCacheProcessor) deleteStaleLocalPrefix(ctx context.Context, previousCachedPath, cachedPath string) {
	previousCachedPath = strings.TrimSpace(previousCachedPath)
	if p.prefixDeleter == nil ||
		previousCachedPath == "" ||
		previousCachedPath == cachedPath ||
		!strings.HasPrefix(previousCachedPath, "local/") {
		return
	}
	lastSlash := strings.LastIndex(previousCachedPath, "/")
	if lastSlash <= 0 {
		return
	}
	prefix := previousCachedPath[:lastSlash+1]
	if strings.HasPrefix(cachedPath, prefix) {
		return
	}
	if _, err := p.prefixDeleter.DeletePrefix(ctx, p.prefixDeleter.Bucket(), prefix); err != nil {
		p.logger.WarnContext(ctx, "metadata image cache: failed to delete stale local image prefix", "prefix", prefix, "error", err)
	}
}

// errLocalImageOutsideRoots is returned by localImagePathResolvedWithinRoots
// when a fully symlink-resolved path escapes every resolved library root.
var errLocalImageOutsideRoots = errors.New("local image path outside library roots")

// localImagePathResolvedWithinRoots resolves symlinks on both the path and the
// library roots and returns the real path when it stays within a real root.
// This closes the gap the lexical check leaves open — an intermediate directory
// symlink planted inside a root that points outside it. Resolving both sides
// keeps a legitimately symlinked root valid. A path that does not exist yet
// surfaces as fs.ErrNotExist for the caller to treat as a stable "missing"
// failure via the reader; any other resolution failure fails closed.
func localImagePathResolvedWithinRoots(path string, roots []string) (string, error) {
	resolvedPath, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", err
	}
	for _, root := range roots {
		resolvedRoot, err := filepath.EvalSymlinks(strings.TrimSpace(root))
		if err != nil {
			continue
		}
		if localImagePathWithinRoots(resolvedPath, []string{resolvedRoot}) {
			return resolvedPath, nil
		}
	}
	return "", errLocalImageOutsideRoots
}

// localImagePathWithinRoots confines path (already cleaned, logical) to the
// given library roots with a separator-aware lexical prefix check.
func localImagePathWithinRoots(path string, roots []string) bool {
	if !filepath.IsAbs(path) {
		return false
	}
	for _, root := range roots {
		root = filepath.Clean(strings.TrimSpace(root))
		if root == "" || root == "." || !filepath.IsAbs(root) {
			continue
		}
		if path == root {
			return true
		}
		prefix := root
		if prefix != string(filepath.Separator) {
			prefix += string(filepath.Separator)
		}
		if strings.HasPrefix(path, prefix) {
			return true
		}
	}
	return false
}

// readLocalImageFile reads a sidecar image with the same guards as discovery:
// Lstat rejects symlinked leaves and non-regular files, the opened handle is
// fstat-re-checked, and reads cap at maxLocalImageSourceBytes. ENOENT/EPERM
// map to the stable-failure texts matched by isStableProviderImageFailure.
func readLocalImageFile(path string) ([]byte, error) {
	classify := func(err error) error {
		switch {
		case errors.Is(err, fs.ErrNotExist):
			return fmt.Errorf("local image missing: %s", path)
		case errors.Is(err, fs.ErrPermission):
			return fmt.Errorf("local image forbidden: %s", path)
		default:
			return fmt.Errorf("local image read failed: %w", err)
		}
	}
	info, err := os.Lstat(path)
	if err != nil {
		return nil, classify(err)
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("local image is not a regular file: %s", path)
	}
	if info.Size() > maxLocalImageSourceBytes {
		return nil, fmt.Errorf("local image exceeds %d byte limit: %s", maxLocalImageSourceBytes, path)
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, classify(err)
	}
	defer func() { _ = file.Close() }()
	stat, err := file.Stat()
	if err != nil {
		return nil, classify(err)
	}
	// Close the symlink-swap window: os.Open follows symlinks, so a leaf swapped
	// to a symlink between the Lstat above and this Open would be followed to its
	// target. Reject unless the opened handle is the exact file Lstat inspected,
	// so an out-of-root target can never be pulled into the public cache.
	if !os.SameFile(info, stat) {
		return nil, fmt.Errorf("local image is not a regular file: %s", path)
	}
	if !stat.Mode().IsRegular() {
		return nil, fmt.Errorf("local image is not a regular file: %s", path)
	}
	if stat.Size() > maxLocalImageSourceBytes {
		return nil, fmt.Errorf("local image exceeds %d byte limit: %s", maxLocalImageSourceBytes, path)
	}
	data, err := io.ReadAll(io.LimitReader(file, maxLocalImageSourceBytes+1))
	if err != nil {
		return nil, classify(err)
	}
	if len(data) > maxLocalImageSourceBytes {
		return nil, fmt.Errorf("local image exceeds %d byte limit: %s", maxLocalImageSourceBytes, path)
	}
	if len(data) == 0 {
		return nil, fmt.Errorf("local image read failed: empty file %s", path)
	}
	return data, nil
}

func imageCacheJobImageType(value string) (ImageType, error) {
	switch value {
	case ImageCacheImagePoster:
		return ImagePoster, nil
	case ImageCacheImageBackdrop:
		return ImageBackdrop, nil
	case ImageCacheImageLogo:
		return ImageLogo, nil
	case ImageCacheImageStill:
		return ImageStill, nil
	case ImageCacheImageProfile:
		return ImageProfile, nil
	default:
		return ImagePoster, fmt.Errorf("unknown metadata image cache image type %q", value)
	}
}
