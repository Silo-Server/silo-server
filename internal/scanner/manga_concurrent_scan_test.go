package scanner

import (
	"archive/zip"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/models"
)

// mangaChapterLinkSQL matches a scan's chapter-to-series link
// (upsertMangaChapter).
const mangaChapterLinkSQL = "INSERT INTO manga_chapters"

// newMangaTestLibrary seeds a manga library rooted in a temp dir.
func newMangaTestLibrary(t *testing.T) (*pgxpool.Pool, *models.MediaFolder) {
	t.Helper()
	pool := newScannerTestPool(t)
	folderID := seedDeadRootTestFolder(t, pool, "manga", fmt.Sprintf("manga-series-%d", time.Now().UnixNano()))
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM media_items WHERE content_id IN (
			SELECT content_id FROM media_files WHERE media_folder_id = $1
			UNION SELECT content_id FROM media_item_libraries WHERE media_folder_id = $1)`, folderID)
	})
	return pool, &models.MediaFolder{ID: folderID, Type: "manga", Paths: []string{t.TempDir()}}
}

// writeMangaChapter writes a two-page comic archive at root/rel.
func writeMangaChapter(t *testing.T, root, rel string) string {
	t.Helper()
	path := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(file)
	for _, name := range []string{"001.jpg", "002.jpg"} {
		w, err := zw.Create(name)
		if err == nil {
			_, err = w.Write([]byte("placeholder"))
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	if err := errors.Join(zw.Close(), file.Close()); err != nil {
		t.Fatal(err)
	}
	return path
}

// requireMangaSeries checks that the chapter at path is linked to a series
// item that is a member of the library, and returns the series title.
func requireMangaSeries(t *testing.T, pool *pgxpool.Pool, folderID int, path string) string {
	t.Helper()
	var title string
	err := pool.QueryRow(t.Context(), `
		SELECT series.title
		FROM media_files mf
		JOIN manga_chapters mc ON mc.chapter_content_id = mf.content_id
		JOIN media_items series ON series.content_id = mc.series_content_id AND series.type = 'manga'
		JOIN media_item_libraries mil ON mil.content_id = series.content_id AND mil.media_folder_id = $1
		WHERE mf.media_folder_id = $1 AND mf.file_path = $2`, folderID, path).Scan(&title)
	if errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("chapter %s has no series in the library", filepath.Base(path))
	}
	if err != nil {
		t.Fatalf("load chapter series: %v", err)
	}
	return title
}

// TestScanFileMangaKeepsNewSeriesDuringConcurrentScanDB covers a scan that
// indexes the first chapter of a new series while another manga scan of the
// library finishes and removes the library's empty series. The other scan
// lands after the first scan created the series and before it linked the
// chapter. The new series must survive with its chapter.
func TestScanFileMangaKeepsNewSeriesDuringConcurrentScanDB(t *testing.T) {
	pool, folder := newMangaTestLibrary(t)
	root := folder.Paths[0]
	chapter := writeMangaChapter(t, root, "Series/Series 001.cbz")
	otherChapter := writeMangaChapter(t, root, "Other/Other 001.cbz")

	other := NewScanner(NewFileRepository(pool), "ffprobe", nil, 2, false, time.Hour)
	var otherErr error
	ran := false
	hooked := &concurrentBookFixture{ffprobe: "ffprobe"}
	s := hooked.hookedScanner(t, mangaChapterLinkSQL, func() {
		ran = true
		otherErr = other.ScanFile(context.Background(), otherChapter, folder)
	})

	if err := s.ScanFile(t.Context(), chapter, folder); err != nil {
		t.Fatalf("ScanFile: %v", err)
	}
	if !ran {
		t.Fatalf("the other scan never ran; no query matched %q", mangaChapterLinkSQL)
	}
	if otherErr != nil {
		t.Fatalf("other scan: %v", otherErr)
	}
	if got := requireMangaSeries(t, pool, folder.ID, chapter); got != "Series" {
		t.Fatalf("series title = %q, want Series", got)
	}
	requireMangaSeries(t, pool, folder.ID, otherChapter)
}

// TestScanFileMangaRelinksUnchangedChapterWithoutSeriesDB covers a chapter
// whose file is unchanged but whose series is gone from the library, the state
// an interrupted or raced scan leaves behind. The next scan must restore the
// series rather than skip the file as unchanged.
func TestScanFileMangaRelinksUnchangedChapterWithoutSeriesDB(t *testing.T) {
	for _, tc := range []struct {
		name, sql string
	}{
		{name: "series deleted", sql: `
			DELETE FROM media_items mi USING media_item_libraries mil
			WHERE mi.type = 'manga' AND mil.content_id = mi.content_id AND mil.media_folder_id = $1`},
		{name: "series membership missing", sql: `
			DELETE FROM media_item_libraries mil USING media_items mi
			WHERE mi.type = 'manga' AND mil.content_id = mi.content_id AND mil.media_folder_id = $1`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pool, folder := newMangaTestLibrary(t)
			chapter := writeMangaChapter(t, folder.Paths[0], "Series/Series 001.cbz")
			s := NewScanner(NewFileRepository(pool), "ffprobe", nil, 2, false, time.Hour)
			if err := s.ScanFile(t.Context(), chapter, folder); err != nil {
				t.Fatalf("first scan: %v", err)
			}
			requireMangaSeries(t, pool, folder.ID, chapter)
			if _, err := pool.Exec(t.Context(), tc.sql, folder.ID); err != nil {
				t.Fatalf("remove series: %v", err)
			}

			if err := s.ScanFile(t.Context(), chapter, folder); err != nil {
				t.Fatalf("rescan: %v", err)
			}
			requireMangaSeries(t, pool, folder.ID, chapter)
		})
	}
}
