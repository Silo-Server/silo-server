package scanner

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/Silo-Server/silo-server/internal/models"
)

// TestScopedScansRejectPathsOutsideLibrary checks that ScanFile and
// ScanSubtree refuse a path that is not under the library's roots before
// touching the catalog or the filesystem.
func TestScopedScansRejectPathsOutsideLibrary(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "library")
	outside := filepath.Join(base, "library-other")
	for _, dir := range []string{root, outside} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	folder := &models.MediaFolder{ID: 7, Type: "movies", Paths: []string{root}}

	for _, path := range []string{
		filepath.Join(outside, "Movie.mkv"),
		filepath.Join(root, "..", "library-other", "Movie.mkv"),
		filepath.Join(base, "Movie.mkv"),
		"Movie.mkv",
	} {
		if err := (&Scanner{}).ScanFile(context.Background(), path, folder); !errors.Is(err, ErrPathOutsideLibrary) {
			t.Errorf("ScanFile(%q) error = %v, want ErrPathOutsideLibrary", path, err)
		}
		if _, err := (&Scanner{}).ScanSubtree(context.Background(), folder, filepath.Dir(path)); !errors.Is(err, ErrPathOutsideLibrary) {
			t.Errorf("ScanSubtree(%q) error = %v, want ErrPathOutsideLibrary", filepath.Dir(path), err)
		}
	}

	if err := (&Scanner{}).ScanFile(context.Background(), filepath.Join(root, "Movie.mkv"), &models.MediaFolder{ID: 7, Type: "movies"}); !errors.Is(err, ErrPathOutsideLibrary) {
		t.Errorf("ScanFile with no library roots error = %v, want ErrPathOutsideLibrary", err)
	}
}

// TestScopedScansAcceptPathsInsideLibrary checks that an in-root path passes
// the check and reaches the library's scan pipeline (the ebook pipeline here,
// which fails later on the fake file with its own aggregate error).
func TestScopedScansAcceptPathsInsideLibrary(t *testing.T) {
	root := t.TempDir()
	bookDir := filepath.Join(root, "Author")
	if err := os.MkdirAll(bookDir, 0o755); err != nil {
		t.Fatal(err)
	}
	filePath := filepath.Join(bookDir, "book.epub")
	if err := os.WriteFile(filePath, []byte("not a real epub"), 0o644); err != nil {
		t.Fatal(err)
	}
	folder := &models.MediaFolder{ID: 44, Type: "ebooks", Paths: []string{root + string(filepath.Separator)}}

	if err := (&Scanner{}).ScanFile(context.Background(), filePath, folder); errors.Is(err, ErrPathOutsideLibrary) {
		t.Fatalf("ScanFile(in-root file) error = %v, want the ebook pipeline", err)
	}
	for _, subtree := range []string{root, bookDir} {
		if _, err := (&Scanner{}).ScanSubtree(context.Background(), folder, subtree); errors.Is(err, ErrPathOutsideLibrary) {
			t.Fatalf("ScanSubtree(%q) error = %v, want the ebook pipeline", subtree, err)
		}
	}
}

// TestRequirePathInLibrarySymlinkedRoot covers a library root that is a
// symlink: the logical paths the scanner records are accepted, while the
// physical path into the root, an outside alias that resolves into it, and the
// target of a symlink inside the root are refused.
func TestRequirePathInLibrarySymlinkedRoot(t *testing.T) {
	base := t.TempDir()
	physicalRoot := filepath.Join(base, "real")
	logicalRoot := filepath.Join(base, "library")
	elsewhere := filepath.Join(base, "elsewhere")
	movieDir := filepath.Join(physicalRoot, "Movie (2020)")
	for _, dir := range []string{movieDir, elsewhere} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	physicalFile := filepath.Join(movieDir, "Movie (2020).mkv")
	outsideFile := filepath.Join(elsewhere, "Other.mkv")
	for _, path := range []string{physicalFile, outsideFile} {
		if err := os.WriteFile(path, []byte("video"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(physicalRoot, logicalRoot); err != nil {
		t.Skipf("symlinks not supported on this platform: %v", err)
	}
	if err := os.Symlink(elsewhere, filepath.Join(physicalRoot, "Linked")); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(base, "alias")
	if err := os.Symlink(logicalRoot, alias); err != nil {
		t.Fatal(err)
	}
	folder := &models.MediaFolder{ID: 9, Type: "movies", Paths: []string{logicalRoot}}

	for _, path := range []string{
		logicalRoot,
		// The walk records files below an inner symlink by their logical path.
		filepath.Join(logicalRoot, "Linked", "Other.mkv"),
		filepath.Join(logicalRoot, "Movie (2020)"),
		filepath.Join(logicalRoot, "Movie (2020)", "Movie (2020).mkv"),
		// A vanished file under the root still matches lexically.
		filepath.Join(logicalRoot, "Gone (2019)", "Gone (2019).mkv"),
	} {
		if err := requirePathInLibrary(path, folder); err != nil {
			t.Errorf("requirePathInLibrary(%q) = %v, want nil", path, err)
		}
	}
	for _, path := range []string{
		outsideFile, elsewhere, base,
		movieDir, physicalFile,
		filepath.Join(alias, "Movie (2020)", "Movie (2020).mkv"),
	} {
		if err := requirePathInLibrary(path, folder); !errors.Is(err, ErrPathOutsideLibrary) {
			t.Errorf("requirePathInLibrary(%q) = %v, want ErrPathOutsideLibrary", path, err)
		}
	}
}
