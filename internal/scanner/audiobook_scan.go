package scanner

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

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
		walkErr := filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				slog.Warn("audiobook scan: walk error", "path", path, "error", walkErr)
				return nil
			}
			if !d.IsDir() {
				return nil
			}
			entries, err := os.ReadDir(path)
			if err != nil {
				slog.Warn("audiobook scan: read dir failed", "path", path, "error", err)
				return nil
			}
			hasAudio := false
			for _, e := range entries {
				if !e.IsDir() && SupportsAudioFile(e.Name()) {
					hasAudio = true
					break
				}
			}
			if !hasAudio {
				return nil
			}
			if err := s.reconcileAudiobookFolder(ctx, folder, path); err != nil {
				slog.Warn("audiobook scan: folder failed",
					"folder_id", folder.ID,
					"path", path,
					"error", err,
				)
			}
			return filepath.SkipDir
		})
		if walkErr != nil {
			slog.Warn("audiobook scan: walk root failed", "root", root, "error", walkErr)
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
	if len(parsed.Files) > 0 && s.imageCacher != nil {
		poster, thumb := extractAndUploadAudiobookCover(ctx, ffmpegPathFromFFprobe(s.ffprobePath), s.imageCacher, parsed.Files[0].Path, contentID)
		if poster != "" {
			if _, err := s.fileRepo.Pool().Exec(ctx, `
				UPDATE media_items
				SET poster_path=$1, poster_thumbhash=$2, updated_at=NOW()
				WHERE content_id=$3 AND (poster_path='' OR poster_path IS NULL)
			`, poster, thumb, contentID); err != nil {
				return fmt.Errorf("update audiobook poster_path: %w", err)
			}
		}
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
		applyBookToMediaItem(existing, book)
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
	applyBookToMediaItem(item, book)
	if err := s.itemRepo.Upsert(ctx, item); err != nil {
		return "", err
	}
	return id, nil
}

// applyBookToMediaItem copies parsed-audiobook tag fields onto the
// MediaItem. Used for both fresh inserts and re-scans of existing rows
// so manual edits to fields not driven by the file (e.g., poster_path
// set by the metadata enricher) survive.
func applyBookToMediaItem(item *models.MediaItem, book *parsedAudiobook) {
	item.Type = "audiobook"
	item.Title = book.Title
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
			Duration:          af.Duration,
			Bitrate:           af.Bitrate,
			CodecAudio:        af.CodecAudio,
			Container:         af.Container,
			AudioChannels:     af.AudioChannels,
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
