package scanner

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/idgen"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/titleutil"
)

// ScanEbookFolder walks an ebooks-typed media folder and reconciles each
// supported ebook file.
func (s *Scanner) ScanEbookFolder(ctx context.Context, folder *models.MediaFolder) error {
	if s == nil || folder == nil {
		return fmt.Errorf("ScanEbookFolder: nil scanner or folder")
	}

	var existingFiles []*models.MediaFile
	if s.fileRepo != nil {
		files, err := s.fileRepo.ListActiveByFolderAndType(ctx, folder.ID, "ebook")
		if err != nil {
			slog.Warn("ebook scan: existing-file listing failed; missing cleanup skipped",
				"folder_id", folder.ID,
				"error", err,
			)
		} else {
			existingFiles = files
		}
	}

	candidates, hadWalkErrors, walkErr := collectLogicalFilePathsWithWalkStatus(ctx, folder.Paths, "ebook")
	if walkErr != nil {
		return fmt.Errorf("walking ebook roots: %w", walkErr)
	}
	seenPaths := make(map[string]bool, len(candidates))
	for _, path := range candidates {
		seenPaths[path] = true
	}

	var attempted int
	var succeeded int
	var failures []error
	for _, path := range candidates {
		if ctx != nil {
			if err := ctx.Err(); err != nil {
				return err
			}
		}

		attempted++
		if err := s.reconcileEbookFile(ctx, folder, path); err != nil {
			failures = append(failures, fmt.Errorf("%s: %w", path, err))
			slog.Warn("ebook scan: file failed",
				"folder_id", folder.ID,
				"path", path,
				"error", err,
			)
			continue
		}
		succeeded++
	}

	if hadWalkErrors {
		slog.Warn("ebook scan: missing-file cleanup skipped because walk had errors",
			"folder_id", folder.ID,
		)
	} else if len(existingFiles) > 0 {
		now := time.Now().UTC()
		for _, existing := range existingFiles {
			if existing == nil || seenPaths[existing.FilePath] {
				continue
			}
			if err := s.fileRepo.MarkMissing(ctx, existing.ID, now); err != nil {
				slog.Warn("ebook scan: failed to mark file missing",
					"folder_id", folder.ID,
					"path", existing.FilePath,
					"error", err,
				)
			}
		}
	}
	if attempted > 0 && succeeded == 0 && len(failures) > 0 {
		return fmt.Errorf("ebook scan failed for every attempted folder_id=%d candidates=%d: %w", folder.ID, len(candidates), errors.Join(failures...))
	}
	return nil
}

func (s *Scanner) reconcileEbookFile(ctx context.Context, folder *models.MediaFolder, path string) error {
	if s == nil || folder == nil {
		return fmt.Errorf("reconcileEbookFile: nil scanner or folder")
	}
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return err
		}
	}

	info, err := os.Stat(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("stat ebook file: %w", err)
	}
	if !info.Mode().IsRegular() {
		return nil
	}

	if s.fileRepo != nil {
		existing, err := s.fileRepo.FindActiveByPath(ctx, folder.ID, path)
		if err != nil {
			return fmt.Errorf("find active ebook file: %w", err)
		}
		if shouldSkip, err := s.ebookFileShouldSkip(ctx, existing, info.Size(), info.ModTime()); err != nil {
			return err
		} else if shouldSkip {
			slog.Debug("ebook scan: unchanged file skipped",
				"folder_id", folder.ID,
				"content_id", existing.ContentID,
				"file", path,
			)
			return nil
		}
	}

	book, err := parseEbookFile(path)
	if err != nil {
		return fmt.Errorf("parse ebook file: %w", err)
	}

	contentID, err := s.upsertEbookMediaItem(ctx, folder.ID, path, &book)
	if err != nil {
		return fmt.Errorf("upsert ebook item: %w", err)
	}
	if err := s.upsertEbookMediaFile(ctx, folder, contentID, path, info, &book); err != nil {
		return fmt.Errorf("upsert ebook file: %w", err)
	}
	if err := s.upsertEbookDetails(ctx, contentID, &book); err != nil {
		return fmt.Errorf("upsert ebook details: %w", err)
	}
	if err := s.upsertEbookPeople(ctx, contentID, &book); err != nil {
		return fmt.Errorf("upsert ebook people: %w", err)
	}
	if err := s.upsertEbookISBN(ctx, contentID, &book); err != nil {
		return err
	}
	if s.libraryRepo == nil {
		return fmt.Errorf("libraryRepo not configured on Scanner")
	}
	if err := s.libraryRepo.Upsert(ctx, contentID, folder.ID, time.Now()); err != nil {
		return fmt.Errorf("upsert ebook library membership: %w", err)
	}
	s.cacheSelectedEbookCover(ctx, contentID, &book, filepath.Dir(path))

	slog.Info("ebook scan: indexed",
		"folder_id", folder.ID,
		"content_id", contentID,
		"title", book.Title,
		"file", path,
	)
	return nil
}

func (s *Scanner) ebookFileShouldSkip(ctx context.Context, existing *models.MediaFile, size int64, modTime time.Time) (bool, error) {
	if existing == nil || existing.ContentID == "" || !ebookFileUnchanged(existing, size, modTime) {
		return false, nil
	}
	if s == nil || s.ebookDetailsRepo == nil {
		return false, nil
	}
	hasDetails, err := s.ebookDetailsRepo.Exists(ctx, existing.ContentID)
	if err != nil {
		return false, fmt.Errorf("check ebook details before unchanged skip: %w", err)
	}
	return hasDetails, nil
}

func ebookFileUnchanged(existing *models.MediaFile, size int64, modTime time.Time) bool {
	if existing == nil || existing.FileModifiedAt == nil {
		return false
	}
	if existing.FileSize != size {
		return false
	}
	return sameFileModifiedAt(existing.FileModifiedAt, normalizeFileModifiedAt(modTime))
}

func selectEbookCover(book *parsedEbook, dir string) ([]byte, string, string) {
	if book != nil && book.Cover != nil && len(book.Cover.Bytes) > 0 {
		return book.Cover.Bytes, book.Cover.ContentType, "embedded"
	}
	for _, name := range []string{"cover.jpg", "cover.jpeg", "cover.png", "folder.jpg", "folder.png"} {
		path := findEbookSidecarCover(dir, name)
		if path == "" {
			continue
		}
		data, err := os.ReadFile(path)
		if err != nil || len(data) == 0 {
			continue
		}
		return data, ebookCoverContentType(path), "sidecar"
	}
	return nil, "", ""
}

func findEbookSidecarCover(dir, name string) string {
	if dir == "" || name == "" {
		return ""
	}
	path := filepath.Join(dir, name)
	if info, err := os.Stat(path); err == nil && info.Mode().IsRegular() {
		return path
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return ""
	}
	want := strings.ToLower(name)
	for _, entry := range entries {
		if entry.IsDir() || strings.ToLower(entry.Name()) != want {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		if info, err := entry.Info(); err == nil && info.Mode().IsRegular() {
			return path
		}
	}
	return ""
}

func ebookCoverContentType(path string) string {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".png":
		return "image/png"
	default:
		return ""
	}
}

func (s *Scanner) cacheSelectedEbookCover(ctx context.Context, contentID string, book *parsedEbook, dir string) {
	if s == nil || s.imageCacher == nil || s.itemRepo == nil || contentID == "" {
		return
	}
	data, contentType, source := selectEbookCover(book, dir)
	if len(data) == 0 {
		return
	}
	basePath, ext, thumbhash, err := s.imageCacher.CacheAudiobookCover(ctx, data, contentID)
	if err != nil {
		slog.Warn("ebook cover: imagecache upload failed",
			"content_id", contentID,
			"source", source,
			"content_type", contentType,
			"error", err,
		)
		return
	}
	posterPath := fmt.Sprintf("%s/original%s", basePath, ext)
	if err := s.itemRepo.UpdateMetadata(ctx, contentID, &catalog.MetadataUpdate{
		PosterPath:      &posterPath,
		PosterThumbhash: &thumbhash,
	}); err != nil {
		slog.Warn("ebook cover: media item poster update failed",
			"content_id", contentID,
			"source", source,
			"poster_path", posterPath,
			"error", err,
		)
	}
}

func (s *Scanner) upsertEbookMediaItem(ctx context.Context, folderID int, filePath string, book *parsedEbook) (string, error) {
	if s.fileRepo == nil {
		return "", fmt.Errorf("fileRepo not configured on Scanner")
	}
	if s.itemRepo == nil {
		return "", fmt.Errorf("itemRepo not configured on Scanner")
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
		if err := upsertEbookMediaItemWithID(ctx, itemWriter, existingID, book); err != nil {
			return "", err
		}
		return existingID, nil
	}
	return createEbookMediaItem(ctx, itemWriter, book)
}

func createEbookMediaItem(ctx context.Context, itemWriter filesystemMediaItemWriter, book *parsedEbook) (string, error) {
	if itemWriter == nil {
		return "", fmt.Errorf("media item writer not configured")
	}
	id, err := idgen.NextID()
	if err != nil {
		return "", fmt.Errorf("generate content_id: %w", err)
	}
	if err := upsertEbookMediaItemWithID(ctx, itemWriter, id, book); err != nil {
		return "", err
	}
	return id, nil
}

func upsertEbookMediaItemWithID(ctx context.Context, itemWriter filesystemMediaItemWriter, contentID string, book *parsedEbook) error {
	if itemWriter == nil {
		return fmt.Errorf("media item writer not configured")
	}
	title := ebookTitleOrDefault(book)
	item := &models.MediaItem{
		ContentID: contentID,
		Type:      "ebook",
		Title:     title,
		SortTitle: titleutil.DeriveDefaultSortTitle(title),
	}
	applyEbookToMediaItem(item, book)
	if item.SortTitle == "" {
		item.SortTitle = titleutil.DeriveDefaultSortTitle(item.Title)
	}
	if err := itemWriter.Upsert(ctx, item); err != nil {
		return err
	}
	return nil
}

func applyEbookToMediaItem(item *models.MediaItem, book *parsedEbook) {
	if item == nil {
		return
	}
	item.Type = "ebook"
	title := ebookTitleOrDefault(book)
	item.Title = title
	item.SortTitle = titleutil.DeriveDefaultSortTitle(title)
	if book == nil {
		return
	}
	item.Year = book.Year
	item.Overview = book.Description
	item.Genres = book.Genres
	if book.Publisher != "" {
		item.Studios = []string{book.Publisher}
	} else {
		item.Studios = nil
	}
	item.OriginalLanguage = book.Language
	if !book.PublishedAt.IsZero() {
		rd := book.PublishedAt.UTC().Format("2006-01-02")
		item.ReleaseDate = &rd
	}
}

func ebookTitleOrDefault(book *parsedEbook) string {
	if book != nil && book.Title != "" {
		return book.Title
	}
	return "Untitled Ebook"
}

func (s *Scanner) upsertEbookMediaFile(
	ctx context.Context,
	folder *models.MediaFolder,
	contentID string,
	filePath string,
	info os.FileInfo,
	book *parsedEbook,
) error {
	if s.fileRepo == nil {
		return fmt.Errorf("fileRepo not configured on Scanner")
	}
	modifiedAt := normalizeFileModifiedAt(info.ModTime())
	mf := models.MediaFile{
		ContentID:          contentID,
		MediaFolderID:      folder.ID,
		CanonicalRootPath:  filePath,
		ObservedRootPath:   filePath,
		ContentGroupKey:    contentID,
		GroupKeyVersion:    1,
		BaseTitle:          ebookTitleOrDefault(book),
		BaseYear:           ebookYear(book),
		BaseType:           "ebook",
		IdentityConfidence: ebookIdentityConfidence(book),
		FilePath:           filePath,
		FileSize:           info.Size(),
		FileModifiedAt:     &modifiedAt,
		Container:          ebookFormat(book),
		ProbeSource:        "local",
	}
	if _, err := s.fileRepo.Upsert(ctx, mf); err != nil {
		return fmt.Errorf("upsert media file %s: %w", filePath, err)
	}
	return nil
}

func (s *Scanner) upsertEbookDetails(ctx context.Context, contentID string, book *parsedEbook) error {
	if s.ebookDetailsRepo == nil {
		return fmt.Errorf("ebookDetailsRepo not configured on Scanner")
	}
	metadata, err := json.Marshal(ebookMetadataSnapshot(book))
	if err != nil {
		return fmt.Errorf("marshal ebook metadata: %w", err)
	}
	return s.ebookDetailsRepo.Upsert(ctx, models.EbookDetails{
		ContentID:    contentID,
		Format:       ebookFormat(book),
		ISBN:         ebookISBN(book),
		Publisher:    ebookPublisher(book),
		PageCount:    ebookPageCount(book),
		SeriesName:   ebookSeries(book),
		SeriesIndex:  ebookSeriesIndex(book),
		MetadataJSON: metadata,
	})
}

func ebookMetadataSnapshot(book *parsedEbook) map[string]any {
	if book == nil {
		return map[string]any{}
	}
	return map[string]any{
		"title":        book.Title,
		"authors":      book.Authors,
		"description":  book.Description,
		"publisher":    book.Publisher,
		"published_at": book.PublishedAt,
		"year":         book.Year,
		"language":     book.Language,
		"isbn":         book.ISBN,
		"series":       book.Series,
		"series_index": book.SeriesIndex,
		"genres":       book.Genres,
		"page_count":   book.PageCount,
	}
}

func (s *Scanner) upsertEbookPeople(ctx context.Context, contentID string, book *parsedEbook) error {
	if s.itemRepo == nil {
		return fmt.Errorf("itemRepo not configured on Scanner")
	}
	if s.personRepo == nil {
		return fmt.Errorf("personRepo not configured on Scanner")
	}
	// Ebook scanner credits are currently author-only. ReplacePeople clears
	// scanner-owned stale authors when a file is retagged or loses authors.
	if book == nil || len(book.Authors) == 0 {
		return s.itemRepo.ReplacePeople(ctx, contentID, nil)
	}
	people := make([]models.ItemPerson, 0, len(book.Authors))
	for i, name := range book.Authors {
		personID, err := s.personRepo.FindOrCreate(ctx, models.Person{Name: name})
		if err != nil {
			return fmt.Errorf("find-or-create person %q: %w", name, err)
		}
		people = append(people, models.ItemPerson{
			Person:    models.Person{ID: personID},
			Kind:      models.PersonKindAuthor,
			SortOrder: i,
		})
	}
	return s.itemRepo.ReplacePeople(ctx, contentID, people)
}

func (s *Scanner) upsertEbookISBN(ctx context.Context, contentID string, book *parsedEbook) error {
	if book == nil || book.ISBN == "" {
		return nil
	}
	if s.fileRepo == nil {
		return fmt.Errorf("fileRepo not configured on Scanner")
	}
	if _, err := s.fileRepo.Pool().Exec(ctx, `
		INSERT INTO media_item_provider_ids (content_id, provider, provider_id, item_type)
		VALUES ($1, 'isbn', $2, 'ebook')
		ON CONFLICT (content_id, provider) DO UPDATE SET
		    provider_id = EXCLUDED.provider_id,
		    item_type = EXCLUDED.item_type,
		    updated_at = NOW()
	`, contentID, book.ISBN); err != nil {
		return fmt.Errorf("upsert ebook ISBN provider id: %w", err)
	}
	return nil
}

func ebookIdentityConfidence(book *parsedEbook) string {
	if book == nil {
		return "low"
	}
	if book.ISBN != "" {
		return "high"
	}
	if book.Title != "" && len(book.Authors) > 0 && book.Year > 0 {
		return "medium"
	}
	return "low"
}

func ebookYear(book *parsedEbook) int {
	if book == nil {
		return 0
	}
	return book.Year
}

func ebookFormat(book *parsedEbook) string {
	if book == nil {
		return ""
	}
	return book.Format
}

func ebookISBN(book *parsedEbook) string {
	if book == nil {
		return ""
	}
	return book.ISBN
}

func ebookPublisher(book *parsedEbook) string {
	if book == nil {
		return ""
	}
	return book.Publisher
}

func ebookPageCount(book *parsedEbook) int {
	if book == nil {
		return 0
	}
	return book.PageCount
}

func ebookSeries(book *parsedEbook) string {
	if book == nil {
		return ""
	}
	return book.Series
}

func ebookSeriesIndex(book *parsedEbook) string {
	if book == nil {
		return ""
	}
	return book.SeriesIndex
}
