package scanner

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

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
func (s *Scanner) audiobookFolderShouldSkip(ctx context.Context, folder *models.MediaFolder, folderPath string) (string, bool, error) {
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
	if !audiobookFolderUnchanged(existing, onDisk) {
		return "", false, nil
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
	walkFailures int
}

func (r *audiobookRootScan) failed() bool {
	return r.rootErr != nil || r.walkFailures > 0
}

func collectAudiobookRootScans(ctx context.Context, folderID int, roots []string) ([]audiobookRootScan, error) {
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
		if statErr == nil && scan.rootErr == nil {
			walkErr := filepath.WalkDir(cleanRoot, func(path string, d fs.DirEntry, walkErr error) error {
				if err := ctx.Err(); err != nil {
					return err
				}
				if walkErr != nil {
					scan.walkFailures++
					slog.WarnContext(ctx, "audiobook scan: walk error", "component", "scanner", "path", path, "error", walkErr)
					return nil
				}
				if !d.IsDir() {
					return nil
				}
				entries, err := os.ReadDir(path)
				if err != nil {
					scan.walkFailures++
					slog.WarnContext(ctx, "audiobook scan: read dir failed", "component", "scanner", "path", path, "error", err)
					return nil
				}
				hadAudio := false
				for _, e := range entries {
					if !e.IsDir() && SupportsAudioFile(e.Name()) {
						scan.seenPaths[filepath.Join(path, e.Name())] = true
						hadAudio = true
					}
				}
				if hadAudio {
					scan.candidates = append(scan.candidates, path)
					return filepath.SkipDir
				}
				return nil
			})
			if walkErr != nil {
				if errors.Is(walkErr, context.Canceled) || errors.Is(walkErr, context.DeadlineExceeded) {
					return nil, walkErr
				}
				scan.rootErr = fmt.Errorf("walk root: %w", walkErr)
			}
		}
		if scan.failed() {
			slog.WarnContext(ctx, "audiobook scan: root walk incomplete; root excluded from missing-file reconciliation", "component", "scanner",
				"folder_id", folderID,
				"root", cleanRoot,
				"walk_failures", scan.walkFailures,
				"error", scan.rootErr,
			)
		}
		scans = append(scans, scan)
	}
	return scans, nil
}

func splitAudiobookReconcileRoots(scans []audiobookRootScan) (roots []string, seenPaths map[string]bool, sawFiles bool) {
	roots = make([]string, 0, len(scans))
	seenPaths = make(map[string]bool)
	for i := range scans {
		scan := &scans[i]
		if scan.failed() {
			continue
		}
		roots = append(roots, scan.root)
		if len(scan.seenPaths) > 0 {
			sawFiles = true
			for path := range scan.seenPaths {
				seenPaths[path] = true
			}
		}
	}
	return roots, seenPaths, sawFiles
}

// ScanAudiobookFolder walks an audiobooks-typed media folder and writes
// one media_items row per subdirectory it can parse as an audiobook,
// plus the corresponding media_files rows and author/narrator links in
// item_people.
//
// Each immediate subdirectory of one of folder.Paths is treated as a
// single audiobook. Subdirectories that contain zero audio files are
// silently skipped (parseAudiobookFolder returns os.ErrNotExist).
//
// This bypasses the per-file movie/TV pipeline because audiobooks are
// inherently folder-scoped (one book = one item, possibly multi-file).
func (s *Scanner) ScanAudiobookFolder(ctx context.Context, folder *models.MediaFolder, fullScan bool) error {
	if s == nil || folder == nil {
		return fmt.Errorf("ScanAudiobookFolder: nil scanner or folder")
	}

	// Phase 1: walk the tree to collect candidate book folders. This is
	// I/O-light (no ffprobe), and avoids holding the worker pool open
	// for the duration of a 240k-folder scan.
	scans, err := collectAudiobookRootScans(ctx, folder.ID, folder.Paths)
	if err != nil {
		return err
	}
	candidates := make([]string, 0)
	for i := range scans {
		candidates = append(candidates, scans[i].candidates...)
	}
	reconcileRoots, seenPaths, _ := splitAudiobookReconcileRoots(scans)

	if len(candidates) == 0 {
		// No books on disk. Still reconcile (guarded) so a legitimately-emptied
		// library converges — but only when a root was readable, and the
		// empty-walk guard requires operator confirmation before deleting.
		if len(reconcileRoots) > 0 {
			if err := s.reconcileAudiobookMissingFiles(ctx, folder, reconcileRoots, seenPaths, fullScan); err != nil {
				slog.WarnContext(ctx, "audiobook scan: missing-file reconcile failed", "component", "scanner", "folder_id", folder.ID, "error", err)
			}
		} else if len(scans) > 0 {
			slog.WarnContext(ctx, "audiobook scan: every root walk failed; skipping missing-file reconciliation", "component", "scanner",
				"folder_id", folder.ID)
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

	ch := make(chan string, workers*2)
	var (
		wg        sync.WaitGroup
		processed int64
		failed    int64
		skipped   int64
		failMu    sync.Mutex
		failures  []error
		cancelErr error
	)
	start := time.Now()
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for path := range ch {
				if ctx.Err() != nil {
					return
				}
				if err := s.reconcileAudiobookFolder(ctx, folder, path, &skipped); err != nil {
					if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
						failMu.Lock()
						if cancelErr == nil {
							cancelErr = err
						}
						failMu.Unlock()
						return
					}
					atomic.AddInt64(&failed, 1)
					failMu.Lock()
					failures = append(failures, fmt.Errorf("%s: %w", path, err))
					failMu.Unlock()
					slog.WarnContext(ctx, "audiobook scan: folder failed", "component", "scanner",
						"folder_id", folder.ID,
						"path", path,
						"error", err,
					)
				}
				n := atomic.AddInt64(&processed, 1)
				if n%500 == 0 {
					slog.InfoContext(ctx, "audiobook scan: progress", "component", "scanner",
						"folder_id", folder.ID,
						"processed", n,
						"failed", atomic.LoadInt64(&failed),
						"skipped", atomic.LoadInt64(&skipped),
						"total", len(candidates),
						"elapsed_sec", int(time.Since(start).Seconds()),
					)
				}
			}
		}()
	}

	for _, p := range candidates {
		if ctx.Err() != nil {
			break
		}
		ch <- p
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
			return fmt.Errorf("audiobook scan failed for every attempted folder_id=%d: %w", folder.ID, errors.Join(failures...))
		}
	}

	// Reconcile files that vanished from disk now that the full walk's
	// seenPaths is known and the scan completed without cancellation.
	if err := s.reconcileAudiobookMissingFiles(ctx, folder, reconcileRoots, seenPaths, fullScan); err != nil {
		slog.WarnContext(ctx, "audiobook scan: missing-file reconcile failed", "component", "scanner", "folder_id", folder.ID, "error", err)
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
func (s *Scanner) reconcileAudiobookMissingFiles(ctx context.Context, folder *models.MediaFolder, roots []string, seenPaths map[string]bool, fullScan bool) error {
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
			if mf == nil || seenPaths[mf.FilePath] {
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

	trashed, removedMemberships, deletedItems, err := s.sweepMissingAndReconcile(ctx, folder, confirmedCleanup)
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

func (s *Scanner) reconcileAudiobookFolder(ctx context.Context, folder *models.MediaFolder, folderPath string, skipped *int64) error {
	existingContentID, isUnchanged, skipErr := s.audiobookFolderShouldSkip(ctx, folder, folderPath)
	if skipErr != nil {
		slog.WarnContext(ctx, "audiobook scan: skip-check failed, falling through", "component", "scanner",
			"folder_id", folder.ID,
			"path", folderPath,
			"error", skipErr,
		)
	} else if isUnchanged {
		s.autoLinkLiteraryWork(ctx, existingContentID)
		atomic.AddInt64(skipped, 1)
		return nil
	}
	parsed, err := parseAudiobookFolder(ctx, s.ffprobePath, folderPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("parse audiobook folder %s: %w", folderPath, err)
	}

	contentID, err := s.upsertAudiobookMediaItem(ctx, folder.ID, folderPath, parsed)
	if err != nil {
		return fmt.Errorf("upsert audiobook item: %w", err)
	}
	if err := s.upsertAudiobookMediaFiles(ctx, folder, contentID, folderPath, parsed); err != nil {
		return fmt.Errorf("upsert audiobook files: %w", err)
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
	if err := s.upsertAudiobookSeries(ctx, contentID, parsed); err != nil {
		return fmt.Errorf("upsert audiobook series: %w", err)
	}
	if _, err := s.fileRepo.Pool().Exec(ctx, `
		INSERT INTO media_item_libraries (content_id, media_folder_id, first_seen_at)
		VALUES ($1, $2, NOW())
		ON CONFLICT (content_id, media_folder_id) DO NOTHING
	`, contentID, folder.ID); err != nil {
		return fmt.Errorf("upsert audiobook library membership: %w", err)
	}
	if parsed.ASIN != "" {
		// Two unique constraints can conflict: (content_id, provider) is the
		// PK, and (provider, provider_id, item_type) prevents two content
		// rows from claiming the same external ID. We want to silently skip
		// when either fires; ON CONFLICT DO NOTHING (no target) catches both.
		if _, err := s.fileRepo.Pool().Exec(ctx, `
			INSERT INTO media_item_provider_ids (content_id, provider, provider_id, item_type)
			VALUES ($1, 'asin', $2, 'audiobook')
			ON CONFLICT DO NOTHING
		`, contentID, parsed.ASIN); err != nil {
			return fmt.Errorf("upsert audiobook ASIN provider id: %w", err)
		}
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

	existingID, err := s.fileRepo.FindContentIDByRootPath(ctx, folderID, folderPath, "audiobook")
	if err != nil {
		return "", fmt.Errorf("find audiobook by root path: %w", err)
	}
	if existingID != "" {
		if err := s.updateExistingAudiobookMediaItem(ctx, existingID, book); err != nil {
			return "", err
		}
		return existingID, nil
	}

	cleanTitle := stripNarratorSuffix(book.Title)
	if cleanTitle == "" {
		cleanTitle = book.Title
	}

	if existing := s.findAudiobookByFilePath(ctx, folderID, book); existing != nil {
		applyBookToMediaItem(existing, book)
		if existing.SortTitle == "" {
			existing.SortTitle = titleutil.DeriveDefaultSortTitle(existing.Title)
		}
		if err := s.itemRepo.Upsert(ctx, existing); err != nil {
			return "", err
		}
		return existing.ContentID, nil
	}

	// Secondary dedup: catch books stored under two folders ("Foo" + "Foo
	// Subtitle Version"). Same scan-time rule as the one-shot
	// scripts/dedup_audiobooks.py — author + narrator + year + duration
	// within tolerance + title is equal or an exact "X" / "X: subtitle"
	// prefix relation. Attaches the new file to the existing row so we
	// don't pile up duplicates as the scan progresses.
	if existing := s.findAudiobookDuplicate(ctx, book, cleanTitle); existing != nil {
		applyBookToMediaItem(existing, book)
		if existing.SortTitle == "" {
			existing.SortTitle = titleutil.DeriveDefaultSortTitle(existing.Title)
		}
		if err := s.itemRepo.Upsert(ctx, existing); err != nil {
			return "", err
		}
		return existing.ContentID, nil
	}

	return createAudiobookMediaItem(ctx, s.itemRepo, book, cleanTitle)
}

func (s *Scanner) updateExistingAudiobookMediaItem(ctx context.Context, contentID string, book *parsedAudiobook) error {
	items, err := s.itemRepo.GetByIDs(ctx, []string{contentID})
	if err != nil {
		return fmt.Errorf("get audiobook media item %s: %w", contentID, err)
	}
	if len(items) == 0 || items[0] == nil {
		return fmt.Errorf("audiobook media item %s not found", contentID)
	}
	item := items[0]
	applyBookToMediaItem(item, book)
	if item.SortTitle == "" {
		item.SortTitle = titleutil.DeriveDefaultSortTitle(item.Title)
	}
	return s.itemRepo.Upsert(ctx, item)
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

// upsertAudiobookMediaFiles writes one media_files row per audio file in the
// parsed audiobook. The content_id ties each file back to the media_items row.
// folderPath is used as the canonical_root_path / observed_root_path.
func (s *Scanner) upsertAudiobookMediaFiles(
	ctx context.Context,
	folder *models.MediaFolder,
	contentID string,
	folderPath string,
	book *parsedAudiobook,
) error {
	partTotal := len(book.Files)
	for idx, af := range book.Files {
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
			CanonicalRootPath:  folderPath,
			ObservedRootPath:   folderPath,
			ContentGroupKey:    contentID,
			GroupKeyVersion:    1,
			BaseTitle:          book.Title,
			BaseYear:           book.Year,
			BaseType:           "audiobook",
			IdentityConfidence: audiobookIdentityConfidence(book, af),
			FilePath:           af.Path,
			Chapters:           chapters,
			ProbeSource:        "local",
			Duration:           af.Duration,
			Bitrate:            af.Bitrate,
			CodecAudio:         af.CodecAudio,
			Container:          af.Container,
			AudioChannels:      af.AudioChannels,
		}
		if partTotal > 1 {
			mf.PresentationKind = "multipart"
			mf.PresentationGroupKey = contentID
			mf.PresentationPartIndex = idx + 1
			mf.PresentationPartTotal = partTotal
		}

		if _, err := s.fileRepo.Upsert(ctx, mf); err != nil {
			return fmt.Errorf("upsert media file %s: %w", af.Path, err)
		}
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
func (s *Scanner) upsertAudiobookSeries(ctx context.Context, contentID string, book *parsedAudiobook) error {
	if s.fileRepo == nil {
		return fmt.Errorf("fileRepo not configured on Scanner")
	}
	desiredName := strings.TrimSpace(book.Series)
	desiredIdx := parseSeriesIndex(book.SeriesPosition)

	// Read current row.
	var currentName *string
	var currentIdx *float64
	err := s.fileRepo.Pool().QueryRow(ctx, `
		SELECT series_name, series_index FROM audiobook_series WHERE content_id = $1
	`, contentID).Scan(&currentName, &currentIdx)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("query audiobook_series: %w", err)
	}

	if desiredName == "" {
		if currentName == nil {
			return nil // already absent
		}
		if _, delErr := s.fileRepo.Pool().Exec(ctx,
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
	if _, err := s.fileRepo.Pool().Exec(ctx, `
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
