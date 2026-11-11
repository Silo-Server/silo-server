package scanner

import (
	"context"
	"fmt"
	"log/slog"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Silo-Server/silo-server/internal/models"
)

// defaultFullScanBatchSize is how many files of a library root a full scan
// holds per-file state for at a time: their scan-state rows, item statuses and
// root and group assignments. A root's memory use then follows the batch, not
// the root's size. Workers may still be finishing one batch while the next is
// loaded, so up to two batches are held at once.
const defaultFullScanBatchSize = 5000

func (s *Scanner) fullScanBatchSize() int {
	if s.scanBatchSize > 0 {
		return s.scanBatchSize
	}
	return defaultFullScanBatchSize
}

// scanBatch is the per-file state of one batch of a scope's primary files.
type scanBatch struct {
	existingByPath   map[string]*scanStateFile
	contentStatuses  map[string]string
	rootAssignments  map[string]fileRootAssignment
	groupAssignments map[string]fileGroupAssignment
}

type scanJob struct {
	path  string
	batch *scanBatch
}

// scopeRun is the state one scope's file processing shares across batches: the
// root and group tallies that span the whole scope, and the counters.
type scopeRun struct {
	folder         *models.MediaFolder
	scope          string
	totalFiles     int
	roots          *scanRootInference
	groups         *scanGroupInference
	groupOverrides map[string]models.MediaGroupOverride
	subtitleCache  *externalSubtitleDirCache

	newCount, updatedCount, unchangedCount, errorCount, processedCount atomic.Int64
}

// newScopeRun loads the scope's overrides and starts its inference.
func (s *Scanner) newScopeRun(
	ctx context.Context,
	folder *models.MediaFolder,
	reconcileRoots []string,
	totalFiles int,
) (*scopeRun, error) {
	rootOverrides, err := s.loadRootOverrides(ctx, folder.ID, reconcileRoots)
	if err != nil {
		return nil, fmt.Errorf("loading root overrides: %w", err)
	}
	identityOverrides, err := s.loadIdentityOverrides(ctx, folder.ID)
	if err != nil {
		return nil, fmt.Errorf("loading identity overrides: %w", err)
	}
	groupOverrides, err := s.loadGroupOverrides(ctx, folder.ID)
	if err != nil {
		return nil, fmt.Errorf("loading group overrides: %w", err)
	}
	return &scopeRun{
		folder:         folder,
		scope:          firstScope(reconcileRoots),
		totalFiles:     totalFiles,
		roots:          newScanRootInference(folder.Type, folder.ID, rootOverrides, folder.Paths...),
		groups:         newScanGroupInference(folder.Type, folder.ID, identityOverrides),
		groupOverrides: groupOverrides,
		subtitleCache:  newExternalSubtitleDirCache(folder.Type),
	}, nil
}

// inference returns the scope's root and group snapshots, covering every batch
// loaded so far.
func (run *scopeRun) inference() (rootInferenceResult, groupInferenceResult) {
	groupInference := run.groups.result()
	applyGroupOverrides(&groupInference, run.groupOverrides)
	return run.roots.result(), groupInference
}

func (run *scopeRun) progress(phase, message string) ProgressUpdate {
	return ProgressUpdate{
		Phase:           phase,
		Message:         message,
		CurrentScope:    run.scope,
		TotalFiles:      run.totalFiles,
		FilesDiscovered: run.totalFiles,
		FilesProcessed:  int(run.processedCount.Load()),
		New:             int(run.newCount.Load()),
		Updated:         int(run.updatedCount.Load()),
		Unchanged:       int(run.unchangedCount.Load()),
		Errors:          int(run.errorCount.Load()),
	}
}

// processPrimaryPaths runs the scope's primary files through processFile, in
// walk order and a batch at a time. Each batch's scan state is read just
// before its files are processed and dropped once they are done.
func (s *Scanner) processPrimaryPaths(ctx context.Context, run *scopeRun, primaryPaths []string) error {
	var wg sync.WaitGroup
	jobs := make(chan scanJob, s.workerCount())
	for range s.workerCount() {
		wg.Go(func() {
			for job := range jobs {
				if ctx.Err() != nil {
					continue
				}
				s.processScanJob(ctx, run, job)
			}
		})
	}
	stopProgress := run.logProgress(ctx)

	var loadErr error
	for paths := range slices.Chunk(primaryPaths, s.fullScanBatchSize()) {
		if ctx.Err() != nil {
			break
		}
		batch, err := s.loadScanBatch(ctx, run, paths)
		if err != nil {
			// A read the cancellation cut short is that cancellation, which
			// the caller reports; it is not a failed scan.
			if ctx.Err() == nil {
				loadErr = err
			}
			break
		}
		for _, path := range paths {
			jobs <- scanJob{path: path, batch: batch}
		}
	}
	close(jobs)
	wg.Wait()
	stopProgress()
	return loadErr
}

// loadScanBatch infers the roots and groups of one batch of files, adding them
// to the scope's tallies, and reads the batch's cataloged scan state.
func (s *Scanner) loadScanBatch(ctx context.Context, run *scopeRun, paths []string) (*scanBatch, error) {
	rootAssignments := run.roots.add(paths)
	s.logRootInferenceDisagreements(rootAssignments)
	groupAssignments := run.groups.add(paths, rootAssignments)

	existing, err := s.fileRepo.GetScanStateByFolderAndPaths(ctx, run.folder.ID, paths)
	if err != nil {
		return nil, fmt.Errorf("getting existing files for folder %d path %q: %w", run.folder.ID, run.scope, err)
	}
	existingByPath := make(map[string]*scanStateFile, len(existing))
	for _, f := range existing {
		existingByPath[f.FilePath] = f
	}
	contentStatuses, err := s.itemRepo.GetStatusByIDs(ctx, collectScanStateContentIDs(existing))
	if err != nil {
		return nil, fmt.Errorf("loading item statuses for folder %d path %q: %w", run.folder.ID, run.scope, err)
	}
	return &scanBatch{
		existingByPath:   existingByPath,
		contentStatuses:  contentStatuses,
		rootAssignments:  rootAssignments,
		groupAssignments: groupAssignments,
	}, nil
}

func (s *Scanner) processScanJob(ctx context.Context, run *scopeRun, job scanJob) {
	path, batch := job.path, job.batch
	action, updateReasons, processErr := s.processFile(ctx, path, run.folder, batch.existingByPath, batch.contentStatuses, batch.rootAssignments[path], batch.groupAssignments[path], run.subtitleCache)
	if processErr != nil {
		slog.ErrorContext(ctx, "scanner: file processing failed", "component", "scanner", "path", path, "error", processErr)
		run.errorCount.Add(1)
		return
	}
	switch action {
	case actionNew:
		run.newCount.Add(1)
		slog.DebugContext(ctx, "scanner: new file added", "component", "scanner", "path", path)
	case actionUpdated:
		run.updatedCount.Add(1)
		slog.DebugContext(ctx, "scanner: file updated", "component", "scanner", "path", path, "reasons", updateReasons)
	case actionUnchanged:
		run.unchangedCount.Add(1)
	}
	run.processedCount.Add(1)
}

// logProgress reports the scope's processing progress on an interval until the
// returned function is called.
func (run *scopeRun) logProgress(ctx context.Context) (stop func()) {
	stopProgress := make(chan struct{})
	progressDone := make(chan struct{})
	go func() {
		ticker := time.NewTicker(scanProgressLogInterval)
		defer ticker.Stop()
		defer close(progressDone)
		for {
			select {
			case <-ctx.Done():
				return
			case <-stopProgress:
				return
			case <-ticker.C:
				update := run.progress("processing", "Processing discovered files")
				slog.InfoContext(ctx, "scanner: processing progress", "component", "scanner",
					"folder_id", run.folder.ID,
					"scope", run.scope,
					"processed", update.FilesProcessed,
					"total", update.TotalFiles,
					"new", update.New,
					"updated", update.Updated,
					"unchanged", update.Unchanged,
					"errors", update.Errors,
				)
				reportProgress(ctx, update)
			}
		}
	}()
	return func() {
		close(stopProgress)
		<-progressDone
	}
}

// processScopeExtras ingests a scope's extras and adds the outcome to result.
// A canceled scan leaves them alone.
func (s *Scanner) processScopeExtras(
	ctx context.Context,
	folder *models.MediaFolder,
	extras []extraCandidate,
	sidecars SidecarListing,
	result *ScanResult,
) error {
	if scanCanceled(ctx) {
		return nil
	}
	existingByPath, err := s.loadExtraScanState(ctx, folder.ID, extras)
	if err != nil {
		// A read the cancellation cut short is that cancellation, which the
		// caller reports; it is not a failed scan.
		return errUnlessCanceled(ctx, err)
	}
	if scanCanceled(ctx) {
		return nil
	}
	stats :=s.processExtraFiles(ctx, folder, extras, existingByPath, sidecars)
	result.New += stats.New
	result.Updated += stats.Updated
	result.Unchanged += stats.Unchanged
	result.Errors += stats.Errors
	return nil
}

// scanCanceled reports whether the scan's context has been canceled.
func scanCanceled(ctx context.Context) bool {
	return ctx.Err() != nil
}

// errUnlessCanceled returns err, or nil when the scan was canceled: an error
// the cancellation caused is reported by the caller as that cancellation.
func errUnlessCanceled(ctx context.Context, err error) error {
	if scanCanceled(ctx) {
		return nil
	}
	return err
}

// loadExtraScanState reads the cataloged scan state of a scope's extras.
// Extras are processed together after the primary files, because binding one
// to its parent looks at the scope's other extras, so their rows are held
// together too; a library has far fewer of them than primary files.
func (s *Scanner) loadExtraScanState(ctx context.Context, folderID int, extras []extraCandidate) (map[string]*scanStateFile, error) {
	existingByPath := make(map[string]*scanStateFile)
	if s.extraRepo == nil {
		return existingByPath, nil
	}
	paths := make([]string, 0, len(extras))
	for _, candidate := range extras {
		paths = append(paths, candidate.Path)
	}
	for chunk := range slices.Chunk(paths, s.fullScanBatchSize()) {
		existing, err := s.fileRepo.GetScanStateByFolderAndPaths(ctx, folderID, chunk)
		if err != nil {
			return nil, fmt.Errorf("getting existing extras for folder %d: %w", folderID, err)
		}
		for _, f := range existing {
			existingByPath[f.FilePath] = f
		}
	}
	return existingByPath, nil
}
