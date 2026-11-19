package scanner

import (
	"os"
	"path/filepath"
	"testing"
)

func TestFindSidecarAudiobookCoverSkipsSymlinks(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "secret.jpg")
	if err := os.WriteFile(target, []byte("not-a-cover"), 0o644); err != nil {
		t.Fatalf("write target: %v", err)
	}
	if err := os.Symlink(target, filepath.Join(dir, "cover.jpg")); err != nil {
		t.Fatalf("symlink cover: %v", err)
	}

	data, path, err := findSidecarAudiobookCover(dir)
	if err != nil {
		t.Fatalf("findSidecarAudiobookCover: %v", err)
	}
	if data != nil || path != "" {
		t.Fatalf("findSidecarAudiobookCover returned data %q path %q, want no cover", string(data), path)
	}
}
