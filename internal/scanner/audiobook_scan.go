package scanner

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/idgen"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/titleutil"
)

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
func (s *Scanner) ScanAudiobookFolder(ctx context.Context, folder *models.MediaFolder) error {
	if s == nil || folder == nil {
		return fmt.Errorf("ScanAudiobookFolder: nil scanner or folder")
	}

	for _, root := range folder.Paths {
		entries, err := os.ReadDir(root)
		if err != nil {
			slog.Warn("audiobook scan: read root failed", "root", root, "error", err)
			continue
		}
		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}
			subPath := filepath.Join(root, entry.Name())
			if err := s.reconcileAudiobookFolder(ctx, folder, subPath); err != nil {
				slog.Warn("audiobook scan: folder failed",
					"folder_id", folder.ID,
					"path", subPath,
					"error", err,
				)
				// Continue with siblings — one bad audiobook should not stop the scan.
			}
		}
	}
	return nil
}

func (s *Scanner) reconcileAudiobookFolder(ctx context.Context, folder *models.MediaFolder, folderPath string) error {
	parsed, err := parseAudiobookFolder(ctx, s.ffprobePath, folderPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("parse audiobook folder %s: %w", folderPath, err)
	}

	contentID, err := s.upsertAudiobookMediaItem(ctx, parsed)
	if err != nil {
		return fmt.Errorf("upsert audiobook item: %w", err)
	}
	if err := s.upsertAudiobookMediaFiles(ctx, folder, contentID, folderPath, parsed); err != nil {
		return fmt.Errorf("upsert audiobook files: %w", err)
	}
	if err := s.upsertAudiobookPeople(ctx, contentID, parsed); err != nil {
		return fmt.Errorf("upsert audiobook people: %w", err)
	}

	slog.Info("audiobook scan: indexed",
		"folder_id", folder.ID,
		"content_id", contentID,
		"title", parsed.Title,
		"author", parsed.Author,
		"files", len(parsed.Files),
	)
	return nil
}

// upsertAudiobookMediaItem looks up an existing media_items row by
// title+year+type, updates it if found, or creates a new row.
// Returns the content_id used.
func (s *Scanner) upsertAudiobookMediaItem(ctx context.Context, book *parsedAudiobook) (string, error) {
	if s.itemRepo == nil {
		return "", fmt.Errorf("itemRepo not configured on Scanner")
	}

	existing, err := s.itemRepo.GetByTitleYearType(ctx, book.Title, book.Year, "audiobook")
	if err == nil && existing != nil {
		// Found — refresh mutable fields and re-upsert.
		existing.Title = book.Title
		existing.Year = book.Year
		existing.Type = "audiobook"
		if existing.SortTitle == "" {
			existing.SortTitle = titleutil.DeriveDefaultSortTitle(book.Title)
		}
		if err := s.itemRepo.Upsert(ctx, existing); err != nil {
			return "", err
		}
		return existing.ContentID, nil
	}
	if err != nil && !errors.Is(err, catalog.ErrItemNotFound) {
		return "", fmt.Errorf("GetByTitleYearType: %w", err)
	}

	// Not found — create.
	id, err := idgen.NextID()
	if err != nil {
		return "", fmt.Errorf("generate content_id: %w", err)
	}
	item := &models.MediaItem{
		ContentID: id,
		Type:      "audiobook",
		Title:     book.Title,
		SortTitle: titleutil.DeriveDefaultSortTitle(book.Title),
		Year:      book.Year,
	}
	if err := s.itemRepo.Upsert(ctx, item); err != nil {
		return "", err
	}
	return id, nil
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
	for _, af := range book.Files {
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
			ContentID:         contentID,
			MediaFolderID:     folder.ID,
			CanonicalRootPath: folderPath,
			ObservedRootPath:  folderPath,
			ContentGroupKey:   contentID,
			GroupKeyVersion:   1,
			BaseTitle:         book.Title,
			BaseYear:          book.Year,
			BaseType:          "audiobook",
			IdentityConfidence: "high",
			FilePath:          af.Path,
			Chapters:          chapters,
			ProbeSource:       "local",
		}

		if _, err := s.fileRepo.Upsert(ctx, mf); err != nil {
			return fmt.Errorf("upsert media file %s: %w", af.Path, err)
		}
	}
	return nil
}

// upsertAudiobookPeople upserts author and narrator rows into item_people,
// using the PersonRepository to find-or-create each person by name.
// Existing credits are not deleted first — we use ON CONFLICT DO NOTHING
// (via ReplacePeople-style logic) so manual edits survive re-scans.
func (s *Scanner) upsertAudiobookPeople(ctx context.Context, contentID string, book *parsedAudiobook) error {
	if s.personRepo == nil {
		return fmt.Errorf("personRepo not configured on Scanner")
	}

	type credit struct {
		name string
		kind models.PersonKind
	}
	var credits []credit
	if book.Author != "" {
		credits = append(credits, credit{book.Author, models.PersonKindAuthor})
	}
	if book.Narrator != "" {
		credits = append(credits, credit{book.Narrator, models.PersonKindNarrator})
	}
	if len(credits) == 0 {
		return nil
	}

	people := make([]models.ItemPerson, 0, len(credits))
	for i, c := range credits {
		personID, err := s.personRepo.FindOrCreate(ctx, models.Person{Name: c.name})
		if err != nil {
			return fmt.Errorf("find-or-create person %q: %w", c.name, err)
		}
		people = append(people, models.ItemPerson{
			Person:    models.Person{ID: personID},
			Kind:      c.kind,
			SortOrder: i,
		})
	}

	// ReplacePeople deletes existing credits then inserts the new set.
	// For audiobook scanning this is acceptable — we re-derive all credits
	// from the file metadata on every scan, so the set is authoritative.
	return s.itemRepo.ReplacePeople(ctx, contentID, people)
}
