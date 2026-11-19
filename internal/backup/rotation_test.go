package backup

import (
	"testing"
	"time"
)

// TestPlanSweepDeletesOnlyFoldersSeenWithoutAManifest: the sweep deletes a
// folder only when a complete listing showed no manifest object in it and
// it is over a day old. A folder whose manifest could not be read, or did
// not verify, may be a complete backup and is kept.
func TestPlanSweepDeletesOnlyFoldersSeenWithoutAManifest(t *testing.T) {
	now := time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)
	old, recent := now.Add(-25*time.Hour), now.Add(-time.Hour)
	for _, tt := range []struct {
		name   string
		folder folder
		swept  bool
	}{
		{"an old folder with no manifest", folder{at: old, newest: old, noManifest: true}, true},
		{"an old folder written to recently", folder{at: old, newest: recent, noManifest: true}, false},
		{"a recent folder with no manifest", folder{at: recent, newest: recent, noManifest: true}, false},
		{"a complete backup", folder{at: old, newest: old, manifest: &Manifest{}}, false},
		{"a manifest that could not be read", folder{at: old, newest: old, unreadable: true}, false},
		{"a manifest that did not verify", folder{at: old, newest: old, tampered: true}, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			tt.folder.id = "folder"
			if got := planSweep([]folder{tt.folder}, now); (len(got) == 1) != tt.swept {
				t.Fatalf("planSweep = %v, want swept %v", got, tt.swept)
			}
		})
	}
}
