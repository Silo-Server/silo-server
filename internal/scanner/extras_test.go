package scanner

import (
	"os"
	"path/filepath"
	"testing"
)

func TestClassifyExtraPathScopedBelowSymlinkedSeason(t *testing.T) {
	// The library walk follows directory symlinks, so a show whose only
	// season folder is a symlink still owns its Extras/ dir when a subtree
	// scan covers just that dir.
	root := t.TempDir()
	show := filepath.Join(root, "tv", "Show (2020)")
	extras := filepath.Join(show, "Extras")
	realSeason := filepath.Join(root, "elsewhere", "Season 01")
	writeTestFile(t, filepath.Join(realSeason, "Show S01E01.mkv"), "media")
	writeTestFile(t, filepath.Join(extras, "making-of.mkv"), "media")
	if err := os.Symlink(realSeason, filepath.Join(show, "Season 01")); err != nil {
		t.Skipf("symlinks unsupported: %v", err)
	}

	path := filepath.Join(extras, "making-of.mkv")
	if _, found := partitionExtraPaths([]string{path}, "series", []string{filepath.Join(root, "tv")}, []string{extras}); len(found) != 1 {
		t.Fatalf("extra under a show with a symlinked season was not classified")
	}
}
