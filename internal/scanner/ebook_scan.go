package scanner

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode"

	"github.com/Silo-Server/silo-server/internal/idgen"
	"github.com/Silo-Server/silo-server/internal/imageutil"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/titleutil"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

type ebookSQLExecutor interface {
	Exec(ctx context.Context, sql string, arguments ...any) (pgconn.CommandTag, error)
}

type filesystemMediaItemReader interface {
	GetByIDs(ctx context.Context, ids []string) ([]*models.MediaItem, error)
}

func ebookScanWorkers() int {
	if v := os.Getenv("SILO_EBOOK_SCAN_WORKERS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return audiobookScanWorkers()
}

func (s *Scanner) ScanEbookFolder(ctx context.Context, folder *models.MediaFolder) error {
	if s == nil || folder == nil {
		return fmt.Errorf("ScanEbookFolder: nil scanner or folder")
	}
	return s.scanEbookPaths(ctx, folder, folder.Paths, true)
}

// ebookRootScan captures the walk outcome for one configured library root.
type ebookRootScan struct {
	root         string
	files        []string
	rootErr      error // root stat failed, or the root is not a directory
	walkFailures int   // entries within the subtree the walk could not read or resolve
}

// failed reports whether the walk under this root is known to be incomplete.
// Files found under a failed root are still indexed, but the root is excluded
// from missing-file reconciliation: a transient mount/permission problem (or
// a mid-walk subtree error) must not cascade into marking — and, with
// empty_trash_after_scan, deleting — everything under it.
func (r *ebookRootScan) failed() bool {
	return r.rootErr != nil || r.walkFailures > 0
}

// collectEbookRootScans walks every configured root with the shared
// logical-tree walker (so symlinked roots and symlinked subdirectories
// resolve and scan exactly like the video pipeline) and records per-root walk
// failures. The only error returned is context cancellation.
func collectEbookRootScans(ctx context.Context, folderID int, roots []string) ([]ebookRootScan, error) {
	scans := make([]ebookRootScan, 0, len(roots))
	visitedPhysicalDirs := make(map[string]struct{})
	for _, root := range roots {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		cleanRoot := filepath.Clean(strings.TrimSpace(root))
		if cleanRoot == "" || cleanRoot == "." {
			continue
		}
		scan := ebookRootScan{root: cleanRoot}
		info, statErr := os.Stat(cleanRoot)
		switch {
		case statErr != nil:
			// Unmounted/missing/permission-broken root: failed, not empty.
			scan.rootErr = fmt.Errorf("stat root: %w", statErr)
		case !info.IsDir():
			scan.rootErr = fmt.Errorf("root is not a directory after symlink resolution")
		}
		if statErr == nil {
			if err := walkLogicalTree(ctx, cleanRoot, cleanRoot, walkModeEbook, visitedPhysicalDirs, &scan.files, &scan.walkFailures); err != nil {
				return nil, err
			}
		}
		if scan.failed() {
			slog.Warn("ebook scan: root walk incomplete; root excluded from missing-file reconciliation",
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

// splitEbookReconcileRoots partitions walked roots into the set that may take
// part in missing-file reconciliation and reports whether any reconcilable
// root saw at least one ebook on disk.
func splitEbookReconcileRoots(scans []ebookRootScan) (reconcileRoots []string, sawFiles bool) {
	reconcileRoots = make([]string, 0, len(scans))
	for i := range scans {
		scan := &scans[i]
		if scan.failed() {
			continue
		}
		if len(scan.files) > 0 {
			sawFiles = true
		}
		reconcileRoots = append(reconcileRoots, scan.root)
	}
	return reconcileRoots, sawFiles
}

func (s *Scanner) scanEbookPaths(ctx context.Context, folder *models.MediaFolder, roots []string, fullScan bool) error {
	if s == nil || folder == nil {
		return fmt.Errorf("scanEbookPaths: nil scanner or folder")
	}
	scans, err := collectEbookRootScans(ctx, folder.ID, roots)
	if err != nil {
		return err
	}
	// Every discovered file is indexed, including files under roots whose
	// walk partially failed: indexing is additive and safe, only the
	// destructive reconciliation below is restricted to cleanly walked roots.
	var candidates []string
	for i := range scans {
		candidates = append(candidates, scans[i].files...)
	}

	if len(candidates) == 0 {
		return s.reconcileEbookScan(ctx, folder, scans, nil, fullScan)
	}

	workers := ebookScanWorkers()
	slog.Info("ebook scan: starting",
		"folder_id", folder.ID,
		"candidates", len(candidates),
		"workers", workers,
	)
	reportEbookScanProgress(ctx, folder.ID, len(candidates), 0, 0, 0)

	ch := make(chan string, workers*2)
	groupLocks := newEbookGroupLocks()
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
				if err := s.reconcileEbookFile(ctx, folder, path, &skipped, groupLocks); err != nil {
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
					slog.Warn("ebook scan: file failed",
						"folder_id", folder.ID,
						"path", path,
						"error", err,
					)
				}
				n := atomic.AddInt64(&processed, 1)
				if n%500 == 0 || n == int64(len(candidates)) {
					failedCount := atomic.LoadInt64(&failed)
					skippedCount := atomic.LoadInt64(&skipped)
					slog.Info("ebook scan: progress",
						"folder_id", folder.ID,
						"processed", n,
						"failed", failedCount,
						"skipped", skippedCount,
						"total", len(candidates),
						"elapsed_sec", int(time.Since(start).Seconds()),
					)
					reportEbookScanProgress(ctx, folder.ID, len(candidates), int(n), int(failedCount), int(skippedCount))
				}
			}
		}()
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

	slog.Info("ebook scan: completed",
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
			return fmt.Errorf("ebook scan failed for every attempted folder_id=%d: %w", folder.ID, errors.Join(failures...))
		}
	}

	seenPaths := make(map[string]bool, len(candidates))
	for _, p := range candidates {
		seenPaths[p] = true
	}
	return s.reconcileEbookScan(ctx, folder, scans, seenPaths, fullScan)
}

// ebookCleanupGuardRepo is the slice of catalog.FolderRepository the
// empty-root cleanup guard needs.
type ebookCleanupGuardRepo interface {
	ConsumeEmptyCleanupAllowance(ctx context.Context, id int) (bool, error)
	SetScanWarning(ctx context.Context, id int, code, message string, warnedAt time.Time) error
}

// ebookEmptyCleanupAllowed mirrors the video pipeline's empty-root guard: a
// scan that saw zero ebooks while the DB still has rows under the scanned
// roots may only reconcile (and therefore delete) when the operator has
// explicitly confirmed the cleanup. Subtree scans never consume the
// folder-level allowance; the next full scan converges them.
func ebookEmptyCleanupAllowed(ctx context.Context, repo ebookCleanupGuardRepo, folderID int, fullScan bool) (bool, error) {
	if !fullScan || repo == nil {
		return false, nil
	}
	allowed, err := repo.ConsumeEmptyCleanupAllowance(ctx, folderID)
	if err != nil {
		return false, fmt.Errorf("checking empty cleanup confirmation for folder %d: %w", folderID, err)
	}
	if !allowed {
		if err := repo.SetScanWarning(ctx, folderID,
			"empty_root",
			"Scan found 0 media files; cleanup was skipped until deletion is confirmed.",
			time.Now().UTC(),
		); err != nil {
			return false, fmt.Errorf("recording empty-root warning for folder %d: %w", folderID, err)
		}
	}
	return allowed, nil
}

// reconcileEbookScan applies the post-walk safety policy and then performs
// missing-file reconciliation for the roots that walked cleanly.
func (s *Scanner) reconcileEbookScan(ctx context.Context, folder *models.MediaFolder, scans []ebookRootScan, seenPaths map[string]bool, fullScan bool) error {
	reconcileRoots, sawFiles := splitEbookReconcileRoots(scans)
	if len(reconcileRoots) == 0 {
		if len(scans) > 0 {
			slog.Warn("ebook scan: every root walk failed; skipping missing-file reconciliation",
				"folder_id", folder.ID,
			)
		}
		return nil
	}
	if s.fileRepo == nil || s.libraryRepo == nil {
		return nil
	}

	if !sawFiles {
		existingCount := 0
		for _, root := range reconcileRoots {
			existing, err := s.fileRepo.GetByFolderAndPathPrefix(ctx, folder.ID, root)
			if err != nil {
				return fmt.Errorf("listing existing ebook files for %q: %w", root, err)
			}
			existingCount += len(existing)
		}
		if existingCount > 0 {
			var guard ebookCleanupGuardRepo
			if s.folderRepo != nil {
				guard = s.folderRepo
			}
			allowed, err := ebookEmptyCleanupAllowed(ctx, guard, folder.ID, fullScan)
			if err != nil {
				return err
			}
			if !allowed {
				slog.Warn("ebook scan: walk saw zero ebooks but the database has files under the scanned roots; skipping reconciliation until cleanup is confirmed",
					"folder_id", folder.ID,
					"existing_files", existingCount,
					"full_scan", fullScan,
				)
				return nil
			}
		}
	}

	if err := s.reconcileMissingEbookFiles(ctx, folder, reconcileRoots, seenPaths); err != nil {
		return err
	}
	if fullScan && s.folderRepo != nil {
		// The cleanup either ran with files present or was explicitly
		// confirmed; any prior empty-root warning is stale now.
		if err := s.folderRepo.ClearScanWarning(ctx, folder.ID); err != nil {
			return fmt.Errorf("clearing scan warning for folder %d: %w", folder.ID, err)
		}
	}
	return nil
}

// reconcileMissingEbookFiles mirrors the video/audio scan cleanup: DB files
// under the scanned roots that were not seen on disk are marked missing, the
// folder trash is optionally emptied, and library memberships are reconciled
// so items with no remaining files are removed (renames therefore converge on
// the newly indexed path instead of leaving a stale duplicate item).
func (s *Scanner) reconcileMissingEbookFiles(ctx context.Context, folder *models.MediaFolder, roots []string, seenPaths map[string]bool) error {
	if s.fileRepo == nil || s.libraryRepo == nil || len(roots) == 0 {
		return nil
	}

	now := time.Now().UTC()
	missing := 0
	for _, root := range roots {
		existing, err := s.fileRepo.GetByFolderAndPathPrefix(ctx, folder.ID, root)
		if err != nil {
			return fmt.Errorf("listing existing ebook files for %q: %w", root, err)
		}
		for _, mf := range existing {
			if mf == nil || seenPaths[mf.FilePath] {
				continue
			}
			if mf.MissingSince == nil {
				if err := s.fileRepo.MarkMissing(ctx, mf.ID, now); err != nil {
					slog.Error("ebook scan: failed to mark file missing",
						"folder_id", folder.ID,
						"path", mf.FilePath,
						"error", err,
					)
					continue
				}
			}
			missing++
		}
	}

	if s.emptyTrashAfterScan {
		trashed, err := s.fileRepo.DeleteMissingByFolder(ctx, folder.ID)
		if err != nil {
			return fmt.Errorf("emptying trash for folder %d: %w", folder.ID, err)
		}
		if trashed > 0 {
			slog.Info("ebook scan: emptied trash", "folder_id", folder.ID, "deleted", trashed)
		}
	}

	removedMemberships, deletedItems, orphanedImageDirs, err := s.reconcileLibraryMemberships(ctx, folder.ID)
	if err != nil {
		return fmt.Errorf("reconciling library membership for folder %d: %w", folder.ID, err)
	}

	// Best-effort S3 image cleanup for orphaned items.
	if s.s3Client != nil && len(orphanedImageDirs) > 0 {
		bucket := s.s3Client.Bucket()
		for _, dir := range orphanedImageDirs {
			_, _ = s.s3Client.DeletePrefix(ctx, bucket, dir)
		}
	}

	if missing > 0 || removedMemberships > 0 || deletedItems > 0 {
		slog.Info("ebook scan: reconciled missing files",
			"folder_id", folder.ID,
			"missing", missing,
			"memberships_removed", removedMemberships,
			"items_deleted", deletedItems,
		)
	}
	return nil
}

func reportEbookScanProgress(ctx context.Context, folderID int, total, processed, failed, skipped int) {
	reportProgress(ctx, ProgressUpdate{
		Phase:           "ebook_scan",
		Message:         fmt.Sprintf("Scanning ebooks in folder %d", folderID),
		CurrentScope:    strconv.Itoa(folderID),
		TotalFiles:      total,
		FilesDiscovered: total,
		FilesProcessed:  processed,
		Errors:          failed,
		Unchanged:       skipped,
	})
}

func (s *Scanner) reconcileEbookFile(ctx context.Context, folder *models.MediaFolder, filePath string, skipped *int64, groupLocks *ebookGroupLocks) error {
	info, err := os.Stat(filePath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("stat ebook file %s: %w", filePath, err)
	}
	size := info.Size()
	modifiedAt := normalizeFileModifiedAt(info.ModTime())

	isUnchanged, skipErr := s.ebookFileShouldSkip(ctx, folder, filePath, size, modifiedAt)
	if skipErr != nil {
		slog.Warn("ebook scan: skip-check failed, falling through",
			"folder_id", folder.ID,
			"path", filePath,
			"error", skipErr,
		)
	} else if isUnchanged {
		atomic.AddInt64(skipped, 1)
		return nil
	}

	parsed, err := parseEbookFile(filePath)
	if err != nil {
		return fmt.Errorf("parse ebook file %s: %w", filePath, err)
	}
	if parsed.Title == "" {
		parsed.Title = ebookTitleFromPath(filePath)
	}

	groupKey := ebookContentGroupKey(&parsed, filePath)
	unlock := groupLocks.lock(groupKey)
	defer unlock()

	contentID, curated, err := s.upsertEbookMediaItem(ctx, folder.ID, filePath, &parsed, groupKey)
	if err != nil {
		return fmt.Errorf("upsert ebook item: %w", err)
	}
	if err := s.upsertEbookMediaFile(ctx, folder, contentID, filePath, size, modifiedAt, &parsed, groupKey); err != nil {
		return fmt.Errorf("upsert ebook file: %w", err)
	}
	if err := applyEbookLocalCover(ctx, s.itemRepo, s.imageCacher, contentID, filePath, &parsed); err != nil {
		slog.Warn("ebook scan: local cover upload failed",
			"folder_id", folder.ID,
			"content_id", contentID,
			"path", filePath,
			"error", err,
		)
	}
	if err := s.upsertEbookPeople(ctx, contentID, &parsed, curated); err != nil {
		return fmt.Errorf("upsert ebook people: %w", err)
	}
	if err := s.upsertEbookSeries(ctx, contentID, &parsed, curated); err != nil {
		return fmt.Errorf("upsert ebook series: %w", err)
	}
	if err := insertEbookLibraryMembership(ctx, s.fileRepo.Pool(), contentID, folder.ID); err != nil {
		return fmt.Errorf("upsert ebook library membership: %w", err)
	}
	if parsed.ISBN != "" {
		if err := insertEbookISBNProviderID(ctx, s.fileRepo.Pool(), contentID, parsed.ISBN); err != nil {
			return fmt.Errorf("upsert ebook ISBN provider id: %w", err)
		}
	}
	slog.Info("ebook scan: indexed",
		"folder_id", folder.ID,
		"content_id", contentID,
		"title", parsed.Title,
		"authors", parsed.Authors,
		"path", filePath,
	)
	return nil
}

func (s *Scanner) ebookFileShouldSkip(ctx context.Context, folder *models.MediaFolder, filePath string, size int64, modifiedAt time.Time) (bool, error) {
	if s.fileRepo == nil || s.itemRepo == nil {
		return false, nil
	}
	existing, err := s.fileRepo.ListByObservedRootPath(ctx, folder.ID, filePath)
	if err != nil {
		return false, fmt.Errorf("list existing files: %w", err)
	}
	if len(existing) != 1 {
		return false, nil
	}
	mf := existing[0]
	if mf.FilePath != filePath || mf.FileSize != size || mf.FileModifiedAt == nil || !sameFileModifiedAt(mf.FileModifiedAt, modifiedAt) {
		return false, nil
	}
	if mf.ContentID == "" {
		return false, nil
	}
	if mf.GroupKeyVersion != ebookGroupKeyVersion {
		// The grouping scheme changed since this row was written; reprocess
		// once so the stored key is rewritten under the current scheme and
		// sibling-format lookups can find it again.
		return false, nil
	}
	statuses, err := s.itemRepo.GetStatusByIDs(ctx, []string{mf.ContentID})
	if err != nil {
		return false, fmt.Errorf("get item status: %w", err)
	}
	return !strings.EqualFold(strings.TrimSpace(statuses[mf.ContentID]), "unmatched"), nil
}

// upsertEbookMediaItem resolves or creates the media item for the file and
// reports whether the item's metadata is curated (provider-matched), in which
// case dependent writes (people, series) must be fill-empty only.
func (s *Scanner) upsertEbookMediaItem(ctx context.Context, folderID int, filePath string, book *parsedEbook, groupKey string) (string, bool, error) {
	if s.itemRepo == nil {
		return "", false, fmt.Errorf("itemRepo not configured on Scanner")
	}
	if s.fileRepo == nil {
		return "", false, fmt.Errorf("fileRepo not configured on Scanner")
	}

	existingID, err := s.fileRepo.FindContentIDByRootPath(ctx, folderID, filePath, "ebook")
	if err != nil {
		return "", false, fmt.Errorf("find ebook by root path: %w", err)
	}
	if existingID != "" {
		curated, err := updateExistingEbookMediaItem(ctx, s.itemRepo, s.itemRepo, existingID, book)
		if err != nil {
			return "", false, err
		}
		return existingID, curated, nil
	}
	if existing := s.findEbookByFilePath(ctx, filePath); existing != nil {
		if ebookItemHasCuratedMetadata(existing) {
			return existing.ContentID, true, nil
		}
		applyEbookToMediaItem(existing, book)
		if existing.SortTitle == "" {
			existing.SortTitle = titleutil.DeriveDefaultSortTitle(existing.Title)
		}
		if err := s.itemRepo.Upsert(ctx, existing); err != nil {
			return "", false, err
		}
		return existing.ContentID, false, nil
	}
	if existing := s.findEbookByContentGroupKey(ctx, folderID, groupKey, book.Format, filePath); existing != nil {
		// The sibling file joins the group either way, but curated metadata
		// on a provider-matched item must not be clobbered by whatever this
		// file happens to embed.
		if ebookItemHasCuratedMetadata(existing) {
			return existing.ContentID, true, nil
		}
		applyEbookToMediaItem(existing, book)
		if existing.SortTitle == "" {
			existing.SortTitle = titleutil.DeriveDefaultSortTitle(existing.Title)
		}
		if err := s.itemRepo.Upsert(ctx, existing); err != nil {
			return "", false, err
		}
		return existing.ContentID, false, nil
	}
	contentID, err := resolveEbookMediaItem(ctx, s.fileRepo, s.itemRepo, folderID, filePath, book)
	if err != nil {
		return "", false, err
	}
	return contentID, false, nil
}

func resolveEbookMediaItem(
	ctx context.Context,
	rootFinder filesystemRootContentFinder,
	itemWriter filesystemMediaItemWriter,
	folderID int,
	filePath string,
	book *parsedEbook,
) (string, error) {
	if rootFinder == nil {
		return "", fmt.Errorf("root content finder not configured")
	}
	if itemWriter == nil {
		return "", fmt.Errorf("media item writer not configured")
	}
	existingID, err := rootFinder.FindContentIDByRootPath(ctx, folderID, filePath, "ebook")
	if err != nil {
		return "", fmt.Errorf("find ebook by root path: %w", err)
	}
	if existingID != "" {
		return existingID, nil
	}

	cleanTitle := strings.TrimSpace(book.Title)
	if cleanTitle == "" {
		cleanTitle = ebookTitleFromPath(filePath)
	}
	return createEbookMediaItem(ctx, itemWriter, book, cleanTitle)
}

func createEbookMediaItem(ctx context.Context, itemWriter filesystemMediaItemWriter, book *parsedEbook, cleanTitle string) (string, error) {
	id, err := idgen.NextID()
	if err != nil {
		return "", fmt.Errorf("generate content_id: %w", err)
	}
	item := &models.MediaItem{
		ContentID: id,
		Type:      "ebook",
		// Explicit "pending" (the item upsert writes the literal status, so the
		// DB default never applies): enrichment promotes it to "matched", which
		// arms ebookItemHasCuratedMetadata against file-metadata clobbering.
		Status:    "pending",
		Title:     cleanTitle,
		SortTitle: titleutil.DeriveDefaultSortTitle(cleanTitle),
		Year:      book.Year,
	}
	applyEbookToMediaItem(item, book)
	if item.SortTitle == "" {
		item.SortTitle = titleutil.DeriveDefaultSortTitle(item.Title)
	}
	if err := itemWriter.Upsert(ctx, item); err != nil {
		return "", err
	}
	return id, nil
}

// updateExistingEbookMediaItem refreshes a known item from file metadata and
// reports whether the item's metadata is curated (and was therefore left
// untouched).
func updateExistingEbookMediaItem(ctx context.Context, itemReader filesystemMediaItemReader, itemWriter filesystemMediaItemWriter, contentID string, book *parsedEbook) (bool, error) {
	if itemReader == nil {
		return false, fmt.Errorf("media item reader not configured")
	}
	if itemWriter == nil {
		return false, fmt.Errorf("media item writer not configured")
	}
	items, err := itemReader.GetByIDs(ctx, []string{contentID})
	if err != nil {
		return false, fmt.Errorf("get ebook media item %s: %w", contentID, err)
	}
	if len(items) == 0 || items[0] == nil {
		return false, fmt.Errorf("ebook media item %s not found", contentID)
	}
	item := items[0]
	if ebookItemHasCuratedMetadata(item) {
		// A matched item's metadata was curated by a provider or a person;
		// re-parsing the file (mtime change, group-key version bump) must not
		// clobber it with embedded file metadata.
		return true, nil
	}
	applyEbookToMediaItem(item, book)
	if item.SortTitle == "" {
		item.SortTitle = titleutil.DeriveDefaultSortTitle(item.Title)
	}
	return false, itemWriter.Upsert(ctx, item)
}

// ebookItemHasCuratedMetadata reports whether the item's metadata is owned by
// a provider match (or manual curation) and therefore must not be overwritten
// by metadata embedded in scanned files.
func ebookItemHasCuratedMetadata(item *models.MediaItem) bool {
	return item != nil && strings.EqualFold(strings.TrimSpace(item.Status), "matched")
}

func applyEbookToMediaItem(item *models.MediaItem, book *parsedEbook) {
	item.Type = "ebook"
	if title := strings.TrimSpace(book.Title); title != "" {
		item.Title = title
	}
	if book.Year > 0 {
		item.Year = book.Year
	}
	if book.Description != "" && (item.Overview == "" || looksLikeHTML(item.Overview)) {
		item.Overview = book.Description
	}
	if book.Publisher != "" {
		item.Studios = mergeUniqueStrings(item.Studios, []string{book.Publisher})
	}
	if len(book.Genres) > 0 {
		item.Genres = mergeUniqueStrings(item.Genres, book.Genres)
	}
	if !book.PublishedAt.IsZero() && item.ReleaseDate == nil {
		rd := book.PublishedAt.UTC().Format("2006-01-02")
		item.ReleaseDate = &rd
	}
	if book.Language != "" && item.OriginalLanguage == "" {
		item.OriginalLanguage = book.Language
	}
}

func (s *Scanner) findEbookByFilePath(ctx context.Context, filePath string) *models.MediaItem {
	if s.fileRepo == nil {
		return nil
	}
	var existingID string
	err := s.fileRepo.Pool().QueryRow(ctx, `
		SELECT mf.content_id
		FROM media_files mf
		JOIN media_items mi ON mi.content_id = mf.content_id
		WHERE mf.file_path = $1
		  AND mi.type = 'ebook'
		LIMIT 1
	`, filePath).Scan(&existingID)
	if err != nil || existingID == "" {
		return nil
	}
	items, err := s.itemRepo.GetByIDs(ctx, []string{existingID})
	if err != nil || len(items) == 0 {
		return nil
	}
	return items[0]
}

// findEbookByContentGroupKey locates an existing item that a sibling format of
// the same book should join. Grouping merges *different* formats only: an item
// that already owns another file of the same format is excluded, so two
// distinct books whose sparse metadata collides on the same key (e.g. every
// volume carrying the series name as its title) stay separate items instead of
// silently merging.
func (s *Scanner) findEbookByContentGroupKey(ctx context.Context, folderID int, groupKey string, format string, filePath string) *models.MediaItem {
	if s.fileRepo == nil || s.itemRepo == nil || strings.TrimSpace(groupKey) == "" {
		return nil
	}
	var existingID string
	err := s.fileRepo.Pool().QueryRow(ctx, `
		SELECT mf.content_id
		FROM media_files mf
		JOIN media_items mi ON mi.content_id = mf.content_id
		WHERE mf.media_folder_id = $1
		  AND mf.group_key_version = $2
		  AND mf.content_group_key = $3
		  AND mf.missing_since IS NULL
		  AND mi.type = 'ebook'
		  AND NOT EXISTS (
			SELECT 1 FROM media_files dup
			WHERE dup.content_id = mf.content_id
			  AND dup.missing_since IS NULL
			  AND lower(dup.container) = lower($4)
			  AND dup.file_path <> $5
		  )
		ORDER BY CASE WHEN lower(trim(mi.status)) = 'matched' THEN 0 ELSE 1 END,
		         mf.id ASC
		LIMIT 1
	`, folderID, ebookGroupKeyVersion, groupKey, format, filePath).Scan(&existingID)
	if err != nil || existingID == "" {
		return nil
	}
	items, err := s.itemRepo.GetByIDs(ctx, []string{existingID})
	if err != nil || len(items) == 0 {
		return nil
	}
	return items[0]
}

// ebookGroupKeyVersion versions the ebookContentGroupKey scheme. Bump it
// whenever the key shape changes so rows keyed under an older scheme are
// reprocessed and re-keyed (see ebookFileShouldSkip) instead of silently never
// matching sibling-format lookups again.
const ebookGroupKeyVersion = 2

func (s *Scanner) upsertEbookMediaFile(ctx context.Context, folder *models.MediaFolder, contentID string, filePath string, size int64, modifiedAt time.Time, book *parsedEbook, groupKey string) error {
	mf := buildEbookMediaFile(folder, contentID, filePath, size, modifiedAt, book, groupKey)
	if _, err := s.fileRepo.Upsert(ctx, mf); err != nil {
		return fmt.Errorf("upsert media file %s: %w", filePath, err)
	}
	return nil
}

func buildEbookMediaFile(folder *models.MediaFolder, contentID string, filePath string, size int64, modifiedAt time.Time, book *parsedEbook, groupKey string) models.MediaFile {
	return models.MediaFile{
		ContentID:          contentID,
		MediaFolderID:      folder.ID,
		CanonicalRootPath:  filePath,
		ObservedRootPath:   filePath,
		ContentGroupKey:    groupKey,
		GroupKeyVersion:    ebookGroupKeyVersion,
		BaseTitle:          book.Title,
		BaseYear:           book.Year,
		BaseType:           "ebook",
		IdentityConfidence: ebookIdentityConfidence(book),
		FilePath:           filePath,
		FileSize:           size,
		FileModifiedAt:     &modifiedAt,
		Container:          book.Format,
		Duration:           book.PageCount,
		ProbeSource:        "local",
	}
}

func ebookContentGroupKey(book *parsedEbook, filePath string) string {
	if book != nil {
		if isbn := normalizeEbookISBN(book.ISBN); isbn != "" {
			return "ebook:isbn:" + isbn
		}
	}

	title := ""
	authors := []string(nil)
	if book != nil {
		title = strings.TrimSpace(book.Title)
		authors = book.Authors
	}
	if title == "" {
		title = ebookTitleFromPath(filePath)
	}
	normalizedTitle := normalizeEbookIdentityPart(title)
	if normalizedTitle == "" {
		cleanPath := strings.TrimSpace(filepath.Clean(filePath))
		if cleanPath == "." {
			return ""
		}
		return "ebook:path:" + cleanPath
	}

	normalizedAuthors := make([]string, 0, len(authors))
	seenAuthors := make(map[string]struct{}, len(authors))
	for _, author := range authors {
		normalized := normalizeEbookIdentityPart(author)
		if normalized == "" {
			continue
		}
		if _, ok := seenAuthors[normalized]; ok {
			continue
		}
		seenAuthors[normalized] = struct{}{}
		normalizedAuthors = append(normalizedAuthors, normalized)
	}
	sort.Strings(normalizedAuthors)
	if len(normalizedAuthors) > 0 {
		return "ebook:title_author:" + normalizedTitle + "|" + strings.Join(normalizedAuthors, ",")
	}

	dir := strings.TrimSpace(filepath.Clean(filepath.Dir(filePath)))
	if dir == "." {
		dir = ""
	}
	return "ebook:title:" + normalizedTitle + "|dir:" + normalizeEbookIdentityPart(dir)
}

func ebookTitleFromPath(filePath string) string {
	base := filepath.Base(filePath)
	if base == "." || base == string(filepath.Separator) {
		return ""
	}
	// ".fb2.zip" is a double extension that filepath.Ext (and the
	// normalized ".fbz" format token) would leave half-stripped.
	if strings.HasSuffix(strings.ToLower(base), ".fb2.zip") {
		return base[:len(base)-len(".fb2.zip")]
	}
	return strings.TrimSuffix(base, filepath.Ext(base))
}

func normalizeEbookIdentityPart(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	var b strings.Builder
	previousSpace := false
	for _, r := range value {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
			previousSpace = false
			continue
		}
		if unicode.IsSpace(r) || r == '&' || r == '-' || r == '_' || r == ':' || r == '/' || r == '\\' || r == ',' || r == '.' {
			if b.Len() > 0 && !previousSpace {
				b.WriteByte(' ')
				previousSpace = true
			}
		}
	}
	return strings.TrimSpace(b.String())
}

type ebookGroupLocks struct {
	mu    sync.Mutex
	locks map[string]*sync.Mutex
}

func newEbookGroupLocks() *ebookGroupLocks {
	return &ebookGroupLocks{locks: make(map[string]*sync.Mutex)}
}

func (l *ebookGroupLocks) lock(key string) func() {
	if l == nil || strings.TrimSpace(key) == "" {
		return func() {}
	}
	l.mu.Lock()
	groupLock := l.locks[key]
	if groupLock == nil {
		groupLock = &sync.Mutex{}
		l.locks[key] = groupLock
	}
	l.mu.Unlock()

	groupLock.Lock()
	return groupLock.Unlock
}

// localEbookPosterPrefix mirrors the storage layout produced by
// imagecache.CacheEbookCover ("local/ebooks/{contentID}/poster/..."); it marks
// posters this pipeline owns and is therefore allowed to refresh.
const localEbookPosterPrefix = "local/ebooks/"

type ebookCoverMetadataStore interface {
	GetPoster(ctx context.Context, contentID string) (posterPath string, posterThumbhash string, err error)
	SetLocalPoster(ctx context.Context, contentID, posterPath, thumbhash, localPrefix string) (bool, error)
}

// applyEbookLocalCover records the best locally available cover for the file:
// a sidecar image that belongs to this book wins over the embedded cover, and
// exactly one cover is applied per reconcile. A sidecar discovery error does
// not block the embedded fallback.
func applyEbookLocalCover(ctx context.Context, store ebookCoverMetadataStore, cacher ebookCoverCacher, contentID string, ebookFilePath string, book *parsedEbook) error {
	if store == nil || cacher == nil || contentID == "" {
		return nil
	}
	var data []byte
	var sidecarErr error
	if ebookFilePath != "" {
		cover, _, err := findSidecarBookCover(ebookFilePath)
		if err != nil {
			sidecarErr = err
		} else if cover != nil {
			data = cover.Bytes
		}
	}
	if len(data) == 0 && book != nil && book.Cover != nil {
		data = book.Cover.Bytes
	}
	if len(data) == 0 {
		return sidecarErr
	}
	return errors.Join(sidecarErr, cacheEbookCoverBytes(ctx, store, cacher, contentID, data))
}

func cacheEbookCoverBytes(ctx context.Context, store ebookCoverMetadataStore, cacher ebookCoverCacher, contentID string, data []byte) error {
	if len(data) == 0 {
		return nil
	}
	existingPath, existingThumbhash, err := store.GetPoster(ctx, contentID)
	if err != nil {
		return fmt.Errorf("get ebook poster for cover: %w", err)
	}
	existingPath = strings.TrimSpace(existingPath)
	if existingPath != "" {
		// Provider or manually applied artwork always wins over scan covers.
		if !strings.HasPrefix(existingPath, localEbookPosterPrefix) {
			return nil
		}
		// Re-extracted bytes of an unchanged cover hash identically; skip
		// the variant regeneration and upload churn. A replaced cover hashes
		// differently and falls through to refresh the stale poster.
		if thumbhash, err := imageutil.Thumbhash(data); err == nil && thumbhash == existingThumbhash {
			return nil
		}
	}
	basePath, ext, thumbhash, err := cacher.CacheEbookCover(ctx, data, contentID)
	if err != nil {
		return err
	}
	posterPath := strings.TrimRight(basePath, "/") + "/original" + ext
	if _, err := store.SetLocalPoster(ctx, contentID, posterPath, thumbhash, localEbookPosterPrefix); err != nil {
		return fmt.Errorf("set ebook local poster: %w", err)
	}
	return nil
}

var sidecarCoverNames = []string{"cover", "folder", "front", "poster", "thumbnail"}
var sidecarCoverExtensions = []string{".jpg", ".jpeg", ".png", ".webp", ".avif", ".gif", ".bmp"}

// findSidecarBookCover looks for cover art next to the ebook file. An image
// named after the book file always belongs to it; the generic artwork names
// (cover.jpg, folder.png, ...) are trusted only when this is the directory's
// sole ebook, so one cover.jpg in a flat multi-book folder is not applied to
// every book in it.
func findSidecarBookCover(ebookFilePath string) (*parsedEbookCover, string, error) {
	if ebookFilePath == "" {
		return nil, "", nil
	}
	entries, err := os.ReadDir(filepath.Dir(ebookFilePath))
	if err != nil {
		return nil, "", err
	}
	byName := make(map[string]string, len(entries))
	ebookCount := 0
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		if SupportsEbookFile(entry.Name()) {
			ebookCount++
		}
		byName[strings.ToLower(entry.Name())] = filepath.Join(filepath.Dir(ebookFilePath), entry.Name())
	}

	var candidates []string
	if base := strings.ToLower(ebookTitleFromPath(ebookFilePath)); base != "" {
		for _, ext := range sidecarCoverExtensions {
			candidates = append(candidates, base+ext)
		}
	}
	if ebookCount <= 1 {
		for _, name := range sidecarCoverNames {
			for _, ext := range sidecarCoverExtensions {
				candidates = append(candidates, name+ext)
			}
		}
	}
	for _, candidate := range candidates {
		path := byName[candidate]
		if path == "" {
			continue
		}
		data, err := readSidecarCover(path)
		if err != nil {
			return nil, path, err
		}
		return &parsedEbookCover{
			ContentType: ebookImageContentType(path),
			Bytes:       data,
		}, path, nil
	}
	return nil, "", nil
}

func readSidecarCover(path string) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if info.Size() > maxEPUBMetadataEntrySize {
		return nil, fmt.Errorf("sidecar cover too large: %s", path)
	}
	data, err := io.ReadAll(io.LimitReader(file, maxEPUBMetadataEntrySize+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxEPUBMetadataEntrySize {
		return nil, fmt.Errorf("sidecar cover too large: %s", path)
	}
	if len(data) == 0 {
		return nil, fmt.Errorf("sidecar cover empty: %s", path)
	}
	return data, nil
}

func insertEbookLibraryMembership(ctx context.Context, exec ebookSQLExecutor, contentID string, folderID int) error {
	_, err := exec.Exec(ctx, `
		INSERT INTO media_item_libraries (content_id, media_folder_id, first_seen_at)
		VALUES ($1, $2, NOW())
		ON CONFLICT (content_id, media_folder_id) DO NOTHING
	`, contentID, folderID)
	return err
}

func insertEbookISBNProviderID(ctx context.Context, exec ebookSQLExecutor, contentID string, isbn string) error {
	_, err := exec.Exec(ctx, `
		INSERT INTO media_item_provider_ids (content_id, provider, provider_id, item_type)
		VALUES ($1, 'isbn', $2, 'ebook')
		ON CONFLICT DO NOTHING
	`, contentID, isbn)
	return err
}

type ebookCredit struct {
	Name string
	Kind models.PersonKind
}

func ebookPeopleCreditsEqual(existing []models.ItemPerson, desired []ebookCredit) bool {
	existingAuthors := make([]models.ItemPerson, 0, len(existing))
	for _, p := range existing {
		if p.Kind == models.PersonKindNarrator {
			return false
		}
		if p.Kind == models.PersonKindAuthor {
			existingAuthors = append(existingAuthors, p)
		}
	}
	if len(existingAuthors) != len(desired) {
		return false
	}
	type key struct {
		name string
		kind models.PersonKind
	}
	have := make(map[key]struct{}, len(existingAuthors))
	for _, p := range existingAuthors {
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

type ebookResolvedAuthor struct {
	ID   int64
	Name string
}

func mergeEbookPeople(existing []models.ItemPerson, authors []ebookResolvedAuthor) []models.ItemPerson {
	people := make([]models.ItemPerson, 0, len(existing)+len(authors))
	for _, p := range existing {
		if p.Kind == models.PersonKindAuthor || p.Kind == models.PersonKindNarrator {
			continue
		}
		p.SortOrder = len(people)
		people = append(people, p)
	}
	for _, author := range authors {
		people = append(people, models.ItemPerson{
			Person:    models.Person{ID: author.ID, Name: author.Name},
			Kind:      models.PersonKindAuthor,
			SortOrder: len(people),
		})
	}
	return people
}

func ebookPeopleForReplace(existing []models.ItemPerson, getErr error, authors []ebookResolvedAuthor) ([]models.ItemPerson, error) {
	if getErr != nil {
		return nil, fmt.Errorf("get ebook people: %w", getErr)
	}
	return mergeEbookPeople(existing, authors), nil
}

// ebookPeopleWriteAllowed reports whether file-embedded authors may be written
// for the item. Curated (provider-matched) items are fill-empty only: file
// metadata may supply authors when the item has none, but must never replace
// provider-enriched author credits.
func ebookPeopleWriteAllowed(curated bool, existing []models.ItemPerson) bool {
	if !curated {
		return true
	}
	for _, p := range existing {
		if p.Kind == models.PersonKindAuthor {
			return false
		}
	}
	return true
}

func (s *Scanner) upsertEbookPeople(ctx context.Context, contentID string, book *parsedEbook, curated bool) error {
	if s.personRepo == nil {
		return fmt.Errorf("personRepo not configured on Scanner")
	}
	if s.itemRepo == nil {
		return fmt.Errorf("itemRepo not configured on Scanner")
	}

	var desired []ebookCredit
	for _, author := range book.Authors {
		if name := strings.TrimSpace(author); name != "" {
			desired = append(desired, ebookCredit{Name: name, Kind: models.PersonKindAuthor})
		}
	}
	if len(desired) == 0 {
		return nil
	}

	existing, err := s.itemRepo.GetPeople(ctx, contentID)
	if err != nil {
		return fmt.Errorf("get ebook people: %w", err)
	}
	if !ebookPeopleWriteAllowed(curated, existing) {
		return nil
	}
	if ebookPeopleCreditsEqual(existing, desired) {
		return nil
	}

	authors := make([]ebookResolvedAuthor, 0, len(desired))
	for _, c := range desired {
		personID, err := s.personRepo.FindOrCreate(ctx, models.Person{Name: c.Name})
		if err != nil {
			return fmt.Errorf("find-or-create person %q: %w", c.Name, err)
		}
		authors = append(authors, ebookResolvedAuthor{ID: personID, Name: c.Name})
	}
	people, err := ebookPeopleForReplace(existing, nil, authors)
	if err != nil {
		return err
	}
	return s.itemRepo.ReplacePeople(ctx, contentID, people)
}

func (s *Scanner) upsertEbookSeries(ctx context.Context, contentID string, book *parsedEbook, curated bool) error {
	if s == nil {
		return fmt.Errorf("Scanner not configured")
	}
	if s.fileRepo == nil {
		return fmt.Errorf("fileRepo not configured on Scanner")
	}

	var currentName *string
	var currentIdx *float64
	err := s.fileRepo.Pool().QueryRow(ctx, `
		SELECT series_name, series_index FROM ebook_series WHERE content_id = $1
	`, contentID).Scan(&currentName, &currentIdx)

	plan, err := planEbookSeriesWrite(book, currentName, currentIdx, err, curated)
	if err != nil {
		return err
	}

	switch plan.Kind {
	case ebookSeriesWriteNone:
		return nil
	case ebookSeriesWriteDelete:
		if _, delErr := s.fileRepo.Pool().Exec(ctx,
			`DELETE FROM ebook_series WHERE content_id = $1`, contentID); delErr != nil {
			return fmt.Errorf("delete ebook_series row: %w", delErr)
		}
		return nil
	case ebookSeriesWriteUpsert:
		var idx any
		if plan.Index != nil {
			idx = *plan.Index
		}
		if _, err := s.fileRepo.Pool().Exec(ctx, `
		INSERT INTO ebook_series (content_id, series_name, series_index, updated_at)
		VALUES ($1, $2, $3, NOW())
		ON CONFLICT (content_id) DO UPDATE SET
			series_name  = EXCLUDED.series_name,
			series_index = EXCLUDED.series_index,
			updated_at   = NOW()
	`, contentID, plan.Name, idx); err != nil {
			return fmt.Errorf("upsert ebook_series row: %w", err)
		}
		return nil
	default:
		return fmt.Errorf("unknown ebook_series write kind: %d", plan.Kind)
	}
}

func ebookSeriesDesired(book *parsedEbook) (string, *float64) {
	if book == nil {
		return "", nil
	}
	return strings.TrimSpace(book.Series), parseSeriesIndex(book.SeriesIndex)
}

type ebookSeriesWriteKind int

const (
	ebookSeriesWriteNone ebookSeriesWriteKind = iota
	ebookSeriesWriteDelete
	ebookSeriesWriteUpsert
)

type ebookSeriesWritePlan struct {
	Kind  ebookSeriesWriteKind
	Name  string
	Index *float64
}

// planEbookSeriesWrite decides how the ebook_series row should change for
// file-embedded series metadata. fillOnly applies to curated
// (provider-matched) items: the file may supply a series when the item has
// none, but must never replace or delete an existing (provider-enriched) row.
func planEbookSeriesWrite(book *parsedEbook, currentName *string, currentIdx *float64, queryErr error, fillOnly bool) (ebookSeriesWritePlan, error) {
	if queryErr != nil {
		if !errors.Is(queryErr, pgx.ErrNoRows) {
			return ebookSeriesWritePlan{}, fmt.Errorf("query ebook_series: %w", queryErr)
		}
		currentName = nil
		currentIdx = nil
	}

	desiredName, desiredIdx := ebookSeriesDesired(book)
	if desiredName == "" {
		if currentName == nil || fillOnly {
			return ebookSeriesWritePlan{Kind: ebookSeriesWriteNone}, nil
		}
		return ebookSeriesWritePlan{Kind: ebookSeriesWriteDelete}, nil
	}

	if currentName != nil {
		if fillOnly {
			return ebookSeriesWritePlan{Kind: ebookSeriesWriteNone}, nil
		}
		if *currentName == desiredName && floatPtrEqual(currentIdx, desiredIdx) {
			return ebookSeriesWritePlan{Kind: ebookSeriesWriteNone}, nil
		}
	}

	return ebookSeriesWritePlan{
		Kind:  ebookSeriesWriteUpsert,
		Name:  desiredName,
		Index: desiredIdx,
	}, nil
}

func ebookIdentityConfidence(book *parsedEbook) string {
	if book == nil {
		return "low"
	}

	score := 0
	if book.Title != "" {
		score++
	}
	if len(book.Authors) > 0 {
		score++
	}
	if book.Year > 0 {
		score++
	}
	if book.ISBN != "" {
		score++
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
