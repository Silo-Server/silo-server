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

// A library scan that runs while another scan is publishing a new ebook must
// not delete it. The publishing scan writes the item and its file row before
// the library membership, so the library scan finds an unchanged file whose
// item has no membership yet, and its membership sweep took that item for an
// orphan. The book then stayed gone: its file row survived, so later scans
// skipped it as unchanged.
func TestScanFileEbookKeepsBookPublishedDuringLibraryScanDB(t *testing.T) {
	pool := newScannerTestPool(t)
	folderID := seedDeadRootTestFolder(t, pool, "ebooks", fmt.Sprintf("concurrent-ebook-%d", time.Now().UnixNano()))
	t.Cleanup(func() {
		bg := context.Background()
		_, _ = pool.Exec(bg, `DELETE FROM media_item_libraries WHERE media_folder_id = $1`, folderID)
		_, _ = pool.Exec(bg, `DELETE FROM media_items WHERE content_id IN (SELECT content_id FROM media_files WHERE media_folder_id = $1)`, folderID)
	})
	root := t.TempDir()
	folder := &models.MediaFolder{ID: folderID, Type: "ebooks", Paths: []string{root}}
	book := filepath.Join(root, "book.epub")
	if err := os.Rename(writeTestEPUBWithOPFBytes(t, []byte(`<?xml version="1.0" encoding="UTF-8"?>
<package xmlns:dc="http://purl.org/dc/elements/1.1/" xmlns:opf="http://www.idpf.org/2007/opf">
  <metadata>
    <dc:title>Concurrent Title</dc:title>
    <dc:creator>Concurrent Author</dc:creator>
  </metadata>
</package>`)), book); err != nil {
		t.Fatal(err)
	}

	library := NewScanner(NewFileRepository(pool), "ffprobe", nil, 1, false, time.Hour)
	var libraryErr error
	ran := false
	publisher := NewScanner(NewFileRepository(hookedPool(t, ebookMembershipInsertSQL, func() {
		ran = true
		libraryErr = library.ScanEbookFolder(context.Background(), folder)
	})), "ffprobe", nil, 1, false, time.Hour)

	if err := publisher.ScanFile(t.Context(), book, folder); err != nil {
		t.Fatalf("ScanFile: %v", err)
	}
	if !ran {
		t.Fatal("the library scan never ran; the membership insert was not traced")
	}
	if libraryErr != nil {
		t.Fatalf("library scan: %v", libraryErr)
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
