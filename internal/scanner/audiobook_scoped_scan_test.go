package scanner

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/models"
)

// A subtree scan rebuilds the books a library scan finds in that place: a
// folder's own audio is one book, a subfolder holding audio is a book of its
// own, and each loose file in a configured library root is a book. Without
// ffprobe every book fails to parse, so the error names each book the scan
// rebuilt.
func TestScanSubtreeAudiobookMatchesLibraryScanBooks(t *testing.T) {
	for _, tc := range []struct {
		name   string
		ignore string            // root .siloignore content
		nested []string          // extra configured roots, relative to the library root
		links  map[string]string // directory symlinks to their targets, relative to the library root
		scope  string
		want   []string // book roots the scan rebuilds, relative to the library root
	}{
		{
			name:  "a folder's own audio is one book beside a book folder",
			scope: "Author",
			want:  []string{"Author", "Author/Book 1"},
		},
		{
			name:   "a configured root's loose files are books of their own",
			nested: []string{"Author/Book 1"},
			scope:  "Author",
			want:   []string{"Author", "Author/Book 1/01.mp3"},
		},
		{
			name:   "a configured root inside the scope is walked from the outer root",
			nested: []string{"Author/Book 1"},
			scope:  ".",
			want:   []string{"Author", "Author/Book 1/01.mp3"},
		},
		{
			name:   "ignored loose audio does not make a book",
			ignore: "Author/foreword.mp3\n",
			scope:  "Author",
			want:   []string{"Author/Book 1"},
		},
		// When a folder's own audio made the folder one book that took in its
		// subfolders, this scope was lifted to rebuild Author. A folder's own
		// book no longer depends on its subfolders, and Book 1 is a book of its
		// own, so the scope rebuilds just Book 1, as a library scan finds it.
		{
			name:  "a scope inside a folder with audio of its own rebuilds only the scope",
			scope: "Author/Book 1",
			want:  []string{"Author/Book 1"},
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
			checkRebuiltAudiobooks(t, root, err, tc.want)
		})
	}
}

// A file change rebuilds its folder's books by the same rule. An admin's
// exact-path request for an excluded file reads no ignore files, so that file
// is rebuilt too.
func TestScanFileAudiobookMatchesLibraryScanBooks(t *testing.T) {
	for _, tc := range []struct {
		name     string
		file     string
		explicit bool
		ignore   string
		want     []string // book roots the scan rebuilds, relative to the library root
	}{
		{name: "file change", file: "Author/foreword.mp3", want: []string{"Author", "Author/Book 1"}},
		// When Author's own audio made it one book that took in Book 1, this
		// change rebuilt Author. Book 1 is now a book of its own.
		{name: "file change inside a subfolder book", file: "Author/Book 1/01.mp3", want: []string{"Author/Book 1"}},
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
			checkRebuiltAudiobooks(t, root, err, tc.want)
		})
	}
}

// A folder's own audio is one book and a subfolder holding audio is a book of
// its own. Book 1 was indexed as its own book; a subtree scan of it keeps that
// item, and a subtree scan of Author adds Author as a one-part book beside it.
// (When a folder's own audio made the folder one book that took in its
// subfolders, a scan inside Book 1 rebuilt Author and retired Book 1's item.)
func TestScanSubtreeAudiobookKeepsSubfolderBookBesideFolderBookDB(t *testing.T) {
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
	foreword := filepath.Join(root, "Author", "foreword.mp3")
	for _, scope := range []string{bookDir, filepath.Join(root, "Author")} {
		if _, err := s.ScanSubtree(ctx, folder, scope); err != nil {
			t.Fatalf("subtree scan of %s: %v", scope, err)
		}
		var partRoot, partContent string
		if err := pool.QueryRow(ctx, `SELECT observed_root_path, content_id FROM media_files WHERE file_path = $1 AND missing_since IS NULL`, partFile).Scan(&partRoot, &partContent); err != nil {
			t.Fatalf("after scanning %s, read Book 1 file: %v", scope, err)
		}
		if partRoot != bookDir || partContent != contentID {
			t.Errorf("after scanning %s, Book 1 file root %s item %s, want root %s item %s", scope, partRoot, partContent, bookDir, contentID)
		}
	}
	var forewordRoot, forewordContent string
	if err := pool.QueryRow(ctx, `SELECT observed_root_path, content_id FROM media_files WHERE media_folder_id = $1 AND file_path = $2 AND missing_since IS NULL`,
		folderID, foreword).Scan(&forewordRoot, &forewordContent); err != nil {
		t.Fatalf("read Author book: %v", err)
	}
	if want := filepath.Join(root, "Author"); forewordRoot != want || forewordContent == contentID {
		t.Errorf("Author book root %s item %s, want root %s and an item of its own", forewordRoot, forewordContent, want)
	}
}

// checkRebuiltAudiobooks compares the book roots a scan without ffprobe failed
// to parse, which are the books it rebuilt, with want.
func checkRebuiltAudiobooks(t *testing.T, root string, err error, want []string) {
	t.Helper()
	if err == nil {
		t.Fatal("scan returned nil, want audiobook parse failures")
	}
	var got []string
	for _, m := range rebuiltAudiobookRE.FindAllStringSubmatch(err.Error(), -1) {
		rel, relErr := filepath.Rel(root, m[1])
		if relErr != nil {
			t.Fatal(relErr)
		}
		got = append(got, filepath.ToSlash(rel))
	}
	slices.Sort(got)
	want = slices.Sorted(slices.Values(want))
	if !slices.Equal(got, want) {
		t.Errorf("rebuilt books = %q, want %q (error %q)", got, want, err)
	}
}

var rebuiltAudiobookRE = regexp.MustCompile(`(?m)(?:^|: )(/[^\n]*?): parse audiobook folder `)

// An admin's scan of a file the ignore files exclude admits that file past
// them and nothing else they exclude, even when the scan widens to the book
// around the file's disc folder. A scan Silo starts on its own admits nothing.
func TestExplicitAudiobookFileScanAdmitsOnlyThatFile(t *testing.T) {
	root := t.TempDir()
	for _, rel := range []string{"Author/Book/CD1/01.mp3", "Author/Book/CD2/02.mp3", "Author/Book/CD2/03.mp3", "Author/Book/Ignored Folder/x.mp3"} {
		writeTestFile(t, filepath.Join(root, rel), "audio")
	}
	writeTestFile(t, filepath.Join(root, ".siloignore"), "Author/Book/CD2/02.mp3\nAuthor/Book/CD2/03.mp3\nAuthor/Book/Ignored Folder\n")
	file := filepath.Join(root, "Author/Book/CD2/02.mp3")
	libraryRoots := []string{root}
	for _, tc := range []struct {
		name     string
		explicit bool
		want     []string
	}{
		{"admin request", true, []string{"Author/Book/CD1/01.mp3", "Author/Book/CD2/02.mp3"}},
		{"file change", false, []string{"Author/Book/CD1/01.mp3"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := t.Context()
			if tc.explicit {
				ctx = WithExplicitFileRequest(ctx)
			}
			honor, admit := audiobookFileScanIgnores(ctx, file, libraryRoots)
			scope, _ := audiobookScopeRoot(filepath.Dir(file), libraryRoots)
			scans, err := collectAudiobookRootScans(ctx, 1, []string{scope}, libraryRoots, honor, admit)
			if err != nil {
				t.Fatal(err)
			}
			_, seen, _ := splitAudiobookReconcileRoots(scans)
			books := groupAudiobookCandidates(scans)
			if len(books) != 1 || books[0][0] != filepath.Join(root, "Author/Book") {
				t.Fatalf("books = %v, want only Author/Book", books)
			}
			parts, err := audiobookBookParts(books[0][0], seen)
			if err != nil {
				t.Fatal(err)
			}
			var got []string
			for _, part := range parts {
				rel, _ := filepath.Rel(root, part)
				got = append(got, filepath.ToSlash(rel))
			}
			if !slices.Equal(got, tc.want) {
				t.Errorf("parts = %q, want %q", got, tc.want)
			}
		})
	}
}
