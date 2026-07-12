package playback

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCleanupOrphanedTranscodeDirsPreservesPlanScopedActiveDirectory(t *testing.T) {
	root := t.TempDir()
	active := "session-1-plan-abc-generation"
	orphan := "session-2-plan-def-generation"
	for _, name := range []string{active, orphan} {
		if err := os.MkdirAll(filepath.Join(root, name), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	removed, err := CleanupOrphanedTranscodeDirs(root, map[string]struct{}{"session-1": {}}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if removed != 1 {
		t.Fatalf("removed = %d, want 1", removed)
	}
	if _, err := os.Stat(filepath.Join(root, active)); err != nil {
		t.Fatalf("active plan directory was removed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, orphan)); !os.IsNotExist(err) {
		t.Fatalf("orphan directory still exists: %v", err)
	}
}
