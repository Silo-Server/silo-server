package scanner

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/idgen"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/titleutil"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

type filesystemRootContentFinder interface {
	FindContentIDByRootPath(ctx context.Context, folderID int, rootPath, preferredType string) (string, error)
}

type filesystemMediaItemWriter interface {
	Upsert(ctx context.Context, item *models.MediaItem) error
}

type audiobookPosterExec interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

type audiobookPosterPathReader interface {
	GetPosterPath(ctx context.Context, contentID string) (string, error)
}

const audiobookMultipartKind = "multipart"

const audiobookDuplicateCandidateSQL = `
	SELECT mi.content_id, mi.title
	FROM media_items mi
	JOIN item_people ipa ON ipa.content_id = mi.content_id AND ipa.kind = 7
	JOIN people pa ON pa.id = ipa.person_id AND LOWER(pa.name) = LOWER($1)
	JOIN item_people ipn ON ipn.content_id = mi.content_id AND ipn.kind = 8
	JOIN people pn ON pn.id = ipn.person_id AND LOWER(pn.name) = LOWER($2)
	JOIN LATERAL (
		SELECT COALESCE(SUM(mf.duration), 0) AS dur
		FROM media_files mf WHERE mf.content_id = mi.content_id
	) f ON TRUE
	JOIN LATERAL (
		SELECT btrim(regexp_replace(
			regexp_replace(lower(mi.title), '[^a-z0-9]+', ' ', 'g'),
			'[[:space:]]+', ' ', 'g'
		)) AS normalized_title
	) t ON TRUE
	WHERE mi.type = 'audiobook'
	  AND mi.year = $3
	  AND f.dur > 0
	  AND ABS(f.dur - $4) <= $5
	  AND (
		  t.normalized_title = $6
	   OR t.normalized_title LIKE $6 || ' %'
	   OR $6 LIKE t.normalized_title || ' %'
	  )
	ORDER BY ABS(f.dur - $4), LENGTH(mi.title), mi.content_id
	LIMIT 25
`

// audiobookDiskFile is the on-disk projection used by audiobookFolderUnchanged.
// Path is the absolute file path; Size and ModTime come from os.Stat.
type audiobookDiskFile struct {
	Path    string
	Size    int64
	ModTime time.Time
}

// audiobookFolderUnchanged reports whether the audio files on disk match the
// existing media_files rows for the same folder one-for-one on path, size,
// and mtime. A new file, removed file, or any byte-level / mtime drift returns
// false so the caller falls through to a full reconcile.
//
// Comparison uses sameFileModifiedAt for mtime to absorb sub-second precision
// differences between filesystem reads.
func audiobookFolderUnchanged(existing []*models.MediaFile, onDisk []audiobookDiskFile) bool {
	if len(existing) != len(onDisk) {
		return false
	}
	byPath := make(map[string]*models.MediaFile, len(existing))
	for _, mf := range existing {
		if mf == nil || mf.HasLegacyAttachedPictureVideo() {
			return false
		}
		byPath[mf.FilePath] = mf
	}
	for _, d := range onDisk {
		mf, ok := byPath[d.Path]
		if !ok {
			return false
		}
		if mf.FileSize != d.Size {
			return false
		}
		if mf.FileModifiedAt == nil || !sameFileModifiedAt(mf.FileModifiedAt, d.ModTime) {
			return false
		}
	}
	return true
}

// audiobookFolderShouldSkip returns true when every audio file on disk in
// folderPath matches an existing media_files row by size + mtime AND the
// linked media_items row is in a non-unmatched status. False under any
// drift, missing row, or unmatched status — the caller then falls through
// to the full reconcile path.
//
// Errors are returned but the worker loop treats them as "do not skip".
func (s *Scanner) audiobookFolderShouldSkip(ctx context.Context, folder *models.MediaFolder, folderPath string, allowedPaths map[string]bool) (string, bool, error) {
	if s.fileRepo == nil || s.itemRepo == nil {
		return "", false, nil
	}

	entries, err := os.ReadDir(folderPath)
	if err != nil {
		return "", false, fmt.Errorf("read folder: %w", err)
	}
	var onDisk []audiobookDiskFile
	for _, e := range entries {
		if e.IsDir() || !SupportsAudioFile(e.Name()) {
			continue
		}
		full := filepath.Join(folderPath, e.Name())
		if allowedPaths != nil && !allowedPaths[full] {
			continue
		}
		info, statErr := os.Stat(full)
		if statErr != nil {
			return "", false, fmt.Errorf("stat %s: %w", full, statErr)
		}
		onDisk = append(onDisk, audiobookDiskFile{
			Path:    full,
			Size:    info.Size(),
			ModTime: normalizeFileModifiedAt(info.ModTime()),
		})
	}
	if len(onDisk) == 0 {
		return "", false, nil
	}

	existing, err := s.fileRepo.ListByObservedRootPath(ctx, folder.ID, folderPath)
	if err != nil {
		return "", false, fmt.Errorf("list existing files: %w", err)
	}
	physical, err := canonicalWalkPath(folderPath)
	if err != nil {
		return "", false, err
	}
	for _, file := range existing {
		if file == nil || file.CanonicalRootPath != physical {
			return "", false, nil
		}
	}
	if !audiobookFolderUnchanged(existing, onDisk) {
		return "", false, nil
	}
	// A previous scan may have retired an ignored part without rebuilding the
	// remaining files' presentation. Matching sizes and mtimes do not make that
	// incomplete multipart book safe to skip.
	for _, file := range existing {
		if len(existing) == 1 {
			if file.PresentationKind == audiobookMultipartKind {
				return "", false, nil
			}
		} else if file.PresentationKind != audiobookMultipartKind || file.PresentationPartTotal != len(existing) {
			return "", false, nil
		}
	}

	contentID := existing[0].ContentID
	if contentID == "" {
		return "", false, nil
	}
	// All files must share the same content_id; fragmented folders need reconcile.
	for _, mf := range existing[1:] {
		if mf.ContentID != contentID {
			return "", false, nil
		}
	}
	items, err := s.itemRepo.GetByIDs(ctx, []string{contentID})
	if err != nil {
		return "", false, fmt.Errorf("get item for skip check: %w", err)
	}
	if len(items) == 0 || items[0] == nil {
		return "", false, nil
	}
	if strings.TrimSpace(items[0].Title) == "" {
		return "", false, nil
	}
	if strings.EqualFold(strings.TrimSpace(items[0].Status), "unmatched") {
		return "", false, nil
	}
	return contentID, true, nil
}

// audiobookScanWorkers returns the configured number of parallel workers
// for audiobook reconciliation. Defaults to 8 — high enough to keep all
// cores busy on the ffprobe step (which dominates per-book wall time)
// without overwhelming a small server. Override with SILO_AUDIOBOOK_SCAN_WORKERS.
func audiobookScanWorkers() int {
	if v := os.Getenv("SILO_AUDIOBOOK_SCAN_WORKERS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return 8
}

type audiobookRootScan struct {
	root         string
	candidates   []string
	seenPaths    map[string]bool
	rootErr      error
	walkFailures []string // logical paths the walk could not read or resolve
}

func (r *audiobookRootScan) failed() bool {
	return r.rootErr != nil || len(r.walkFailures) > 0
}

// walkAudiobookDirectories keeps catalog paths under the configured root while
// following directory symlinks. Only ancestors are tracked: aliases must retain
// their own seen paths so missing-file reconciliation does not retire them.
// Callers filter inherited patterns before resolving or entering a directory.
func walkAudiobookDirectories(ctx context.Context, path string, scan *audiobookRootScan, ancestors map[string]bool, ignoreRulesStack []ignoreRules, honorIgnores bool, readDir func(string) ([]os.DirEntry, error)) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	recordFailure := func(err error) {
		recordWalkFailure(&scan.walkFailures, path)
		slog.WarnContext(ctx, "audiobook scan: walk error", "component", "scanner", "path", path, "error", err)
	}
	canonical, err := canonicalWalkPath(path)
	if err != nil {
		recordFailure(err)
		return nil
	}
	if ancestors[canonical] {
		return nil
	}
	ancestors[canonical] = true
	defer delete(ancestors, canonical)
	entries, err := readDirectoryWithRetry(ctx, path, readDir)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		recordFailure(err)
		return nil
	}
	childRules := ignoreRulesStack
	if honorIgnores {
		var skip bool
		childRules, skip = dirIgnoreRules(ignoreRulesStack, path, path, entries)
		if skip {
			return nil
		}
	}
	directories := make([]string, 0)
	hadAudio := false
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return err
		}
		child := filepath.Join(path, entry.Name())
		if ignoreRulesMatch(childRules, child, entry.IsDir()) {
			continue
		}
		isDir := entry.IsDir()
		if entry.Type()&os.ModeSymlink != 0 {
			info, err := os.Stat(child)
			if err != nil {
				recordWalkFailure(&scan.walkFailures, child)
				slog.WarnContext(ctx, "audiobook scan: symlink stat failed", "component", "scanner", "path", child, "error", err)
				continue
			}
			isDir = info.IsDir()
			// Directory-only patterns apply once the link resolves to a directory.
			if isDir && ignoreRulesMatch(childRules, child, true) {
				continue
			}
		}
		if isDir {
			directories = append(directories, child)
		} else if SupportsAudioFile(entry.Name()) {
			scan.seenPaths[child] = true
			hadAudio = true
		}
	}
	if hadAudio {
		scan.candidates = append(scan.candidates, path)
		// Stop below a book, but loose root audio must not hide sibling books.
		if path != scan.root {
			return nil
		}
	}
	for _, directory := range directories {
		if err := walkAudiobookDirectories(ctx, directory, scan, ancestors, childRules, honorIgnores, readDir); err != nil {
			return err
		}
	}
	return nil
}

func collectAudiobookRootScans(ctx context.Context, folderID int, roots, libraryRoots []string, honorIgnores bool) ([]audiobookRootScan, error) {
	scans := make([]audiobookRootScan, 0, len(roots))
	for _, root := range roots {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		cleanRoot := filepath.Clean(strings.TrimSpace(root))
		if cleanRoot == "" || cleanRoot == "." {
			continue
		}
		scan := audiobookRootScan{
			root:      cleanRoot,
			seenPaths: make(map[string]bool),
		}
		info, statErr := os.Stat(cleanRoot)
		switch {
		case statErr != nil:
			scan.rootErr = fmt.Errorf("stat root: %w", statErr)
		case !info.IsDir():
			scan.rootErr = fmt.Errorf("root is not a directory after symlink resolution")
		}
		var rules []ignoreRules
		var ignored bool
		if scan.rootErr == nil && honorIgnores {
			rules, ignored, scan.rootErr = scanRootIgnoreRules(cleanRoot, libraryRoots)
		}
		if scan.rootErr == nil && !ignored {
			walkErr := walkAudiobookDirectories(ctx, cleanRoot, &scan, make(map[string]bool), rules, honorIgnores, os.ReadDir)
			if walkErr != nil {
				if errors.Is(walkErr, context.Canceled) || errors.Is(walkErr, context.DeadlineExceeded) {
					return nil, walkErr
				}
				scan.rootErr = fmt.Errorf("walk root: %w", walkErr)
			}
			sort.Strings(scan.candidates)
		}
		if scan.failed() {
			slog.WarnContext(ctx, "audiobook scan: walk incomplete; unreadable paths protected from missing-file reconciliation", "component", "scanner",
				"folder_id", folderID,
				"root", cleanRoot,
				"walk_failures", len(scan.walkFailures),
				"error", scan.rootErr,
			)
		}
		scans = append(scans, scan)
	}
	return scans, nil
}

// Keep readable portions of partial walks eligible for cleanup. Failed logical
// paths protect only their own files/subtrees, including during the trash sweep.
func splitAudiobookReconcileRoots(scans []audiobookRootScan) (roots []string, seenPaths map[string]bool, protectedPaths []string) {
	roots = make([]string, 0, len(scans))
	seenPaths = make(map[string]bool)
	seenRoots := make(map[string]struct{}, len(scans))
	for i := range scans {
		scan := &scans[i]
		protectedPaths = append(protectedPaths, scan.walkFailures...)
		if scan.rootErr != nil {
			protectedPaths = append(protectedPaths, scan.root)
			continue
		}
		if pathWithinAnyRoot(scan.root, scan.walkFailures) {
			continue
		}
		if _, seen := seenRoots[scan.root]; !seen {
			roots = append(roots, scan.root)
			seenRoots[scan.root] = struct{}{}
		}
		if len(scan.seenPaths) > 0 {
			for path := range scan.seenPaths {
				seenPaths[path] = true
			}
		}
	}
	return roots, seenPaths, protectedPaths
}

// groupAudiobookCandidates dispatches each physical book once. Retain logical
// aliases for retry if the preferred path disappears after discovery.
func groupAudiobookCandidates(scans []audiobookRootScan) [][]string {
	var groups [][]string
	byPhysical := make(map[string]int)
	seen := make(map[string]bool)
	for _, scan := range scans {
		for _, path := range scan.candidates {
			if seen[path] {
				continue
			}
			seen[path] = true
			physical, err := canonicalWalkPath(path)
			if err != nil {
				// Let reconciliation report the failure rather than silently
				// dropping a book that vanished after the walk.
				physical = path
			}
			if index, ok := byPhysical[physical]; ok {
				groups[index] = append(groups[index], path)
			} else {
				byPhysical[physical] = len(groups)
				groups = append(groups, []string{path})
			}
		}
	}
	return groups
}

func (s *Scanner) reconcileAudiobookAliases(ctx context.Context, folder *models.MediaFolder, aliases []string, allowedPaths map[string]bool, skipped *int64) error {
	// Prefer the already indexed location. Adding an alias should not churn
	// file IDs or overwrite a filesystem-derived title on every scan.
	if s.fileRepo != nil && len(aliases) > 1 {
		for i, path := range aliases {
			id, err := s.fileRepo.FindContentIDByObservedRootPath(ctx, folder.ID, path, "audiobook")
			if err != nil {
				return err
			}
			if id != "" {
				aliases[0], aliases[i] = aliases[i], aliases[0]
				break
			}
		}
	}
	var failures []error
	for _, path := range aliases {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := s.reconcileAudiobookFolder(ctx, folder, path, allowedPaths, skipped); err != nil {
			if !errors.Is(err, errFolderHasNoMedia) {
				failures = append(failures, err)
			}
			continue
		}
		return nil
	}
	return errors.Join(failures...)
}

// ScanAudiobookFolder walks an audiobooks-typed media folder and writes
// one media_items row per subdirectory it can parse as an audiobook,
// plus the corresponding media_files rows and author/narrator links in
// item_people.
//
// Directories containing audio are treated as books; discovery follows directory
// symlinks and stops below each book so its parts remain one candidate. A root
// containing loose audio is also a candidate without hiding sibling books.
//
// This bypasses the per-file movie/TV pipeline because audiobooks are
// inherently folder-scoped (one book = one item, possibly multi-file).
func (s *Scanner) ScanAudiobookFolder(ctx context.Context, folder *models.MediaFolder, fullScan bool) error {
	if folder == nil {
		return fmt.Errorf("ScanAudiobookFolder: nil folder")
	}
	return s.scanAudiobookPaths(ctx, folder, folder.Paths, fullScan, true)
}

func (s *Scanner) scanAudiobookPaths(ctx context.Context, folder *models.MediaFolder, roots []string, fullScan, honorIgnores bool) error {
	if s == nil || folder == nil {
		return fmt.Errorf("ScanAudiobookFolder: nil scanner or folder")
	}

	warning, err := s.scanWarningBeforeWalk(ctx, folder.ID, fullScan)
	if err != nil {
		return err
	}

	// Phase 1: walk the tree to collect candidate book folders. This is
	// I/O-light (no ffprobe), and avoids holding the worker pool open
	// for the duration of a 240k-folder scan.
	scans, err := collectAudiobookRootScans(ctx, folder.ID, roots, folder.Paths, honorIgnores)
	if err != nil {
		return err
	}
	walkFailures := 0
	for _, scan := range scans {
		walkFailures += len(scan.walkFailures)
		if scan.rootErr != nil {
			walkFailures++
		}
	}
	// Cleanup may replace this with a stronger empty/dead-root warning.
	if err := s.setPartialWalkWarning(ctx, folder.ID, walkFailures, !fullScan); err != nil {
		return err
	}
	candidates := groupAudiobookCandidates(scans)
	reconcileRoots, seenPaths, protectedPaths := splitAudiobookReconcileRoots(scans)
	reportAudiobookScanProgress(ctx, folder.ID, len(candidates), 0, 0, 0)

	if len(candidates) == 0 {
		// No books on disk. Still reconcile (guarded) so a legitimately-emptied
		// library converges — but only when a root was readable, and the
		// empty-walk guard requires operator confirmation before deleting.
		if len(reconcileRoots) > 0 {
			if err := s.reconcileAudiobookMissingFiles(ctx, folder, reconcileRoots, seenPaths, protectedPaths, fullScan); err != nil {
				slog.WarnContext(ctx, "audiobook scan: missing-file reconcile failed", "component", "scanner", "folder_id", folder.ID, "error", err)
			}
		} else if len(scans) > 0 {
			slog.WarnContext(ctx, "audiobook scan: every root walk failed; skipping missing-file reconciliation", "component", "scanner",
				"folder_id", folder.ID)
		}
		if walkFailures > 0 {
			return s.setPartialWalkWarning(ctx, folder.ID, walkFailures, true)
		}
		if fullScan {
			return s.clearPartialWalkWarning(ctx, folder.ID, warning)
		}
		return nil
	}

	// Phase 2: reconcile in parallel. ffprobe dominates per-book wall time
	// (hundreds of ms each), so single-threaded scans of a large library
	// take days. Worker pool brings this to ~hours.
	workers := audiobookScanWorkers()
	slog.InfoContext(ctx, "audiobook scan: starting", "component", "scanner",
		"folder_id", folder.ID,
		"candidates", len(candidates),
		"workers", workers,
	)

	ch := make(chan []string, workers*2)
	var (
		wg        sync.WaitGroup
		processed int64
		failed    int64
		skipped   int64
		cancelMu  sync.Mutex
		failures  scanFailures
		cancelErr error
	)
	start := time.Now()
	for range workers {
		wg.Go(func() {
			for aliases := range ch {
				path := aliases[0]
				if ctx.Err() != nil {
					return
				}
				if err := s.reconcileAudiobookAliases(ctx, folder, aliases, seenPaths, &skipped); err != nil {
					if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
						cancelMu.Lock()
						if cancelErr == nil {
							cancelErr = err
						}
						cancelMu.Unlock()
						return
					}
					atomic.AddInt64(&failed, 1)
					failures.addf("%s: %w", path, err)
					slog.WarnContext(ctx, "audiobook scan: folder failed", "component", "scanner",
						"folder_id", folder.ID,
						"path", path,
						"error", err,
					)
				}
				n := atomic.AddInt64(&processed, 1)
				if n%500 == 0 || n == int64(len(candidates)) {
					failedCount := atomic.LoadInt64(&failed)
					skippedCount := atomic.LoadInt64(&skipped)
					slog.InfoContext(ctx, "audiobook scan: progress", "component", "scanner",
						"folder_id", folder.ID,
						"processed", n,
						"failed", failedCount,
						"skipped", skippedCount,
						"total", len(candidates),
						"elapsed_sec", int(time.Since(start).Seconds()),
					)
					reportAudiobookScanProgress(ctx, folder.ID, len(candidates), int(n), int(failedCount), int(skippedCount))
				}
			}
		})
	}

	for _, p := range candidates {
		select {
		case ch <- p:
		case <-ctx.Done():
			close(ch)
			wg.Wait()
			return ctx.Err()
		}
	}
	close(ch)
	wg.Wait()
	if err := ctx.Err(); err != nil {
		return err
	}
	if cancelErr != nil {
		return cancelErr
	}

	slog.InfoContext(ctx, "audiobook scan: completed", "component", "scanner",
		"folder_id", folder.ID,
		"processed", atomic.LoadInt64(&processed),
		"failed", atomic.LoadInt64(&failed),
		"skipped", atomic.LoadInt64(&skipped),
		"elapsed_sec", int(time.Since(start).Seconds()),
	)
	if processedCount := atomic.LoadInt64(&processed); processedCount > 0 {
		failedCount := atomic.LoadInt64(&failed)
		skippedCount := atomic.LoadInt64(&skipped)
		if failedCount > 0 && skippedCount == 0 && failedCount == processedCount {
			return fmt.Errorf("audiobook scan failed for every attempted folder_id=%d: %w", folder.ID, failures.join())
		}
	}

	// Reconcile files that vanished from disk now that the full walk's
	// seenPaths is known and the scan completed without cancellation.
	if err := s.reconcileAudiobookMissingFiles(ctx, folder, reconcileRoots, seenPaths, protectedPaths, fullScan); err != nil {
		slog.WarnContext(ctx, "audiobook scan: missing-file reconcile failed", "component", "scanner", "folder_id", folder.ID, "error", err)
	}
	if walkFailures > 0 {
		return s.setPartialWalkWarning(ctx, folder.ID, walkFailures, true)
	}
	if fullScan {
		return s.clearPartialWalkWarning(ctx, folder.ID, warning)
	}
	return nil
}

// reconcileAudiobookMissingFiles mirrors reconcileMissingEbookFiles: DB files
// under the scanned roots that were not seen on disk are marked missing, the
// folder trash is optionally emptied, and library memberships are reconciled so
// books with no remaining files are removed (a rename therefore converges on
// the newly indexed path instead of leaving a stale duplicate). A scan that saw
// zero files while the DB still has rows only reconciles when the operator has
// confirmed cleanup, so an unmounted source can't wipe the catalog.
func (s *Scanner) reconcileAudiobookMissingFiles(ctx context.Context, folder *models.MediaFolder, roots []string, seenPaths map[string]bool, protectedPaths []string, fullScan bool) error {
	if s.fileRepo == nil || s.libraryRepo == nil || len(roots) == 0 {
		return nil
	}

	confirmedCleanup, blockAll, err := s.emptyCleanupDecision(
		ctx, folder, roots, seenPaths, fullScan, "audiobook",
	)
	if err != nil {
		return err
	}
	if blockAll {
		return nil
	}

	now := time.Now().UTC()
	missing := 0
	for _, root := range roots {
		existing, err := s.fileRepo.GetByFolderAndPathPrefix(ctx, folder.ID, root)
		if err != nil {
			return fmt.Errorf("listing existing audiobook files for %q: %w", root, err)
		}
		for _, mf := range existing {
			if mf == nil || seenPaths[mf.FilePath] || pathWithinAnyRoot(mf.FilePath, protectedPaths) {
				continue
			}
			if mf.MissingSince == nil {
				if err := s.fileRepo.MarkMissing(ctx, mf.ID, now); err != nil {
					slog.ErrorContext(ctx, "audiobook scan: failed to mark file missing", "component", "scanner",
						"folder_id", folder.ID, "path", mf.FilePath, "error", err)
					continue
				}
			}
			missing++
		}
	}

	trashed, removedMemberships, deletedItems, err := s.sweepMissingAndReconcile(ctx, folder, confirmedCleanup, protectedPaths...)
	if trashed > 0 {
		slog.InfoContext(ctx, "audiobook scan: emptied trash", "component", "scanner", "folder_id", folder.ID, "deleted", trashed)
	}
	if err != nil {
		return err
	}
	if missing > 0 || removedMemberships > 0 || deletedItems > 0 {
		slog.InfoContext(ctx, "audiobook scan: reconciled missing files", "component", "scanner",
			"folder_id", folder.ID, "missing", missing,
			"memberships_removed", removedMemberships, "items_deleted", deletedItems)
	}
	return nil
}

func reportAudiobookScanProgress(ctx context.Context, folderID int, total, processed, failed, skipped int) {
	reportProgress(ctx, ProgressUpdate{
		Phase:           "audiobook_scan",
		Message:         fmt.Sprintf("Scanning audiobooks in folder %d", folderID),
		CurrentScope:    strconv.Itoa(folderID),
		TotalFiles:      total,
		FilesDiscovered: total,
		FilesProcessed:  processed,
		Errors:          failed,
		Unchanged:       skipped,
	})
}

func (s *Scanner) reconcileAudiobookFolder(ctx context.Context, folder *models.MediaFolder, folderPath string, allowedPaths map[string]bool, skipped *int64) error {
	_, isUnchanged, skipErr := s.audiobookFolderShouldSkip(ctx, folder, folderPath, allowedPaths)
	if skipErr != nil {
		slog.WarnContext(ctx, "audiobook scan: skip-check failed, falling through", "component", "scanner",
			"folder_id", folder.ID,
			"path", folderPath,
			"error", skipErr,
		)
	} else if isUnchanged {
		// Linking runs when either format is added or its metadata changes.
		// An unchanged, unlinked book must not repeat the candidate search.
		atomic.AddInt64(skipped, 1)
		return nil
	}
	parsed, err := parseAudiobookFolder(ctx, s.ffprobePath, folderPath, allowedPaths)
	if err != nil {
		if errors.Is(err, errFolderHasNoMedia) {
			return err
		}
		return fmt.Errorf("parse audiobook folder %s: %w", folderPath, err)
	}

	contentID, err := s.upsertAudiobookMediaItem(ctx, folder.ID, folderPath, parsed)
	if err != nil {
		return fmt.Errorf("upsert audiobook item: %w", err)
	}
	// Keep item metadata, file rows, and folder-level audiobook indexes in one
	// transaction. A multi-part book must not become visible with only some of
	// its parts after a worker or database failure.
	tx, err := s.fileRepo.Pool().Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin audiobook folder transaction: %w", err)
	}
	cleanupCtx, cancelCleanup := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancelCleanup()
	defer func() { _ = tx.Rollback(cleanupCtx) }()
	if err := s.upsertAudiobookPresentationTx(ctx, tx, folder, contentID, folderPath, parsed); err != nil {
		return fmt.Errorf("upsert audiobook presentation: %w", err)
	}
	if err := s.upsertAudiobookSeriesTx(ctx, tx, contentID, parsed); err != nil {
		return fmt.Errorf("upsert audiobook series: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO media_item_libraries (content_id, media_folder_id, first_seen_at)
		VALUES ($1, $2, NOW())
		ON CONFLICT (content_id, media_folder_id) DO NOTHING
	`, contentID, folder.ID); err != nil {
		return fmt.Errorf("upsert audiobook library membership: %w", err)
	}
	if parsed.ASIN != "" {
		if _, err := tx.Exec(ctx, `
			INSERT INTO media_item_provider_ids (content_id, provider, provider_id, item_type)
			VALUES ($1, 'asin', $2, 'audiobook')
			ON CONFLICT DO NOTHING
		`, contentID, parsed.ASIN); err != nil {
			return fmt.Errorf("upsert audiobook ASIN provider id: %w", err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit audiobook folder transaction: %w", err)
	}
	if err := applyAudiobookSidecarCover(ctx, s.itemRepo, s.imageCacher, contentID, folderPath); err != nil {
		slog.WarnContext(ctx, "audiobook scan: sidecar cover upload failed", "component", "scanner",
			"folder_id", folder.ID,
			"content_id", contentID,
			"path", folderPath,
			"error", err,
		)
	}
	if len(parsed.Files) > 0 && s.fileRepo != nil {
		if _, err := applyAudiobookEmbeddedCover(ctx, s.itemRepo, s.fileRepo.Pool(), FFmpegPathFromFFprobe(s.ffprobePath), s.imageCacher, parsed.Files[0].Path, contentID); err != nil {
			slog.WarnContext(ctx, "audiobook scan: embedded cover failed", "component", "scanner",
				"folder_id", folder.ID,
				"content_id", contentID,
				"path", parsed.Files[0].Path,
				"error", err,
			)
		}
	}
	if err := s.upsertAudiobookPeople(ctx, contentID, parsed); err != nil {
		return fmt.Errorf("upsert audiobook people: %w", err)
	}
	s.autoLinkLiteraryWork(ctx, contentID)
	slog.InfoContext(ctx, "audiobook scan: indexed", "component", "scanner",
		"folder_id", folder.ID,
		"content_id", contentID,
		"title", parsed.Title,
		"author", parsed.Author,
		"files", len(parsed.Files),
	)
	return nil
}

func applyAudiobookEmbeddedCover(
	ctx context.Context,
	reader audiobookPosterPathReader,
	exec audiobookPosterExec,
	ffmpegPath string,
	cacher audiobookCoverCacher,
	audioFilePath string,
	contentID string,
) (bool, error) {
	if exec == nil {
		return false, nil
	}
	if reader != nil {
		existingPosterPath, err := reader.GetPosterPath(ctx, contentID)
		if err != nil {
			return false, fmt.Errorf("get audiobook poster path for embedded cover: %w", err)
		}
		if strings.TrimSpace(existingPosterPath) != "" {
			return false, nil
		}
	}
	poster, thumb := ExtractAndUploadAudiobookCover(ctx, ffmpegPath, cacher, audioFilePath, contentID)
	if poster == "" {
		return false, nil
	}
	_, err := exec.Exec(ctx, `
		UPDATE media_items
		SET poster_path = $1, poster_thumbhash = $2, updated_at = NOW()
		WHERE content_id = $3 AND (poster_path IS NULL OR poster_path = '')
	`, poster, thumb, contentID)
	if err != nil {
		return false, fmt.Errorf("update audiobook embedded cover: %w", err)
	}
	return true, nil
}

// upsertAudiobookMediaItem reuses an item already linked to the same filesystem
// root, then falls back to the stricter ABS duplicate rule, or creates a new row.
// It intentionally avoids title/year-only dedupe because audiobooks often share
// titles, unknown years, or edition-specific credits. Returns the content_id used.
func (s *Scanner) upsertAudiobookMediaItem(ctx context.Context, folderID int, folderPath string, book *parsedAudiobook) (string, error) {
	if s.itemRepo == nil {
		return "", fmt.Errorf("itemRepo not configured on Scanner")
	}
	if s.fileRepo == nil {
		return "", fmt.Errorf("fileRepo not configured on Scanner")
	}

	physical, err := canonicalWalkPath(folderPath)
	if err != nil {
		return "", fmt.Errorf("resolve audiobook root: %w", err)
	}
	// Include missing rows so a surviving alias restores the same identity.
	var existingID string
	err = s.fileRepo.Pool().QueryRow(ctx, `
		SELECT mf.content_id FROM media_files mf
		JOIN media_items mi ON mi.content_id = mf.content_id
		WHERE mf.media_folder_id = $1 AND mf.canonical_root_path = $2
		  AND mi.type = 'audiobook'
		ORDER BY mf.missing_since NULLS FIRST, mf.id
		LIMIT 1
	`, folderID, physical).Scan(&existingID)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return "", fmt.Errorf("find audiobook by physical root: %w", err)
	}
	if existingID == "" {
		// Upgrade books indexed before canonical roots followed symlinks.
		existingID, err = s.fileRepo.FindContentIDByRootPath(ctx, folderID, folderPath, "audiobook")
	} else {
		err = nil
	}
	if err != nil {
		return "", fmt.Errorf("find audiobook by root path: %w", err)
	}
	cleanTitle := stripNarratorSuffix(book.Title)
	if cleanTitle == "" {
		cleanTitle = book.Title
	}
	if existingID == "" {
		if existing := s.findAudiobookByFilePath(ctx, folderID, book); existing != nil {
			existingID = existing.ContentID
		} else if existing := s.findAudiobookDuplicate(ctx, book, cleanTitle); existing != nil {
			existingID = existing.ContentID
		}
	}
	contentID, err := s.claimAudiobookIdentity(ctx, folderID, physical, existingID, book, cleanTitle)
	if err != nil {
		return "", err
	}
	return contentID, nil
}

// Use the existing root-claim table so concurrent scoped scans through different
// aliases agree on identity before either has written its media files.
func (s *Scanner) claimAudiobookIdentity(ctx context.Context, folderID int, physical, existingID string, book *parsedAudiobook, title string) (string, error) {
	tx, err := s.fileRepo.Pool().Begin(ctx)
	if err != nil {
		return "", err
	}
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	defer func() { _ = tx.Rollback(cleanupCtx) }()
	if err := lockAudiobookRoot(ctx, tx, folderID, physical); err != nil {
		return "", err
	}
	var claimedID string
	err = tx.QueryRow(ctx, `SELECT content_id FROM media_item_roots
		WHERE media_folder_id = $1 AND canonical_root_path = $2`, folderID, physical).Scan(&claimedID)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return "", err
	}
	if claimedID == "" {
		claimedID = existingID
		if claimedID == "" {
			claimedID, err = idgen.NextID()
			if err != nil {
				return "", err
			}
			item := &models.MediaItem{ContentID: claimedID, SortTitle: titleutil.DeriveDefaultSortTitle(title)}
			applyBookToMediaItem(item, book)
			if err := s.itemRepo.UpsertTx(ctx, tx, item); err != nil {
				return "", err
			}
		}
		if err := tx.QueryRow(ctx, `INSERT INTO media_item_roots (media_folder_id, canonical_root_path, content_id)
			VALUES ($1, $2, $3) ON CONFLICT (media_folder_id, canonical_root_path)
			DO UPDATE SET last_seen_at = NOW() RETURNING content_id`, folderID, physical, claimedID).Scan(&claimedID); err != nil {
			return "", err
		}
	} else if err := catalog.NewRootClaimRepository(s.fileRepo.Pool()).Claim(ctx, tx, folderID, physical, claimedID); err != nil {
		return "", err
	}
	if err := tx.Commit(ctx); err != nil {
		return "", err
	}
	return claimedID, nil
}

func lockAudiobookRoot(ctx context.Context, tx pgx.Tx, folderID int, physical string) error {
	_, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, fmt.Sprintf("audiobook:%d:%s", folderID, physical))
	return err
}

func (s *Scanner) updateAudiobookMediaItemTx(ctx context.Context, tx pgx.Tx, contentID string, book *parsedAudiobook) error {
	item, err := s.itemRepo.GetByIDTx(ctx, tx, contentID)
	if err != nil {
		return fmt.Errorf("get audiobook media item %s: %w", contentID, err)
	}
	applyBookToMediaItem(item, book)
	if item.SortTitle == "" {
		item.SortTitle = titleutil.DeriveDefaultSortTitle(item.Title)
	}
	return s.itemRepo.UpsertTx(ctx, tx, item)
}

func resolveAudiobookMediaItem(
	ctx context.Context,
	rootFinder filesystemRootContentFinder,
	itemWriter filesystemMediaItemWriter,
	folderID int,
	folderPath string,
	book *parsedAudiobook,
) (string, error) {
	if rootFinder == nil {
		return "", fmt.Errorf("root content finder not configured")
	}
	if itemWriter == nil {
		return "", fmt.Errorf("media item writer not configured")
	}
	existingID, err := rootFinder.FindContentIDByRootPath(ctx, folderID, folderPath, "audiobook")
	if err != nil {
		return "", fmt.Errorf("find audiobook by root path: %w", err)
	}
	if existingID != "" {
		return existingID, nil
	}

	cleanTitle := stripNarratorSuffix(book.Title)
	if cleanTitle == "" {
		cleanTitle = book.Title
	}
	return createAudiobookMediaItem(ctx, itemWriter, book, cleanTitle)
}

func createAudiobookMediaItem(ctx context.Context, itemWriter filesystemMediaItemWriter, book *parsedAudiobook, cleanTitle string) (string, error) {
	id, err := idgen.NextID()
	if err != nil {
		return "", fmt.Errorf("generate content_id: %w", err)
	}
	item := &models.MediaItem{
		ContentID: id,
		Type:      "audiobook",
		Title:     cleanTitle,
		SortTitle: titleutil.DeriveDefaultSortTitle(cleanTitle),
		Year:      book.Year,
	}
	applyBookToMediaItem(item, book)
	if item.SortTitle == "" {
		item.SortTitle = titleutil.DeriveDefaultSortTitle(item.Title)
	}
	if err := itemWriter.Upsert(ctx, item); err != nil {
		return "", err
	}
	return id, nil
}

// findAudiobookDuplicate returns an existing audiobook media_items row that
// matches the parsed book on (author, narrator, year, duration ±0.5%/±10s,
// punctuation-normalized title). Used after the file-path lookup misses to
// detect the same recording stored under a different folder name. Returns nil
// when no match exists or any required attribute (author, narrator, year,
// duration) is missing — under-tagged files don't qualify for automatic dedup.
func (s *Scanner) findAudiobookDuplicate(ctx context.Context, book *parsedAudiobook, cleanTitle string) *models.MediaItem {
	if s.fileRepo == nil {
		return nil
	}
	if book.Author == "" || book.Narrator == "" || book.Year == 0 {
		return nil
	}
	var totalDuration int
	for _, f := range book.Files {
		totalDuration += f.Duration
	}
	if totalDuration <= 0 {
		return nil
	}
	tolerance := totalDuration / 200 // 0.5%
	if tolerance < 10 {
		tolerance = 10
	}
	titleKey := normalizeAudiobookDedupeTitle(cleanTitle)
	if titleKey == "" {
		return nil
	}
	rows, err := s.fileRepo.Pool().Query(ctx,
		audiobookDuplicateCandidateSQL,
		book.Author,
		book.Narrator,
		book.Year,
		totalDuration,
		tolerance,
		titleKey,
	)
	if err != nil {
		return nil
	}
	defer rows.Close()

	var existingID string
	for rows.Next() {
		var candidateID, candidateTitle string
		if err := rows.Scan(&candidateID, &candidateTitle); err != nil {
			return nil
		}
		if audiobookDedupeTitlesMatch(candidateTitle, cleanTitle) {
			existingID = candidateID
			break
		}
	}
	if rows.Err() != nil || existingID == "" {
		return nil
	}

	items, err := s.itemRepo.GetByIDs(ctx, []string{existingID})
	if err != nil || len(items) == 0 {
		return nil
	}
	return items[0]
}

// findAudiobookByFilePath returns an existing audiobook media_items row
// whose media_files reference any of this book's audio file paths.
// Returns nil when no match exists (a fresh scan of a new folder).
func (s *Scanner) findAudiobookByFilePath(ctx context.Context, folderID int, book *parsedAudiobook) *models.MediaItem {
	if s.fileRepo == nil || len(book.Files) == 0 {
		return nil
	}
	paths := audiobookLookupPaths(book.Files)
	if len(paths) == 0 {
		return nil
	}
	var existingID string
	err := s.fileRepo.Pool().QueryRow(ctx, `
		SELECT mf.content_id
		FROM media_files mf
		JOIN media_items mi ON mi.content_id = mf.content_id
		WHERE mf.media_folder_id = $2
		  AND (mf.file_path = ANY($1) OR LOWER(mf.file_path) = ANY($1))
		  AND mi.type = 'audiobook'
		LIMIT 1
	`, paths, folderID).Scan(&existingID)
	if err != nil || existingID == "" {
		return nil
	}
	items, err := s.itemRepo.GetByIDs(ctx, []string{existingID})
	if err != nil || len(items) == 0 {
		return nil
	}
	return items[0]
}

func audiobookLookupPaths(files []parsedAudiobookFile) []string {
	seen := make(map[string]struct{}, len(files)*2)
	paths := make([]string, 0, len(files)*2)
	for _, f := range files {
		path := strings.TrimSpace(f.Path)
		if path == "" {
			continue
		}
		for _, candidate := range []string{path, strings.ToLower(path)} {
			if candidate == "" {
				continue
			}
			if _, ok := seen[candidate]; ok {
				continue
			}
			seen[candidate] = struct{}{}
			paths = append(paths, candidate)
		}
	}
	return paths
}

// applyBookToMediaItem copies parsed-audiobook tag fields onto the
// MediaItem. Used for both fresh inserts and re-scans of existing rows
// so manual edits to fields not driven by the file (e.g., poster_path
// set by the metadata enricher) survive.
//
// Audiobook titles are stripped of trailing narrator/edition suffixes
// (`Foo (Read by Bar)`, `Foo - read by Bar`, etc.) since that data is
// already captured as item_people kind=8. The raw tag value is preserved
// in OriginalTitle when it differs so the original is never lost.
func applyBookToMediaItem(item *models.MediaItem, book *parsedAudiobook) {
	item.Type = "audiobook"
	raw := strings.TrimSpace(book.Title)
	cleaned := stripNarratorSuffix(raw)
	if cleaned == "" {
		cleaned = raw
	}
	item.Title = cleaned
	if cleaned != raw && item.OriginalTitle == "" {
		item.OriginalTitle = raw
	}
	item.Year = book.Year
	if book.Overview != "" && item.Overview == "" {
		item.Overview = book.Overview
	}
	if book.Publisher != "" {
		item.Studios = mergeUniqueStrings(item.Studios, []string{book.Publisher})
	}
	if len(book.Genres) > 0 {
		item.Genres = mergeUniqueStrings(item.Genres, book.Genres)
	}
	if rd := normalizeReleaseDateForSQL(book.ReleaseDate); rd != "" && (item.ReleaseDate == nil || *item.ReleaseDate == "") {
		item.ReleaseDate = &rd
	}
	if book.Language != "" && item.OriginalLanguage == "" {
		item.OriginalLanguage = book.Language
	}
}

// normalizeReleaseDateForSQL coerces tag-derived date strings into the
// ISO YYYY-MM-DD shape media_items.release_date expects. Year-only
// tags ("2018") become "2018-01-01"; ISO already-correct values pass
// through; everything else returns "" (skip the column).
func normalizeReleaseDateForSQL(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	if len(s) >= 10 && s[4] == '-' && s[7] == '-' {
		return s[:10]
	}
	if len(s) >= 4 {
		y := s[:4]
		ok := true
		for _, c := range y {
			if c < '0' || c > '9' {
				ok = false
				break
			}
		}
		if ok {
			return y + "-01-01"
		}
	}
	return ""
}

func mergeUniqueStrings(existing, additions []string) []string {
	seen := make(map[string]struct{}, len(existing)+len(additions))
	out := make([]string, 0, len(existing)+len(additions))
	for _, v := range existing {
		k := strings.ToLower(strings.TrimSpace(v))
		if k == "" {
			continue
		}
		if _, ok := seen[k]; ok {
			continue
		}
		seen[k] = struct{}{}
		out = append(out, v)
	}
	for _, v := range additions {
		k := strings.ToLower(strings.TrimSpace(v))
		if k == "" {
			continue
		}
		if _, ok := seen[k]; ok {
			continue
		}
		seen[k] = struct{}{}
		out = append(out, v)
	}
	return out
}

// upsertAudiobookPresentationTx writes item metadata and one row per audio
// file under the physical root lock. File and observed-root paths retain the
// selected logical alias; canonical-root paths identify the physical book.
func (s *Scanner) upsertAudiobookPresentationTx(
	ctx context.Context,
	tx pgx.Tx,
	folder *models.MediaFolder,
	contentID string,
	folderPath string,
	book *parsedAudiobook,
) error {
	physical, err := canonicalWalkPath(folderPath)
	if err != nil {
		return fmt.Errorf("resolve audiobook root: %w", err)
	}
	if err := lockAudiobookRoot(ctx, tx, folder.ID, physical); err != nil {
		return err
	}
	// The selected alias supplies both filesystem-derived metadata and files.
	// Keep them under the same lock and commit so concurrent scans agree.
	if err := s.updateAudiobookMediaItemTx(ctx, tx, contentID, book); err != nil {
		return err
	}
	partTotal := len(book.Files)
	probeUpdatedAt := time.Now().UTC()
	mediaFiles := make([]models.MediaFile, 0, partTotal)
	for idx, af := range book.Files {
		info, err := os.Stat(af.Path)
		if err != nil {
			return fmt.Errorf("stat media file %s: %w", af.Path, err)
		}
		// The probe read a specific version of the file. If size or mtime
		// moved while it ran, the probe facts describe bytes this row would
		// not: fail the folder so the next scan reprocesses it instead of
		// persisting a mismatch the unchanged check would then trust.
		if af.Size != info.Size() || !sameFileModifiedAt(&af.ModifiedAt, info.ModTime()) {
			return fmt.Errorf("media file %s changed while it was probed; will rescan", af.Path)
		}
		modifiedAt := normalizeFileModifiedAt(info.ModTime())
		chapters := make([]models.MediaChapter, len(af.Chapters))
		for i, ch := range af.Chapters {
			chapters[i] = models.MediaChapter{
				Index:        ch.Index,
				Title:        ch.Title,
				StartSeconds: ch.StartSeconds,
				EndSeconds:   ch.EndSeconds,
				Source:       ch.Source,
			}
		}

		mf := models.MediaFile{
			ContentID:          contentID,
			MediaFolderID:      folder.ID,
			CanonicalRootPath:  physical,
			ObservedRootPath:   folderPath,
			ContentGroupKey:    contentID,
			GroupKeyVersion:    1,
			BaseTitle:          book.Title,
			BaseYear:           book.Year,
			BaseType:           "audiobook",
			IdentityConfidence: audiobookIdentityConfidence(book, af),
			FilePath:           af.Path,
			FileSize:           info.Size(),
			FileModifiedAt:     &modifiedAt,
			Chapters:           chapters,
			ProbeSource:        "local",
			ProbeUpdatedAt:     &probeUpdatedAt,
			Duration:           af.Duration,
			Bitrate:            af.Bitrate,
			CodecAudio:         af.CodecAudio,
			Container:          af.Container,
			AudioChannels:      af.AudioChannels,
		}
		if partTotal > 1 {
			mf.PresentationKind = audiobookMultipartKind
			mf.PresentationGroupKey = contentID
			mf.PresentationPartIndex = idx + 1
			mf.PresentationPartTotal = partTotal
		}

		mediaFiles = append(mediaFiles, mf)
	}
	if err := s.fileRepo.UpsertBatchTx(ctx, tx, mediaFiles); err != nil {
		return fmt.Errorf("upsert audiobook media files: %w", err)
	}
	// One physical recording has one playable set of parts. Retire the old
	// alias only after its replacement files have been written in this same
	// transaction, so a failed scan cannot take away the working location.
	if _, err := tx.Exec(ctx, `
		UPDATE media_files SET missing_since = COALESCE(missing_since, NOW())
		WHERE media_folder_id = $1 AND content_id = $2
		  AND canonical_root_path = $3 AND observed_root_path <> $4
	`, folder.ID, contentID, physical, folderPath); err != nil {
		return fmt.Errorf("retire alternate audiobook paths: %w", err)
	}
	return nil
}

// audiobookCredit pairs a person name with a credit kind. Used to compare
// the desired-from-tags set against the existing item_people set without
// having to materialize a full []models.ItemPerson (which requires
// resolved person IDs).
type audiobookCredit struct {
	Name string
	Kind models.PersonKind
}

// audiobookPeopleCreditsEqual returns true when the existing item_people
// rows for an audiobook match the desired credit set one-for-one on
// (case-insensitive name, kind). Order is irrelevant because the upsert
// path orders by SortOrder.
//
// Case-insensitive comparison: audiobook tag casing drifts between rips
// and we don't want a stylistic re-cap to trigger DELETE+INSERT on every
// scan.
func audiobookPeopleCreditsEqual(existing []models.ItemPerson, desired []audiobookCredit) bool {
	if len(existing) != len(desired) {
		return false
	}
	type key struct {
		name string
		kind models.PersonKind
	}
	have := make(map[key]struct{}, len(existing))
	for _, p := range existing {
		have[key{strings.ToLower(strings.TrimSpace(p.Person.Name)), p.Kind}] = struct{}{}
	}
	for _, d := range desired {
		k := key{strings.ToLower(strings.TrimSpace(d.Name)), d.Kind}
		if _, ok := have[k]; !ok {
			return false
		}
	}
	return true
}

// upsertAudiobookPeople upserts author and narrator rows into item_people,
// using the PersonRepository to find-or-create each person by name. When the
// parsed credits differ, ReplacePeople makes scanner-derived credits authoritative.
// Skips the DELETE+INSERT entirely when the existing credit set already
// matches the desired set (case-insensitive on name, exact on kind).
func (s *Scanner) upsertAudiobookPeople(ctx context.Context, contentID string, book *parsedAudiobook) error {
	if s.personRepo == nil {
		return fmt.Errorf("personRepo not configured on Scanner")
	}

	var desired []audiobookCredit
	if book.Author != "" {
		desired = append(desired, audiobookCredit{Name: book.Author, Kind: models.PersonKindAuthor})
	}
	if book.Narrator != "" {
		desired = append(desired, audiobookCredit{Name: book.Narrator, Kind: models.PersonKindNarrator})
	}
	if len(desired) == 0 {
		return nil
	}

	existing, err := s.itemRepo.GetPeople(ctx, contentID)
	if err == nil && audiobookPeopleCreditsEqual(existing, desired) {
		return nil
	}

	people := make([]models.ItemPerson, 0, len(desired))
	for i, c := range desired {
		personID, err := s.personRepo.FindOrCreate(ctx, models.Person{Name: c.Name})
		if err != nil {
			return fmt.Errorf("find-or-create person %q: %w", c.Name, err)
		}
		people = append(people, models.ItemPerson{
			Person:    models.Person{ID: personID},
			Kind:      c.Kind,
			SortOrder: i,
		})
	}

	return s.itemRepo.ReplacePeople(ctx, contentID, people)
}

// upsertAudiobookSeries writes the parsed series_name and series_index into
// the audiobook_series table, overwriting any prior row (e.g. one populated
// by migration 145's title-pattern backfill). A blank book.Series clears
// the row so books explicitly retagged out of a series stop appearing in
// the "In this series" rail on the next scan.
func (s *Scanner) upsertAudiobookSeriesTx(ctx context.Context, tx pgx.Tx, contentID string, book *parsedAudiobook) error {
	if s.fileRepo == nil {
		return fmt.Errorf("fileRepo not configured on Scanner")
	}
	desiredName := strings.TrimSpace(book.Series)
	desiredIdx := parseSeriesIndex(book.SeriesPosition)

	// Read current row.
	var currentName *string
	var currentIdx *float64
	err := tx.QueryRow(ctx, `
		SELECT series_name, series_index FROM audiobook_series WHERE content_id = $1
	`, contentID).Scan(&currentName, &currentIdx)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("query audiobook_series: %w", err)
	}

	if desiredName == "" {
		if currentName == nil {
			return nil // already absent
		}
		if _, delErr := tx.Exec(ctx,
			`DELETE FROM audiobook_series WHERE content_id = $1`, contentID); delErr != nil {
			return fmt.Errorf("delete audiobook_series row: %w", delErr)
		}
		return nil
	}

	if currentName != nil && *currentName == desiredName && floatPtrEqual(currentIdx, desiredIdx) {
		return nil // identical row, skip the write
	}

	var idx any
	if desiredIdx != nil {
		idx = *desiredIdx
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO audiobook_series (content_id, series_name, series_index, updated_at)
		VALUES ($1, $2, $3, NOW())
		ON CONFLICT (content_id) DO UPDATE SET
			series_name  = EXCLUDED.series_name,
			series_index = EXCLUDED.series_index,
			updated_at   = NOW()
	`, contentID, desiredName, idx); err != nil {
		return fmt.Errorf("upsert audiobook_series row: %w", err)
	}
	return nil
}

func audiobookIdentityConfidence(book *parsedAudiobook, file parsedAudiobookFile) string {
	if book == nil {
		return "low"
	}

	score := 0
	if book.Title != "" {
		score++
	}
	if book.Author != "" || book.Narrator != "" {
		score++
	}
	if book.Year > 0 {
		score++
	}
	for _, ch := range file.Chapters {
		if ch.Title != "" && ch.EndSeconds > ch.StartSeconds {
			score++
			break
		}
	}

	switch {
	case score >= 4:
		return "high"
	case score > 0:
		return "medium"
	default:
		return "low"
	}
}

// floatPtrEqual returns true when two *float64 values represent the same
// state — both nil, or both non-nil and equal. Used to skip audiobook_series
// re-upserts when only the series_index needs to be NULL=NULL compared.
func floatPtrEqual(a, b *float64) bool {
	if a == nil && b == nil {
		return true
	}
	if a == nil || b == nil {
		return false
	}
	return *a == *b
}

// parseSeriesIndex extracts a leading numeric value from a freeform tag
// like "5", "1.5", "2 of 8", or "1a". Returns nil when no leading number
// is present so the audiobook_series.series_index column stays NULL rather
// than carrying a misleading zero.
func parseSeriesIndex(raw string) *float64 {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	end := 0
	dot := false
	for end < len(raw) {
		c := raw[end]
		if c >= '0' && c <= '9' {
			end++
			continue
		}
		if c == '.' && !dot {
			dot = true
			end++
			continue
		}
		break
	}
	if end == 0 {
		return nil
	}
	var v float64
	if _, err := fmt.Sscanf(raw[:end], "%f", &v); err != nil {
		return nil
	}
	return &v
}
