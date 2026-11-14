package scanner

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/models"
)

// A subtree scan rebuilds the books a library scan finds in that place:
// discovery stops below a folder holding audio, except at a configured library
// root. Without ffprobe every book fails to parse, so the error names each book
// the scan rebuilt.
func TestScanSubtreeAudiobookMatchesLibraryScanBooks(t *testing.T) {
	for _, tc := range []struct {
		name   string
		ignore string   // root .siloignore content
		nested []string // extra configured roots, relative to the library root
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
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			writeTestFile(t, filepath.Join(root, "Author", "foreword.mp3"), "audio")
			writeTestFile(t, filepath.Join(root, "Author", "Book 1", "01.mp3"), "audio")
			if tc.ignore != "" {
				writeTestFile(t, filepath.Join(root, ".siloignore"), tc.ignore)
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
		explicit bool
		ignore   string
		want     []string
		absent   []string
	}{
		{name: "file change", want: []string{"Author"}, absent: []string{"Book 1"}},
		{name: "admin request for an excluded file", explicit: true, ignore: "Author/foreword.mp3\n", want: []string{"Author", "Author/Book 1"}},
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
			err := s.ScanFile(ctx, filepath.Join(root, "Author", "foreword.mp3"), &models.MediaFolder{ID: 42, Type: "audiobooks", Paths: []string{root}})
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
