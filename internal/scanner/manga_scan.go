package scanner

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/idgen"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/titleutil"
	"github.com/jackc/pgx/v5"
)

// ScanMangaFolder scans a manga library. It is a fork of ScanEbookFolder: the
// chapter files are kept as readable type='ebook' items exactly as the ebook
// pipeline does, while each file additionally find-or-creates a single
// type='manga' series item per series folder and links the chapter to it.
func (s *Scanner) ScanMangaFolder(ctx context.Context, folder *models.MediaFolder) error {
	if s == nil || folder == nil {
		return fmt.Errorf("ScanMangaFolder: nil scanner or folder")
	}
	return s.scanMangaPaths(ctx, folder, folder.Paths, true)
}

// scanMangaPaths mirrors scanEbookPaths exactly (root collection, worker pool,
// missing-file reconciliation) but dispatches each file to reconcileMangaFile.
func (s *Scanner) scanMangaPaths(ctx context.Context, folder *models.MediaFolder, roots []string, fullScan bool) error {
	if s == nil || folder == nil {
		return fmt.Errorf("scanMangaPaths: nil scanner or folder")
	}
	scans, err := collectEbookRootScans(ctx, folder.ID, roots)
	if err != nil {
		return err
	}
	// Every discovered file is indexed, including files under roots whose walk
	// partially failed: indexing is additive and safe, only the destructive
	// reconciliation below is restricted to cleanly walked roots.
	var candidates []string
	for i := range scans {
		candidates = append(candidates, scans[i].files...)
	}

	if len(candidates) == 0 {
		return s.reconcileMangaScan(ctx, folder, scans, nil, fullScan)
	}

	workers := ebookScanWorkers()
	slog.Info("manga scan: starting",
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
				if err := s.reconcileMangaFile(ctx, folder, path, &skipped, groupLocks); err != nil {
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
					slog.Warn("manga scan: file failed",
						"folder_id", folder.ID,
						"path", path,
						"error", err,
					)
				}
				n := atomic.AddInt64(&processed, 1)
				if n%500 == 0 || n == int64(len(candidates)) {
					failedCount := atomic.LoadInt64(&failed)
					skippedCount := atomic.LoadInt64(&skipped)
					slog.Info("manga scan: progress",
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

	slog.Info("manga scan: completed",
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
			return fmt.Errorf("manga scan failed for every attempted folder_id=%d: %w", folder.ID, errors.Join(failures...))
		}
	}

	seenPaths := make(map[string]bool, len(candidates))
	for _, p := range candidates {
		seenPaths[p] = true
	}
	return s.reconcileMangaScan(ctx, folder, scans, seenPaths, fullScan)
}

// reconcileMangaScan runs the shared ebook missing-file reconciliation (which
// removes vanished chapters) and then deletes any type='manga' series left with
// no chapters. Series items are file-less parents reconciled by chapter count,
// not file presence — catalog.ReconcileFolderMembership deliberately skips them.
func (s *Scanner) reconcileMangaScan(ctx context.Context, folder *models.MediaFolder, scans []ebookRootScan, seenPaths map[string]bool, fullScan bool) error {
	if err := s.reconcileEbookScan(ctx, folder, scans, seenPaths, fullScan); err != nil {
		return err
	}
	return s.deleteOrphanedMangaSeries(ctx, folder.ID)
}

// deleteOrphanedMangaSeries removes type='manga' series items in the folder that
// have no remaining linked chapters (e.g. once every chapter was deleted as
// missing). The cascade clears the now-empty library membership.
func (s *Scanner) deleteOrphanedMangaSeries(ctx context.Context, folderID int) error {
	if s == nil || s.fileRepo == nil {
		return nil
	}
	tx, err := s.fileRepo.Pool().Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin orphaned manga series delete tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	rows, err := tx.Query(ctx, `
		DELETE FROM media_items mi
		WHERE mi.type = 'manga'
		  AND EXISTS (
			SELECT 1 FROM media_item_libraries mil
			WHERE mil.content_id = mi.content_id AND mil.media_folder_id = $1
		  )
		  AND NOT EXISTS (
			SELECT 1 FROM manga_chapters mc WHERE mc.series_content_id = mi.content_id
		  )
		RETURNING mi.content_id
	`, folderID)
	if err != nil {
		return fmt.Errorf("deleting orphaned manga series for folder %d: %w", folderID, err)
	}
	defer rows.Close()
	var deletedIDs []string
	for rows.Next() {
		var contentID string
		if err := rows.Scan(&contentID); err != nil {
			return fmt.Errorf("scanning deleted manga series id: %w", err)
		}
		deletedIDs = append(deletedIDs, contentID)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterating deleted manga series ids: %w", err)
	}
	if err := catalog.EnqueueSearchIndexDeletes(ctx, tx, deletedIDs); err != nil {
		return fmt.Errorf("enqueueing catalog search manga series delete: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit orphaned manga series delete tx: %w", err)
	}
	if len(deletedIDs) > 0 {
		slog.Info("manga scan: removed orphaned series", "folder_id", folderID, "deleted", len(deletedIDs))
	}
	return nil
}

// reconcileMangaFile indexes one .cbz/.cbr chapter file: it keeps the file as a
// readable type='ebook' chapter item (exactly as reconcileEbookFile does), then
// find-or-creates the single type='manga' series item for the chapter's series
// folder and links the chapter to it with its parsed index/volume.
func (s *Scanner) reconcileMangaFile(ctx context.Context, folder *models.MediaFolder, filePath string, skipped *int64, groupLocks *ebookGroupLocks) error {
	info, err := os.Stat(filePath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("stat manga file %s: %w", filePath, err)
	}
	size := info.Size()
	modifiedAt := normalizeFileModifiedAt(info.ModTime())

	_, isUnchanged, skipErr := s.ebookFileShouldSkip(ctx, folder, filePath, size, modifiedAt)
	if skipErr != nil {
		slog.Warn("manga scan: skip-check failed, falling through",
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
		return fmt.Errorf("parse manga file %s: %w", filePath, err)
	}
	if parsed.Title == "" {
		parsed.Title = ebookTitleFromPath(filePath)
	}

	seriesName := mangaSeriesFromPath(filePath)
	if seriesName == "" {
		seriesName = ebookTitleFromPath(filePath)
	}
	stem := strings.TrimSuffix(filepath.Base(filePath), filepath.Ext(filePath))
	vol, idx, has := mangaIndexForFile(stem, seriesName)

	// 1. Keep the file as a readable type='ebook' chapter item, exactly as the
	//    ebook pipeline does (cover + page count + media file + membership).
	chapterGroupKey := ebookContentGroupKey(&parsed, filePath)
	chapterID, err := func() (string, error) {
		unlock := groupLocks.lock(chapterGroupKey)
		defer unlock()

		contentID, curated, err := s.upsertEbookMediaItem(ctx, folder.ID, filePath, &parsed, chapterGroupKey)
		if err != nil {
			return "", fmt.Errorf("upsert manga chapter item: %w", err)
		}
		if err := s.upsertEbookMediaFile(ctx, folder, contentID, filePath, size, modifiedAt, &parsed, chapterGroupKey); err != nil {
			return "", fmt.Errorf("upsert manga chapter file: %w", err)
		}
		if err := applyEbookLocalCover(ctx, s.itemRepo, s.imageCacher, contentID, filePath, &parsed); err != nil {
			slog.Warn("manga scan: local cover upload failed",
				"folder_id", folder.ID,
				"content_id", contentID,
				"path", filePath,
				"error", err,
			)
		}
		if err := s.upsertEbookPeople(ctx, contentID, &parsed, curated); err != nil {
			return "", fmt.Errorf("upsert manga chapter people: %w", err)
		}
		if err := insertEbookLibraryMembership(ctx, s.fileRepo.Pool(), contentID, folder.ID); err != nil {
			return "", fmt.Errorf("upsert manga chapter library membership: %w", err)
		}
		return contentID, nil
	}()
	if err != nil {
		return err
	}

	// 2. Find-or-create the single type='manga' series item for this folder and
	//    link the chapter to it.
	seriesID, err := s.findOrCreateMangaSeries(ctx, folder.ID, seriesName, groupLocks)
	if err != nil {
		return fmt.Errorf("find-or-create manga series: %w", err)
	}
	if seriesID != "" {
		// The series item carries no media file of its own, so it must be given a
		// library membership explicitly (the chapter path gets this via its file
		// reconcile). Without it the library-scoped catalog browse, which joins
		// media_item_libraries, would never surface the series card. The insert is
		// ON CONFLICT DO NOTHING, so re-scans never duplicate the membership.
		if err := insertEbookLibraryMembership(ctx, s.fileRepo.Pool(), seriesID, folder.ID); err != nil {
			return fmt.Errorf("upsert manga series library membership: %w", err)
		}
		idxPtr, volOut := mangaChapterWrite(vol, idx, has)
		if err := upsertMangaChapter(ctx, s.fileRepo.Pool(), chapterID, seriesID, idxPtr, volOut); err != nil {
			return fmt.Errorf("link manga chapter to series: %w", err)
		}
	}

	slog.Debug("manga scan: indexed",
		"folder_id", folder.ID,
		"chapter_id", chapterID,
		"series_id", seriesID,
		"series", seriesName,
		"path", filePath,
	)
	return nil
}

// mangaSeriesProvider is the provider namespace under which a manga series
// item's content-group key is recorded in media_item_provider_ids. The table's
// UNIQUE (provider, provider_id, item_type) constraint guarantees exactly one
// type='manga' series item per group key, which is what makes re-scans
// idempotent across processes.
const mangaSeriesProvider = "manga_series"

// findOrCreateMangaSeries resolves the single type='manga' series item for the
// given series name in the folder, creating it on first sight. It is idempotent:
// re-scanning any chapter of the same series resolves to the same series
// content_id. The group-key lock serializes creation across this process's
// worker goroutines; the SELECT-after-conflicting-INSERT recovers the winner's
// content_id if another process raced us.
func (s *Scanner) findOrCreateMangaSeries(ctx context.Context, folderID int, seriesName string, groupLocks *ebookGroupLocks) (string, error) {
	if s.itemRepo == nil {
		return "", fmt.Errorf("itemRepo not configured on Scanner")
	}
	if s.fileRepo == nil {
		return "", fmt.Errorf("fileRepo not configured on Scanner")
	}
	groupKey := mangaSeriesGroupKey(folderID, seriesName)
	if groupKey == "" {
		return "", nil
	}

	unlock := groupLocks.lock(groupKey)
	defer unlock()

	if existing, err := s.lookupMangaSeries(ctx, groupKey); err != nil {
		return "", err
	} else if existing != "" {
		return existing, nil
	}

	id, err := idgen.NextID()
	if err != nil {
		return "", fmt.Errorf("generate manga series content_id: %w", err)
	}
	title := strings.TrimSpace(seriesName)
	item := &models.MediaItem{
		ContentID: id,
		Type:      "manga",
		// Explicit "pending" mirrors the ebook chapter path: enrichment promotes
		// it to "matched". This is the "needs metadata" status.
		Status:    "pending",
		Title:     title,
		SortTitle: titleutil.DeriveDefaultSortTitle(title),
	}
	if err := s.itemRepo.Upsert(ctx, item); err != nil {
		return "", fmt.Errorf("create manga series item: %w", err)
	}

	tag, err := s.fileRepo.Pool().Exec(ctx, `
		INSERT INTO media_item_provider_ids (content_id, provider, provider_id, item_type)
		VALUES ($1, $2, $3, 'manga')
		ON CONFLICT (provider, provider_id, item_type) DO NOTHING
	`, id, mangaSeriesProvider, groupKey)
	if err != nil {
		return "", fmt.Errorf("record manga series key: %w", err)
	}
	if tag.RowsAffected() == 0 {
		// Another process created the series first; our freshly minted item is a
		// dangling orphan. Delete it and adopt the winner so no duplicate series
		// survives.
		winner, lookupErr := s.lookupMangaSeries(ctx, groupKey)
		if lookupErr != nil {
			return "", lookupErr
		}
		if winner != "" && winner != id {
			tx, txErr := s.fileRepo.Pool().Begin(ctx)
			if txErr != nil {
				return "", fmt.Errorf("begin duplicate manga series delete tx: %w", txErr)
			}
			_, delErr := tx.Exec(ctx, `DELETE FROM media_items WHERE content_id = $1`, id)
			if delErr != nil {
				_ = tx.Rollback(ctx)
				slog.Warn("manga scan: failed to delete duplicate series item",
					"folder_id", folderID,
					"content_id", id,
					"error", delErr,
				)
			} else if eventErr := catalog.EnqueueSearchIndexDelete(ctx, tx, id); eventErr != nil {
				_ = tx.Rollback(ctx)
				return "", fmt.Errorf("enqueue catalog search duplicate manga series delete: %w", eventErr)
			} else if commitErr := tx.Commit(ctx); commitErr != nil {
				return "", fmt.Errorf("commit duplicate manga series delete tx: %w", commitErr)
			}
			return winner, nil
		}
	}
	return id, nil
}

// lookupMangaSeries returns the content_id of the type='manga' series item
// already recorded for the group key, or "" if none exists.
func (s *Scanner) lookupMangaSeries(ctx context.Context, groupKey string) (string, error) {
	var id string
	err := s.fileRepo.Pool().QueryRow(ctx, `
		SELECT content_id
		FROM media_item_provider_ids
		WHERE provider = $1 AND provider_id = $2 AND item_type = 'manga'
		LIMIT 1
	`, mangaSeriesProvider, groupKey).Scan(&id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", nil
		}
		return "", fmt.Errorf("lookup manga series by key: %w", err)
	}
	return id, nil
}
