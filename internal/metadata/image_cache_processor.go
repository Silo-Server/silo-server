package metadata

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"sync"
	"time"

	"github.com/Silo-Server/silo-server/internal/models"
)

type ImageCacheJobClaimer interface {
	ClaimDue(ctx context.Context, workerID string, limit int) ([]*models.MetadataImageCacheJob, error)
	MarkSucceeded(ctx context.Context, id int64) error
	MarkFailed(ctx context.Context, id int64, attemptCount int, errText string) error
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
	return &ImageCacheProcessor{
		jobs:     jobs,
		cacher:   cacher,
		resolver: resolver,
		targets:  targets,
		logger:   slog.Default(),
	}
}

type ImageCacheRunStats struct {
	Batches          int
	EnqueuedExisting int
	Claimed          int
	Succeeded        int
	Failed           int
	Skipped          int
	DeletedSucceeded int
	RuntimeLimited   bool
}

func (s *ImageCacheRunStats) add(other ImageCacheRunStats) {
	s.EnqueuedExisting += other.EnqueuedExisting
	s.Claimed += other.Claimed
	s.Succeeded += other.Succeeded
	s.Failed += other.Failed
	s.Skipped += other.Skipped
	s.DeletedSucceeded += other.DeletedSucceeded
}

func (p *ImageCacheProcessor) RunOnce(ctx context.Context, workerID string, claimLimit int, concurrency int) (ImageCacheRunStats, error) {
	var stats ImageCacheRunStats
	if p == nil || p.jobs == nil || p.cacher == nil {
		return stats, nil
	}
	if claimLimit <= 0 {
		claimLimit = 100
	}
	if concurrency <= 0 {
		concurrency = 4
	}

	enqueued, err := p.jobs.EnqueueExistingProviderArtwork(ctx, claimLimit)
	if err != nil {
		return stats, err
	}
	stats.EnqueuedExisting = enqueued

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
	for _, job := range jobs {
		job := job
		wg.Add(1)
		go func() {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
				defer func() { <-sem }()
			case <-ctx.Done():
				return
			}
			outcome := p.processOne(ctx, job)
			mu.Lock()
			switch outcome {
			case "succeeded":
				stats.Succeeded++
			case "skipped":
				stats.Skipped++
			default:
				stats.Failed++
			}
			mu.Unlock()
		}()
	}
	wg.Wait()

	p.cleanupSucceeded(ctx, &stats)
	if ctxErr := ctx.Err(); ctxErr != nil && stats.Claimed == 0 {
		return stats, ctxErr
	}
	return stats, nil
}

func (p *ImageCacheProcessor) RunUntilIdle(ctx context.Context, workerID string, claimLimit int, concurrency int, maxRuntime time.Duration) (ImageCacheRunStats, error) {
	var total ImageCacheRunStats
	if maxRuntime <= 0 {
		stats, err := p.RunOnce(ctx, workerID, claimLimit, concurrency)
		stats.Batches = 1
		return stats, err
	}

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
		if stats.EnqueuedExisting == 0 && stats.Claimed == 0 {
			return total, nil
		}
	}
}

func (p *ImageCacheProcessor) cleanupSucceeded(ctx context.Context, stats *ImageCacheRunStats) {
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	deleted, err := p.jobs.DeleteSucceededBefore(cleanupCtx, time.Now().Add(-30*24*time.Hour), 1000)
	if err != nil {
		p.logger.Warn("metadata image cache: failed to delete old succeeded jobs", "error", err)
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
	if err := p.jobs.MarkFailed(writeCtx, job.ID, job.AttemptCount, errText); err != nil {
		p.logger.Warn("metadata image cache: failed to mark job failed", "job_id", job.ID, "error", err)
	}
}

func (p *ImageCacheProcessor) markSucceeded(parent context.Context, job *models.MetadataImageCacheJob) {
	writeCtx, cancel := terminalJobContext(parent)
	defer cancel()
	if err := p.jobs.MarkSucceeded(writeCtx, job.ID); err != nil {
		p.logger.Warn("metadata image cache: failed to mark job succeeded", "job_id", job.ID, "error", err)
	}
}

func (p *ImageCacheProcessor) processOne(ctx context.Context, job *models.MetadataImageCacheJob) string {
	if job == nil {
		return "skipped"
	}
	imageType, err := imageCacheJobImageType(job.ImageType)
	if err != nil {
		p.markFailed(ctx, job, err.Error())
		return "failed"
	}
	downloadURL := job.SourcePath
	if isProviderImagePath(downloadURL) {
		if p.resolver == nil {
			p.markFailed(ctx, job, "missing image resolver")
			return "failed"
		}
		downloadURL = p.resolver.ResolveImageURL(ctx, job.SourcePath, "original")
		if downloadURL == "" {
			p.markFailed(ctx, job, "image resolver returned empty URL")
			return "failed"
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
		return "failed"
	}

	if result == nil {
		p.markFailed(ctx, job, "image cache returned no result")
		return "failed"
	}
	cachedPath := cachedOriginalImagePath(result.BasePath, result.Ext)
	if cachedPath == "" {
		p.markFailed(ctx, job, "image cache returned empty stored path")
		return "failed"
	}
	var updated bool
	switch job.TargetType {
	case ImageCacheTargetItem:
		if p.targets.Items == nil {
			p.markFailed(ctx, job, "missing item updater")
			return "failed"
		}
		updated, err = p.targets.Items.UpdateArtworkIfSourceMatches(ctx, job.TargetContentID, job.ImageType, job.SourcePath, cachedPath, result.Thumbhash)
	case ImageCacheTargetItemLocalization:
		if p.targets.ItemLocalizations == nil {
			p.markFailed(ctx, job, "missing item localization updater")
			return "failed"
		}
		updated, err = p.targets.ItemLocalizations.UpdateArtworkIfSourceMatches(ctx, job.TargetContentID, job.TargetLanguage, job.ImageType, job.SourcePath, cachedPath, result.Thumbhash)
	case ImageCacheTargetSeason:
		if p.targets.Seasons == nil {
			p.markFailed(ctx, job, "missing season updater")
			return "failed"
		}
		updated, err = p.targets.Seasons.UpdateArtworkIfSourceMatches(ctx, job.TargetContentID, job.SourcePath, cachedPath, result.Thumbhash)
	case ImageCacheTargetSeasonLocalization:
		if p.targets.SeasonLocalizations == nil {
			p.markFailed(ctx, job, "missing season localization updater")
			return "failed"
		}
		updated, err = p.targets.SeasonLocalizations.UpdateArtworkIfSourceMatches(ctx, job.TargetContentID, job.TargetLanguage, job.SourcePath, cachedPath, result.Thumbhash)
	case ImageCacheTargetEpisode:
		if p.targets.Episodes == nil {
			p.markFailed(ctx, job, "missing episode updater")
			return "failed"
		}
		updated, err = p.targets.Episodes.UpdateStillIfSourceMatches(ctx, job.TargetContentID, job.SourcePath, cachedPath, result.Thumbhash)
	case ImageCacheTargetPerson:
		if p.targets.People == nil {
			p.markFailed(ctx, job, "missing person updater")
			return "failed"
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
		return "failed"
	}
	if !updated {
		p.markSucceeded(ctx, job)
		return "skipped"
	}
	p.markSucceeded(ctx, job)
	return "succeeded"
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
