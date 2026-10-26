package scanner

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/fspath"
)

func TestExtraStillDirsFind(t *testing.T) {
	video := func(dir string) string { return filepath.Join(dir, "Making Of.mkv") }
	tests := []struct {
		name  string
		files map[string]string // name -> content; "->target" makes a symlink
		want  string
	}{
		{name: "no image", files: map[string]string{"Making Of.mkv": "v"}},
		{name: "plain name", files: map[string]string{"Making Of.jpg": "img"}, want: "Making Of.jpg"},
		{
			name:  "thumb suffix beats plain name",
			files: map[string]string{"Making Of.jpg": "img", "Making Of-thumb.png": "img"},
			want:  "Making Of-thumb.png",
		},
		{
			name:  "extension order within a suffix",
			files: map[string]string{"Making Of.webp": "img", "Making Of.png": "img"},
			want:  "Making Of.png",
		},
		{name: "case-insensitive", files: map[string]string{"making of-THUMB.JPG": "img"}, want: "making of-THUMB.JPG"},
		{name: "another extra's image", files: map[string]string{"Making Of 2.jpg": "img", "Making Of-poster.jpg": "img"}},
		{name: "empty file skipped", files: map[string]string{"Making Of-thumb.jpg": "", "Making Of.jpg": "img"}, want: "Making Of.jpg"},
		{name: "symlink skipped", files: map[string]string{"real.jpg": "img", "Making Of-thumb.jpg": "->real.jpg"}},
		{name: "non-image extension", files: map[string]string{"Making Of.nfo": "x", "Making Of.bmp": "x"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			for name, content := range tt.files {
				path := filepath.Join(dir, name)
				if target, ok := strings.CutPrefix(content, "->"); ok {
					if err := os.Symlink(filepath.Join(dir, target), path); err != nil {
						t.Fatal(err)
					}
					continue
				}
				writeTestFile(t, path, content)
			}
			got, info, ok := newExtraStillDirs(nil).find(video(dir))
			if !ok {
				t.Fatal("find could not read the folder")
			}
			want := ""
			if tt.want != "" {
				want = filepath.Join(dir, tt.want)
			}
			if got != want {
				t.Fatalf("find = %q, want %q", got, want)
			}
			if (info != nil) != (want != "") {
				t.Fatalf("info = %v for path %q", info, got)
			}
		})
	}
}

// Two spellings of one name can coexist on a case-sensitive filesystem; the
// exact spelling wins.
func TestExtraStillDirsFindPrefersExactSpelling(t *testing.T) {
	dir := t.TempDir()
	writeTestFile(t, filepath.Join(dir, "MAKING OF.jpg"), "upper")
	writeTestFile(t, filepath.Join(dir, "Making Of.jpg"), "exact")
	if _, err := os.Stat(filepath.Join(dir, "making of.JPG")); err == nil {
		t.Skip("case-insensitive filesystem")
	}
	got, _, _ := newExtraStillDirs(nil).find(filepath.Join(dir, "Making Of.mkv"))
	if want := filepath.Join(dir, "Making Of.jpg"); got != want {
		t.Fatalf("find = %q, want %q", got, want)
	}
}

// The walk's listing stands in for reading the folder again.
func TestExtraStillDirsUsesTheWalkListing(t *testing.T) {
	dir := t.TempDir()
	writeTestFile(t, filepath.Join(dir, "Making Of-thumb.jpg"), "listed")
	writeTestFile(t, filepath.Join(dir, "Making Of.jpg"), "not listed")
	walked := SidecarListing{dir: {"Making Of-thumb.jpg", "Making Of.nfo"}}
	dirs := newExtraStillDirs(walked)
	if got, _, ok := dirs.find(filepath.Join(dir, "Making Of.mkv")); !ok || got != filepath.Join(dir, "Making Of-thumb.jpg") {
		t.Fatalf("find = %q, %v; want the listed image", got, ok)
	}
	walked[dir] = []string{"Making Of.nfo"}
	if got, _, ok := newExtraStillDirs(walked).find(filepath.Join(dir, "Making Of.mkv")); !ok || got != "" {
		t.Fatalf("find = %q, %v; want no image when the listing has none", got, ok)
	}
}

// A folder that cannot be read says nothing about the extra's image.
func TestExtraStillDirsUnreadableFolder(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "gone")
	if got, _, ok := newExtraStillDirs(nil).find(filepath.Join(missing, "Making Of.mkv")); ok || got != "" {
		t.Fatalf("find = %q, %v; want unknown", got, ok)
	}
}

type recordingStillCacher struct {
	scannerImageCacher
	data []byte
}

func (c *recordingStillCacher) CacheExtraStill(_ context.Context, data []byte, _ string) (string, string, error) {
	c.data = data
	return "stored", "", nil
}

// An extra whose folder and name are not UTF-8 comes from the walk with an
// fspath-encoded path; its image is still found and read.
func TestExtraStillNonUTF8Name(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "B\xe5rd")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Skipf("file system rejects non-UTF-8 names: %v", err)
	}
	writeTestFile(t, filepath.Join(dir, "P\xe5 tur-thumb.jpg"), "img")
	video := fspath.Encode(filepath.Join(dir, "P\xe5 tur.mkv"))
	got, info, ok := newExtraStillDirs(nil).find(video)
	if want := fspath.Encode(filepath.Join(dir, "P\xe5 tur-thumb.jpg")); !ok || got != want || info == nil {
		t.Fatalf("find = %q, %v, %v; want %q", got, info, ok, want)
	}
	cacher := &recordingStillCacher{}
	s := &Scanner{imageCacher: cacher}
	if _, _, err := s.cacheExtraStill(t.Context(), "extra", got, info); err != nil || string(cacher.data) != "img" {
		t.Fatalf("cacheExtraStill = %v, data %q", err, cacher.data)
	}
}
