package api

import (
	"os"
	"path/filepath"
	"testing"
)

// TestCheckLibraryFolder: a folder this server can open is accepted, and a
// missing path, a file, or a relative path is not.
func TestCheckLibraryFolder(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "file")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := checkLibraryFolder(dir); err != nil {
		t.Fatalf("a folder: %v", err)
	}
	for _, path := range []string{filepath.Join(dir, "missing"), file, "relative"} {
		if checkLibraryFolder(path) == nil {
			t.Errorf("%s was accepted", path)
		}
	}
}
