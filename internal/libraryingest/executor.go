package libraryingest

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

	"github.com/Silo-Server/silo-server/internal/cache"
	"github.com/Silo-Server/silo-server/internal/librarykind"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/notifications"
	"github.com/Silo-Server/silo-server/internal/scanner"
)

// Scanner executes the scan phase for a library scope.
type Scanner interface {
	ScanFolder(ctx context.Context, folder *models.MediaFolder) (*scanner.ScanResult, error)
	ScanSubtree(ctx context.Context, folder *models.MediaFolder, subtreePath string) (*scanner.ScanResult, error)
	ScanFile(ctx context.Context, filePath string, folder *models.MediaFolder) error
	FinalizeVariantsByPathPrefix(ctx context.Context, folder *models.MediaFolder, pathPrefix string) error
	ObserveFileRoot(ctx context.Context, folderID int, filePath, libraryType string, libraryRoots ...string) (scanner.RootObservation, bool, error)
}

// Matcher drains unmatched files and retries linked unmatched items.
type Matcher interface {
	ProcessBatchByFolderAndPathPrefix(ctx context.Context, folderID int, pathPrefix string, attemptBefore time.Time) (processed int, err error)
	ProcessAllByFolderAndPathPrefix(ctx context.Context, folderID int, pathPrefix string, attemptBefore time.Time) (processed int, err error)
	RetryUnmatchedItemsByFolderAndPathPrefix(ctx context.Context, folderID int, pathPrefix string) (retried int, stillUnmatched int, err error)
}

// FolderRepository provides scan bookkeeping for library ingest.
type FolderRepository interface {
	UpdateLastScanned(ctx context.Context, id int, scannedAt time.Time) error
}

// SkippedRootRepository persists and reconciles skipped roots discovered by scans.
type SkippedRootRepository interface {
	Upsert(ctx context.Context, root models.SkippedMediaRoot) error
	Delete(ctx context.Context, folderID int, rootPath string) error
	DeleteMissingByFolder(ctx context.Context, folderID int, seenRoots []string) error
	DeleteMissingInScope(ctx context.Context, folderID int, scopePath string, seenRoots []string) error
}

// Result captures the outcome of a full ingest run for one scope.
type Result struct {
	ScanResult             *scanner.ScanResult
	MatchedFiles           int
	RetriedItems           int
	StillUnmatchedWarnings int
	ScanDuration           time.Duration
	MatchDuration          time.Duration
	RetryDuration          time.Duration
}

type scopeMode string

const (
	scopeModeLibrary scopeMode = "library"
	scopeModeSubtree scopeMode = "subtree"
	scopeModeFile    scopeMode = "file"
)

type scopeClaim struct {
	folderID int
	mode     scopeMode
	path     string
}

const scopedDrainInterval = 2 * time.Second
const scopedTVDrainSettleWindow = 11 * time.Second

// runningClaim pairs a scope claim with an optional cancel function so that
// running and waiting scans can be canceled from the outside (e.g. via admin
// API).
type runningClaim struct {
	scopeClaim
	cancel context.CancelFunc
	// done is closed when the claim leaves the executor: when its scan
	// finishes, or when it stops waiting without scanning.
	done chan struct{}
}

// Executor coordinates scan, scoped matching, retry, and completion events.
type Executor struct {
	scanner         Scanner
	matcher         Matcher
	folders         FolderRepository
	skippedRootRepo SkippedRootRepository
	events          cache.EventBus
	realtime        *notifications.Hub
	availability    *notifications.AvailabilityDetector
	now             func() time.Time

	// tvDrainSettleWindow overrides scopedTVDrainSettleWindow when > 0. Kept
	// injectable so tests can exercise the settle-window shutdown path without
	// waiting the full production interval.
	tvDrainSettleWindow time.Duration

	mu      sync.Mutex
	running []*runningClaim
	// waiting holds claims blocked behind an overlapping claim, in arrival
	// order, so CancelLibrary reaches them before they start scanning.
	waiting []*runningClaim
}

// NewExecutor creates a new ingest executor.
func NewExecutor(
	scanner Scanner,
	matcher Matcher,
	folders FolderRepository,
	skippedRootRepo SkippedRootRepository,
	events cache.EventBus,
	realtime *notifications.Hub,
) *Executor {
	return &Executor{
		scanner:         scanner,
		matcher:         matcher,
		folders:         folders,
		skippedRootRepo: skippedRootRepo,
		events:          events,
		realtime:        realtime,
		now:             time.Now,
	}
}

// SetAvailabilityDetector wires episode-availability detection for release
// notifications. Optional; runs after matching completes and never blocks or
// fails the ingest.
func (e *Executor) SetAvailabilityDetector(detector *notifications.AvailabilityDetector) {
	if e != nil {
		e.availability = detector
	}
}

// IngestFolder runs the full ingest workflow for an entire library.
func (e *Executor) IngestFolder(ctx context.Context, folder *models.MediaFolder) (*Result, error) {
	return e.ingest(ctx, folder, scopeModeLibrary, "")
}

// IngestSubtree runs the full ingest workflow for one subtree.
func (e *Executor) IngestSubtree(ctx context.Context, folder *models.MediaFolder, subtreePath string) (*Result, error) {
	return e.ingest(ctx, folder, scopeModeSubtree, subtreePath)
}

// IngestFile runs the full ingest workflow for one file path.
func (e *Executor) IngestFile(ctx context.Context, folder *models.MediaFolder, filePath string) (*Result, error) {
	return e.ingest(ctx, folder, scopeModeFile, filePath)
}

func (e *Executor) ingest(ctx context.Context, folder *models.MediaFolder, mode scopeMode, rawPath string) (*Result, error) {
	if e == nil || e.scanner == nil || e.matcher == nil || folder == nil {
		return nil, fmt.Errorf("library ingest executor is not fully configured")
	}

	// Wrap the caller's context so we can cancel from CancelLibrary.
	scanCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	claim := scopeClaim{
		folderID: folder.ID,
		mode:     mode,
		path:     cleanScopePath(rawPath),
	}
	matchScopes := scopeMatchPaths(folder, mode, claim.path)
	entry, err := e.begin(scanCtx, claim, cancel)
	if err != nil {
		return nil, err
	}
	defer e.finish(entry)

	var (
		concurrentMatched       atomic.Int64
		concurrentMatchDuration atomic.Int64
	)
	reportProgress(scanCtx, ProgressUpdate{
		Phase:        "preparing",
		Message:      "Preparing scan",
		CurrentScope: claim.path,
	})
	runStartedAt := time.Now().UTC()
	drainerStopCtx, stopDrainers := context.WithCancel(context.Background())
	defer stopDrainers()
	drainerErrCh := make(chan error, 1)
	var drainerWG sync.WaitGroup
	if len(matchScopes) > 0 {
		slog.InfoContext(ctx, "library ingest: concurrent scoped matching started", "component", "libraryingest",
			"folder_id", folder.ID,
			"mode", mode,
			"scope_count", len(matchScopes),
		)
		for _, scopePath := range matchScopes {
			scopePath := scopePath
			drainerWG.Go(func() {
				ticker := time.NewTicker(scopedDrainInterval)
				defer ticker.Stop()
				for {
					select {
					case <-scanCtx.Done():
						return
					case <-drainerStopCtx.Done():
						return
					default:
					}
					started := time.Now()
					processed, err := e.matcher.ProcessBatchByFolderAndPathPrefix(scanCtx, folder.ID, scopePath, runStartedAt)
					concurrentMatchDuration.Add(time.Since(started).Nanoseconds())
					if processed > 0 {
						concurrentMatched.Add(int64(processed))
					}
					if err != nil {
						if scanCtx.Err() != nil {
							return
						}
						select {
						case drainerErrCh <- fmt.Errorf("concurrent match scope %q: %w", scopePath, err):
						default:
						}
						cancel()
						return
					}
					select {
					case <-scanCtx.Done():
						return
					case <-drainerStopCtx.Done():
						return
					case <-ticker.C:
					}
				}
			})
		}
	}

	scanProgressCtx := scanner.WithProgressReporter(scanCtx, func(update scanner.ProgressUpdate) {
		reportProgress(scanCtx, ProgressUpdate{
			Phase:           update.Phase,
			Message:         update.Message,
			CurrentScope:    update.CurrentScope,
			TotalFiles:      update.TotalFiles,
			FilesDiscovered: update.FilesDiscovered,
			FilesProcessed:  update.FilesProcessed,
			New:             update.New,
			Updated:         update.Updated,
			Unchanged:       update.Unchanged,
			Errors:          update.Errors,
		})
	})
	scanStarted := time.Now()
	scanMatchScopes, scanResult, err := e.scan(scanProgressCtx, folder, mode, claim.path)
	if err != nil {
		cancel()
		stopDrainers()
		drainerWG.Wait()
		if len(matchScopes) > 0 {
			slog.InfoContext(ctx, "library ingest: concurrent scoped matching stopped", "component", "libraryingest",
				"folder_id", folder.ID,
				"mode", mode,
				"scope_count", len(matchScopes),
				"matched_files", concurrentMatched.Load(),
			)
		}
		select {
		case drainerErr := <-drainerErrCh:
			return nil, drainerErr
		default:
		}
		return nil, err
	}
	matchScopes = scanMatchScopes
	if shouldWaitForTVQueueSettle(folder, scanResult) {
		settleWindow := e.tvDrainSettleWindow
		if settleWindow <= 0 {
			settleWindow = scopedTVDrainSettleWindow
		}
		slog.InfoContext(ctx, "library ingest: waiting for tv queue settle window", "component", "libraryingest",
			"folder_id", folder.ID,
			"mode", mode,
			"wait", settleWindow,
		)
		timer := time.NewTimer(settleWindow)
		select {
		case <-scanCtx.Done():
			timer.Stop()
			return nil, scanCtx.Err()
		case drainerErr := <-drainerErrCh:
			timer.Stop()
			return nil, drainerErr
		case <-timer.C:
		}
	}
	stopDrainers()
	drainerWG.Wait()
	if len(matchScopes) > 0 {
		slog.InfoContext(ctx, "library ingest: concurrent scoped matching stopped", "component", "libraryingest",
			"folder_id", folder.ID,
			"mode", mode,
			"scope_count", len(matchScopes),
			"matched_files", concurrentMatched.Load(),
		)
	}
	select {
	case drainerErr := <-drainerErrCh:
		return nil, drainerErr
	default:
	}
	if err := e.reconcileSkippedRoots(scanCtx, folder.ID, folder.Type, mode, claim.path, matchScopes, scanResult, folder.Paths...); err != nil {
		return nil, err
	}

	result := &Result{
		ScanResult:    scanResult,
		ScanDuration:  time.Since(scanStarted),
		MatchedFiles:  int(concurrentMatched.Load()),
		MatchDuration: time.Duration(concurrentMatchDuration.Load()),
	}
	reportProgress(scanCtx, ProgressUpdate{
		Phase:        "matching",
		Message:      "Matching unmatched items",
		CurrentScope: claim.path,
		MatchedFiles: result.MatchedFiles,
		FilesDiscovered: scanResultCount(scanResult, func(value *scanner.ScanResult) int {
			return value.New + value.Updated + value.Unchanged
		}),
	})
	for _, scopePath := range matchScopes {
		if !usesDedicatedEnrichment(folder.Type) {
			matchStarted := time.Now()
			matched, err := e.matcher.ProcessAllByFolderAndPathPrefix(scanCtx, folder.ID, scopePath, runStartedAt)
			result.MatchDuration += time.Since(matchStarted)
			result.MatchedFiles += matched
			reportProgress(scanCtx, ProgressUpdate{
				Phase:        "matching",
				Message:      "Matching unmatched items",
				CurrentScope: scopePath,
				MatchedFiles: result.MatchedFiles,
			})
			if err != nil {
				return result, fmt.Errorf("match scope %q: %w", scopePath, err)
			}

			retryStarted := time.Now()
			retried, stillUnmatched, err := e.matcher.RetryUnmatchedItemsByFolderAndPathPrefix(scanCtx, folder.ID, scopePath)
			result.RetryDuration += time.Since(retryStarted)
			result.RetriedItems += retried
			result.StillUnmatchedWarnings += stillUnmatched
			reportProgress(scanCtx, ProgressUpdate{
				Phase:        "retrying",
				Message:      "Retrying unmatched items",
				CurrentScope: scopePath,
				MatchedFiles: result.MatchedFiles,
				RetriedItems: result.RetriedItems,
			})
			if err != nil {
				return result, fmt.Errorf("retry scope %q: %w", scopePath, err)
			}
		}
		if err := e.scanner.FinalizeVariantsByPathPrefix(scanCtx, folder, scopePath); err != nil {
			return result, fmt.Errorf("finalize variants for scope %q: %w", scopePath, err)
		}
	}

	if mode == scopeModeLibrary && e.folders != nil {
		if err := e.folders.UpdateLastScanned(scanCtx, folder.ID, e.now().UTC()); err != nil {
			return result, fmt.Errorf("update last scanned: %w", err)
		}
	}

	// Content availability runs after matching/reconcile so releases are tied
	// to resolved items. It runs detached: the detector is best-effort with
	// its own deadline (it detaches from scanCtx internally, surviving its
	// cancellation), and a slow pass must not delay scan completion or the
	// serialized scan queue.
	if e.availability != nil {
		lk := librarykind.Of(folder.Type)
		// Audiobook/ebook are dedicated library types, never part of "mixed"
		// (the scanner routes mixed folders through the video pipeline only).
		kinds := notifications.AvailabilityKinds{
			Episodes:   lk.TV || lk.Mixed,
			Movies:     lk.Movie || lk.Mixed,
			Audiobooks: lk.Audiobook,
			Ebooks:     lk.Ebook,
		}
		if kinds.Any() {
			go e.availability.HandleIngestCompleted(scanCtx, folder.ID, mode == scopeModeLibrary, matchScopes, kinds)
		}
	}

	if shouldPublish(result) && e.events != nil {
		if err := e.events.Publish(scanCtx, cache.ChannelCatalog, cache.Event{
			Type:    cache.EventScanComplete,
			Payload: strconv.Itoa(folder.ID),
		}); err != nil {
			return result, fmt.Errorf("publish scan complete: %w", err)
		}
	}
	if shouldPublish(result) && e.realtime != nil {
		if err := e.realtime.PublishCatalogLibraryChanged(scanCtx, notifications.LibraryChangeEvent{
			LibraryID:              folder.ID,
			Reason:                 "scan",
			New:                    scanResultCount(scanResult, func(value *scanner.ScanResult) int { return value.New }),
			Updated:                scanResultCount(scanResult, func(value *scanner.ScanResult) int { return value.Updated }),
			Missing:                scanResultCount(scanResult, func(value *scanner.ScanResult) int { return value.Missing }),
			MatchedFiles:           result.MatchedFiles,
			RetriedItems:           result.RetriedItems,
			StillUnmatchedWarnings: result.StillUnmatchedWarnings,
		}); err != nil {
			return result, fmt.Errorf("publish realtime library change: %w", err)
		}
	}

	reportProgress(scanCtx, ProgressUpdate{
		Phase:        "completed",
		Message:      "Scan completed",
		CurrentScope: claim.path,
		MatchedFiles: result.MatchedFiles,
		RetriedItems: result.RetriedItems,
		New:          scanResultCount(scanResult, func(value *scanner.ScanResult) int { return value.New }),
		Updated:      scanResultCount(scanResult, func(value *scanner.ScanResult) int { return value.Updated }),
		Unchanged:    scanResultCount(scanResult, func(value *scanner.ScanResult) int { return value.Unchanged }),
		Errors:       scanResultCount(scanResult, func(value *scanner.ScanResult) int { return value.Errors }),
	})

	slog.InfoContext(ctx, "library ingest: completed", "component", "libraryingest",
		"folder_id", folder.ID,
		"mode", mode,
		"scope", claim.path,
		"scan_duration", result.ScanDuration,
		"match_duration", result.MatchDuration,
		"retry_duration", result.RetryDuration,
		"matched_files", result.MatchedFiles,
		"retried_items", result.RetriedItems,
		"still_unmatched_warnings", result.StillUnmatchedWarnings,
	)

	return result, nil
}

func scanResultCount(result *scanner.ScanResult, getter func(*scanner.ScanResult) int) int {
	if result == nil || getter == nil {
		return 0
	}
	return getter(result)
}

func scopeMatchPaths(folder *models.MediaFolder, mode scopeMode, scopePath string) []string {
	if folder == nil {
		return nil
	}
	// Audiobook/podcast/ebook/manga libraries use scanner-driven grouping where
	// the scanner assigns content_ids by folder root. Running the concurrent
	// match drainer while the scan writes files causes per-file content_ids to
	// be created for unlinked files. Skip concurrent matching; the post-scan
	// drain handles these libraries correctly.
	switch strings.ToLower(strings.TrimSpace(folder.Type)) {
	case "audiobook", "audiobooks", "podcast", "podcasts", "ebook", "ebooks", "manga", "comics":
		return nil
	}
	switch mode {
	case scopeModeLibrary:
		return cleanRoots(folder.Paths)
	case scopeModeSubtree, scopeModeFile:
		if strings.TrimSpace(scopePath) == "" {
			return nil
		}
		return []string{cleanScopePath(scopePath)}
	default:
		return nil
	}
}

func usesDedicatedEnrichment(folderType string) bool {
	return librarykind.Of(folderType).Ebook
}

func shouldWaitForTVQueueSettle(folder *models.MediaFolder, scanResult *scanner.ScanResult) bool {
	if folder == nil || (!librarykind.IsTV(folder.Type) && !librarykind.IsMixed(folder.Type)) {
		return false
	}
	if scanResult == nil {
		return false
	}
	return scanResult.New > 0 || scanResult.Updated > 0
}

func (e *Executor) scan(ctx context.Context, folder *models.MediaFolder, mode scopeMode, scopePath string) ([]string, *scanner.ScanResult, error) {
	switch mode {
	case scopeModeLibrary:
		result, err := e.scanner.ScanFolder(ctx, folder)
		if err != nil {
			return nil, nil, fmt.Errorf("scan folder: %w", err)
		}
		return cleanRoots(folder.Paths), result, nil
	case scopeModeSubtree:
		result, err := e.scanner.ScanSubtree(ctx, folder, scopePath)
		if err != nil {
			return nil, nil, fmt.Errorf("scan subtree: %w", err)
		}
		return []string{scopePath}, result, nil
	case scopeModeFile:
		if err := e.scanner.ScanFile(ctx, scopePath, folder); err != nil {
			return nil, nil, fmt.Errorf("scan file: %w", err)
		}
		return []string{scopePath}, nil, nil
	default:
		return nil, nil, fmt.Errorf("unsupported ingest mode %q", mode)
	}
}

// reconcileSkippedRoots persists and prunes skipped-root records for admin
// visibility. Note: skipped roots are now diagnostic-only — they no longer
// gate scanning or matching behavior. Files under roots without embedded
// provider IDs still enter the match queue with status "pending".
func (e *Executor) reconcileSkippedRoots(
	ctx context.Context,
	folderID int,
	folderType string,
	mode scopeMode,
	scopePath string,
	matchScopes []string,
	scanResult *scanner.ScanResult,
	libraryRoots ...string,
) error {
	if e == nil || e.skippedRootRepo == nil {
		return nil
	}

	observations := make([]scanner.RootObservation, 0)
	switch {
	case scanResult != nil:
		observations = append(observations, scanResult.RootObservations...)
	case mode == scopeModeFile:
		observation, ok, err := e.scanner.ObserveFileRoot(ctx, folderID, scopePath, folderType, libraryRoots...)
		if err != nil {
			return fmt.Errorf("observe root of %q: %w", scopePath, err)
		}
		if ok {
			observations = append(observations, observation)
		}
	}

	seenRootsByScope := make(map[string][]string, len(matchScopes))
	for _, scope := range matchScopes {
		seenRootsByScope[scope] = []string{}
	}
	seenSkippedRoots := make([]string, 0, len(observations))

	for _, observation := range observations {
		if observation.HasProviderIDs {
			if err := e.skippedRootRepo.Delete(ctx, folderID, observation.RootPath); err != nil {
				return fmt.Errorf("clear skipped root %q: %w", observation.RootPath, err)
			}
		} else {
			if err := e.skippedRootRepo.Upsert(ctx, models.SkippedMediaRoot{
				MediaFolderID:  folderID,
				RootPath:       observation.RootPath,
				Reason:         observation.Reason,
				SampleFilePath: observation.SampleFilePath,
				FileCount:      observation.FileCount,
			}); err != nil {
				return fmt.Errorf("upsert skipped root %q: %w", observation.RootPath, err)
			}
			seenSkippedRoots = append(seenSkippedRoots, observation.RootPath)
		}

		for _, scope := range matchScopes {
			if isSameOrDescendant(scope, observation.RootPath) {
				seenRootsByScope[scope] = append(seenRootsByScope[scope], observation.RootPath)
			}
		}
	}

	if mode == scopeModeLibrary {
		if err := e.skippedRootRepo.DeleteMissingByFolder(ctx, folderID, seenSkippedRoots); err != nil {
			return fmt.Errorf("delete stale skipped roots for folder %d: %w", folderID, err)
		}
		return nil
	}

	for _, scope := range matchScopes {
		if err := e.skippedRootRepo.DeleteMissingInScope(ctx, folderID, scope, seenRootsByScope[scope]); err != nil {
			return fmt.Errorf("delete stale skipped roots in scope %q: %w", scope, err)
		}
	}

	return nil
}

// begin records claim as running. When claim overlaps a scan already running
// in this process, it waits for that scan to finish first rather than skipping:
// the caller asked to observe the filesystem after its own signal, and the
// running scan may already have walked past the change. Overlapping waiters
// start in arrival order. While waiting the claim is registered for
// CancelLibrary, and it returns ctx's error if ctx ends first. A waiter waits
// only on running scans, which make progress independently, or on an older
// waiter, so waits cannot form a cycle.
func (e *Executor) begin(ctx context.Context, claim scopeClaim, cancel context.CancelFunc) (*runningClaim, error) {
	entry := &runningClaim{scopeClaim: claim, cancel: cancel, done: make(chan struct{})}

	e.mu.Lock()
	blocker := e.blockerLocked(entry)
	if blocker == nil {
		e.running = append(e.running, entry)
		e.mu.Unlock()
		return entry, nil
	}
	e.waiting = append(e.waiting, entry)
	e.mu.Unlock()

	slog.InfoContext(ctx, "library ingest: waiting for overlapping scan to finish", "component", "libraryingest",
		"folder_id", claim.folderID,
		"mode", claim.mode,
		"scope", claim.path,
		"blocking_mode", blocker.mode,
		"blocking_scope", blocker.path,
	)
	reportProgress(ctx, ProgressUpdate{
		Phase:        "preparing",
		Message:      "Waiting for an overlapping scan to finish",
		CurrentScope: claim.path,
	})

	for {
		select {
		case <-ctx.Done():
		case <-blocker.done:
		}

		e.mu.Lock()
		// Re-check ctx under the lock: when the blocker finishes and ctx ends
		// together, select may pick either, and a canceled waiter must not start.
		if err := ctx.Err(); err != nil {
			e.removeWaitingLocked(entry)
			// Newer waiters may be blocked on this one; let them re-check.
			close(entry.done)
			e.mu.Unlock()
			return nil, fmt.Errorf("wait for overlapping scan: %w", err)
		}
		blocker = e.blockerLocked(entry)
		if blocker == nil {
			// done stays open: newer waiters blocked on this claim keep waiting
			// until its scan finishes.
			e.removeWaitingLocked(entry)
			e.running = append(e.running, entry)
			e.mu.Unlock()
			slog.InfoContext(ctx, "library ingest: overlapping scan finished, starting", "component", "libraryingest",
				"folder_id", claim.folderID,
				"mode", claim.mode,
				"scope", claim.path,
			)
			return entry, nil
		}
		e.mu.Unlock()
	}
}

// blockerLocked returns the claim entry has to wait for: a running claim that
// overlaps it, or an overlapping claim that started waiting before it. e.mu
// must be held.
func (e *Executor) blockerLocked(entry *runningClaim) *runningClaim {
	for _, running := range e.running {
		if conflicts(running.scopeClaim, entry.scopeClaim) {
			return running
		}
	}
	for _, waiting := range e.waiting {
		if waiting == entry {
			break
		}
		if conflicts(waiting.scopeClaim, entry.scopeClaim) {
			return waiting
		}
	}
	return nil
}

// removeWaitingLocked drops entry from the waiting list. e.mu must be held.
func (e *Executor) removeWaitingLocked(entry *runningClaim) {
	if i := slices.Index(e.waiting, entry); i >= 0 {
		e.waiting = slices.Delete(e.waiting, i, i+1)
	}
}

// finish releases a running claim and wakes the waiters blocked on it.
func (e *Executor) finish(entry *runningClaim) {
	e.mu.Lock()
	defer e.mu.Unlock()

	if i := slices.Index(e.running, entry); i >= 0 {
		e.running = slices.Delete(e.running, i, i+1)
	}
	close(entry.done)
}

// CancelLibrary cancels all running scans for the given library (folder ID),
// including scans still waiting for an overlapping scan to finish. Returns the
// number of scans that were canceled.
func (e *Executor) CancelLibrary(folderID int) int {
	e.mu.Lock()
	defer e.mu.Unlock()

	canceled := 0
	for _, claims := range [][]*runningClaim{e.running, e.waiting} {
		for _, claim := range claims {
			if claim.folderID == folderID && claim.cancel != nil {
				claim.cancel()
				canceled++
			}
		}
	}
	return canceled
}

// conflicts reports whether two claims must not scan at the same time in one
// process; the later claim waits for the earlier one (see begin).
func conflicts(a, b scopeClaim) bool {
	if a.folderID != b.folderID {
		return false
	}
	// Two library scans on the same folder still conflict.
	if a.mode == scopeModeLibrary && b.mode == scopeModeLibrary {
		return true
	}
	// Subtree/file scans can run alongside a library scan. The scanner
	// scopes missing-file detection to reconcileRoots and PostgreSQL
	// ON CONFLICT handles concurrent upserts safely.
	if a.mode == scopeModeLibrary || b.mode == scopeModeLibrary {
		return false
	}
	return pathsOverlap(a.path, b.path)
}

func pathsOverlap(a, b string) bool {
	return isSameOrDescendant(a, b) || isSameOrDescendant(b, a)
}

func isSameOrDescendant(ancestor, path string) bool {
	rel, err := filepath.Rel(cleanScopePath(ancestor), cleanScopePath(path))
	if err != nil {
		return false
	}
	if rel == "." {
		return true
	}
	return rel != "" && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func cleanScopePath(path string) string {
	if path == "" {
		return ""
	}
	return filepath.Clean(path)
}

func cleanRoots(paths []string) []string {
	out := make([]string, 0, len(paths))
	for _, path := range paths {
		if path == "" {
			continue
		}
		out = append(out, filepath.Clean(path))
	}
	return out
}

func shouldPublish(result *Result) bool {
	if result == nil {
		return false
	}
	if result.MatchedFiles > 0 || result.RetriedItems > 0 || result.StillUnmatchedWarnings > 0 {
		return true
	}
	if result.ScanResult == nil {
		return true
	}
	return result.ScanResult.New > 0 ||
		result.ScanResult.Updated > 0 ||
		result.ScanResult.Missing > 0 ||
		result.ScanResult.FilesDeleted > 0 ||
		result.ScanResult.MembershipsRemoved > 0 ||
		result.ScanResult.ItemsDeleted > 0
}
