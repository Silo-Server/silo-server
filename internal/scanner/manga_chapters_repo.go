package scanner

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
)

// mangaChapterWrite turns a parsed (volume, index, has) into the (index, volume)
// values to persist: index is nil when has=false, volume is "" when absent.
func mangaChapterWrite(volume string, index float64, has bool) (idx *float64, vol string) {
	if !has {
		return nil, ""
	}
	i := index
	return &i, volume
}

// upsertMangaChapter inserts or updates a row in manga_chapters for the given
// chapter. A nil index is stored as NULL (chapter number unparseable or absent).
func upsertMangaChapter(ctx context.Context, pool *pgxpool.Pool, chapterID, seriesID string, index *float64, volume string) error {
	_, err := pool.Exec(ctx, `
		INSERT INTO manga_chapters (chapter_content_id, series_content_id, chapter_index, volume, updated_at)
		VALUES ($1, $2, $3, $4, NOW())
		ON CONFLICT (chapter_content_id) DO UPDATE SET
			series_content_id = EXCLUDED.series_content_id,
			chapter_index     = EXCLUDED.chapter_index,
			volume            = EXCLUDED.volume,
			updated_at        = NOW()
	`, chapterID, seriesID, index, volume)
	if err != nil {
		return fmt.Errorf("upsert manga_chapters row: %w", err)
	}
	return nil
}

// listMangaChapters returns the chapter_content_id values for all chapters
// belonging to the given series, ordered by chapter_index (NULLs last) then
// by content ID for a stable secondary sort.
func listMangaChapters(ctx context.Context, pool *pgxpool.Pool, seriesID string) ([]string, error) {
	rows, err := pool.Query(ctx, `
		SELECT chapter_content_id
		FROM manga_chapters
		WHERE series_content_id = $1
		ORDER BY chapter_index NULLS LAST, chapter_content_id
	`, seriesID)
	if err != nil {
		return nil, fmt.Errorf("list manga_chapters: %w", err)
	}
	defer rows.Close()

	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan manga_chapters row: %w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate manga_chapters: %w", err)
	}
	return ids, nil
}
