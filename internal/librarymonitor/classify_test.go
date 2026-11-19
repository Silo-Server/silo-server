package librarymonitor

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestWalkTreeSkipsWhatTheScannerSkips(t *testing.T) {
	root := t.TempDir()
	mkdirs(t, root,
		"Movie A",
		"Movie A/Extras",
		"NoMedia/Inside",
		"EmptyIgnore/Inside",
		"InvalidIgnore/Inside",
		"PatternIgnore/Inside",
		"@eaDir/thumbs",
		"Downloading.partial",
		".downloads",
		".tool-workspace/Inside",
		"Target/Deep",
	)
	writeFile(t, filepath.Join(root, "NoMedia", ".nomedia"), "")
	writeFile(t, filepath.Join(root, "EmptyIgnore", ".ignore"), "# only a comment\n\n")
	// Only invalid patterns: the scanner drops them and skips the folder.
	writeFile(t, filepath.Join(root, "InvalidIgnore", ".ignore"), "[\n")
	writeFile(t, filepath.Join(root, "PatternIgnore", ".ignore"), "*.nfo\n")
	// A symlinked directory is followed, and a loop back to the root is cut.
	if err := os.Symlink(filepath.Join(root, "Target"), filepath.Join(root, "Linked")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(root, filepath.Join(root, "Target", "Loop")); err != nil {
		t.Fatal(err)
	}
	// A hidden symlink to a directory is skipped like a hidden directory.
	if err := os.Symlink(filepath.Join(root, "Target"), filepath.Join(root, ".linked")); err != nil {
		t.Fatal(err)
	}

	var entered []string
	err := walkTree(context.Background(), root, walkVisitor{
		enter: func(dir string, link bool) (bool, error) {
			if link {
				entered = append(entered, rel(t, root, dir)+" (link)")
			} else {
				entered = append(entered, rel(t, root, dir))
			}
			return true, nil
		},
		listed: func(dir string, skipped bool) bool {
			if skipped {
				entered = append(entered, "skip "+rel(t, root, dir))
			}
			return skipped
		},
	})
	if err != nil {
		t.Fatalf("walkTree: %v", err)
	}
	want := []string{
		".",
		"EmptyIgnore", "skip EmptyIgnore",
		"InvalidIgnore", "skip InvalidIgnore",
		"Linked (link)", "Linked/Deep",
		"Movie A", "Movie A/Extras",
		"NoMedia", "skip NoMedia",
		"PatternIgnore", "PatternIgnore/Inside",
	}
	if !reflect.DeepEqual(entered, want) {
		t.Fatalf("entered = %q\nwant      %q", entered, want)
	}
}

func mkdirs(t *testing.T, root string, dirs ...string) {
	t.Helper()
	for _, dir := range dirs {
		if err := os.MkdirAll(filepath.Join(root, dir), 0o755); err != nil {
			t.Fatal(err)
		}
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func rel(t *testing.T, root, path string) string {
	t.Helper()
	r, err := filepath.Rel(root, path)
	if err != nil {
		t.Fatal(err)
	}
	return filepath.ToSlash(r)
}
