package scanner

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/models"
)

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
		_, _ = pool.Exec(bg, `DELETE FROM media_item_libraries WHERE media_folder_id = $1`, folderID)
		_, _ = pool.Exec(bg, `DELETE FROM media_items WHERE content_id IN (SELECT content_id FROM media_files WHERE media_folder_id = $1)`, folderID)
	})
	root := t.TempDir()
	folder := &models.MediaFolder{ID: folderID, Type: "ebooks", Paths: []string{root}}
	writeBook := func(dir, title string) string {
		t.Helper()
		if err := os.MkdirAll(filepath.Join(root, dir), 0o755); err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(root, dir, "book.epub")
		if err := os.Rename(writeTestEPUBWithOPFBytes(t, []byte(`<?xml version="1.0" encoding="UTF-8"?>
<package xmlns:dc="http://purl.org/dc/elements/1.1/" xmlns:opf="http://www.idpf.org/2007/opf">
  <metadata>
    <dc:title>`+title+`</dc:title>
    <dc:creator>Concurrent Author</dc:creator>
  </metadata>
</package>`)), path); err != nil {
			t.Fatal(err)
		}
		return path
	}
	book := writeBook("Published", "Published Title")
	sibling := filepath.Dir(writeBook("Sibling", "Sibling Title"))

	other := NewScanner(NewFileRepository(pool), "ffprobe", nil, 1, false, time.Hour)
	var otherErr error
	ran := false
	publisher := NewScanner(NewFileRepository(hookedPool(t, ebookMembershipInsertSQL, func() {
		ran = true
		_, otherErr = other.ScanSubtree(context.Background(), folder, sibling)
	})), "ffprobe", nil, 1, false, time.Hour)

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
