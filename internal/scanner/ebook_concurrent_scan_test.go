package scanner

import (
	"archive/zip"
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/models"
)

// ebookSkipStateSQL matches the ebook and manga skip check, a scan's first
// read after its walk (loadEbookSkipState).
const ebookSkipStateSQL = "mf.observed_root_path = ANY($2)"

// TestScanBookFolderKeepsBookIndexedByConcurrentScanDB covers a library scan
// of an ebook or manga library that walks it while a second book is absent. A
// single-file scan indexes that book while the library scan runs: after the
// walk (a new book), or as the library scan marks it missing (a book restored).
// The library scan must leave the book present.
func TestScanBookFolderKeepsBookIndexedByConcurrentScanDB(t *testing.T) {
	kinds := []struct {
		kind          string
		first, second string
		content       func(t *testing.T, n int) []byte
		scan          func(context.Context, *Scanner, *models.MediaFolder) error
	}{
		{
			kind: "ebooks", first: "First.epub", second: "Second.epub",
			content: func(t *testing.T, n int) []byte {
				data, err := os.ReadFile(writeTestEPUB(t, []string{fmt.Sprintf("urn:uuid:concurrent-%d", n)}))
				if err != nil {
					t.Fatal(err)
				}
				return data
			},
			scan: func(ctx context.Context, s *Scanner, f *models.MediaFolder) error {
				return s.ScanEbookFolder(ctx, f)
			},
		},
		{
			kind: "manga", first: "Series/Series 001.cbz", second: "Series/Series 002.cbz",
			content: func(t *testing.T, _ int) []byte { return testCBZ(t) },
			scan: func(ctx context.Context, s *Scanner, f *models.MediaFolder) error {
				return s.ScanMangaFolder(ctx, f)
			},
		},
	}
	timings := []struct {
		name     string
		restored bool   // the second book was indexed, then removed before the library scan
		match    string // the library scan query the other scan runs before
	}{
		{name: "new after walk", match: ebookSkipStateSQL},
		{name: "restored while marking", restored: true, match: "SET missing_since = $1"},
	}
	for _, k := range kinds {
		for _, timing := range timings {
			t.Run(k.kind+"/"+timing.name, func(t *testing.T) {
				pool := newScannerTestPool(t)
				folderID := seedDeadRootTestFolder(t, pool, k.kind, fmt.Sprintf("concurrent-%s-%d", k.kind, time.Now().UnixNano()))
				t.Cleanup(func() {
					bg := context.Background()
					_, _ = pool.Exec(bg, `DELETE FROM media_items WHERE content_id IN (
						SELECT content_id FROM media_files WHERE media_folder_id = $1
						UNION SELECT content_id FROM media_item_libraries WHERE media_folder_id = $1)`, folderID)
				})
				root := t.TempDir()
				folder := &models.MediaFolder{ID: folderID, Type: k.kind, Paths: []string{root}}
				first, second := filepath.Join(root, k.first), filepath.Join(root, k.second)
				if err := writeBookFile(first, k.content(t, 1)); err != nil {
					t.Fatal(err)
				}
				secondData := k.content(t, 2)

				other := NewScanner(NewFileRepository(pool), "ffprobe", nil, 2, false, time.Hour)
				if timing.restored {
					if err := writeBookFile(second, secondData); err != nil {
						t.Fatal(err)
					}
					if err := k.scan(t.Context(), other, folder); err != nil {
						t.Fatalf("initial scan: %v", err)
					}
					if err := os.Remove(second); err != nil {
						t.Fatal(err)
					}
				}
				var otherErr error
				ran := false
				s := newHookedScanner(t, "ffprobe", timing.match, func() {
					ran = true
					if otherErr = writeBookFile(second, secondData); otherErr == nil {
						otherErr = other.ScanFile(context.Background(), second, folder)
					}
				})

				if err := k.scan(t.Context(), s, folder); err != nil {
					t.Fatalf("library scan: %v", err)
				}
				if !ran {
					t.Fatalf("the other scan never ran; no query matched %q", timing.match)
				}
				if otherErr != nil {
					t.Fatalf("other scan: %v", otherErr)
				}
				requirePresentFilePaths(t, pool, folderID, first, second)
			})
		}
	}
}

// requirePresentFilePaths compares the library's present files with want.
func requirePresentFilePaths(t *testing.T, pool *pgxpool.Pool, folderID int, want ...string) {
	t.Helper()
	rows, err := pool.Query(t.Context(), `
		SELECT file_path FROM media_files
		WHERE media_folder_id = $1 AND missing_since IS NULL ORDER BY file_path`, folderID)
	if err != nil {
		t.Fatalf("list present files: %v", err)
	}
	got, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		t.Fatalf("list present files: %v", err)
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("present files:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

func writeBookFile(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}

// testCBZ returns a two-page comic archive.
func testCBZ(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, name := range []string{"001.jpg", "002.jpg"} {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte("placeholder")); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// ebookMembershipInsertSQL matches insertEbookLibraryMembership, the last
// write that publishes a new book.
const ebookMembershipInsertSQL = "INSERT INTO media_item_libraries (content_id, media_folder_id, first_seen_at)"

// A scan elsewhere in an ebook library must not delete a book another scan is
// still publishing. The publishing scan writes the item and its file row
// before the library membership. A scan of a sibling folder reconciles
// memberships across the whole library, and took the item, which had a file
// row and no membership yet, for an orphan.
func TestScanFileEbookKeepsBookPublishedDuringSiblingScanDB(t *testing.T) {
	pool := newScannerTestPool(t)
	folderID := seedDeadRootTestFolder(t, pool, "ebooks", fmt.Sprintf("concurrent-ebook-%d", time.Now().UnixNano()))
	t.Cleanup(func() {
		bg := context.Background()
		_, _ = pool.Exec(bg, `DELETE FROM media_items WHERE content_id IN (
			SELECT content_id FROM media_files WHERE media_folder_id = $1
			UNION SELECT content_id FROM media_item_libraries WHERE media_folder_id = $1)`, folderID)
	})
	root := t.TempDir()
	folder := &models.MediaFolder{ID: folderID, Type: "ebooks", Paths: []string{root}}
	writeBook := func(dir, title string) string {
		t.Helper()
		data, err := os.ReadFile(writeTestEPUBWithOPFBytes(t, []byte(`<?xml version="1.0" encoding="UTF-8"?>
<package xmlns:dc="http://purl.org/dc/elements/1.1/" xmlns:opf="http://www.idpf.org/2007/opf">
  <metadata>
    <dc:title>`+title+`</dc:title>
    <dc:creator>Concurrent Author</dc:creator>
  </metadata>
</package>`)))
		if err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(root, dir, "book.epub")
		if err := writeBookFile(path, data); err != nil {
			t.Fatal(err)
		}
		return path
	}
	book := writeBook("Published", "Published Title")
	sibling := filepath.Dir(writeBook("Sibling", "Sibling Title"))

	other := NewScanner(NewFileRepository(pool), "ffprobe", nil, 1, false, time.Hour)
	var otherErr error
	ran := false
	publisher := newHookedScanner(t, "ffprobe", ebookMembershipInsertSQL, func() {
		ran = true
		_, otherErr = other.ScanSubtree(context.Background(), folder, sibling)
	})

	if err := publisher.ScanFile(t.Context(), book, folder); err != nil {
		t.Fatalf("ScanFile: %v", err)
	}
	if !ran {
		t.Fatal("the sibling scan never ran; the membership insert was not traced")
	}
	if otherErr != nil {
		t.Fatalf("sibling scan: %v", otherErr)
	}

	var item, membership bool
	if err := pool.QueryRow(t.Context(), `
		SELECT EXISTS (SELECT 1 FROM media_items mi WHERE mi.content_id = mf.content_id),
		       EXISTS (SELECT 1 FROM media_item_libraries mil WHERE mil.content_id = mf.content_id AND mil.media_folder_id = $1)
		FROM media_files mf
		WHERE mf.media_folder_id = $1 AND mf.file_path = $2 AND mf.missing_since IS NULL`,
		folderID, book).Scan(&item, &membership); err != nil {
		t.Fatalf("load the book's present file: %v", err)
	}
	if !item || !membership {
		t.Fatalf("book after both scans: item exists %v, library membership %v; want both", item, membership)
	}
}
