package scanner

import (
	"path/filepath"

	"testing"
)

// A folder that cannot be read says nothing about the extra's image.
func TestExtraStillDirsUnreadableFolder(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "gone")
	if got, _, ok := newExtraStillDirs(nil).find(filepath.Join(missing, "Making Of.mkv")); ok || got != "" {
		t.Fatalf("find = %q, %v; want unknown", got, ok)
	}
}
