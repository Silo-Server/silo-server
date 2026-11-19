package markers

import (
	"testing"

	"github.com/Silo-Server/silo-server/internal/models"
)

type overlaySegment struct {
	kind       string
	start, end float64
	source     string
}

func overlayFile(segments ...overlaySegment) *models.MediaFile {
	f := &models.MediaFile{ID: 7}
	fields := fileSegmentFields(f)
	for _, s := range segments {
		for _, field := range fields {
			if field.kind != s.kind {
				continue
			}
			start, end, source := s.start, s.end, s.source
			*field.start, *field.end, *field.source = &start, &end, &source
		}
		f.MarkerSegments = append(f.MarkerSegments, models.MarkerSegment{Kind: s.kind, StartSeconds: s.start, EndSeconds: s.end})
	}
	return f
}

func assertOverlaySegment(t *testing.T, file *models.MediaFile, kind string, start float64, source string) {
	t.Helper()
	for _, field := range fileSegmentFields(file) {
		if field.kind != kind {
			continue
		}
		if *field.start == nil || **field.start != start || *field.source == nil || **field.source != source {
			t.Fatalf("%s = %v from %v, want %v from %s", kind, *field.start, *field.source, start, source)
		}
	}
	for _, segment := range models.EffectiveMarkerSegments(file) {
		if segment.Kind == kind {
			if segment.StartSeconds != start {
				t.Fatalf("%s segment starts at %v, want %v", kind, segment.StartSeconds, start)
			}
			return
		}
	}
	t.Fatalf("no %s segment in %+v", kind, file.MarkerSegments)
}

// A manual marker wins, and only provider markers are laid over, so a
// detector marker cleared after the view was read stays cleared. An online
// marker outranks a local one, and a fresh provider marker replaces one left
// over from stored mode, as it did when the view was built.
func TestOverlayOnlineKeepsStoredPrecedence(t *testing.T) {
	view := overlayFile(
		overlaySegment{models.MarkerSegmentIntro, 20, 80, models.MarkerSourceOnline},
		overlaySegment{models.MarkerSegmentCredits, 1700, 1780, models.MarkerSourceOnline},
		overlaySegment{models.MarkerSegmentRecap, 0, 15, models.MarkerSourcePlugin},
		overlaySegment{models.MarkerSegmentPreview, 1790, 1800, models.MarkerSourceScanner},
	)
	stored := overlayFile(
		overlaySegment{models.MarkerSegmentIntro, 5, 45, models.MarkerSourceManual},
		overlaySegment{models.MarkerSegmentCredits, 1690, 1770, models.MarkerSourceScanner},
		overlaySegment{models.MarkerSegmentRecap, 0, 12, models.MarkerSourceOnline},
	)

	got := OverlayOnline(stored, view)
	assertOverlaySegment(t, got, models.MarkerSegmentIntro, 5, models.MarkerSourceManual)
	assertOverlaySegment(t, got, models.MarkerSegmentCredits, 1700, models.MarkerSourceOnline)
	assertOverlaySegment(t, got, models.MarkerSegmentRecap, 0, models.MarkerSourcePlugin)
	if got.PreviewStart != nil {
		t.Fatalf("a cleared detector preview came back: %v", *got.PreviewStart)
	}
	if got.RecapEnd == nil || *got.RecapEnd != 15 {
		t.Fatalf("recap end = %v, want the fresh plugin recap", got.RecapEnd)
	}
}

func TestOverlayOnlineCannotRestoreManualDeletion(t *testing.T) {
	stored := &models.MediaFile{ID: 42, Duration: 1000, IntroMarkersSource: new(models.MarkerSourceManual)}
	view := &models.MediaFile{ID: 42, Duration: 1000, IntroStart: new(10.0), IntroEnd: new(30.0), IntroMarkersSource: new(models.MarkerSourceOnline)}
	got := OverlayOnline(stored, view)
	if got.IntroStart != nil || got.IntroEnd != nil || len(models.EffectiveMarkerSegments(got)) != 0 {
		t.Fatal("old provider view restored a manually deleted intro")
	}
}
