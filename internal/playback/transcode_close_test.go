package playback

import (
	"os"
	"path/filepath"
	"testing"
)

// CloseProcess must leave the output directory intact so a session sharing the
// same directory (e.g. a concurrent reconstruct winner) keeps serving its
// segments and init.mp4. Close, by contrast, removes the directory.
func TestCloseProcessKeepsOutputDir(t *testing.T) {
	dir := t.TempDir()
	seg := filepath.Join(dir, "init.mp4")
	if err := os.WriteFile(seg, []byte("data"), 0o644); err != nil {
		t.Fatalf("seed segment: %v", err)
	}

	s := &TranscodeSession{outputDir: dir}
	if err := s.CloseProcess(); err != nil {
		t.Fatalf("CloseProcess: %v", err)
	}
	if _, err := os.Stat(seg); err != nil {
		t.Fatalf("CloseProcess must not remove output dir, but segment is gone: %v", err)
	}

	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("Close must remove output dir, stat err = %v", err)
	}
}
