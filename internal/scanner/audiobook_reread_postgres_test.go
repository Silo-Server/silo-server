package scanner

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/models"
)

// An admin's metadata refresh of a book reads its unchanged files' tags again;
// the book keeps its item, its file row, and the listening progress that
// points at them. An ordinary scan still skips it, and so does the refresh
// for a loose book beside it in the scanned folder. The stub ffprobe reports a
// new title on the second read, as a change to how tags are read would.
func TestAudiobookTagRereadKeepsItemDB(t *testing.T) {
	pool := newScannerTestPool(t)
	ctx := t.Context()
	root := t.TempDir()
	ffprobe := filepath.Join(t.TempDir(), "ffprobe")
	stubTitle := func(title string) {
		// The loose book beside it is titled "Other <title>".
		writeFakeTool(t, ffprobe, `#!/bin/sh
for last; do :; done
title='`+title+`'
case "$last" in *other.mp3) title="Other $title" ;; esac
cat <<JSON
{"format":{"format_name":"mp3","duration":"60.0","size":"1","bit_rate":"64000","tags":{"title":"$title","artist":"Ann Author"}},"streams":[{"index":0,"codec_type":"audio","codec_name":"mp3","channels":2}],"chapters":[]}
JSON
`)
	}
	// Loose books at the library root: refreshing one scans the root.
	part := filepath.Join(root, "book.mp3")
	sibling := filepath.Join(root, "other.mp3")
	writeTestFile(t, part, "audio")
	writeTestFile(t, sibling, "audio")
	folderID := seedDeadRootTestFolder(t, pool, "audiobooks", fmt.Sprintf("tag-reread-%d", time.Now().UnixNano()))
	folder := &models.MediaFolder{ID: folderID, Type: "audiobooks", Paths: []string{root}}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM media_items WHERE content_id IN (SELECT content_id FROM media_files WHERE media_folder_id = $1)`, folderID)
		_, _ = pool.Exec(context.Background(), `DELETE FROM media_item_libraries WHERE media_folder_id = $1`, folderID)
	})
	s := NewScanner(NewFileRepository(pool), ffprobe, nil, 1, false, time.Hour)

	readFile := func(path string) (contentID string, fileID int, title string) {
		t.Helper()
		if err := pool.QueryRow(ctx, `SELECT mf.content_id, mf.id, mi.title FROM media_files mf
			JOIN media_items mi ON mi.content_id = mf.content_id
			WHERE mf.media_folder_id = $1 AND mf.file_path = $2 AND mf.missing_since IS NULL`, folderID, path).Scan(&contentID, &fileID, &title); err != nil {
			t.Fatalf("read book: %v", err)
		}
		return contentID, fileID, title
	}
	read := func() (string, int, string) { return readFile(part) }

	stubTitle("Saga 2 - Book Title")
	if _, err := s.ScanSubtree(ctx, folder, root); err != nil {
		t.Fatal(err)
	}
	contentID, fileID, _ := read()
	var userID int
	if err := pool.QueryRow(ctx, `INSERT INTO users (role, username) VALUES ('user', $1) RETURNING id`,
		fmt.Sprintf("tag-reread-%d", folderID)).Scan(&userID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DELETE FROM users WHERE id = $1`, userID) })
	if _, err := pool.Exec(ctx, `INSERT INTO user_watch_progress (user_id, profile_id, media_item_id, position_seconds, duration_seconds, last_file_id)
		VALUES ($1, 'p', $2, 30, 60, $3)`, userID, contentID, fileID); err != nil {
		t.Fatal(err)
	}

	stubTitle("Book Title")
	if _, err := s.ScanSubtree(ctx, folder, root); err != nil {
		t.Fatal(err)
	}
	if _, _, title := read(); title != "Saga 2 - Book Title" {
		t.Fatalf("ordinary scan read the unchanged book again: title %q", title)
	}

	marked := WithAudiobookTagReread(ctx, contentID)
	if _, err := s.ScanSubtree(marked, folder, root); err != nil {
		t.Fatal(err)
	}
	if !AudiobookTagRereadDone(marked) {
		t.Error("AudiobookTagRereadDone = false after the book was read")
	}
	gotContent, gotFile, title := read()
	if title != "Book Title" {
		t.Errorf("after the refresh, title %q, want the tags read again", title)
	}
	if gotContent != contentID || gotFile != fileID {
		t.Errorf("after the refresh, item %s file %d, want item %s file %d", gotContent, gotFile, contentID, fileID)
	}
	if _, _, title := readFile(sibling); title != "Other Saga 2 - Book Title" {
		t.Errorf("the refresh read the loose book beside it again: title %q", title)
	}
	var position float64
	if err := pool.QueryRow(ctx, `SELECT p.position_seconds FROM user_watch_progress p
		JOIN media_items mi ON mi.content_id = p.media_item_id
		JOIN media_files mf ON mf.id = p.last_file_id AND mf.content_id = mi.content_id
		WHERE p.user_id = $1`, userID).Scan(&position); err != nil || position != 30 {
		t.Errorf("progress after the refresh: position %v, err %v; want 30 on the same item and file", position, err)
	}

	// When the book cannot be read, the scan still succeeds because the book
	// beside it was skipped; the mark reports that the book was not read.
	writeFakeTool(t, ffprobe, "#!/bin/sh\necho 'Invalid data found when processing input' >&2\nexit 1\n")
	marked = WithAudiobookTagReread(ctx, contentID)
	if _, err := s.ScanSubtree(marked, folder, root); err != nil {
		t.Fatalf("scan with an unreadable marked book: %v", err)
	}
	if AudiobookTagRereadDone(marked) {
		t.Error("AudiobookTagRereadDone = true for a book that could not be read")
	}
}
