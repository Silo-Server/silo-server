package scanner

import (
	"testing"

	"github.com/Silo-Server/silo-server/internal/models"
)

// Ebook/comic files (epub, pdf, cbz, cbr — including manga chapters, which are
// BaseType "ebook") are read directly and never carry ffprobe playback
// metadata. Treating them as needing repair re-ran ffprobe on every detail
// load and never converged.
func TestNeedsCriticalProbeRepair_EbookFileNeverNeedsRepair(t *testing.T) {
	f := &models.MediaFile{
		BaseType:  "ebook",
		Container: "epub",
		// no ProbeUpdatedAt, no audio/video — the unprobed state ebooks ship in
	}
	if NeedsCriticalProbeRepair(f) {
		t.Fatal("an ebook/comic file must not need probe repair")
	}
}

func TestNeedsCriticalProbeRepair_UnprobedNonEbookFileRepairs(t *testing.T) {
	if !NeedsCriticalProbeRepair(&models.MediaFile{}) {
		t.Fatal("an unprobed non-ebook file must need probe repair")
	}
}
