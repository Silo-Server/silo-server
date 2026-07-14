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

// An active session owns a dir only when the name is the session id exactly or
// the id followed by the generation separator ('-', see transportGenerationV3).
// A foreign session whose id merely shares the active id as a raw string
// prefix must not have its dirs retained.
func TestCleanupOrphanedTranscodeDirsRequiresSeparatorBoundary(t *testing.T) {
	root := t.TempDir()
	keep := []string{"session-1", "session-1-plan-abc"}
	reap := []string{"session-10", "session-10-plan-def", "session-1extra"}
	for _, name := range append(append([]string{}, keep...), reap...) {
		if err := os.MkdirAll(filepath.Join(root, name), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	removed, err := CleanupOrphanedTranscodeDirs(root, map[string]struct{}{"session-1": {}}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if removed != len(reap) {
		t.Fatalf("removed = %d, want %d", removed, len(reap))
	}
	for _, name := range keep {
		if _, err := os.Stat(filepath.Join(root, name)); err != nil {
			t.Fatalf("active session directory %q was removed: %v", name, err)
		}
	}
	for _, name := range reap {
		if _, err := os.Stat(filepath.Join(root, name)); !os.IsNotExist(err) {
			t.Fatalf("foreign directory %q still exists: %v", name, err)
		}
	}
}
