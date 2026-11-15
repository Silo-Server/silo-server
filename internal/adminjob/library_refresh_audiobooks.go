package adminjob

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"time"

	"github.com/Silo-Server/silo-server/internal/cache"
	"github.com/Silo-Server/silo-server/internal/events"
	"github.com/Silo-Server/silo-server/internal/libraryingest"
	"github.com/Silo-Server/silo-server/internal/librarykind"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/notifications"
	"github.com/Silo-Server/silo-server/internal/scanbatch"
	"github.com/Silo-Server/silo-server/internal/scanner"
	"github.com/Silo-Server/silo-server/internal/scanqueue"
)

// ErrLibraryScanInProgress reports that a library scan of the library is
// already accepted or running, so a full refresh of an audiobook library does
// not start a second one.
var ErrLibraryScanInProgress = errors.New("a scan of this library is already running; refresh its metadata when the scan finishes")

// errLibraryScanRunLost reports a refresh whose scan run was canceled or
// failed elsewhere while it scanned.
var errLibraryScanRunLost = errors.New("the library scan was canceled")

// libraryRefreshFolderIngester ingests a whole library
// (libraryingest.Executor).
type libraryRefreshFolderIngester interface {
	IngestFolder(ctx context.Context, folder *models.MediaFolder) (*libraryingest.Result, error)
}

// scanRunProgressWriter persists a running scan's progress
// (scanqueue.Repository).
type scanRunProgressWriter interface {
	UpdateProgress(ctx context.Context, id string, startedAt *time.Time, result *events.ScanRunResult) (*models.ScanRun, error)
}

// refreshAudiobookLibrary is a full refresh of an audiobook library. One
// library scan, run directly and recorded as one scan run, reads every book's
// tags again on the scanner's worker pool; it otherwise runs as any library
// scan. Then each unmatched book is made due for the audiobook enricher's
// next sweep. Matched books keep their match, ignored books stay ignored, and
// no provider is asked. handled is false for a library that is not an
// audiobook library, or when the executor cannot run a library scan.
func (e *LibraryRefreshExecutor) refreshAudiobookLibrary(
	ctx context.Context,
	req LibraryRefreshRequest,
	progress func(current, total int, message string),
) (*LibraryRefreshResult, bool, error) {
	ingester, ok := e.ingester.(libraryRefreshFolderIngester)
	if !ok || e.scanRuns == nil {
		return nil, false, nil
	}
	folder, err := e.folderRepo.GetByID(ctx, req.LibraryID)
	if err != nil {
		return nil, true, fmt.Errorf("load library %d: %w", req.LibraryID, err)
	}
	if !librarykind.IsAudiobook(folder.Type) {
		return nil, false, nil
	}

	run, created, err := e.scanRuns.Create(ctx, scanqueue.CreateInput{
		LibraryID: req.LibraryID,
		Mode:      scanqueue.ModeLibrary,
		Trigger:   libraryRefreshScanTrigger,
	})
	if err != nil {
		return nil, true, fmt.Errorf("create scan run: %w", err)
	}
	if !created {
		// idx_scan_runs_active_scope: a queued or running library scan
		// owns the library. A refresh after it reads every book again.
		slog.InfoContext(ctx, "library refresh: a library scan is already active", "component", "adminjob",
			"library_id", req.LibraryID, "active_scan_id", activeScanRunID(run))
		return nil, true, ErrLibraryScanInProgress
	}
	runID := run.ID
	if run, err = e.scanRuns.Start(ctx, runID); err != nil {
		startErr := fmt.Errorf("start scan run: %w", err)
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), directScanFinishTimeout)
		defer cancel()
		if _, _, cancelErr := e.scanRuns.MarkCancelled(cleanupCtx, runID); cancelErr != nil {
			startErr = errors.Join(startErr, fmt.Errorf("cancel scan run after start failure: %w", cancelErr))
		}
		return nil, true, startErr
	}

	// Every book the scan reaches commits its re-read tags on its own, so a
	// scan that ends early (run lost, scan error, job canceled) has still
	// changed some books: invalidate caches and notify clients on every
	// return from here, as on success.
	defer e.publishAudiobookLibraryRefreshed(ctx, req.LibraryID)

	if progress != nil {
		progress(0, 0, "Reading audiobook tags")
	}
	// The scan ends when its run is canceled, here or on another server, or
	// failed as abandoned: a canceled run must not keep reading the library.
	runCtx, cancelRun := context.WithCancelCause(ctx)
	defer cancelRun(nil)
	scanCtx := scanner.WithAudiobookLibraryTagReread(runCtx)
	scanCtx = libraryingest.WithProgressReporter(scanCtx, e.audiobookScanProgress(run, progress))
	heartbeatEvery := e.scanHeartbeatEvery
	if heartbeatEvery <= 0 {
		heartbeatEvery = directScanHeartbeatEvery
	}
	stopHeartbeat := startDirectScanHeartbeatUntilLost(e.scanRuns, run, heartbeatEvery, func() { cancelRun(errLibraryScanRunLost) })
	ingestResult, err := ingester.IngestFolder(scanbatch.WithRunID(scanCtx, run.ID), folder)
	stopHeartbeat()
	if errors.Is(context.Cause(runCtx), errLibraryScanRunLost) {
		return nil, true, fmt.Errorf("scan library: %w", errLibraryScanRunLost)
	}
	if err != nil {
		return nil, true, fmt.Errorf("scan library: %w", failDirectScan(ctx, e.scanRuns, run, err))
	}
	if err := completeDirectScan(ctx, e.scanRuns, run, ingestResult); err != nil {
		return nil, true, fmt.Errorf("scan library: %w", err)
	}

	result := &LibraryRefreshResult{
		LibraryID:   req.LibraryID,
		LibraryName: req.LibraryName,
		Mode:        req.Mode,
		PipelineOK:  int(scanner.AudiobookTagRereadCount(scanCtx)),
	}
	if ingestResult != nil && ingestResult.ScanResult != nil {
		result.PipelineFailed = ingestResult.ScanResult.Errors
	}
	if err := e.requeueUnmatchedAudiobooks(ctx, req, result); err != nil {
		return nil, true, err
	}
	return result, true, nil
}

// publishAudiobookLibraryRefreshed invalidates the library's cached rails and
// notifies clients after its refresh scan, on a context that outlives a
// canceled job.
func (e *LibraryRefreshExecutor) publishAudiobookLibraryRefreshed(ctx context.Context, libraryID int) {
	e.publish(cache.EventScanComplete, strconv.Itoa(libraryID))
	e.publish(cache.EventMetadataUpdated, strconv.Itoa(libraryID))
	if e.realtimeHub != nil {
		publishCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), directScanFinishTimeout)
		defer cancel()
		_ = e.realtimeHub.PublishCatalogItemChanged(publishCtx, notifications.MetadataUpdateEvent{
			LibraryID: libraryID,
			Change:    metadataUpdatedChange,
		})
	}
}

// requeueUnmatchedAudiobooks hands the library's unmatched audiobooks back to
// the audiobook enricher, after the scan so books it found are included.
func (e *LibraryRefreshExecutor) requeueUnmatchedAudiobooks(ctx context.Context, req LibraryRefreshRequest, result *LibraryRefreshResult) error {
	items, err := e.itemLister.ListLibraryItems(ctx, req.LibraryID, req.Mode)
	if err != nil {
		return fmt.Errorf("load library items: %w", err)
	}
	for _, item := range items {
		if item.Audiobook {
			result.TotalItems++
		}
	}
	result.ItemsWithoutIDs = result.TotalItems
	if e.audiobooks == nil {
		return nil
	}
	queued, err := e.audiobooks.RequeueUnmatchedInLibrary(ctx, req.LibraryID)
	if err != nil {
		return fmt.Errorf("queue audiobooks for matching: %w", err)
	}
	result.AudiobooksQueued = queued
	return nil
}

// audiobookScanProgress forwards the library scan's own progress to the job
// and to its scan run, as the scan queue does for a queued scan.
func (e *LibraryRefreshExecutor) audiobookScanProgress(run *models.ScanRun, progress func(current, total int, message string)) func(libraryingest.ProgressUpdate) {
	writer, _ := e.scanRuns.(scanRunProgressWriter)
	return func(update libraryingest.ProgressUpdate) {
		if progress != nil {
			progress(update.FilesProcessed, update.TotalFiles, "Reading audiobook tags")
		}
		if writer == nil || run == nil {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), directScanHeartbeatTimeout)
		defer cancel()
		if _, err := writer.UpdateProgress(ctx, run.ID, run.StartedAt, &events.ScanRunResult{
			New:             update.New,
			Updated:         update.Updated,
			Unchanged:       update.Unchanged,
			Errors:          update.Errors,
			MatchedFiles:    update.MatchedFiles,
			Phase:           update.Phase,
			Message:         update.Message,
			CurrentScope:    update.CurrentScope,
			TotalFiles:      update.TotalFiles,
			FilesDiscovered: update.FilesDiscovered,
			FilesProcessed:  update.FilesProcessed,
		}); err != nil && !errors.Is(err, scanqueue.ErrScanRunNotFound) {
			slog.Warn("library refresh: failed to persist scan progress", "component", "adminjob", "scan_id", run.ID, "error", err)
		}
	}
}
