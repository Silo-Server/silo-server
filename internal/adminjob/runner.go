package adminjob

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/Silo-Server/silo-server/internal/artworkmetrics"
	"github.com/Silo-Server/silo-server/internal/catalogseed"
	"github.com/Silo-Server/silo-server/internal/metadata"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/notifications"
)

type ArtifactStore interface {
	Bucket() string
	GetObject(ctx context.Context, bucket, key string) ([]byte, error)
	UploadFile(ctx context.Context, bucket, key, path, contentType string) (int64, error)
	DeleteObject(ctx context.Context, bucket, key string) error
}

const remoteCatalogImportTimeout = 10 * time.Minute

// Maximum wall-clock time a single admin job execution may run before its
// context is cancelled. This is the safety net that prevents a hung
// operation (e.g. an unreachable S3 endpoint) from blocking the job queue
// indefinitely, while still giving large jobs a budget that matches their
// actual scope.
const (
	deleteLibraryTimeout       = 2 * time.Hour
	imageCacheCleanupTimeout   = 2 * time.Hour
	libraryRefreshTimeout      = 6 * time.Hour
	templateBundleApplyTimeout = 2 * time.Hour
	artworkStorageTimeout      = 6 * time.Hour
	jobTimeoutLong             = 2 * time.Hour // catalog_export, catalog_import
)

type Runner struct {
	repo                *Repository
	exporter            *catalogseed.Service
	store               ArtifactStore
	itemRefresh         itemRefreshExecutor
	libraryRefresh      libraryRefreshExecutor
	libraryDelete       deleteLibraryExecutor
	imageCacheCleanup   imageCacheCleanupExecutor
	templateBundleApply templateBundleApplyExecutor
	artworkStorage      *metadata.ArtworkStorageService
	artworkPurge        artworkPurgeExecutor
	realtimeHub         *notifications.Hub
	pollInterval        time.Duration
	cleanupInterval     time.Duration
	heartbeatInterval   time.Duration
	staleAfter          time.Duration
	retention           time.Duration
	cancelRegistry      *CancelRegistry
	stop                chan struct{}
	stopOnce            sync.Once
}

func (r *Runner) SetArtworkStorageExecutors(storage *metadata.ArtworkStorageService, purge artworkPurgeExecutor) {
	if r == nil {
		return
	}
	r.artworkStorage = storage
	r.artworkPurge = purge
}

type itemRefreshExecutor interface {
	Execute(ctx context.Context, req ItemRefreshRequest, progress func(current, total int, message string)) (*ItemRefreshResult, error)
}

type libraryRefreshExecutor interface {
	Execute(ctx context.Context, req LibraryRefreshRequest, progress func(current, total int, message string)) (*LibraryRefreshResult, error)
}

type templateBundleApplyExecutor interface {
	ExecuteTemplateBundleApply(ctx context.Context, req TemplateBundleApplyRequest, progress func(current, total int, message string)) (any, error)
}

func NewRunner(
	repo *Repository,
	exporter *catalogseed.Service,
	store ArtifactStore,
	itemRefresh itemRefreshExecutor,
	libraryRefresh libraryRefreshExecutor,
	libraryDelete deleteLibraryExecutor,
	imageCacheCleanup imageCacheCleanupExecutor,
	templateBundleApply templateBundleApplyExecutor,
	realtimeHub *notifications.Hub,
) *Runner {
	return &Runner{
		repo:                repo,
		exporter:            exporter,
		store:               store,
		itemRefresh:         itemRefresh,
		libraryRefresh:      libraryRefresh,
		libraryDelete:       libraryDelete,
		imageCacheCleanup:   imageCacheCleanup,
		templateBundleApply: templateBundleApply,
		realtimeHub:         realtimeHub,
		pollInterval:        5 * time.Second,
		cleanupInterval:     time.Hour,
		heartbeatInterval:   10 * time.Second,
		staleAfter:          2 * time.Minute,
		retention:           7 * 24 * time.Hour,
		cancelRegistry:      NewCancelRegistry(),
		stop:                make(chan struct{}),
	}
}

func (r *Runner) SetCancelRegistry(registry *CancelRegistry) {
	if registry != nil {
		r.cancelRegistry = registry
	}
}

func (r *Runner) Start() {
	go func() {
		r.requeueStaleJobs()

		pollTicker := time.NewTicker(r.pollInterval)
		cleanupTicker := time.NewTicker(r.cleanupInterval)
		defer pollTicker.Stop()
		defer cleanupTicker.Stop()

		for {
			select {
			case <-r.stop:
				return
			case <-pollTicker.C:
				r.runNext()
			case <-cleanupTicker.C:
				r.cleanupExpired()
			}
		}
	}()
}

func (r *Runner) Stop() {
	r.stopOnce.Do(func() {
		close(r.stop)
	})
}

func (r *Runner) runNext() {
	r.requeueStaleJobs()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	job, err := r.repo.ClaimNextQueuedByTypes(ctx, []string{
		JobTypeCatalogImport,
		JobTypeCatalogExport,
		JobTypeItemRefresh,
		JobTypeLibraryRefresh,
		JobTypeDeleteLibrary,
		JobTypeTemplateBundleApply,
		JobTypeArtworkStorageRefresh,
		JobTypeArtworkPurge,
		JobTypeArtworkStorageImport,
	})
	cancel()
	if err != nil {
		slog.Warn("admin jobs: failed to claim next catalog job", "error", err)
		return
	}
	if job == nil {
		ctx, cancel = context.WithTimeout(context.Background(), 30*time.Second)
		job, err = r.repo.ClaimNextQueued(ctx, JobTypeImageCacheCleanup)
		cancel()
		if err != nil {
			slog.Warn("admin jobs: failed to claim next image cache cleanup job", "error", err)
			return
		}
	}
	if job == nil {
		return
	}
	r.publishJob(context.Background(), notifications.TypeJobProgress, job)

	switch job.JobType {
	case JobTypeCatalogImport:
		r.executeCatalogImport(job)
	case JobTypeCatalogExport:
		r.executeCatalogExport(job)
	case JobTypeItemRefresh:
		r.executeItemRefresh(job)
	case JobTypeLibraryRefresh:
		r.executeLibraryRefresh(job)
	case JobTypeDeleteLibrary:
		r.executeDeleteLibrary(job)
	case JobTypeTemplateBundleApply:
		r.executeTemplateBundleApply(job)
	case JobTypeImageCacheCleanup:
		r.executeImageCacheCleanup(job)
	case JobTypeArtworkStorageRefresh:
		r.executeArtworkStorageRefresh(job)
	case JobTypeArtworkPurge:
		r.executeArtworkPurge(job)
	case JobTypeArtworkStorageImport:
		r.executeArtworkStorageImport(job)
	default:
		r.failJob(job.ID, 0, 0, "Admin job failed", "unsupported admin job type")
	}
}

func (r *Runner) executeArtworkStorageImport(job *models.AdminJob) {
	if r.artworkStorage == nil {
		r.failJob(job.ID, 0, 0, "Artwork store import failed", "artwork storage executor is not configured")
		return
	}
	checkpoint, err := decodeArtworkInventoryCheckpoint(job.Checkpoint)
	if err != nil {
		r.failJob(job.ID, 0, 0, "Artwork store import failed", err.Error())
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), artworkStorageTimeout)
	defer cancel()
	heartbeatStop := make(chan struct{})
	go r.heartbeatLoop(ctx, job.ID, heartbeatStop)
	defer close(heartbeatStop)
	progress := func(current, total int, message string) {
		if err := r.repo.UpdateProgress(ctx, job.ID, current, total, message); err == nil {
			r.publishJobByID(ctx, notifications.TypeJobProgress, job.ID)
		}
	}
	result, err := r.artworkStorage.ImportPortable(ctx, checkpoint, func(next metadata.ArtworkInventoryCheckpoint) error {
		return r.repo.UpdateCheckpoint(ctx, job.ID, next)
	}, progress)
	if err != nil {
		r.failJob(job.ID, 0, 0, "Artwork store import failed", artworkStorageErrorMessage(ctx, err))
		return
	}
	if err := r.repo.Complete(ctx, job.ID, CompleteJobInput{
		ResultPayload: result, Message: "Portable artwork store imported",
		ProgressCurrent: int(result.ImportedSeeds + result.AdoptedLive + result.RetainedUnverifiable),
		ProgressTotal:   int(result.ImportedSeeds + result.AdoptedLive + result.RetainedUnverifiable), ExpiresAt: time.Now().UTC().Add(r.retention),
	}); err != nil {
		slog.Warn("admin jobs: failed to complete artwork store import", "job_id", job.ID, "error", err)
		return
	}
	r.publishJobByID(ctx, notifications.TypeJobCompleted, job.ID)
}

func (r *Runner) executeArtworkStorageRefresh(job *models.AdminJob) {
	if r.artworkStorage == nil {
		r.failJob(job.ID, 0, 0, "Artwork storage refresh failed", "artwork storage executor is not configured")
		return
	}
	checkpoint, err := decodeArtworkInventoryCheckpoint(job.Checkpoint)
	if err != nil {
		r.failJob(job.ID, 0, 0, "Artwork storage refresh failed", err.Error())
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), artworkStorageTimeout)
	defer cancel()
	heartbeatStop := make(chan struct{})
	go r.heartbeatLoop(ctx, job.ID, heartbeatStop)
	defer close(heartbeatStop)
	progress := func(current, total int, message string) {
		if err := r.repo.UpdateProgress(ctx, job.ID, current, total, message); err == nil {
			r.publishJobByID(ctx, notifications.TypeJobProgress, job.ID)
		}
	}
	result, err := r.artworkStorage.Refresh(ctx, checkpoint, func(next metadata.ArtworkInventoryCheckpoint) error {
		return r.repo.UpdateCheckpoint(ctx, job.ID, next)
	}, progress)
	if err != nil {
		r.failJob(job.ID, 0, 0, "Artwork storage refresh failed", artworkStorageErrorMessage(ctx, err))
		return
	}
	if err := r.repo.Complete(ctx, job.ID, CompleteJobInput{
		ResultPayload: result, Message: "Artwork storage accounting refreshed",
		ProgressCurrent: int(result.KnownRevisions), ProgressTotal: int(result.KnownRevisions),
		ExpiresAt: time.Now().UTC().Add(r.retention),
	}); err != nil {
		slog.Warn("admin jobs: failed to complete artwork storage refresh", "job_id", job.ID, "error", err)
		return
	}
	r.publishJobByID(ctx, notifications.TypeJobCompleted, job.ID)
}

func (r *Runner) executeArtworkPurge(job *models.AdminJob) {
	if r.artworkPurge == nil {
		r.failJob(job.ID, 0, 0, "Artwork purge failed", "artwork purge executor is not configured")
		return
	}
	req, err := decodeArtworkPurgeRequest(job.RequestPayload)
	if err != nil {
		artworkmetrics.Purge(req.DryRun, "failed", 0, 0)
		r.failJob(job.ID, 0, 0, "Artwork purge failed", err.Error())
		return
	}
	req.DryRun = job.DryRun
	checkpoint, err := decodeArtworkPurgeCheckpoint(job.Checkpoint)
	if err != nil {
		artworkmetrics.Purge(req.DryRun, "failed", 0, 0)
		r.failJob(job.ID, 0, 0, "Artwork purge failed", err.Error())
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), artworkStorageTimeout)
	defer cancel()
	heartbeatStop := make(chan struct{})
	go r.heartbeatLoop(ctx, job.ID, heartbeatStop)
	defer close(heartbeatStop)
	progress := func(current, total int, message string) {
		if err := r.repo.UpdateProgress(ctx, job.ID, current, total, message); err == nil {
			r.publishJobByID(ctx, notifications.TypeJobProgress, job.ID)
		}
	}
	result, err := r.artworkPurge.Execute(ctx, req, checkpoint, func(next ArtworkPurgeCheckpoint) error {
		return r.repo.UpdateCheckpoint(ctx, job.ID, next)
	}, progress)
	if err != nil {
		artworkmetrics.Purge(req.DryRun, "failed", 0, 0)
		r.failJob(job.ID, 0, 0, "Artwork purge failed", artworkStorageErrorMessage(ctx, err))
		return
	}
	artworkmetrics.Purge(req.DryRun, "completed", result.PendingBytes, result.ReclaimableBytes)
	completion := func(refreshQueued bool) CompleteJobInput {
		result.AccountingRefreshQueued = refreshQueued
		return CompleteJobInput{
			ResultPayload: result, Message: "Artwork purge completed",
			ProgressCurrent: 1, ProgressTotal: 1, ExpiresAt: time.Now().UTC().Add(r.retention),
		}
	}
	// The completion and the follow-up refresh go in together, so a completed
	// purge never reports a refresh that no row backs. If the pair cannot be
	// written, complete the purge alone and say the refresh was not queued.
	if !req.DryRun && req.Mode == ArtworkPurgeModeSafeMaterialized {
		_, err := r.repo.CompleteWithFollowUp(ctx, job.ID, completion(true), CreateJobInput{
			JobType: JobTypeArtworkStorageRefresh, CreatedByUserID: job.CreatedByUserID,
			RequestPayload: map[string]any{}, Message: "Queued post-purge artwork storage refresh",
		})
		if err == nil {
			r.publishJobByID(ctx, notifications.TypeJobCompleted, job.ID)
			return
		}
		slog.Warn("admin jobs: failed to queue post-purge artwork refresh", "job_id", job.ID, "error", err)
	}
	if err := r.repo.Complete(ctx, job.ID, completion(false)); err != nil {
		slog.Warn("admin jobs: failed to complete artwork purge", "job_id", job.ID, "error", err)
		return
	}
	r.publishJobByID(ctx, notifications.TypeJobCompleted, job.ID)
}

func artworkStorageErrorMessage(ctx context.Context, err error) string {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return fmt.Sprintf("timed out after %s: %v", artworkStorageTimeout, err)
	}
	if errors.Is(ctx.Err(), context.Canceled) {
		return fmt.Sprintf("context canceled: %v", err)
	}
	return err.Error()
}

func (r *Runner) executeDeleteLibrary(job *models.AdminJob) {
	if r.libraryDelete == nil {
		r.failJob(job.ID, 0, 0, "Library deletion failed", "library delete executor is not configured")
		return
	}

	req, err := decodeDeleteLibraryRequest(job.RequestPayload)
	if err != nil {
		r.failJob(job.ID, 0, 0, "Library deletion failed", err.Error())
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), deleteLibraryTimeout)
	defer cancel()

	heartbeatStop := make(chan struct{})
	go r.heartbeatLoop(ctx, job.ID, heartbeatStop)
	defer close(heartbeatStop)

	progress := func(current, total int, message string) {
		if err := r.repo.UpdateProgress(ctx, job.ID, current, total, message); err != nil {
			slog.Warn("admin jobs: failed to update delete progress", "job_id", job.ID, "error", err)
			return
		}
		r.publishJobByID(ctx, notifications.TypeJobProgress, job.ID)
	}

	result, err := r.libraryDelete.Execute(ctx, req, progress)
	if err != nil {
		msg := err.Error()
		if ctx.Err() != nil {
			msg = fmt.Sprintf("timed out after %s: %s", deleteLibraryTimeout, msg)
		}
		r.failJob(job.ID, 0, 5, "Library deletion failed", msg)
		return
	}

	if err := r.repo.Complete(ctx, job.ID, CompleteJobInput{
		ResultPayload:   result,
		Message:         "Library deletion completed",
		ProgressCurrent: 5,
		ProgressTotal:   5,
		ExpiresAt:       time.Now().UTC().Add(r.retention),
	}); err != nil {
		slog.Warn("admin jobs: failed to mark library deletion complete", "job_id", job.ID, "error", err)
		return
	}
	r.publishJobByID(ctx, notifications.TypeJobCompleted, job.ID)
}

func (r *Runner) executeImageCacheCleanup(job *models.AdminJob) {
	if r.imageCacheCleanup == nil {
		r.failJob(job.ID, 0, 0, "Image cache cleanup failed", "image cache cleanup executor is not configured")
		return
	}

	req, err := decodeImageCacheCleanupRequest(job.RequestPayload)
	if err != nil {
		r.failJob(job.ID, 0, 0, "Image cache cleanup failed", err.Error())
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), imageCacheCleanupTimeout)
	defer cancel()

	heartbeatStop := make(chan struct{})
	go r.heartbeatLoop(ctx, job.ID, heartbeatStop)
	defer close(heartbeatStop)

	progress := func(current, total int, message string) {
		if err := r.repo.UpdateProgress(ctx, job.ID, current, total, message); err != nil {
			slog.Warn("admin jobs: failed to update image cache cleanup progress", "job_id", job.ID, "error", err)
			return
		}
		r.publishJobByID(ctx, notifications.TypeJobProgress, job.ID)
	}

	result, err := r.imageCacheCleanup.Execute(ctx, req, progress)
	if err != nil {
		msg := err.Error()
		if ctx.Err() != nil {
			msg = fmt.Sprintf("timed out after %s: %s", imageCacheCleanupTimeout, msg)
		}
		r.failJob(job.ID, 0, 1, "Image cache cleanup failed", msg)
		return
	}

	if err := r.repo.Complete(ctx, job.ID, CompleteJobInput{
		ResultPayload:   result,
		Message:         "Cached image cleanup completed",
		ProgressCurrent: 1,
		ProgressTotal:   1,
		ExpiresAt:       time.Now().UTC().Add(r.retention),
	}); err != nil {
		slog.Warn("admin jobs: failed to mark image cache cleanup complete", "job_id", job.ID, "error", err)
		return
	}
	r.publishJobByID(ctx, notifications.TypeJobCompleted, job.ID)
}

func (r *Runner) executeLibraryRefresh(job *models.AdminJob) {
	if r.libraryRefresh == nil {
		r.failJob(job.ID, 0, 0, "Library metadata refresh failed", "library refresh executor is not configured")
		return
	}

	req, err := decodeLibraryRefreshRequest(job.RequestPayload)
	if err != nil {
		r.failJob(job.ID, 0, 0, "Library metadata refresh failed", err.Error())
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), libraryRefreshTimeout)
	defer cancel()
	unregisterCancel := r.cancelRegistry.Register(job.ID, cancel)
	defer unregisterCancel()

	heartbeatStop := make(chan struct{})
	go r.heartbeatLoop(ctx, job.ID, heartbeatStop)
	defer close(heartbeatStop)

	if err := r.repo.UpdateProgress(ctx, job.ID, 0, 0, "Preparing library metadata refresh"); err != nil {
		slog.Warn("admin jobs: failed to set initial library refresh progress", "job_id", job.ID, "error", err)
	} else {
		r.publishJobByID(ctx, notifications.TypeJobProgress, job.ID)
	}

	current := 0
	total := 0
	result, err := r.libraryRefresh.Execute(ctx, req, func(nextCurrent, nextTotal int, message string) {
		current = nextCurrent
		total = nextTotal
		if updateErr := r.repo.UpdateProgress(ctx, job.ID, nextCurrent, nextTotal, message); updateErr != nil {
			slog.Warn("admin jobs: failed to update library refresh progress",
				"job_id", job.ID,
				"current", nextCurrent,
				"total", nextTotal,
				"error", updateErr,
			)
			return
		}
		r.publishJobByID(ctx, notifications.TypeJobProgress, job.ID)
	})
	if err != nil {
		if errors.Is(ctx.Err(), context.Canceled) {
			r.cancelJob(job.ID, current, total, "Library metadata refresh cancelled")
			return
		}
		msg := err.Error()
		if ctx.Err() != nil {
			msg = fmt.Sprintf("timed out after %s: %s", libraryRefreshTimeout, msg)
		}
		r.failJob(job.ID, current, total, "Library metadata refresh failed", msg)
		return
	}

	if err := r.repo.Complete(ctx, job.ID, CompleteJobInput{
		ResultPayload:   result,
		Message:         "Library metadata refresh completed",
		ProgressCurrent: current,
		ProgressTotal:   total,
		ExpiresAt:       time.Now().UTC().Add(r.retention),
	}); err != nil {
		slog.Warn("admin jobs: failed to complete library refresh", "job_id", job.ID, "error", err)
		return
	}
	r.publishJobByID(ctx, notifications.TypeJobCompleted, job.ID)
}

func (r *Runner) executeTemplateBundleApply(job *models.AdminJob) {
	if r.templateBundleApply == nil {
		r.failJob(job.ID, 0, 0, "Collection defaults apply failed", "template bundle apply executor is not configured")
		return
	}

	req, err := decodeTemplateBundleApplyRequest(job.RequestPayload)
	if err != nil {
		r.failJob(job.ID, 0, 0, "Collection defaults apply failed", err.Error())
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), templateBundleApplyTimeout)
	defer cancel()

	heartbeatStop := make(chan struct{})
	go r.heartbeatLoop(ctx, job.ID, heartbeatStop)
	defer close(heartbeatStop)

	if err := r.repo.UpdateProgress(ctx, job.ID, 0, 0, "Loading selected libraries"); err != nil {
		slog.Warn("admin jobs: failed to set initial template bundle apply progress", "job_id", job.ID, "error", err)
	} else {
		r.publishJobByID(ctx, notifications.TypeJobProgress, job.ID)
	}

	lastCurrent := job.ProgressCurrent
	lastTotal := job.ProgressTotal
	result, err := r.templateBundleApply.ExecuteTemplateBundleApply(ctx, req, func(current, total int, message string) {
		lastCurrent = current
		lastTotal = total
		if updateErr := r.repo.UpdateProgress(ctx, job.ID, current, total, message); updateErr != nil {
			slog.Warn("admin jobs: failed to update template bundle apply progress",
				"job_id", job.ID,
				"current", current,
				"total", total,
				"error", updateErr,
			)
			return
		}
		r.publishJobByID(ctx, notifications.TypeJobProgress, job.ID)
	})
	if err != nil {
		msg := err.Error()
		if ctx.Err() != nil {
			msg = fmt.Sprintf("timed out after %s: %s", templateBundleApplyTimeout, msg)
		}
		r.failJob(job.ID, lastCurrent, lastTotal, "Collection defaults apply failed", msg)
		return
	}

	if err := r.repo.Complete(ctx, job.ID, CompleteJobInput{
		ResultPayload:   result,
		Message:         "Collection defaults applied",
		ProgressCurrent: lastCurrent,
		ProgressTotal:   lastTotal,
		ExpiresAt:       time.Now().UTC().Add(r.retention),
	}); err != nil {
		slog.Warn("admin jobs: failed to complete template bundle apply", "job_id", job.ID, "error", err)
		return
	}
	r.publishJobByID(ctx, notifications.TypeJobCompleted, job.ID)
}

func (r *Runner) requeueStaleJobs() {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if requeued, err := r.repo.RequeueStaleRunning(ctx, time.Now().UTC().Add(-r.staleAfter)); err != nil {
		slog.Warn("admin jobs: failed to requeue stale jobs", "error", err)
	} else if requeued > 0 {
		slog.Info("admin jobs: requeued stale jobs", "count", requeued)
	}
}

func (r *Runner) executeCatalogExport(job *models.AdminJob) {
	if r.store == nil {
		r.failJob(job.ID, 0, 0, "Catalog export failed", "private internal S3 is not configured")
		return
	}

	var opts catalogseed.ExportOptions
	if len(job.RequestPayload) > 0 {
		if err := json.Unmarshal(job.RequestPayload, &opts); err != nil {
			r.failJob(job.ID, 0, 0, "Catalog export failed", fmt.Sprintf("invalid export request payload: %v", err))
			return
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), jobTimeoutLong)
	defer cancel()

	heartbeatStop := make(chan struct{})
	go r.heartbeatLoop(ctx, job.ID, heartbeatStop)

	if err := r.repo.UpdateProgress(ctx, job.ID, 0, 0, "Exporting catalog"); err != nil {
		slog.Warn("admin jobs: failed to update initial export progress", "job_id", job.ID, "error", err)
	} else {
		r.publishJobByID(ctx, notifications.TypeJobProgress, job.ID)
	}

	tempFile, err := os.CreateTemp("", "silo-catalog-seed-*.json.gz")
	if err != nil {
		r.failJob(job.ID, 0, 0, "Catalog export failed", fmt.Sprintf("creating temp file: %v", err))
		return
	}
	tempPath := tempFile.Name()
	defer os.Remove(tempPath)

	var (
		lastProgressUpdate time.Time
		lastProgress       catalogseed.ExportProgress
	)
	summary, exportErr := r.exporter.ExportToWriter(ctx, tempFile, opts, func(progress catalogseed.ExportProgress) {
		lastProgress = progress
		if time.Since(lastProgressUpdate) < time.Second && progress.Current != progress.Total {
			return
		}
		lastProgressUpdate = time.Now()
		if err := r.repo.UpdateProgress(ctx, job.ID, progress.Current, progress.Total, progress.Message); err != nil {
			slog.Warn("admin jobs: failed to update export progress", "job_id", job.ID, "error", err)
			return
		}
		r.publishJobByID(ctx, notifications.TypeJobProgress, job.ID)
	})
	close(heartbeatStop)
	if err := tempFile.Close(); err != nil && exportErr == nil {
		exportErr = fmt.Errorf("closing temp export file: %w", err)
	}
	if exportErr != nil {
		r.failJob(job.ID, lastProgress.Current, lastProgress.Total, "Catalog export failed", exportErr.Error())
		return
	}

	key := filepath.ToSlash(filepath.Join(
		"catalog-seeds",
		time.Now().UTC().Format("2006"),
		time.Now().UTC().Format("01"),
		time.Now().UTC().Format("02"),
		job.ID+".json.gz",
	))

	uploadCtx, uploadCancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer uploadCancel()
	if err := r.repo.UpdateProgress(uploadCtx, job.ID, lastProgress.Total, lastProgress.Total, "Uploading catalog export"); err != nil {
		slog.Warn("admin jobs: failed to mark upload phase", "job_id", job.ID, "error", err)
	} else {
		r.publishJobByID(uploadCtx, notifications.TypeJobProgress, job.ID)
	}
	size, err := r.store.UploadFile(uploadCtx, r.store.Bucket(), key, tempPath, "application/gzip")
	if err != nil {
		r.failJob(job.ID, lastProgress.Total, lastProgress.Total, "Catalog export failed", err.Error())
		return
	}

	if err := r.repo.Complete(uploadCtx, job.ID, CompleteJobInput{
		ResultPayload:     summary,
		Message:           "Catalog export completed",
		ProgressCurrent:   lastProgress.Total,
		ProgressTotal:     lastProgress.Total,
		ArtifactBucket:    r.store.Bucket(),
		ArtifactKey:       key,
		ArtifactSizeBytes: size,
		ExpiresAt:         time.Now().UTC().Add(r.retention),
	}); err != nil {
		slog.Warn("admin jobs: failed to mark export complete", "job_id", job.ID, "error", err)
		return
	}
	r.publishJobByID(uploadCtx, notifications.TypeJobCompleted, job.ID)
}

func (r *Runner) executeCatalogImport(job *models.AdminJob) {
	var req CatalogImportRequest
	if len(job.RequestPayload) > 0 {
		if err := json.Unmarshal(job.RequestPayload, &req); err != nil {
			r.failJob(job.ID, 0, 0, "Catalog import failed", fmt.Sprintf("invalid import request payload: %v", err))
			return
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), jobTimeoutLong)
	defer cancel()

	heartbeatStop := make(chan struct{})
	go r.heartbeatLoop(ctx, job.ID, heartbeatStop)
	defer close(heartbeatStop)

	if err := r.repo.UpdateProgress(ctx, job.ID, 0, 0, "Loading catalog import source"); err != nil {
		slog.Warn("admin jobs: failed to update initial import progress", "job_id", job.ID, "error", err)
	} else {
		r.publishJobByID(ctx, notifications.TypeJobProgress, job.ID)
	}

	var data []byte
	if req.LocalPath != "" {
		var err error
		data, err = os.ReadFile(req.LocalPath)
		if err != nil {
			r.failJob(job.ID, 0, 0, "Catalog import failed", fmt.Sprintf("reading local file: %v", err))
			return
		}
	} else if req.RemoteURL != "" {
		if err := r.repo.UpdateProgress(ctx, job.ID, 0, 0, "Downloading catalog import source"); err != nil {
			slog.Warn("admin jobs: failed to update remote import progress", "job_id", job.ID, "error", err)
		} else {
			r.publishJobByID(ctx, notifications.TypeJobProgress, job.ID)
		}
		var err error
		data, err = downloadRemoteCatalogSeed(ctx, req.RemoteURL)
		if err != nil {
			r.failJob(job.ID, 0, 0, "Catalog import failed", err.Error())
			return
		}
	} else {
		if r.store == nil {
			r.failJob(job.ID, 0, 0, "Catalog import failed", "private internal S3 is not configured")
			return
		}
		if req.SourceBucket == "" {
			req.SourceBucket = r.store.Bucket()
		}
		if req.SourceKey == "" {
			r.failJob(job.ID, 0, 0, "Catalog import failed", "missing import source object")
			return
		}
		var err error
		data, err = r.store.GetObject(ctx, req.SourceBucket, req.SourceKey)
		if err != nil {
			r.failJob(job.ID, 0, 0, "Catalog import failed", err.Error())
			return
		}
		if req.CleanupSource {
			defer func() {
				if err := r.store.DeleteObject(context.Background(), req.SourceBucket, req.SourceKey); err != nil {
					slog.Warn("admin jobs: failed to delete staged import object", "job_id", job.ID, "bucket", req.SourceBucket, "key", req.SourceKey, "error", err)
				}
			}()
		}
	}

	var (
		lastProgressUpdate time.Time
		lastProgress       catalogseed.ImportProgress
	)
	result, importErr := r.exporter.ImportWithProgress(ctx, data, req.Options, func(progress catalogseed.ImportProgress) {
		lastProgress = progress
		if time.Since(lastProgressUpdate) < time.Second && progress.Current != progress.Total {
			return
		}
		lastProgressUpdate = time.Now()
		if err := r.repo.UpdateProgress(ctx, job.ID, progress.Current, progress.Total, progress.Message); err != nil {
			slog.Warn("admin jobs: failed to update import progress", "job_id", job.ID, "error", err)
			return
		}
		r.publishJobByID(ctx, notifications.TypeJobProgress, job.ID)
	})
	if importErr != nil {
		r.failJob(job.ID, lastProgress.Current, lastProgress.Total, "Catalog import failed", importErr.Error())
		return
	}

	completeCtx, completeCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer completeCancel()
	if err := r.repo.Complete(completeCtx, job.ID, CompleteJobInput{
		ResultPayload:   result,
		Message:         "Catalog import completed",
		ProgressCurrent: lastProgress.Total,
		ProgressTotal:   lastProgress.Total,
		ExpiresAt:       time.Now().UTC().Add(r.retention),
	}); err != nil {
		slog.Warn("admin jobs: failed to mark import complete", "job_id", job.ID, "error", err)
		return
	}
	r.publishJobByID(completeCtx, notifications.TypeJobCompleted, job.ID)
}

func downloadRemoteCatalogSeed(ctx context.Context, remoteURL string) ([]byte, error) {
	parsed, err := url.Parse(remoteURL)
	if err != nil {
		return nil, fmt.Errorf("invalid remote URL: %w", err)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return nil, fmt.Errorf("invalid remote URL scheme")
	}
	if !strings.HasSuffix(strings.ToLower(parsed.Path), ".json.gz") {
		return nil, fmt.Errorf("remote URL must point to a .json.gz file")
	}

	reqCtx, cancel := context.WithTimeout(ctx, remoteCatalogImportTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, remoteURL, nil)
	if err != nil {
		return nil, fmt.Errorf("building remote import request: %w", err)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("downloading remote catalog seed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("downloading remote catalog seed: unexpected status %d", resp.StatusCode)
	}

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("reading remote catalog seed: %w", err)
	}

	return data, nil
}

func (r *Runner) executeItemRefresh(job *models.AdminJob) {
	if r.itemRefresh == nil {
		r.failJob(job.ID, 0, 3, "Item refresh failed", "item refresh executor is not configured")
		return
	}

	var req ItemRefreshRequest
	if len(job.RequestPayload) > 0 {
		if err := json.Unmarshal(job.RequestPayload, &req); err != nil {
			r.failJob(job.ID, 0, 3, "Item refresh failed", fmt.Sprintf("invalid item refresh payload: %v", err))
			return
		}
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	heartbeatStop := make(chan struct{})
	go r.heartbeatLoop(ctx, job.ID, heartbeatStop)
	defer close(heartbeatStop)

	if err := r.repo.UpdateProgress(ctx, job.ID, 0, 3, "Resolving scan scope"); err != nil {
		slog.Warn("admin jobs: failed to set initial item refresh progress", "job_id", job.ID, "error", err)
	} else {
		r.publishJobByID(ctx, notifications.TypeJobProgress, job.ID)
	}

	// The executor decides how many steps a refresh has (artwork caching adds a
	// fourth), so the failure and completion totals track what it published
	// rather than a hard-coded 3 that would make progress jump backwards.
	progressTotal := 3
	result, err := r.itemRefresh.Execute(ctx, req, func(current, total int, message string) {
		if total > 0 {
			progressTotal = total
		}
		if updateErr := r.repo.UpdateProgress(ctx, job.ID, current, total, message); updateErr != nil {
			slog.Warn("admin jobs: failed to update item refresh progress",
				"job_id", job.ID,
				"current", current,
				"total", total,
				"error", updateErr,
			)
			return
		}
		r.publishJobByID(ctx, notifications.TypeJobProgress, job.ID)
	})
	if err != nil {
		message := err.Error()
		switch {
		case containsPhase(message, "scan scope"):
			r.failJob(job.ID, 1, progressTotal, "Item refresh failed", message)
		case containsPhase(message, "match discovered files"):
			r.failJob(job.ID, 2, progressTotal, "Item refresh failed", message)
		case containsPhase(message, "refresh metadata"):
			r.failJob(job.ID, 3, progressTotal, "Item refresh failed", message)
		default:
			r.failJob(job.ID, 0, progressTotal, "Item refresh failed", message)
		}
		return
	}
	message := "Metadata refreshed"
	if result != nil && result.ArtworkCacheWarning != "" {
		message = "Metadata refreshed, artwork incomplete"
	}
	if err := r.repo.Complete(ctx, job.ID, CompleteJobInput{
		ResultPayload:   result,
		Message:         message,
		ProgressCurrent: progressTotal,
		ProgressTotal:   progressTotal,
	}); err != nil {
		slog.Warn("admin jobs: failed to complete item refresh", "job_id", job.ID, "error", err)
		return
	}
	r.publishJobByID(ctx, notifications.TypeJobCompleted, job.ID)
}

func (r *Runner) heartbeatLoop(ctx context.Context, jobID string, stop <-chan struct{}) {
	ticker := time.NewTicker(r.heartbeatInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-stop:
			return
		case <-ticker.C:
			if err := r.repo.TouchHeartbeat(ctx, jobID); err != nil && !errors.Is(err, ErrJobNotFound) {
				slog.WarnContext(ctx, "admin jobs: failed to touch heartbeat", "component", "adminjob", "job_id", jobID, "error", err)
			}
		}
	}
}

func containsPhase(value, phase string) bool {
	return strings.Contains(value, phase)
}

func (r *Runner) cleanupExpired() {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	jobs, err := r.repo.ListExpired(ctx, time.Now().UTC(), 50)
	if err != nil {
		slog.Warn("admin jobs: failed to list expired jobs", "error", err)
		return
	}

	for _, job := range jobs {
		if r.store != nil && job.ArtifactBucket != "" && job.ArtifactKey != "" {
			if err := r.store.DeleteObject(ctx, job.ArtifactBucket, job.ArtifactKey); err != nil {
				slog.Warn("admin jobs: failed to delete expired artifact", "job_id", job.ID, "error", err)
				continue
			}
		}
		if err := r.repo.DeleteByID(ctx, job.ID); err != nil && !errors.Is(err, ErrJobNotFound) {
			slog.Warn("admin jobs: failed to delete expired job", "job_id", job.ID, "error", err)
		}
	}
}

func (r *Runner) publishJobByID(ctx context.Context, eventType notifications.Type, id string) {
	if r == nil || r.realtimeHub == nil || r.repo == nil || id == "" {
		return
	}

	job, err := r.repo.GetByID(ctx, id)
	if err != nil {
		if !errors.Is(err, ErrJobNotFound) {
			slog.WarnContext(ctx, "admin jobs: failed to load job for realtime event", "component", "adminjob", "job_id", id, "error", err)
		}
		return
	}

	r.publishJob(ctx, eventType, job)
}

func (r *Runner) publishJob(ctx context.Context, eventType notifications.Type, job *models.AdminJob) {
	if r == nil || r.realtimeHub == nil || job == nil {
		return
	}
	if err := r.realtimeHub.PublishJob(ctx, eventType, job); err != nil {
		slog.WarnContext(ctx, "admin jobs: failed to publish realtime job event", "component", "adminjob",
			"job_id", job.ID,
			"type", eventType,
			"error", err,
		)
	}
}

func (r *Runner) failJob(id string, current, total int, message, errorMessage string) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if err := r.repo.Fail(ctx, id, FailJobInput{
		Message:         message,
		ErrorMessage:    errorMessage,
		ProgressCurrent: current,
		ProgressTotal:   total,
		ExpiresAt:       time.Now().UTC().Add(r.retention),
	}); err != nil {
		slog.Warn("admin jobs: failed to mark job failed", "job_id", id, "error", err)
		return
	}
	r.publishJobByID(ctx, notifications.TypeJobFailed, id)
}

func (r *Runner) cancelJob(id string, current, total int, message string) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := r.repo.UpdateProgress(ctx, id, current, total, message); err != nil {
		slog.Warn("admin jobs: failed to update cancellation progress", "job_id", id, "error", err)
	}
	job, err := r.repo.Cancel(ctx, id, message, time.Now().UTC().Add(r.retention))
	if err != nil {
		slog.Warn("admin jobs: failed to mark job cancelled", "job_id", id, "error", err)
		return
	}
	if r.realtimeHub != nil {
		if err := r.realtimeHub.PublishJob(ctx, notifications.TypeJobCancelled, job); err != nil {
			slog.Warn("admin jobs: failed to publish job cancellation", "job_id", id, "error", err)
		}
	}
}
