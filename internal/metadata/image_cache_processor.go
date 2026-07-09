package metadata

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Silo-Server/silo-server/internal/models"
)

// imageCacheDiscoveryInterval throttles the full-catalog backfill sweep so an
// idle installation does not re-scan every entity table on every task tick.
// Draining of already-queued jobs is unaffected and stays responsive.
const imageCacheDiscoveryInterval = 15 * time.Minute

type ImageCacheJobClaimer interface {
	ClaimDue(ctx context.Context, workerID string, limit int) ([]*models.MetadataImageCacheJob, error)
	MarkSucceeded(ctx context.Context, id int64, lockedBy string) error
	MarkFailed(ctx context.Context, id int64, attemptCount int, lockedBy string, errText string) error
	RequeueClaimed(ctx context.Context, ids []int64, workerID string) error
	CurrentTargetSourcePath(ctx context.Context, job *models.MetadataImageCacheJob) (string, error)
	EnqueueExistingProviderArtwork(ctx context.Context, limit int) (int, error)
	DeleteSucceededBefore(ctx context.Context, before time.Time, limit int) (int, error)
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

	enabled atomic.Bool

	discoveryInterval time.Duration
	discoveryMu       sync.Mutex
	lastDiscovery     time.Time
}

// SetEnabled toggles background caching. When disabled the processor performs
// no discovery, claiming, or uploading, honoring metadata.cache_images so that
// merely configuring object storage does not download the whole catalog.
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
		jobs:              jobs,
		cacher:            cacher,
		resolver:          resolver,
		targets:           targets,
		logger:            slog.Default(),
		discoveryInterval: imageCacheDiscoveryInterval,
	}
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
}

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
// run catalog discovery; callers (RunUntilIdle) drive discovery on a throttled
// cadence so backlog draining stays decoupled from full-table sweeps.
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
	stats.Claimed = len(jobs)
	if len(jobs) == 0 {
		p.cleanupSucceeded(ctx, &stats)
		return stats, nil
	}

	sem := make(chan struct{}, concurrency)
	var wg sync.WaitGroup
	var mu sync.Mutex
	var unstarted []int64
loop:
	for i, job := range jobs {
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

	p.cleanupSucceeded(ctx, &stats)
	if ctxErr := ctx.Err(); ctxErr != nil && stats.Claimed == 0 {
		return stats, ctxErr
	}
	return stats, nil
}

func (p *ImageCacheProcessor) RunUntilIdle(ctx context.Context, workerID string, claimLimit int, concurrency int, maxRuntime time.Duration) (ImageCacheRunStats, error) {
	var total ImageCacheRunStats
	if p == nil || p.jobs == nil || p.cacher == nil || !p.enabled.Load() {
		return total, nil
	}

	if maxRuntime <= 0 {
		enqueued, derr := p.discoverExisting(ctx, claimLimit)
		total.EnqueuedExisting += enqueued
		if derr != nil {
			return total, derr
		}
		stats, err := p.RunOnce(ctx, workerID, claimLimit, concurrency)
		total.add(stats)
		total.Batches = 1
		return total, err
	}

	// Decide once per run whether a full-catalog backfill sweep is due. Within a
	// due run we keep sweeping until the catalog is exhausted; otherwise we only
	// drain the existing queue.
	sweep := p.discoveryDue()
	deadline := time.Now().Add(maxRuntime)
	for {
		if err := ctx.Err(); err != nil {
			return total, err
		}
		if !time.Now().Before(deadline) {
			total.RuntimeLimited = true
			return total, nil
		}

		stats, err := p.RunOnce(ctx, workerID, claimLimit, concurrency)
		total.Batches++
		total.add(stats)
		if err != nil {
			return total, err
		}
		if stats.Claimed > 0 {
			// Keep draining the queue before spending a full-table sweep.
			continue
		}
		if !sweep {
			return total, nil
		}
		enqueued, err := p.jobs.EnqueueExistingProviderArtwork(ctx, claimLimit)
		if err != nil {
			return total, err
		}
		total.EnqueuedExisting += enqueued
		if enqueued == 0 {
			// Catalog fully swept; throttle the next sweep.
			p.markDiscovered()
			return total, nil
		}
	}
}

// discoveryDue reports whether enough time has elapsed since the last completed
// sweep to run another one.
func (p *ImageCacheProcessor) discoveryDue() bool {
	if p.discoveryInterval <= 0 {
		return true
	}
	p.discoveryMu.Lock()
	defer p.discoveryMu.Unlock()
	return p.lastDiscovery.IsZero() || time.Since(p.lastDiscovery) >= p.discoveryInterval
}

func (p *ImageCacheProcessor) markDiscovered() {
	p.discoveryMu.Lock()
	p.lastDiscovery = time.Now()
	p.discoveryMu.Unlock()
}

// discoverExisting runs an unthrottled sweep (single-pass path) and records the
// time so the throttle applies to subsequent interval-driven runs.
func (p *ImageCacheProcessor) discoverExisting(ctx context.Context, limit int) (int, error) {
	enqueued, err := p.jobs.EnqueueExistingProviderArtwork(ctx, limit)
	if err == nil {
		p.markDiscovered()
	}
	return enqueued, err
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

	// Confirm the target still references this job's source before uploading.
	// CacheImage writes to a deterministic, source-independent storage key, so a
	// stale job whose source an admin or newer refresh has already replaced would
	// otherwise overwrite the live artwork object even though the conditional DB
	// update later no-ops. Dropping the obsolete job here avoids that.
	current, err := p.jobs.CurrentTargetSourcePath(ctx, job)
	if err != nil {
		p.markFailed(ctx, job, err.Error())
		return imageCacheProcessResult{outcome: "failed"}
	}
	if current != job.SourcePath {
		p.markSucceeded(ctx, job)
		return imageCacheProcessResult{outcome: "skipped"}
	}

	downloadURL := job.SourcePath
	if isProviderImagePath(downloadURL) {
		if p.resolver == nil {
			p.markFailed(ctx, job, "missing image resolver")
			return imageCacheProcessResult{outcome: "failed"}
		}
		downloadURL = p.resolver.ResolveImageURL(ctx, job.SourcePath, "original")
		if downloadURL == "" {
			p.markFailed(ctx, job, "image resolver returned empty URL")
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
	cachedPath := cachedOriginalImagePath(result.BasePath, result.Ext)
	if cachedPath == "" {
		p.markFailed(ctx, job, "image cache returned empty stored path")
		processResult.outcome = "failed"
		return processResult
	}
	var updated bool
	switch job.TargetType {
	case ImageCacheTargetItem:
		if p.targets.Items == nil {
			p.markFailed(ctx, job, "missing item updater")
			processResult.outcome = "failed"
			return processResult
		}
		updated, err = p.targets.Items.UpdateArtworkIfSourceMatches(ctx, job.TargetContentID, job.ImageType, job.SourcePath, cachedPath, result.Thumbhash)
	case ImageCacheTargetItemLocalization:
		if p.targets.ItemLocalizations == nil {
			p.markFailed(ctx, job, "missing item localization updater")
			processResult.outcome = "failed"
			return processResult
		}
		updated, err = p.targets.ItemLocalizations.UpdateArtworkIfSourceMatches(ctx, job.TargetContentID, job.TargetLanguage, job.ImageType, job.SourcePath, cachedPath, result.Thumbhash)
	case ImageCacheTargetSeason:
		if p.targets.Seasons == nil {
			p.markFailed(ctx, job, "missing season updater")
			processResult.outcome = "failed"
			return processResult
		}
		updated, err = p.targets.Seasons.UpdateArtworkIfSourceMatches(ctx, job.TargetContentID, job.SourcePath, cachedPath, result.Thumbhash)
	case ImageCacheTargetSeasonLocalization:
		if p.targets.SeasonLocalizations == nil {
			p.markFailed(ctx, job, "missing season localization updater")
			processResult.outcome = "failed"
			return processResult
		}
		updated, err = p.targets.SeasonLocalizations.UpdateArtworkIfSourceMatches(ctx, job.TargetContentID, job.TargetLanguage, job.SourcePath, cachedPath, result.Thumbhash)
	case ImageCacheTargetEpisode:
		if p.targets.Episodes == nil {
			p.markFailed(ctx, job, "missing episode updater")
			processResult.outcome = "failed"
			return processResult
		}
		updated, err = p.targets.Episodes.UpdateStillIfSourceMatches(ctx, job.TargetContentID, job.SourcePath, cachedPath, result.Thumbhash)
	case ImageCacheTargetPerson:
		if p.targets.People == nil {
			p.markFailed(ctx, job, "missing person updater")
			processResult.outcome = "failed"
			return processResult
		}
		personID, parseErr := strconv.ParseInt(job.TargetContentID, 10, 64)
		if parseErr != nil {
			err = fmt.Errorf("invalid person image cache target %q: %w", job.TargetContentID, parseErr)
			break
		}
		updated, err = p.targets.People.UpdatePhotoIfSourceMatches(ctx, personID, job.SourcePath, cachedPath, result.Thumbhash)
	default:
		err = fmt.Errorf("unknown image cache target type %q", job.TargetType)
	}
	if err != nil {
		p.markFailed(ctx, job, err.Error())
		processResult.outcome = "failed"
		return processResult
	}
	if !updated {
		p.markSucceeded(ctx, job)
		processResult.outcome = "skipped"
		return processResult
	}
	p.markSucceeded(ctx, job)
	processResult.outcome = "succeeded"
	return processResult
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
