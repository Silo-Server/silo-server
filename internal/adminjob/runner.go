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

	"github.com/Silo-Server/silo-server/internal/workmetrics"

	"github.com/Silo-Server/silo-server/internal/catalogseed"
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
	LibraryRefreshTimeout      = 6 * time.Hour // also bounds each library in the full refresh task
	templateBundleApplyTimeout = 2 * time.Hour
	storageTransitionTimeout   = 7 * 24 * time.Hour
	jobTimeoutLong             = 2 * time.Hour // catalog_export, catalog_import
)

type Runner struct {
	observation                *workmetrics.Run
	workCtx                    context.Context
	repo                       *Repository
	exporter                   *catalogseed.Service
	store                      ArtifactStore
	itemRefresh                itemRefreshExecutor
	libraryRefresh             libraryRefreshExecutor
	libraryDelete              deleteLibraryExecutor
	imageCacheCleanup          imageCacheCleanupExecutor
	templateBundleApply        templateBundleApplyExecutor
	storageTransition          storageTransitionExecutor
	storageTransitionCommitted func(context.Context) error
	realtimeHub                *notifications.Hub
	pollInterval               time.Duration
	cleanupInterval            time.Duration
	heartbeatInterval          time.Duration
	staleAfter                 time.Duration
	retention                  time.Duration
	cancelRegistry             *CancelRegistry
	storageRestart             *storageRestartState
	stop                       chan struct{}
	stopOnce                   sync.Once
}

// storageRestartState is shared by every job this runner executes. Once the
// process commits a transition, or cannot confirm its commit, it keeps the
// source fences until it restarts. A later claim of the same transition must
// not execute again or request a second restart.
type storageRestartState struct {
	mu      sync.Mutex
	pending bool
	manual  bool
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

type StorageTransitionRequest struct {
	TransitionID string `json:"transition_id"`
	Policy       string `json:"policy"`
}

// StorageTransitionProgress carries a phase chosen by the transition owner.
// Message may contain storage object keys and must stay out of API responses.
type StorageTransitionProgress struct {
	Current int
	Total   int
	Phase   string
	Message string
}

const storageTransitionPhaseRestartPending = "restart_pending"

type StorageTransitionReceipt struct {
	Phase                 string `json:"phase"`
	VerifiedObjects       int    `json:"verified_objects"`
	ClaimGeneration       int64  `json:"claim_generation,omitzero"`
	FailureCategory       string `json:"failure_category,omitempty"`
	RestartRequired       bool   `json:"restart_required,omitzero"`
	ManualRestartRequired bool   `json:"manual_restart_required,omitzero"`
}

type storageTransitionExecutor interface {
	ExecuteStorageTransition(context.Context, StorageTransitionRequest, func(StorageTransitionProgress)) (any, error)
}

type storageTransitionCancellationRecorder interface {
	CancelStorageTransition(context.Context, StorageTransitionRequest) error
}

type storageTransitionCommitResult interface {
	StorageTransitionCommitUnknown() bool
}

type storageTransitionRestartResult interface {
	WithStorageTransitionRestartReceipt(bool, int64) any
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
		storageRestart:      &storageRestartState{},
		stop:                make(chan struct{}),
	}
}

func (r *Runner) SetCancelRegistry(registry *CancelRegistry) {
	if registry != nil {
		r.cancelRegistry = registry
	}
}

func (r *Runner) SetStorageTransitionExecutor(executor storageTransitionExecutor) {
	r.storageTransition = executor
}

func (r *Runner) SetStorageTransitionCommitted(callback func(context.Context) error) {
	r.storageTransitionCommitted = callback
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
		JobTypeStorageTransition,
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
	workCtx, observation := workmetrics.Start(context.Background(), "admin", job.RequestedAt)
	defer workmetrics.Profile(workCtx)()
	defer observation.Finish("unknown")
	// Each execution owns a repository fenced to this durable claim. Recovery
	// increments its generation; an old worker cannot publish another outcome.
	r = &Runner{
		observation: observation, workCtx: workCtx,
		repo: r.repo.withClaim(job), exporter: r.exporter, store: r.store,
		itemRefresh: r.itemRefresh, libraryRefresh: r.libraryRefresh, libraryDelete: r.libraryDelete,
		imageCacheCleanup: r.imageCacheCleanup, templateBundleApply: r.templateBundleApply, storageTransition: r.storageTransition,
		storageTransitionCommitted: r.storageTransitionCommitted,
		realtimeHub:                r.realtimeHub, heartbeatInterval: r.heartbeatInterval, retention: r.retention, cancelRegistry: r.cancelRegistry,
		storageRestart: r.storageRestart, stop: r.stop,
	}
	if job.JobType == JobTypeStorageTransition && r.storageRestartPending() {
		// Stale recovery requeued a transition this process committed or could
		// not confirm. Its fences are still held; only the restart settles it.
		r.settleCommittedStorageTransition(job, job.ProgressCurrent, job.ProgressTotal, false)
		return
	}
	if job.CancelRequested {
		message := "Library metadata refresh canceled"
		if job.JobType == JobTypeStorageTransition {
			message = "Storage transition canceled; verified copy checkpoints retained"
			if recorder, ok := r.storageTransition.(storageTransitionCancellationRecorder); ok {
				var req StorageTransitionRequest
				if err := json.Unmarshal(job.RequestPayload, &req); err != nil {
					slog.Warn("admin jobs: decode queued storage transition cancellation", "job_id", job.ID, "error", err)
					return
				}
				cancelCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				err := recorder.CancelStorageTransition(cancelCtx, req)
				cancel()
				if errors.Is(err, ErrStorageTransitionAlreadyCommitted) {
					// The transition committed before this cancellation was seen.
					r.settleCommittedStorageTransition(job, job.ProgressCurrent, job.ProgressTotal, true)
					return
				}
				if err != nil {
					slog.Warn("admin jobs: release queued storage transition", "job_id", job.ID, "error", err)
					return
				}
			}
		}
		r.cancelJob(job.ID, job.ProgressCurrent, job.ProgressTotal, message)
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
	case JobTypeStorageTransition:
		r.executeStorageTransition(job)
	default:
		r.failJob(job.ID, 0, 0, "Admin job failed", "unsupported admin job type")
	}
}

func (r *Runner) executeStorageTransition(job *models.AdminJob) {
	if r.storageTransition == nil {
		r.failJob(job.ID, 0, 0, "Storage transition failed", "storage transition executor is not configured")
		return
	}
	var req StorageTransitionRequest
	if err := json.Unmarshal(job.RequestPayload, &req); err != nil {
		r.failJob(job.ID, 0, 0, "Storage transition failed", "invalid transition request: "+err.Error())
		return
	}
	ctx, cancel := context.WithTimeout(r.executionContext(), storageTransitionTimeout)
	defer cancel()
	go func() {
		ticker := time.NewTicker(r.heartbeatInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				current, err := r.repo.GetByID(ctx, job.ID)
				if err == nil && (current.CancelRequested || current.ClaimGeneration != job.ClaimGeneration) {
					cancel()
					return
				}
			}
		}
	}()
	unregisterCancel := r.cancelRegistry.Register(job.ID, cancel)
	defer unregisterCancel()
	heartbeatStop := make(chan struct{})
	go r.heartbeatLoop(ctx, job.ID, heartbeatStop)
	defer close(heartbeatStop)
	current, total := 0, 0
	phase := "preparing"
	result, err := r.storageTransition.ExecuteStorageTransition(ctx, req, func(progress StorageTransitionProgress) {
		current, total, phase = progress.Current, progress.Total, progress.Phase
		receipt := StorageTransitionReceipt{Phase: progress.Phase, VerifiedObjects: max(progress.Current, 0), ClaimGeneration: job.ClaimGeneration}
		if updateErr := r.repo.UpdateProgressResult(ctx, job.ID, current, total, progress.Message, receipt); updateErr != nil {
			slog.Warn("admin jobs: failed to update storage transition progress", "job_id", job.ID, "error", updateErr)
			return
		}
		r.publishJobByID(ctx, notifications.TypeJobProgress, job.ID)
	})
	if errors.Is(err, ErrStorageTransitionAlreadyCommitted) {
		r.settleCommittedStorageTransition(job, current, total, true)
		return
	}
	if err != nil {
		if errors.Is(ctx.Err(), context.Canceled) {
			r.cancelJob(job.ID, current, total, "Storage transition canceled; verified copy checkpoints retained")
			return
		}
		r.failJobWithResult(job.ID, current, total, "Storage transition failed", err.Error(), StorageTransitionReceipt{
			Phase: "failed", VerifiedObjects: max(current, 0), ClaimGeneration: job.ClaimGeneration, FailureCategory: storageTransitionFailureCategory(phase),
		})
		return
	}
	if total == 0 {
		current, total = 1, 1
	}
	// ExecuteStorageTransition only returns success after the new storage
	// settings have committed. From that point onward the source mutation fences
	// intentionally remain held until this process exits, so requesting the
	// restart must not depend on the best-effort job receipt write below. In
	// particular, a canceled context or a transient database outage must not
	// leave the old process running indefinitely with storage writes blocked.
	message := "Storage transition completed; restarting Silo"
	manual := r.requestStorageTransitionRestartOnce(job.ID)
	if uncertain, ok := result.(storageTransitionCommitResult); ok && uncertain.StorageTransitionCommitUnknown() {
		message = "Storage commit outcome is unknown; restarting Silo to recover safely"
		if manual {
			message = "Storage commit outcome is unknown; automatic restart unavailable — restart Silo manually"
		}
		if structured, ok := result.(storageTransitionRestartResult); ok {
			result = structured.WithStorageTransitionRestartReceipt(manual, job.ClaimGeneration)
		}
		r.holdStorageTransitionForRestart(job.ID, current, total, message, result)
		return
	}
	if manual {
		message = "Storage transition committed; automatic restart unavailable — restart Silo manually"
	}
	if structured, ok := result.(storageTransitionRestartResult); ok {
		result = structured.WithStorageTransitionRestartReceipt(manual, job.ClaimGeneration)
	}
	completeCtx, completeCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer completeCancel()
	if err := r.repo.CompleteCommittedStorageTransition(completeCtx, job.ID, CompleteJobInput{ResultPayload: result, Message: message, ProgressCurrent: current, ProgressTotal: total, ExpiresAt: time.Now().UTC().Add(r.retention)}); err != nil {
		slog.Warn("admin jobs: failed to complete storage transition", "job_id", job.ID, "error", err)
		r.keepStorageTransitionReceiptAlive(job.ID)
		return
	}
	r.publishJobByID(completeCtx, notifications.TypeJobCompleted, job.ID)
}

func storageTransitionFailureCategory(phase string) string {
	switch phase {
	case "checking_target":
		return "target_check_failed"
	case "copying":
		return "copy_failed"
	case "verifying":
		return "verification_failed"
	case "committing", storageTransitionPhaseRestartPending:
		return "commit_failed"
	default:
		return "preparation_failed"
	}
}

// A possibly committed transition keeps its source mutation fences until the
// process exits. Keep its running receipt alive for the same period so this
// process cannot reclaim it while those fences are still held. A restart ends
// the heartbeat and lets boot finalization or stale job recovery decide what
// the database actually committed.
func (r *Runner) keepStorageTransitionReceiptAlive(jobID string) {
	go r.heartbeatLoop(context.Background(), jobID, r.stop)
}

func (r *Runner) storageRestartPending() bool {
	if r.storageRestart == nil {
		return false
	}
	r.storageRestart.mu.Lock()
	defer r.storageRestart.mu.Unlock()
	return r.storageRestart.pending
}

// requestStorageTransitionRestartOnce asks the host to restart after a commit
// and reports whether the administrator must restart Silo by hand. A second
// request in the same process reuses the first outcome: the host refuses a
// repeated request, which would otherwise read as a missing restart callback.
func (r *Runner) requestStorageTransitionRestartOnce(jobID string) (manual bool) {
	state := r.storageRestart
	if state == nil {
		state = &storageRestartState{}
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.pending {
		return state.manual
	}
	if err := r.requestStorageTransitionRestart(jobID); err != nil {
		slog.Warn("admin jobs: storage transition requires a manual restart", "job_id", jobID, "error", err)
		state.manual = true
	}
	state.pending = true
	return state.manual
}

// settleCommittedStorageTransition handles a claim that must not execute: its
// transition already committed, or this process committed or could not confirm
// it and still holds the source fences. The claim records the restart receipt
// and keeps it alive; after the restart, boot recovery completes a committed
// job and stale recovery requeues one whose commit did not apply.
func (r *Runner) settleCommittedStorageTransition(job *models.AdminJob, current, total int, committed bool) {
	manual := r.requestStorageTransitionRestartOnce(job.ID)
	state := "Storage transition is waiting for a restart"
	if committed {
		state = "Storage transition committed"
	}
	message := state + "; restarting Silo to finish recovery"
	if manual {
		message = state + "; automatic restart unavailable — restart Silo manually"
	}
	receipt := StorageTransitionReceipt{
		Phase: storageTransitionPhaseRestartPending, VerifiedObjects: max(current, 0),
		ClaimGeneration: job.ClaimGeneration, RestartRequired: true, ManualRestartRequired: manual,
	}
	r.holdStorageTransitionForRestart(job.ID, current, total, message, receipt)
}

// holdStorageTransitionForRestart records the receipt, clearing any pending
// cancellation, and keeps it alive until the process exits.
func (r *Runner) holdStorageTransitionForRestart(jobID string, current, total int, message string, result any) {
	updateCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := r.repo.HoldStorageTransitionForRestart(updateCtx, jobID, current, total, message, result); err != nil {
		slog.Warn("admin jobs: failed to record storage transition restart receipt", "job_id", jobID, "error", err)
	} else {
		r.publishJobByID(updateCtx, notifications.TypeJobProgress, jobID)
	}
	r.keepStorageTransitionReceiptAlive(jobID)
}

func (r *Runner) requestStorageTransitionRestart(jobID string) error {
	if r.storageTransitionCommitted == nil {
		return errors.New("server restart callback is not configured")
	}
	if err := r.storageTransitionCommitted(context.Background()); err != nil {
		return fmt.Errorf("request server restart for job %s: %w", jobID, err)
	}
	return nil
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

	ctx, cancel := context.WithTimeout(r.executionContext(), deleteLibraryTimeout)
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

	if cleanupJob := r.queueImageCacheCleanup(context.Background(), job.CreatedByUserID, result); cleanupJob != nil {
		result.ImageCleanupQueued = true
		result.ImageCleanupJobID = cleanupJob.ID
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

func (r *Runner) queueImageCacheCleanup(ctx context.Context, createdByUserID int, result *DeleteLibraryResult) *models.AdminJob {
	if r == nil || r.repo == nil || r.imageCacheCleanup == nil || result == nil || len(result.orphanedImageDirs) == 0 {
		return nil
	}

	cleanupJob, err := r.repo.Create(ctx, CreateJobInput{
		JobType:         JobTypeImageCacheCleanup,
		CreatedByUserID: createdByUserID,
		RequestPayload: ImageCacheCleanupRequest{
			LibraryID:   result.LibraryID,
			LibraryName: result.LibraryName,
			Prefixes:    append([]string(nil), result.orphanedImageDirs...),
		},
		Message: "Queued cached image cleanup",
	})
	if err != nil {
		slog.WarnContext(ctx, "admin jobs: failed to queue image cache cleanup", "component", "adminjob",
			"library_id", result.LibraryID,
			"library_name", result.LibraryName,
			"error", err,
		)
		return nil
	}

	r.publishJob(ctx, notifications.TypeJobCreated, cleanupJob)
	return cleanupJob
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

	ctx, cancel := context.WithTimeout(r.executionContext(), imageCacheCleanupTimeout)
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
		r.failJob(job.ID, 0, len(req.Prefixes), "Image cache cleanup failed", msg)
		return
	}

	if err := r.repo.Complete(ctx, job.ID, CompleteJobInput{
		ResultPayload:   result,
		Message:         "Cached image cleanup completed",
		ProgressCurrent: len(req.Prefixes),
		ProgressTotal:   len(req.Prefixes),
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
	// A later claim recovers a job from a worker that stopped heartbeating.
	// That worker may still hold the library lock, so wait for it to let go
	// rather than failing the job against its own earlier attempt.
	req.waitForLibraryLock = job.ClaimGeneration > 1

	ctx, cancel := context.WithTimeout(r.executionContext(), LibraryRefreshTimeout)
	defer cancel()
	go func() {
		ticker := time.NewTicker(r.heartbeatInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				current, err := r.repo.GetByID(ctx, job.ID)
				if err == nil && (current.CancelRequested || current.ClaimGeneration != job.ClaimGeneration) {
					cancel()
					return
				}
			}
		}
	}()
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
			msg = fmt.Sprintf("timed out after %s: %s", LibraryRefreshTimeout, msg)
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

	ctx, cancel := context.WithTimeout(r.executionContext(), templateBundleApplyTimeout)
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
	ctx, cancel := context.WithTimeout(r.executionContext(), 30*time.Second)
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

	ctx, cancel := context.WithTimeout(r.executionContext(), jobTimeoutLong)
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

	uploadCtx, uploadCancel := context.WithTimeout(r.executionContext(), 30*time.Minute)
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

	ctx, cancel := context.WithTimeout(r.executionContext(), jobTimeoutLong)
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

	completeCtx, completeCancel := context.WithTimeout(r.executionContext(), 30*time.Second)
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

	ctx, cancel := context.WithCancel(r.executionContext())
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
	ctx, cancel := context.WithTimeout(r.executionContext(), 5*time.Minute)
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
	if r == nil || r.repo == nil || id == "" {
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
	if r == nil || job == nil {
		return
	}
	if r.observation != nil {
		switch job.Status {
		case StatusCancelled, StatusCompleted, StatusFailed:
			r.observation.Finish(job.Status)
		default:
			if eventType == notifications.TypeJobProgress {
				workmetrics.Progress("admin")
			}
		}
	}
	if r.realtimeHub == nil {
		return
	}
	// Cancellation may win the terminal database transition even when the
	// executor returned success or failure. Publish the committed outcome.
	switch job.Status {
	case StatusCancelled:
		eventType = notifications.TypeJobCancelled
	case StatusCompleted:
		eventType = notifications.TypeJobCompleted
	case StatusFailed:
		eventType = notifications.TypeJobFailed
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
	r.failJobWithResult(id, current, total, message, errorMessage, nil)
}

func (r *Runner) failJobWithResult(id string, current, total int, message, errorMessage string, result any) {
	ctx, cancel := context.WithTimeout(r.executionContext(), 30*time.Second)
	defer cancel()

	if err := r.repo.Fail(ctx, id, FailJobInput{
		Message:         message,
		ErrorMessage:    errorMessage,
		ResultPayload:   result,
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
	ctx, cancel := context.WithTimeout(r.executionContext(), 30*time.Second)
	defer cancel()
	if err := r.repo.UpdateProgress(ctx, id, current, total, message); err != nil {
		slog.Warn("admin jobs: failed to update cancellation progress", "job_id", id, "error", err)
	}
	job, err := r.repo.Cancel(ctx, id, message, time.Now().UTC().Add(r.retention))
	if err != nil {
		slog.Warn("admin jobs: failed to mark job cancelled", "job_id", id, "error", err)
		return
	}
	if r.observation != nil {
		r.observation.Finish(job.Status)
	}
	if r.realtimeHub != nil {
		if err := r.realtimeHub.PublishJob(ctx, notifications.TypeJobCancelled, job); err != nil {
			slog.Warn("admin jobs: failed to publish job cancellation", "job_id", id, "error", err)
		}
	}
}

func (r *Runner) executionContext() context.Context {
	if r.workCtx != nil {
		return r.workCtx
	}
	return context.Background()
}
