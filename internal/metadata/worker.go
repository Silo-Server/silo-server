package metadata

import (
	"context"
	"fmt"
	"log/slog"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Silo-Server/silo-server/internal/models"
)

// MatchWorker processes unmatched files in the background.
type MatchWorker struct {
	service                 *MetadataService
	fileLister              UnmatchedFileLister
	itemLister              UnmatchedItemLister
	movieClaimer            MovieFileClaimer
	seriesClaimer           SeriesRootClaimer
	enableTVSeriesRootQueue bool
	workers                 int
	batchSize               int
	interval                time.Duration
}

type NonSeriesFileClaimer interface {
	ClaimUnmatchedNonSeries(ctx context.Context, limit int) ([]*models.MediaFile, error)
	ClaimUnmatchedNonSeriesByFolderAndPathPrefix(ctx context.Context, folderID int, pathPrefix string, limit int, attemptBefore time.Time) ([]*models.MediaFile, error)
}

type MixedFileClaimer interface {
	ClaimUnmatchedMixed(ctx context.Context, limit int) ([]*models.MediaFile, error)
	ClaimUnmatchedMixedByFolderAndPathPrefix(ctx context.Context, folderID int, pathPrefix string, limit int, attemptBefore time.Time) ([]*models.MediaFile, error)
}

type MovieFileClaimer interface {
	Claim(ctx context.Context, limit int) ([]*models.MediaFile, error)
	ClaimByFolderAndPathPrefix(ctx context.Context, folderID int, pathPrefix string, limit int, attemptBefore time.Time) ([]*models.MediaFile, error)
	Delete(ctx context.Context, mediaFileID int) error
	UpdateError(ctx context.Context, mediaFileID int, errText string) error
}

type SeriesRootClaimer interface {
	Claim(ctx context.Context, limit int) ([]models.SeriesRootMatchJob, error)
	ClaimByFolderAndPathPrefix(ctx context.Context, folderID int, pathPrefix string, limit int, attemptBefore time.Time) ([]models.SeriesRootMatchJob, error)
	Delete(ctx context.Context, folderID int, observedRootPath string) error
	UpdateError(ctx context.Context, folderID int, observedRootPath string, errText string) error
	ListByFolder(ctx context.Context, folderID int, limit int, offset int) ([]models.SeriesRootMatchQueueEntry, int, error)
	CountByFolder(ctx context.Context, folderID int) (int, error)
}

// NewMatchWorker creates a new background match worker.
func NewMatchWorker(service *MetadataService, fileLister UnmatchedFileLister, workers, batchSize int, interval time.Duration) *MatchWorker {
	if workers < 1 {
		workers = 8
	}
	if batchSize <= 0 {
		batchSize = 500
	}
	if interval == 0 {
		interval = 30 * time.Second
	}
	var itemLister UnmatchedItemLister
	if service != nil {
		itemLister = service.itemRepo
	}
	return &MatchWorker{
		service:    service,
		fileLister: fileLister,
		itemLister: itemLister,
		workers:    workers,
		batchSize:  batchSize,
		interval:   interval,
	}
}

// SetSeriesRootClaimer enables native TV root-backed matching when enabled is true.
func (w *MatchWorker) SetSeriesRootClaimer(claimer SeriesRootClaimer, enabled bool) {
	if w == nil {
		return
	}
	w.seriesClaimer = claimer
	w.enableTVSeriesRootQueue = enabled
}

// SetMovieFileClaimer enables queue-backed movie matching when claimer is non-nil.
func (w *MatchWorker) SetMovieFileClaimer(claimer MovieFileClaimer) {
	if w == nil {
		return
	}
	w.movieClaimer = claimer
}

// Run starts the match worker loop. It blocks until ctx is cancelled.
func (w *MatchWorker) Run(ctx context.Context) {
	ticker := time.NewTicker(w.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			w.processUnmatched(ctx)
		}
	}
}

// processUnmatched fetches a batch of unmatched files and processes them.
func (w *MatchWorker) processUnmatched(ctx context.Context) {
	if w.enableTVSeriesRootQueue && w.seriesClaimer != nil {
		jobs, err := w.seriesClaimer.Claim(ctx, w.batchSize)
		if err != nil {
			slog.Error("metadata: failed to claim unmatched series roots", "error", err)
		} else if len(jobs) > 0 {
			slog.Info("metadata: processing unmatched series roots", "count", len(jobs))
			if _, err := w.processSeriesRoots(ctx, jobs); err != nil {
				slog.Error("metadata: failed to process unmatched series roots", "error", err)
			}
		}
	}

	if w.movieClaimer != nil {
		files, err := w.movieClaimer.Claim(ctx, w.batchSize)
		if err != nil {
			slog.Error("metadata: failed to claim queued movie files", "error", err)
		} else if len(files) > 0 {
			slog.Info("metadata: processing queued movie files", "count", len(files))
			w.processQueuedMovieFiles(ctx, files)
		}
	}

	files, err := w.claimBackgroundFiles(ctx)
	if err != nil {
		slog.Error("metadata: failed to list unmatched files", "error", err)
		return
	}
	if len(files) == 0 {
		return
	}

	slog.Info("metadata: processing unmatched files", "count", len(files))
	w.processFiles(ctx, files)
}

func (w *MatchWorker) processFile(ctx context.Context, file *models.MediaFile) {
	if w.fileLister != nil {
		defer func() {
			stampCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			defer cancel()

			if err := w.fileLister.MarkMatchAttempted(stampCtx, file.ID); err != nil {
				slog.Warn("metadata: failed to record match attempt",
					"file_id", file.ID,
					"path", file.FilePath,
					"error", err)
			}
		}()
	}
	w.processFileWithFolderCache(ctx, file, nil, nil)
}

func (w *MatchWorker) processFileWithFolderCache(ctx context.Context, file *models.MediaFile, folderEnabledCache *sync.Map, deferredSeriesLinks *sync.Map) {
	if !w.folderEnabled(ctx, file.MediaFolderID, folderEnabledCache) {
		slog.Info("metadata: skipping file in disabled library",
			"file_id", file.ID,
			"path", file.FilePath,
			"folder_id", file.MediaFolderID,
		)
		return
	}

	// Phase 0: Create skeleton item or find existing.
	skeleton, err := w.service.createOrFindSkeleton(ctx, file, file.MediaFolderID)
	if err != nil {
		slog.Warn("metadata: skeleton creation failed",
			"file_id", file.ID, "path", file.FilePath, "error", err)
		return
	}
	// If the file was linked to an existing item via dedup, skip enrichment.
	// The first file for each series creates the skeleton (IsNew=true) and runs
	// the full provider pipeline. Subsequent files only need linking.
	if !skeleton.IsNew {
		// For series files, defer episode linking to a single call per series
		// after the batch completes, rather than calling per-file.
		if skeleton.Type == "series" && deferredSeriesLinks != nil {
			deferredSeriesLinks.Store(skeleton.ContentID, struct{}{})
		} else if skeleton.Type == "series" {
			// Fallback for callers that don't provide a deferred map.
			if err := w.service.ensureSeriesEpisodeLinks(ctx, skeleton.ContentID); err != nil {
				slog.Warn("metadata: failed to ensure series episode links",
					"content_id", skeleton.ContentID,
					"file_id", file.ID,
					"path", file.FilePath,
					"error", err)
			}
		}
		return
	}
	if skeleton.ItemStatus == "ambiguous" {
		return
	}

	req := w.buildProcessRequestForGroup(ctx, file, skeleton, nil)
	result, err := w.service.Process(ctx, req)
	if err != nil {
		slog.Warn("metadata: enrichment failed",
			"file_id", file.ID, "path", file.FilePath, "error", err)
		w.logStatusUpdateFailure(ctx, skeleton.ContentID, "unmatched", "content_id", skeleton.ContentID, "file_id", file.ID, "path", file.FilePath)
		// For series items, synthesize fallback episode structure so episodes
		// are visible even when no provider match was found.
		if skeleton.Type == "series" {
			if fbErr := w.service.SynthesizeFallbackEpisodes(ctx, skeleton.ContentID); fbErr != nil {
				slog.Warn("metadata: fallback episode synthesis failed after enrichment error",
					"content_id", skeleton.ContentID, "error", fbErr)
			}
		}
		return
	}

	if result != nil && !result.Updated {
		w.logStatusUpdateFailure(ctx, skeleton.ContentID, "unmatched", "content_id", skeleton.ContentID, "file_id", file.ID, "path", file.FilePath)
		// Same fallback synthesis for series when no provider data was returned.
		if skeleton.Type == "series" {
			if fbErr := w.service.SynthesizeFallbackEpisodes(ctx, skeleton.ContentID); fbErr != nil {
				slog.Warn("metadata: fallback episode synthesis failed for unmatched series",
					"content_id", skeleton.ContentID, "error", fbErr)
			}
		}
	}
}

func (w *MatchWorker) buildProcessRequestForGroup(ctx context.Context, representative *models.MediaFile, skeleton *skeletonResult, preloadedGroupFiles []*models.MediaFile) ProcessRequest {
	groupFiles := preloadedGroupFiles
	if len(groupFiles) == 0 {
		groupFiles = []*models.MediaFile{representative}
		if skeleton != nil && skeleton.Type == "series" && w.service != nil && w.service.fileRepo != nil {
			loadedFiles, err := w.service.fileRepo.ListByObservedRootPath(ctx, representative.MediaFolderID, skeleton.ObservedRootPath)
			if err != nil {
				slog.Warn("metadata: failed to load observed-root files",
					"folder_id", representative.MediaFolderID,
					"observed_root_path", skeleton.ObservedRootPath,
					"error", err,
				)
			} else if len(loadedFiles) > 0 {
				groupFiles = loadedFiles
			}
		}
	}

	groupFilePaths := make([]string, 0, len(groupFiles))
	groupFilesForSidecars := make([]*models.MediaFile, 0, len(groupFiles))
	for _, groupFile := range groupFiles {
		if groupFile == nil {
			continue
		}
		groupFilePaths = append(groupFilePaths, groupFile.FilePath)
		groupFilesForSidecars = append(groupFilesForSidecars, groupFile)
	}
	if len(groupFilesForSidecars) == 0 {
		groupFilePaths = []string{representative.FilePath}
		groupFilesForSidecars = []*models.MediaFile{representative}
	}
	slices.Sort(groupFilePaths)
	sidecarSearchPaths := w.service.directorySidecarSearchPathsForFiles(ctx, groupFilesForSidecars)
	safeObservedRootPath := ""
	if w.service.canUseObservedRootForDirectorySidecars(
		ctx,
		representative.MediaFolderID,
		skeleton.ObservedRootPath,
		skeleton.GroupKeyVersion,
		skeleton.ContentGroupKey,
	) {
		safeObservedRootPath = skeleton.ObservedRootPath
	}

	hints := &MatchHints{
		FileHash:                  representative.FileHash,
		FilePath:                  representative.FilePath,
		RepresentativeFilePath:    representative.FilePath,
		ObservedRootPath:          safeObservedRootPath,
		AllGroupFilePaths:         groupFilePaths,
		PrimarySidecarSearchPaths: sidecarSearchPaths,
		Title:                     skeleton.Title,
		Year:                      skeleton.Year,
		Type:                      skeleton.Type,
		TmdbID:                    skeleton.TmdbID,
		ImdbID:                    skeleton.ImdbID,
		TvdbID:                    skeleton.TvdbID,
		HintSource:                "scanner",
	}

	return ProcessRequest{
		ContentID: skeleton.ContentID,
		Hints:     hints,
		FolderID:  formatFolderID(representative.MediaFolderID),
		Mode:      ModeInitialMatch,
	}
}

func compactUniquePaths(paths []string) []string {
	if len(paths) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(paths))
	out := make([]string, 0, len(paths))
	for _, path := range paths {
		clean := filepath.Clean(path)
		if clean == "." || clean == "" {
			continue
		}
		if _, ok := seen[clean]; ok {
			continue
		}
		seen[clean] = struct{}{}
		out = append(out, clean)
	}
	return out
}

// ProcessFile applies the normal unmatched-file pipeline to a single file.
func (w *MatchWorker) ProcessFile(ctx context.Context, file *models.MediaFile) {
	w.processFile(ctx, file)
}

// ProcessBatch fetches and processes one batch of unmatched files. Returns the
// number of files processed.
func (w *MatchWorker) ProcessBatch(ctx context.Context) (processed int, err error) {
	if w.enableTVSeriesRootQueue && w.seriesClaimer != nil {
		jobs, err := w.seriesClaimer.Claim(ctx, w.batchSize)
		if err != nil {
			return 0, err
		}
		if len(jobs) > 0 {
			return w.processSeriesRoots(ctx, jobs)
		}
	}
	if w.movieClaimer != nil {
		files, err := w.movieClaimer.Claim(ctx, w.batchSize)
		if err != nil {
			return 0, err
		}
		if len(files) > 0 {
			return w.processQueuedMovieFiles(ctx, files), nil
		}
	}

	files, err := w.claimBackgroundFiles(ctx)
	if err != nil {
		return 0, err
	}
	if len(files) == 0 {
		return 0, nil
	}

	return w.processFiles(ctx, files), nil
}

// ProcessBatchByFolderAndPathPrefix processes unmatched files within a single
// library subtree immediately instead of waiting for the periodic worker loop.
func (w *MatchWorker) ProcessBatchByFolderAndPathPrefix(ctx context.Context, folderID int, pathPrefix string, attemptBefore time.Time) (processed int, err error) {
	useSeriesQueue, useMovieQueue, err := w.queueUsageForFolder(ctx, folderID)
	if err != nil {
		return 0, err
	}
	if useSeriesQueue {
		jobs, err := w.seriesClaimer.ClaimByFolderAndPathPrefix(ctx, folderID, pathPrefix, w.batchSize, attemptBefore)
		if err != nil {
			return 0, err
		}
		processed, err := w.processSeriesRoots(ctx, jobs)
		if err != nil || processed > 0 {
			return processed, err
		}
	}
	if useSeriesQueue && !useMovieQueue {
		return processed, nil
	}
	if useMovieQueue {
		files, err := w.movieClaimer.ClaimByFolderAndPathPrefix(ctx, folderID, pathPrefix, w.batchSize, attemptBefore)
		if err != nil {
			return 0, err
		}
		processed := w.processQueuedMovieFiles(ctx, files)
		if processed > 0 {
			return processed, nil
		}
		if !useSeriesQueue {
			return processed, nil
		}
	}

	files, err := w.claimScopedFiles(ctx, folderID, pathPrefix, attemptBefore, scopedFallbackMode(useSeriesQueue, useMovieQueue))
	if err != nil {
		return 0, err
	}
	return w.processFiles(ctx, files), nil
}

// ProcessAllByFolderAndPathPrefix keeps draining scoped unmatched files until
// the subtree is empty.
func (w *MatchWorker) ProcessAllByFolderAndPathPrefix(ctx context.Context, folderID int, pathPrefix string, attemptBefore time.Time) (processed int, err error) {
	if attemptBefore.IsZero() {
		attemptBefore = time.Now().UTC()
	}
	useSeriesQueue, useMovieQueue, err := w.queueUsageForFolder(ctx, folderID)
	if err != nil {
		return 0, err
	}
	slog.Info("metadata: scoped matcher selected",
		"folder_id", folderID,
		"path_prefix", pathPrefix,
		"matcher_path", scopedMatcherPath(useSeriesQueue, useMovieQueue),
	)

	for {
		if ctx.Err() != nil {
			return processed, ctx.Err()
		}

		if useSeriesQueue {
			jobs, err := w.seriesClaimer.ClaimByFolderAndPathPrefix(ctx, folderID, pathPrefix, w.batchSize, attemptBefore)
			if err != nil {
				return processed, err
			}
			if len(jobs) > 0 {
				batchProcessed, err := w.processSeriesRoots(ctx, jobs)
				if err != nil {
					return processed, err
				}
				processed += batchProcessed
				continue
			}
		}
		if useMovieQueue {
			files, err := w.movieClaimer.ClaimByFolderAndPathPrefix(ctx, folderID, pathPrefix, w.batchSize, attemptBefore)
			if err != nil {
				return processed, err
			}
			if len(files) > 0 {
				batchProcessed := w.processQueuedMovieFiles(ctx, files)
				processed += batchProcessed
				continue
			}
		}
		if useSeriesQueue != useMovieQueue {
			return processed, nil
		}

		files, err := w.claimScopedFiles(ctx, folderID, pathPrefix, attemptBefore, scopedFallbackMode(useSeriesQueue, useMovieQueue))
		if err != nil {
			return processed, err
		}
		batchProcessed := w.processFiles(ctx, files)
		processed += batchProcessed
		if batchProcessed == 0 {
			return processed, nil
		}
	}
}

func (w *MatchWorker) processFiles(ctx context.Context, files []*models.MediaFile) int {
	if len(files) == 0 {
		return 0
	}

	claimedCount := len(files)
	folders := sync.Map{}
	selectedFiles := w.collapseClaimedSeriesBatch(ctx, files)

	fileChan := make(chan *models.MediaFile, len(selectedFiles))
	for _, f := range selectedFiles {
		fileChan <- f
	}
	close(fileChan)

	var (
		wg                  sync.WaitGroup
		processed           atomic.Int64
		deferredSeriesLinks sync.Map
	)
	for i := 0; i < w.workers; i++ {
		wg.Go(func() {
			for file := range fileChan {
				if ctx.Err() != nil {
					return
				}
				w.processFileWithFolderCache(ctx, file, &folders, &deferredSeriesLinks)
				processed.Add(1)
			}
		})
	}
	wg.Wait()

	// Run deferred series episode linking once per unique series.
	if ctx.Err() == nil {
		deferredSeriesLinks.Range(func(key, _ any) bool {
			if ctx.Err() != nil {
				return false
			}
			contentID := key.(string)
			if err := w.service.ensureSeriesEpisodeLinks(ctx, contentID); err != nil {
				slog.Warn("metadata: deferred series episode link failed",
					"content_id", contentID, "error", err)
			}
			return true
		})
	}

	return claimedCount
}

func (w *MatchWorker) processQueuedMovieFiles(ctx context.Context, files []*models.MediaFile) int {
	if len(files) == 0 {
		return 0
	}

	fileChan := make(chan *models.MediaFile, len(files))
	for _, f := range files {
		fileChan <- f
	}
	close(fileChan)

	var (
		wg        sync.WaitGroup
		processed atomic.Int64
		folders   sync.Map
	)
	for i := 0; i < w.workers; i++ {
		wg.Go(func() {
			for file := range fileChan {
				if ctx.Err() != nil {
					return
				}
				if w.processQueuedMovieFile(ctx, file, &folders) {
					processed.Add(1)
				}
			}
		})
	}
	wg.Wait()

	return int(processed.Load())
}

func (w *MatchWorker) processQueuedMovieFile(ctx context.Context, file *models.MediaFile, folderEnabledCache *sync.Map) bool {
	if file == nil || w == nil || w.service == nil || w.movieClaimer == nil {
		return false
	}
	if !w.folderEnabled(ctx, file.MediaFolderID, folderEnabledCache) {
		return false
	}

	skeleton, reusedLinkedItem, err := w.queuedMovieSkeleton(ctx, file)
	if err != nil {
		queueErr := truncateSeriesQueueError(err.Error())
		if updateErr := w.movieClaimer.UpdateError(ctx, file.ID, queueErr); updateErr != nil {
			slog.Warn("metadata: failed to update movie queue error",
				"file_id", file.ID,
				"path", file.FilePath,
				"error", updateErr,
			)
		}
		slog.Warn("metadata: movie queue skeleton creation failed",
			"file_id", file.ID,
			"path", file.FilePath,
			"error", err,
		)
		return false
	}
	if skeleton == nil || strings.TrimSpace(skeleton.ContentID) == "" {
		if updateErr := w.movieClaimer.UpdateError(ctx, file.ID, truncateSeriesQueueError("movie queue claimed without a content id")); updateErr != nil {
			slog.Warn("metadata: failed to update movie queue error",
				"file_id", file.ID,
				"path", file.FilePath,
				"error", updateErr,
			)
		}
		return false
	}
	if skeleton.ItemStatus == "ambiguous" {
		if err := w.movieClaimer.Delete(ctx, file.ID); err != nil {
			slog.Warn("metadata: failed to delete ambiguous movie queue row",
				"file_id", file.ID,
				"path", file.FilePath,
				"error", err,
			)
			return false
		}
		return true
	}

	if skeleton.IsNew || reusedLinkedItem {
		req := w.buildProcessRequestForGroup(ctx, file, skeleton, nil)
		result, processErr := w.service.Process(ctx, req)
		if processErr != nil {
			queueErr := truncateSeriesQueueError(processErr.Error())
			if updateErr := w.movieClaimer.UpdateError(ctx, file.ID, queueErr); updateErr != nil {
				slog.Warn("metadata: failed to update movie queue error",
					"file_id", file.ID,
					"path", file.FilePath,
					"error", updateErr,
				)
			}
			slog.Warn("metadata: enrichment failed",
				"file_id", file.ID,
				"path", file.FilePath,
				"error", processErr,
			)
			w.logStatusUpdateFailure(ctx, skeleton.ContentID, "unmatched", "content_id", skeleton.ContentID, "file_id", file.ID, "path", file.FilePath)
			return false
		} else if result != nil && !result.Updated {
			if updateErr := w.movieClaimer.UpdateError(ctx, file.ID, truncateSeriesQueueError(ErrMetadataNotFound.Error())); updateErr != nil {
				slog.Warn("metadata: failed to update movie queue error",
					"file_id", file.ID,
					"path", file.FilePath,
					"error", updateErr,
				)
			}
			w.logStatusUpdateFailure(ctx, skeleton.ContentID, "unmatched", "content_id", skeleton.ContentID, "file_id", file.ID, "path", file.FilePath)
			return false
		}
	}

	if err := w.movieClaimer.Delete(ctx, file.ID); err != nil {
		slog.Warn("metadata: failed to delete movie queue row",
			"file_id", file.ID,
			"path", file.FilePath,
			"error", err,
		)
		return false
	}
	return true
}

func (w *MatchWorker) queuedMovieSkeleton(ctx context.Context, file *models.MediaFile) (*skeletonResult, bool, error) {
	if skeleton, ok := w.reusableQueuedMovieSkeleton(ctx, file); ok {
		return skeleton, true, nil
	}

	skeleton, err := w.service.createOrFindSkeleton(ctx, file, file.MediaFolderID)
	if err != nil {
		return nil, false, err
	}
	return skeleton, false, nil
}

func (w *MatchWorker) reusableQueuedMovieSkeleton(ctx context.Context, file *models.MediaFile) (*skeletonResult, bool) {
	if w == nil || w.service == nil || w.service.itemRepo == nil || file == nil {
		return nil, false
	}
	contentID := strings.TrimSpace(file.ContentID)
	if contentID == "" {
		return nil, false
	}

	item, err := w.service.itemRepo.GetByID(ctx, contentID)
	if err != nil || item == nil {
		return nil, false
	}
	status := strings.ToLower(strings.TrimSpace(item.Status))
	if !isSkeletonLikeStatus(status) && status != "ambiguous" {
		return nil, false
	}

	rootPath := filepath.Dir(file.FilePath)
	if file.CanonicalRootPath != "" {
		rootPath = filepath.Clean(file.CanonicalRootPath)
	}
	observedRootPath := filepath.Dir(file.FilePath)
	if file.ObservedRootPath != "" {
		observedRootPath = filepath.Clean(file.ObservedRootPath)
	}
	title := strings.TrimSpace(item.Title)
	if title == "" {
		title = file.BaseTitle
	}
	itemType := strings.TrimSpace(item.Type)
	if itemType == "" {
		itemType = file.BaseType
	}
	if itemType == "" {
		itemType = "movie"
	}
	year := item.Year
	if year == 0 {
		year = file.BaseYear
	}

	return &skeletonResult{
		ContentID:        item.ContentID,
		ItemStatus:       item.Status,
		RootPath:         rootPath,
		ObservedRootPath: observedRootPath,
		GroupKeyVersion:  file.GroupKeyVersion,
		ContentGroupKey:  strings.TrimSpace(file.ContentGroupKey),
		Title:            title,
		Year:             year,
		Type:             itemType,
		TmdbID:           item.TmdbID,
		ImdbID:           item.ImdbID,
		TvdbID:           item.TvdbID,
	}, true
}

func scopedMatcherPath(useSeriesQueue bool, useMovieQueue bool) string {
	switch {
	case useSeriesQueue && useMovieQueue:
		return "series_root_queue+movie_file_queue"
	case useSeriesQueue:
		return "series_root_queue"
	case useMovieQueue:
		return "movie_file_queue"
	default:
		return "file"
	}
}

func (w *MatchWorker) processSeriesRoots(ctx context.Context, jobs []models.SeriesRootMatchJob) (int, error) {
	if len(jobs) == 0 {
		return 0, nil
	}

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	jobChan := make(chan models.SeriesRootMatchJob, len(jobs))
	for _, job := range jobs {
		jobChan <- job
	}
	close(jobChan)

	var (
		wg         sync.WaitGroup
		processed  atomic.Int64
		firstErr   error
		firstErrMu sync.Mutex
		folders    sync.Map
	)
	for i := 0; i < w.workers; i++ {
		wg.Go(func() {
			for job := range jobChan {
				if runCtx.Err() != nil {
					return
				}
				count, err := w.processSeriesRoot(runCtx, job, &folders)
				if err != nil {
					firstErrMu.Lock()
					if firstErr == nil {
						firstErr = err
						cancel()
					}
					firstErrMu.Unlock()
					return
				}
				processed.Add(int64(count))
			}
		})
	}
	wg.Wait()

	return int(processed.Load()), firstErr
}

func (w *MatchWorker) processSeriesRoot(ctx context.Context, job models.SeriesRootMatchJob, folderEnabledCache *sync.Map) (int, error) {
	if !w.folderEnabled(ctx, job.MediaFolderID, folderEnabledCache) {
		return 0, nil
	}
	if w.service == nil || w.service.fileRepo == nil || w.seriesClaimer == nil {
		return 0, fmt.Errorf("series root matching requires file repo and queue claimer")
	}

	groupFiles, err := w.service.fileRepo.ListByObservedRootPath(ctx, job.MediaFolderID, job.ObservedRootPath)
	if err != nil {
		return 0, fmt.Errorf("loading files for series root %d/%s: %w", job.MediaFolderID, job.ObservedRootPath, err)
	}
	if len(groupFiles) == 0 {
		if err := w.seriesClaimer.Delete(ctx, job.MediaFolderID, job.ObservedRootPath); err != nil {
			return 0, err
		}
		slog.Info("metadata: series root dropped because files disappeared",
			"folder_id", job.MediaFolderID,
			"observed_root_path", job.ObservedRootPath,
		)
		return 0, nil
	}

	representative := selectRepresentativeGroupFile(groupFiles)
	if representative == nil {
		if err := w.seriesClaimer.Delete(ctx, job.MediaFolderID, job.ObservedRootPath); err != nil {
			return 0, err
		}
		return 0, nil
	}
	if !hasUnlinkedGroupFile(groupFiles) {
		if strings.TrimSpace(representative.ContentID) != "" {
			if skeleton, ok := w.reusableQueuedMovieSkeleton(ctx, representative); ok && skeleton.ItemStatus != "ambiguous" {
				req := w.buildProcessRequestForGroup(ctx, representative, skeleton, groupFiles)
				result, processErr := w.service.Process(ctx, req)
				if processErr != nil {
					queueErr := truncateSeriesQueueError(processErr.Error())
					if updateErr := w.seriesClaimer.UpdateError(ctx, job.MediaFolderID, job.ObservedRootPath, queueErr); updateErr != nil {
						return 0, updateErr
					}
					slog.Warn("metadata: enrichment failed",
						"file_id", representative.ID,
						"path", representative.FilePath,
						"error", processErr)
					w.logStatusUpdateFailure(ctx, skeleton.ContentID, "unmatched",
						"content_id", skeleton.ContentID,
						"file_id", representative.ID,
						"path", representative.FilePath)
					if fbErr := w.service.SynthesizeFallbackEpisodes(ctx, skeleton.ContentID); fbErr != nil {
						slog.Warn("metadata: fallback episode synthesis failed after series root enrichment error",
							"content_id", skeleton.ContentID, "error", fbErr)
					}
					return 0, nil
				}
				if result != nil && !result.Updated {
					if updateErr := w.seriesClaimer.UpdateError(ctx, job.MediaFolderID, job.ObservedRootPath, truncateSeriesQueueError(ErrMetadataNotFound.Error())); updateErr != nil {
						return 0, updateErr
					}
					w.logStatusUpdateFailure(ctx, skeleton.ContentID, "unmatched",
						"content_id", skeleton.ContentID,
						"file_id", representative.ID,
						"path", representative.FilePath)
					if fbErr := w.service.SynthesizeFallbackEpisodes(ctx, skeleton.ContentID); fbErr != nil {
						slog.Warn("metadata: fallback episode synthesis failed for unmatched series root",
							"content_id", skeleton.ContentID, "error", fbErr)
					}
					return 0, nil
				}
			}
			if err := w.service.ensureSeriesEpisodeLinks(ctx, representative.ContentID); err != nil {
				if updateErr := w.seriesClaimer.UpdateError(ctx, job.MediaFolderID, job.ObservedRootPath, truncateSeriesQueueError(err.Error())); updateErr != nil {
					return 0, updateErr
				}
				return 0, fmt.Errorf("ensuring series episode links for %s: %w", representative.ContentID, err)
			}
		}
		if err := w.seriesClaimer.Delete(ctx, job.MediaFolderID, job.ObservedRootPath); err != nil {
			return 0, err
		}
		return len(groupFiles), nil
	}

	skeleton, err := w.service.createOrFindSkeleton(ctx, representative, job.MediaFolderID)
	if err != nil {
		queueErr := truncateSeriesQueueError(err.Error())
		if updateErr := w.seriesClaimer.UpdateError(ctx, job.MediaFolderID, job.ObservedRootPath, queueErr); updateErr != nil {
			return 0, updateErr
		}
		slog.Warn("metadata: series root skeleton creation failed",
			"folder_id", job.MediaFolderID,
			"observed_root_path", job.ObservedRootPath,
			"sample_file_path", job.SampleFilePath,
			"error", err,
		)
		return 0, nil
	}
	if skeleton == nil || strings.TrimSpace(skeleton.ContentID) == "" {
		return 0, nil
	}

	if _, err := w.service.fileRepo.UpdateContentIDByObservedRootPath(ctx, job.MediaFolderID, job.ObservedRootPath, skeleton.ContentID); err != nil {
		if updateErr := w.seriesClaimer.UpdateError(ctx, job.MediaFolderID, job.ObservedRootPath, truncateSeriesQueueError(err.Error())); updateErr != nil {
			return 0, updateErr
		}
		return 0, fmt.Errorf("relinking series root %d/%s: %w", job.MediaFolderID, job.ObservedRootPath, err)
	}

	if skeleton.IsNew && skeleton.ItemStatus != "ambiguous" {
		req := w.buildProcessRequestForGroup(ctx, representative, skeleton, groupFiles)
		result, processErr := w.service.Process(ctx, req)
		if processErr != nil {
			queueErr := truncateSeriesQueueError(processErr.Error())
			if updateErr := w.seriesClaimer.UpdateError(ctx, job.MediaFolderID, job.ObservedRootPath, queueErr); updateErr != nil {
				return 0, updateErr
			}
			slog.Warn("metadata: enrichment failed",
				"file_id", representative.ID,
				"path", representative.FilePath,
				"error", processErr)
			w.logStatusUpdateFailure(ctx, skeleton.ContentID, "unmatched",
				"content_id", skeleton.ContentID,
				"file_id", representative.ID,
				"path", representative.FilePath,
				"folder_id", job.MediaFolderID,
				"observed_root_path", job.ObservedRootPath,
			)
			if fbErr := w.service.SynthesizeFallbackEpisodes(ctx, skeleton.ContentID); fbErr != nil {
				slog.Warn("metadata: fallback episode synthesis failed after enrichment error",
					"content_id", skeleton.ContentID,
					"error", fbErr,
				)
			}
			return 0, nil
		} else if result != nil && !result.Updated {
			if updateErr := w.seriesClaimer.UpdateError(ctx, job.MediaFolderID, job.ObservedRootPath, truncateSeriesQueueError(ErrMetadataNotFound.Error())); updateErr != nil {
				return 0, updateErr
			}
			w.logStatusUpdateFailure(ctx, skeleton.ContentID, "unmatched",
				"content_id", skeleton.ContentID,
				"file_id", representative.ID,
				"path", representative.FilePath,
				"folder_id", job.MediaFolderID,
				"observed_root_path", job.ObservedRootPath,
			)
			if fbErr := w.service.SynthesizeFallbackEpisodes(ctx, skeleton.ContentID); fbErr != nil {
				slog.Warn("metadata: fallback episode synthesis failed for unmatched series",
					"content_id", skeleton.ContentID,
					"error", fbErr,
				)
			}
			return 0, nil
		}
	}

	finalContentID, err := w.service.fileRepo.FindContentIDByObservedRootPath(ctx, job.MediaFolderID, job.ObservedRootPath, "series")
	if err != nil {
		if updateErr := w.seriesClaimer.UpdateError(ctx, job.MediaFolderID, job.ObservedRootPath, truncateSeriesQueueError(err.Error())); updateErr != nil {
			return 0, updateErr
		}
		return 0, fmt.Errorf("resolving final content for series root %d/%s: %w", job.MediaFolderID, job.ObservedRootPath, err)
	}
	if finalContentID == "" {
		finalContentID = skeleton.ContentID
	}
	if strings.TrimSpace(finalContentID) != "" {
		if err := w.service.ensureSeriesEpisodeLinks(ctx, finalContentID); err != nil {
			if updateErr := w.seriesClaimer.UpdateError(ctx, job.MediaFolderID, job.ObservedRootPath, truncateSeriesQueueError(err.Error())); updateErr != nil {
				return 0, updateErr
			}
			return 0, fmt.Errorf("ensuring series episode links for %s: %w", finalContentID, err)
		}
		if _, ok := w.service.confirmedOwnershipItem(ctx, finalContentID); ok {
			w.service.claimConfirmedSeriesRootOwnership(ctx, job.MediaFolderID, job.ObservedRootPath, finalContentID, groupFiles)
		}
	}

	if err := w.seriesClaimer.Delete(ctx, job.MediaFolderID, job.ObservedRootPath); err != nil {
		return 0, err
	}

	slog.Info("metadata: series root completed",
		"folder_id", job.MediaFolderID,
		"observed_root_path", job.ObservedRootPath,
		"content_id", finalContentID,
		"file_count", len(groupFiles),
	)
	return len(groupFiles), nil
}

func (w *MatchWorker) logStatusUpdateFailure(ctx context.Context, contentID, status string, attrs ...any) {
	if w == nil || w.service == nil {
		return
	}
	if err := w.service.updateItemStatus(ctx, contentID, status); err != nil {
		args := append([]any{"content_id", contentID, "status", status, "error", err}, attrs...)
		slog.Warn("metadata: failed to update item status", args...)
	}
}

func (w *MatchWorker) collapseClaimedSeriesBatch(ctx context.Context, files []*models.MediaFile) []*models.MediaFile {
	if len(files) == 0 || w == nil || w.service == nil || w.service.folderRepo == nil {
		return files
	}

	out := make([]*models.MediaFile, 0, len(files))
	folderTypes := make(map[int]string)
	seenGroups := make(map[string]struct{})

	for _, file := range files {
		if file == nil {
			continue
		}
		folderType, ok := folderTypes[file.MediaFolderID]
		if !ok {
			folder, err := w.service.folderRepo.GetByID(ctx, file.MediaFolderID)
			if err != nil {
				slog.Warn("metadata: failed to load folder type for batch compaction",
					"folder_id", file.MediaFolderID,
					"file_id", file.ID,
					"error", err,
				)
			}
			if folder != nil {
				folderType = strings.ToLower(strings.TrimSpace(folder.Type))
			}
			folderTypes[file.MediaFolderID] = folderType
		}

		switch folderType {
		case "series", "tv", "show", "tvshows":
		default:
			out = append(out, file)
			continue
		}
		if file.ContentGroupKey == "" {
			out = append(out, file)
			continue
		}

		groupKey := fmt.Sprintf("%d:%d:%s", file.MediaFolderID, file.GroupKeyVersion, file.ContentGroupKey)
		if _, ok := seenGroups[groupKey]; ok {
			continue
		}
		seenGroups[groupKey] = struct{}{}
		out = append(out, file)
	}

	return out
}

func (w *MatchWorker) folderEnabled(ctx context.Context, folderID int, cache *sync.Map) bool {
	if folderID <= 0 || w == nil || w.service == nil || w.service.folderRepo == nil {
		return true
	}

	if cache != nil {
		if cached, ok := cache.Load(folderID); ok {
			return cached.(bool)
		}
	}

	enabled := true
	folder, err := w.service.folderRepo.GetByID(ctx, folderID)
	if err != nil {
		slog.Warn("metadata: failed to load folder state during match",
			"folder_id", folderID,
			"error", err,
		)
	} else if folder != nil {
		enabled = folder.Enabled
	}

	if cache != nil {
		cache.Store(folderID, enabled)
	}

	return enabled
}

func (w *MatchWorker) folderType(ctx context.Context, folderID int) (string, error) {
	if folderID <= 0 || w == nil || w.service == nil || w.service.folderRepo == nil {
		return "", nil
	}
	folder, err := w.service.folderRepo.GetByID(ctx, folderID)
	if err != nil {
		return "", err
	}
	if folder == nil {
		return "", nil
	}
	return strings.ToLower(strings.TrimSpace(folder.Type)), nil
}

func (w *MatchWorker) queueUsageForFolder(ctx context.Context, folderID int) (useSeriesQueue bool, useMovieQueue bool, err error) {
	folderType, err := w.folderType(ctx, folderID)
	if err != nil {
		return false, false, err
	}
	useSeriesQueue = w.enableTVSeriesRootQueue &&
		w.seriesClaimer != nil &&
		(isTVLibraryType(folderType) || isMixedLibraryType(folderType))
	useMovieQueue = w.movieClaimer != nil && isMovieLibraryType(folderType)
	return useSeriesQueue, useMovieQueue, nil
}

func (w *MatchWorker) claimBackgroundFiles(ctx context.Context) ([]*models.MediaFile, error) {
	if w.enableTVSeriesRootQueue && w.movieClaimer != nil {
		if claimer, ok := w.fileLister.(MixedFileClaimer); ok {
			return claimer.ClaimUnmatchedMixed(ctx, w.batchSize)
		}
		return nil, fmt.Errorf("mixed-library file claimer is not configured")
	}
	if w.enableTVSeriesRootQueue {
		if claimer, ok := w.fileLister.(NonSeriesFileClaimer); ok {
			return claimer.ClaimUnmatchedNonSeries(ctx, w.batchSize)
		}
		return nil, fmt.Errorf("non-series file claimer is not configured")
	}
	return w.fileLister.ClaimUnmatched(ctx, w.batchSize)
}

type scopedFallbackClaimMode int

const (
	scopedFallbackGeneric scopedFallbackClaimMode = iota
	scopedFallbackNonSeries
	scopedFallbackMixed
)

func scopedFallbackMode(useSeriesQueue bool, useMovieQueue bool) scopedFallbackClaimMode {
	switch {
	case useSeriesQueue && useMovieQueue:
		return scopedFallbackMixed
	case useSeriesQueue:
		return scopedFallbackNonSeries
	default:
		return scopedFallbackGeneric
	}
}

func (w *MatchWorker) claimScopedFiles(ctx context.Context, folderID int, pathPrefix string, attemptBefore time.Time, mode scopedFallbackClaimMode) ([]*models.MediaFile, error) {
	switch mode {
	case scopedFallbackMixed:
		if claimer, ok := w.fileLister.(MixedFileClaimer); ok {
			return claimer.ClaimUnmatchedMixedByFolderAndPathPrefix(ctx, folderID, pathPrefix, w.batchSize, attemptBefore)
		}
		return nil, fmt.Errorf("mixed-library file claimer is not configured")
	case scopedFallbackNonSeries:
		if claimer, ok := w.fileLister.(NonSeriesFileClaimer); ok {
			return claimer.ClaimUnmatchedNonSeriesByFolderAndPathPrefix(ctx, folderID, pathPrefix, w.batchSize, attemptBefore)
		}
		return nil, fmt.Errorf("non-series file claimer is not configured")
	default:
		return w.fileLister.ClaimUnmatchedByFolderAndPathPrefix(ctx, folderID, pathPrefix, w.batchSize, attemptBefore)
	}
}

func selectRepresentativeGroupFile(groupFiles []*models.MediaFile) *models.MediaFile {
	var (
		firstUnlinked *models.MediaFile
		firstAny      *models.MediaFile
	)
	for _, file := range groupFiles {
		if file == nil {
			continue
		}
		if firstAny == nil || file.ID < firstAny.ID {
			firstAny = file
		}
		if strings.TrimSpace(file.ContentID) == "" && (firstUnlinked == nil || file.ID < firstUnlinked.ID) {
			firstUnlinked = file
		}
	}
	if firstUnlinked != nil {
		return firstUnlinked
	}
	return firstAny
}

func hasUnlinkedGroupFile(groupFiles []*models.MediaFile) bool {
	for _, file := range groupFiles {
		if file != nil && strings.TrimSpace(file.ContentID) == "" {
			return true
		}
	}
	return false
}

func truncateSeriesQueueError(errText string) string {
	errText = strings.TrimSpace(errText)
	if len(errText) <= 1024 {
		return errText
	}
	return errText[:1024]
}

func isTVLibraryType(folderType string) bool {
	switch strings.ToLower(strings.TrimSpace(folderType)) {
	case "series", "tv", "show", "tvshows":
		return true
	default:
		return false
	}
}

func isMixedLibraryType(folderType string) bool {
	return strings.ToLower(strings.TrimSpace(folderType)) == "mixed"
}

func isMovieLibraryType(folderType string) bool {
	switch strings.ToLower(strings.TrimSpace(folderType)) {
	case "movie", "movies", "mixed":
		return true
	default:
		return false
	}
}

// RetryUnmatchedItemsByFolderAndPathPrefix revisits linked unmatched items in
// scope once. Per-item retry failures are counted as warnings, not fatal.
func (w *MatchWorker) RetryUnmatchedItemsByFolderAndPathPrefix(ctx context.Context, folderID int, pathPrefix string) (retried int, stillUnmatched int, err error) {
	if w.service == nil {
		return 0, 0, fmt.Errorf("metadata match worker requires a service")
	}
	if w.itemLister == nil {
		return 0, 0, fmt.Errorf("metadata match worker requires an item lister")
	}

	contentIDs, err := w.itemLister.ListUnmatchedByFolderAndPathPrefix(ctx, folderID, pathPrefix, 0)
	if err != nil {
		return 0, 0, err
	}

	for _, contentID := range contentIDs {
		if ctx.Err() != nil {
			return retried, stillUnmatched, ctx.Err()
		}

		retried++
		result, processErr := w.service.Process(ctx, ProcessRequest{
			ContentID: contentID,
			FolderID:  formatFolderID(folderID),
			Mode:      ModeScheduledRefresh,
		})
		if processErr != nil {
			stillUnmatched++
			slog.Warn("metadata: scoped retry failed",
				"content_id", contentID,
				"folder_id", folderID,
				"path_prefix", pathPrefix,
				"error", processErr)
			continue
		}
		if result == nil || !result.Updated {
			stillUnmatched++
		}
	}

	if repaired, repairErr := w.service.repairMatchedDuplicateProviderOwnersByFolderAndPathPrefix(ctx, folderID, pathPrefix); repairErr != nil {
		return retried, stillUnmatched, repairErr
	} else if repaired > 0 {
		retried += repaired
		slog.Info("metadata: repaired matched duplicate items in scope",
			"folder_id", folderID,
			"path_prefix", pathPrefix,
			"repaired_items", repaired,
		)
	}

	return retried, stillUnmatched, nil
}

func formatFolderID(id int) string {
	return strconv.Itoa(id)
}
