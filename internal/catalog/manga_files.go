package catalog

// Manga chapter ↔ series linkage helpers for surfaces beyond the series
// detail page: the chapter detail payload (reader back/next navigation),
// continue-reading cards (series heading), and the series file-details
// dialog.

import (
	"context"
	"path/filepath"
	"strings"
)

// mangaSeriesForChapterQuery resolves the owning manga series for a chapter
// (a type='ebook' item linked via manga_chapters).
const mangaSeriesForChapterQuery = `
	SELECT mc.series_content_id, si.title
	FROM manga_chapters mc
	JOIN media_items si ON si.content_id = mc.series_content_id
	WHERE mc.chapter_content_id = $1
`

// lookupMangaSeriesForChapter returns the series content id and title when the
// given item is a manga chapter; ok is false for ordinary ebooks.
func (s *DetailService) lookupMangaSeriesForChapter(ctx context.Context, chapterContentID string) (string, string, bool) {
	if s == nil || s.itemRepo == nil || s.itemRepo.pool == nil {
		return "", "", false
	}
	var seriesID, seriesTitle string
	err := s.itemRepo.pool.QueryRow(ctx, mangaSeriesForChapterQuery, chapterContentID).
		Scan(&seriesID, &seriesTitle)
	if err != nil {
		return "", "", false
	}
	return seriesID, seriesTitle, true
}

// MangaChapterFile is one local file backing a chapter of a manga series, for
// the series "View Details" dialog.
type MangaChapterFile struct {
	ContentID    string   `json:"content_id"`
	Title        string   `json:"title"`
	ChapterIndex *float64 `json:"chapter_index,omitempty"`
	Volume       string   `json:"volume,omitempty"`
	FilePath     string   `json:"file_path,omitempty"`
	FileName     string   `json:"file_name"`
	FileSize     int64    `json:"file_size"`
	Container    string   `json:"container,omitempty"`
}

// MangaSeriesFiles is the series file-details payload: the folder(s) the
// chapter files live in plus one row per file in reading order.
type MangaSeriesFiles struct {
	FolderPaths []string           `json:"folder_paths,omitempty"`
	Files       []MangaChapterFile `json:"files"`
}

// mangaChapterFilesQuery lists a manga series' chapter files in reading order
// (mirrors mangaChaptersQuery ordering).
const mangaChapterFilesQuery = `
	SELECT m.content_id, m.title, mc.chapter_index, mc.volume,
	       f.file_path, COALESCE(f.file_size, 0), COALESCE(f.container, '')
	FROM manga_chapters mc
	JOIN media_items m ON m.content_id = mc.chapter_content_id
	JOIN media_files f ON f.content_id = mc.chapter_content_id
	WHERE mc.series_content_id = $1
	ORDER BY mc.chapter_index NULLS LAST, m.sort_title, f.file_path
`

// GetMangaChapterFiles returns the local file listing for an accessible manga
// series. File paths are always populated here; the API layer strips them for
// viewers without file-path visibility (same policy as item versions).
func (s *DetailService) GetMangaChapterFiles(ctx context.Context, seriesContentID string, filter AccessFilter) (*MangaSeriesFiles, error) {
	if err := s.itemRepo.EnsureAccessible(ctx, seriesContentID, filter); err != nil {
		return nil, err
	}

	rows, err := s.itemRepo.pool.Query(ctx, mangaChapterFilesQuery, seriesContentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	result := &MangaSeriesFiles{Files: make([]MangaChapterFile, 0, 16)}
	folders := make([]string, 0, 1)
	seenFolders := make(map[string]struct{})
	for rows.Next() {
		var (
			file   MangaChapterFile
			index  *float64
			volume *string
		)
		if err := rows.Scan(&file.ContentID, &file.Title, &index, &volume, &file.FilePath, &file.FileSize, &file.Container); err != nil {
			return nil, err
		}
		file.ChapterIndex = index
		if volume != nil {
			file.Volume = *volume
		}
		file.FileName = filepath.Base(file.FilePath)
		if dir := filepath.Dir(file.FilePath); dir != "." && dir != "/" && strings.TrimSpace(dir) != "" {
			if _, ok := seenFolders[dir]; !ok {
				seenFolders[dir] = struct{}{}
				folders = append(folders, dir)
			}
		}
		result.Files = append(result.Files, file)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	result.FolderPaths = folders
	return result, nil
}
