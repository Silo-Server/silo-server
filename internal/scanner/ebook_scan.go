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

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/idgen"
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
	return s.scanEbookPaths(ctx, folder, folder.Paths)
}

func (s *Scanner) scanEbookPaths(ctx context.Context, folder *models.MediaFolder, roots []string) error {
	if s == nil || folder == nil {
		return fmt.Errorf("scanEbookPaths: nil scanner or folder")
	}
	var candidates []string
	for _, root := range roots {
		if err := ctx.Err(); err != nil {
			return err
		}
		walkErr := filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
			if err := ctx.Err(); err != nil {
				return err
			}
			if walkErr != nil {
				slog.Warn("ebook scan: walk error", "path", path, "error", walkErr)
				return nil
			}
			if d.IsDir() {
				return nil
			}
			if SupportsEbookFile(path) {
				candidates = append(candidates, path)
			}
			return nil
		})
		if walkErr != nil {
			slog.Warn("ebook scan: walk root failed", "root", root, "error", walkErr)
		}
	}

	if len(candidates) == 0 {
		return nil
	}

	workers := ebookScanWorkers()
	slog.Info("ebook scan: starting",
		"folder_id", folder.ID,
		"candidates", len(candidates),
		"workers", workers,
	)
	reportEbookScanProgress(ctx, folder.ID, len(candidates), 0, 0, 0)

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
				if err := s.reconcileEbookFile(ctx, folder, path, &skipped); err != nil {
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

func (s *Scanner) reconcileEbookFile(ctx context.Context, folder *models.MediaFolder, filePath string, skipped *int64) error {
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
		parsed.Title = strings.TrimSuffix(filepath.Base(filePath), filepath.Ext(filePath))
	}

	contentID, err := s.upsertEbookMediaItem(ctx, folder.ID, filePath, &parsed)
	if err != nil {
		return fmt.Errorf("upsert ebook item: %w", err)
	}
	if err := s.upsertEbookMediaFile(ctx, folder, contentID, filePath, size, modifiedAt, &parsed); err != nil {
		return fmt.Errorf("upsert ebook file: %w", err)
	}
	if err := applyEbookEmbeddedCover(ctx, s.itemRepo, s.imageCacher, contentID, &parsed); err != nil {
		slog.Warn("ebook scan: embedded cover upload failed",
			"folder_id", folder.ID,
			"content_id", contentID,
			"path", filePath,
			"error", err,
		)
	}
	if err := s.upsertEbookPeople(ctx, contentID, &parsed); err != nil {
		return fmt.Errorf("upsert ebook people: %w", err)
	}
	if err := s.upsertEbookSeries(ctx, contentID, &parsed); err != nil {
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
	statuses, err := s.itemRepo.GetStatusByIDs(ctx, []string{mf.ContentID})
	if err != nil {
		return false, fmt.Errorf("get item status: %w", err)
	}
	return !strings.EqualFold(strings.TrimSpace(statuses[mf.ContentID]), "unmatched"), nil
}

func (s *Scanner) upsertEbookMediaItem(ctx context.Context, folderID int, filePath string, book *parsedEbook) (string, error) {
	if s.itemRepo == nil {
		return "", fmt.Errorf("itemRepo not configured on Scanner")
	}
	if s.fileRepo == nil {
		return "", fmt.Errorf("fileRepo not configured on Scanner")
	}

	existingID, err := s.fileRepo.FindContentIDByRootPath(ctx, folderID, filePath, "ebook")
	if err != nil {
		return "", fmt.Errorf("find ebook by root path: %w", err)
	}
	if existingID != "" {
		if err := updateExistingEbookMediaItem(ctx, s.itemRepo, s.itemRepo, existingID, book); err != nil {
			return "", err
		}
		return existingID, nil
	}
	if existing := s.findEbookByFilePath(ctx, filePath); existing != nil {
		applyEbookToMediaItem(existing, book)
		if existing.SortTitle == "" {
			existing.SortTitle = titleutil.DeriveDefaultSortTitle(existing.Title)
		}
		if err := s.itemRepo.Upsert(ctx, existing); err != nil {
			return "", err
		}
		return existing.ContentID, nil
	}
	return resolveEbookMediaItem(ctx, s.fileRepo, s.itemRepo, folderID, filePath, book)
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
		cleanTitle = strings.TrimSuffix(filepath.Base(filePath), filepath.Ext(filePath))
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

func updateExistingEbookMediaItem(ctx context.Context, itemReader filesystemMediaItemReader, itemWriter filesystemMediaItemWriter, contentID string, book *parsedEbook) error {
	if itemReader == nil {
		return fmt.Errorf("media item reader not configured")
	}
	if itemWriter == nil {
		return fmt.Errorf("media item writer not configured")
	}
	items, err := itemReader.GetByIDs(ctx, []string{contentID})
	if err != nil {
		return fmt.Errorf("get ebook media item %s: %w", contentID, err)
	}
	if len(items) == 0 || items[0] == nil {
		return fmt.Errorf("ebook media item %s not found", contentID)
	}
	item := items[0]
	applyEbookToMediaItem(item, book)
	if item.SortTitle == "" {
		item.SortTitle = titleutil.DeriveDefaultSortTitle(item.Title)
	}
	return itemWriter.Upsert(ctx, item)
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

func (s *Scanner) upsertEbookMediaFile(ctx context.Context, folder *models.MediaFolder, contentID string, filePath string, size int64, modifiedAt time.Time, book *parsedEbook) error {
	mf := buildEbookMediaFile(folder, contentID, filePath, size, modifiedAt, book)
	if _, err := s.fileRepo.Upsert(ctx, mf); err != nil {
		return fmt.Errorf("upsert media file %s: %w", filePath, err)
	}
	return nil
}

func buildEbookMediaFile(folder *models.MediaFolder, contentID string, filePath string, size int64, modifiedAt time.Time, book *parsedEbook) models.MediaFile {
	return models.MediaFile{
		ContentID:          contentID,
		MediaFolderID:      folder.ID,
		CanonicalRootPath:  filePath,
		ObservedRootPath:   filePath,
		ContentGroupKey:    contentID,
		GroupKeyVersion:    1,
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

type ebookCoverMetadataStore interface {
	GetByIDs(ctx context.Context, ids []string) ([]*models.MediaItem, error)
	UpdateMetadata(ctx context.Context, contentID string, upd *catalog.MetadataUpdate) error
}

func applyEbookEmbeddedCover(ctx context.Context, store ebookCoverMetadataStore, cacher ebookCoverCacher, contentID string, book *parsedEbook) error {
	if store == nil || cacher == nil || contentID == "" || book == nil || book.Cover == nil || len(book.Cover.Bytes) == 0 {
		return nil
	}
	items, err := store.GetByIDs(ctx, []string{contentID})
	if err != nil {
		return fmt.Errorf("get ebook media item for cover: %w", err)
	}
	if len(items) > 0 && items[0] != nil && strings.TrimSpace(items[0].PosterPath) != "" {
		return nil
	}
	basePath, ext, thumbhash, err := cacher.CacheEbookCover(ctx, book.Cover.Bytes, contentID)
	if err != nil {
		return err
	}
	posterPath := strings.TrimRight(basePath, "/") + "/original" + ext
	update := &catalog.MetadataUpdate{PosterPath: &posterPath}
	if thumbhash != "" {
		update.PosterThumbhash = &thumbhash
	}
	return store.UpdateMetadata(ctx, contentID, update)
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

func (s *Scanner) upsertEbookPeople(ctx context.Context, contentID string, book *parsedEbook) error {
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

func (s *Scanner) upsertEbookSeries(ctx context.Context, contentID string, book *parsedEbook) error {
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

	plan, err := planEbookSeriesWrite(book, currentName, currentIdx, err)
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

func planEbookSeriesWrite(book *parsedEbook, currentName *string, currentIdx *float64, queryErr error) (ebookSeriesWritePlan, error) {
	if queryErr != nil {
		if !errors.Is(queryErr, pgx.ErrNoRows) {
			return ebookSeriesWritePlan{}, fmt.Errorf("query ebook_series: %w", queryErr)
		}
		currentName = nil
		currentIdx = nil
	}

	desiredName, desiredIdx := ebookSeriesDesired(book)
	if desiredName == "" {
		if currentName == nil {
			return ebookSeriesWritePlan{Kind: ebookSeriesWriteNone}, nil
		}
		return ebookSeriesWritePlan{Kind: ebookSeriesWriteDelete}, nil
	}

	if currentName != nil && *currentName == desiredName && floatPtrEqual(currentIdx, desiredIdx) {
		return ebookSeriesWritePlan{Kind: ebookSeriesWriteNone}, nil
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
