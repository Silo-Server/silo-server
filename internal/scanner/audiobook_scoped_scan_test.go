package scanner

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/models"
)

// A subtree scan rebuilds the books a library scan finds in that place:
// discovery stops below a folder holding audio, except at a configured library
// root. Without ffprobe every book fails to parse, so the error names each book
// the scan rebuilt.
func TestScanSubtreeAudiobookMatchesLibraryScanBooks(t *testing.T) {
	for _, tc := range []struct {
		name   string
		ignore string            // root .siloignore content
		nested []string          // extra configured roots, relative to the library root
		links  map[string]string // directory symlinks to their targets, relative to the library root
		scope  string
		want   []string
		absent []string
	}{
		{
			name:   "loose audio in the scope makes it the book",
			scope:  "Author",
			want:   []string{"Author"},
			absent: []string{"Book 1"},
		},
		{
			name:   "a configured root below the book is still walked",
			nested: []string{"Author/Book 1"},
			scope:  "Author",
			want:   []string{"Author", "Author/Book 1"},
		},
		{
			name:   "a configured root below a book is walked from the outer root",
			nested: []string{"Author/Book 1"},
			scope:  ".",
			want:   []string{"Author", "Author/Book 1"},
		},
		{
			name:   "ignored loose audio does not make a book",
			ignore: "Author/foreword.mp3\n",
			scope:  "Author",
			want:   []string{"Author/Book 1"},
		},
		{
			name:   "a scope inside a book rebuilds the book",
			scope:  "Author/Book 1",
			want:   []string{"Author"},
			absent: []string{"Book 1"},
		},
		{
			name:   "ignored loose audio above the scope does not make a book",
			ignore: "Author/foreword.mp3\n",
			scope:  "Author/Book 1",
			want:   []string{"Author/Book 1"},
		},
		{
			name:   "audio in a configured root above the scope does not make a book",
			nested: []string{"Author"},
			scope:  "Author/Book 1",
			want:   []string{"Author/Book 1"},
		},
		{
			name:  "a book reached through an alias the library walk rejects is not lifted",
			links: map[string]string{"Loop": "."},
			scope: "Loop/Author/Book 1",
			want:  []string{"Loop/Author/Book 1"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			writeTestFile(t, filepath.Join(root, "Author", "foreword.mp3"), "audio")
			writeTestFile(t, filepath.Join(root, "Author", "Book 1", "01.mp3"), "audio")
			if tc.ignore != "" {
				writeTestFile(t, filepath.Join(root, ".siloignore"), tc.ignore)
			}
			for link, target := range tc.links {
				if err := os.Symlink(filepath.Join(root, target), filepath.Join(root, link)); err != nil {
					t.Fatal(err)
				}
			}
			paths := []string{root}
			for _, nested := range tc.nested {
				paths = append(paths, filepath.Join(root, nested))
			}
			folder := &models.MediaFolder{ID: 42, Type: "audiobooks", Paths: paths}

			s := &Scanner{ffprobePath: "definitely-missing-ffprobe"}
			_, err := s.ScanSubtree(t.Context(), folder, filepath.Join(root, tc.scope))
			if err == nil {
				t.Fatal("ScanSubtree returned nil, want audiobook parse failures")
			}
			for _, book := range tc.want {
				if !strings.Contains(err.Error(), filepath.Join(root, book)+":") {
					t.Errorf("error = %q, want %s rebuilt", err, book)
				}
			}
			for _, book := range tc.absent {
				if strings.Contains(err.Error(), book) {
					t.Errorf("error = %q, want %s left alone", err, book)
				}
			}
		})
	}
}

// A file change rebuilds its folder's books by the same rule. An admin's
// exact-path request for an excluded file reads no ignore files, so it cannot
// tell that a library scan would skip the folder's audio and walk below it; it
// keeps walking rather than retire the books there.
func TestScanFileAudiobookMatchesLibraryScanBooks(t *testing.T) {
	for _, tc := range []struct {
		name     string
		file     string
		explicit bool
		ignore   string
		want     []string
		absent   []string
	}{
		{name: "file change", file: "Author/foreword.mp3", want: []string{"Author"}, absent: []string{"Book 1"}},
		{name: "file change inside a book", file: "Author/Book 1/01.mp3", want: []string{"Author"}, absent: []string{"Book 1"}},
		{name: "admin request for an excluded file", file: "Author/foreword.mp3", explicit: true, ignore: "Author/foreword.mp3\n", want: []string{"Author", "Author/Book 1"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			writeTestFile(t, filepath.Join(root, "Author", "foreword.mp3"), "audio")
			writeTestFile(t, filepath.Join(root, "Author", "Book 1", "01.mp3"), "audio")
			if tc.ignore != "" {
				writeTestFile(t, filepath.Join(root, ".siloignore"), tc.ignore)
			}
			ctx := t.Context()
			if tc.explicit {
				ctx = WithExplicitFileRequest(ctx)
			}

			s := &Scanner{ffprobePath: "definitely-missing-ffprobe"}
			err := s.ScanFile(ctx, filepath.Join(root, tc.file), &models.MediaFolder{ID: 42, Type: "audiobooks", Paths: []string{root}})
			if err == nil {
				t.Fatal("ScanFile returned nil, want audiobook parse failures")
			}
			for _, book := range tc.want {
				if !strings.Contains(err.Error(), filepath.Join(root, book)+":") {
					t.Errorf("error = %q, want %s rebuilt", err, book)
				}
			}
			for _, book := range tc.absent {
				if strings.Contains(err.Error(), book) {
					t.Errorf("error = %q, want %s left alone", err, book)
				}
			}
		})
	}
}

// A subtree scan inside a book reconciles missing files over the whole book,
// as a library scan does: a subfolder an earlier scoped scan imported as its
// own book has its files marked missing instead of being rebuilt.
func TestScanSubtreeAudiobookInsideBookRetiresSubfolderBookDB(t *testing.T) {
	pool := newScannerTestPool(t)
	ctx := t.Context()
	root := t.TempDir()
	ffprobe := filepath.Join(t.TempDir(), "ffprobe")
	writeFakeTool(t, ffprobe, `#!/bin/sh
cat <<'JSON'
{"format":{"format_name":"mp3","duration":"60.0","size":"1","bit_rate":"64000","tags":{"title":"Author","artist":"Author"}},"streams":[{"index":0,"codec_type":"audio","codec_name":"mp3","channels":2}],"chapters":[]}
JSON
`)
	writeTestFile(t, filepath.Join(root, "Author", "foreword.mp3"), "audio")
	bookDir := filepath.Join(root, "Author", "Book 1")
	partFile := filepath.Join(bookDir, "01.mp3")
	writeTestFile(t, partFile, "audio")
	folderID := seedDeadRootTestFolder(t, pool, "audiobooks", fmt.Sprintf("scope-inside-book-%d", time.Now().UnixNano()))
	folder := &models.MediaFolder{ID: folderID, Type: "audiobooks", Paths: []string{root}}
	contentID := fmt.Sprintf("audiobook-subfolder-%d", folderID)
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM media_item_libraries WHERE media_folder_id = $1`, folderID)
		_, _ = pool.Exec(context.Background(), `DELETE FROM media_items WHERE content_id IN (SELECT content_id FROM media_files WHERE media_folder_id = $1) OR content_id = $2`, folderID, contentID)
	})
	for _, stmt := range []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO media_items (content_id, type, title, status, genres) VALUES ($1, 'audiobook', 'Book 1', 'local', '{}'::text[])`, []any{contentID}},
		{`INSERT INTO media_item_libraries (content_id, media_folder_id) VALUES ($1, $2)`, []any{contentID, folderID}},
		{`INSERT INTO media_files (content_id, media_folder_id, file_path, observed_root_path, canonical_root_path, file_size, file_modified_at)
		  VALUES ($1, $2, $3, $4, $4, 5, now())`, []any{contentID, folderID, partFile, bookDir}},
	} {
		if _, err := pool.Exec(ctx, stmt.sql, stmt.args...); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}

	s := NewScanner(NewFileRepository(pool), ffprobe, nil, 2, false, time.Hour)
	if _, err := s.ScanSubtree(ctx, folder, bookDir); err != nil {
		t.Fatalf("subtree scan: %v", err)
	}

	var missing bool
	if err := pool.QueryRow(ctx, `SELECT missing_since IS NOT NULL FROM media_files WHERE file_path = $1`, partFile).Scan(&missing); err != nil {
		t.Fatalf("read subfolder file: %v", err)
	}
	if !missing {
		t.Error("Book 1 file still present, want it marked missing")
	}
	var bookRoot string
	if err := pool.QueryRow(ctx, `SELECT observed_root_path FROM media_files WHERE media_folder_id = $1 AND file_path = $2 AND missing_since IS NULL`,
		folderID, filepath.Join(root, "Author", "foreword.mp3")).Scan(&bookRoot); err != nil {
		t.Fatalf("read rebuilt book: %v", err)
	}
	if want := filepath.Join(root, "Author"); bookRoot != want {
		t.Errorf("rebuilt book root = %s, want %s", bookRoot, want)
	}
}
