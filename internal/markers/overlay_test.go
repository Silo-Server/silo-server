package markers

import (
	"testing"
	"time"

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

// An on-demand online intro is never saved, so it is laid over the stored
// row, while credits local analysis just saved come from storage.
func TestOverlayOnlineKeepsUnsavedOnlineMarkers(t *testing.T) {
	view := overlayFile(overlaySegment{models.MarkerSegmentIntro, 20, 80, models.MarkerSourceOnline})
	stored := overlayFile(overlaySegment{models.MarkerSegmentCredits, 1700, 1780, models.MarkerSourceScanner})

	got := OverlayOnline(stored, view)
	assertOverlaySegment(t, got, models.MarkerSegmentIntro, 20, models.MarkerSourceOnline)
	assertOverlaySegment(t, got, models.MarkerSegmentCredits, 1700, models.MarkerSourceScanner)
	if len(got.MarkerSegments) != 2 {
		t.Fatalf("segments = %+v, want intro and credits", got.MarkerSegments)
	}
	if got.MarkersSource == nil || *got.MarkersSource != models.MarkerSourceOnline {
		t.Fatalf("summary source = %v, want online", got.MarkersSource)
	}
	if stored.IntroStart != nil || len(stored.MarkerSegments) != 1 {
		t.Fatal("OverlayOnline modified stored")
	}
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

// In on_demand mode a stored online marker is left over from stored mode.
// ApplyResult let the fresh lookup replace it in the view, so the overlay
// must not bring the stale range back.
func TestOverlayOnlineReplacesStaleStoredOnlineMarker(t *testing.T) {
	stored := overlayFile(overlaySegment{models.MarkerSegmentIntro, 10, 50, models.MarkerSourceOnline})
	view := ApplyResult(stored, Result{ProviderID: "provider", SourceClass: models.MarkerSourceOnline,
		Markers: []Marker{{Kind: MarkerKindIntro, Start: 20 * time.Second, End: 80 * time.Second, Confidence: 0.9}}})
	if view.IntroStart == nil || *view.IntroStart != 20 {
		t.Fatalf("view intro = %v, want the fresh lookup", view.IntroStart)
	}

	got := OverlayOnline(stored, view)
	assertOverlaySegment(t, got, models.MarkerSegmentIntro, 20, models.MarkerSourceOnline)
	if *got.IntroEnd != 80 {
		t.Fatalf("intro end = %v, want 80", *got.IntroEnd)
	}
}

// A stored legacy marker carries only the file-level source, which
// ApplyResult recomputes in the view. It is not an online marker to lay over
// the stored row, where it may since have been replaced or cleared.
func TestOverlayOnlineSkipsLegacyViewMarkers(t *testing.T) {
	legacyStart, legacyEnd, scanner := 10.0, 50.0, models.MarkerSourceScanner
	legacy := &models.MediaFile{ID: 7, IntroStart: &legacyStart, IntroEnd: &legacyEnd, MarkersSource: &scanner}
	view := ApplyResult(legacy, Result{ProviderID: "provider", SourceClass: models.MarkerSourceOnline,
		Markers: []Marker{{Kind: MarkerKindCredits, Start: 1700 * time.Second, End: 1780 * time.Second, Confidence: 0.9}}})
	if view.MarkersSource == nil || *view.MarkersSource != models.MarkerSourceOnline || view.IntroMarkersSource != nil {
		t.Fatalf("view summary = %v, intro source = %v; want online summary over a legacy intro", view.MarkersSource, view.IntroMarkersSource)
	}

	replaced := overlayFile(overlaySegment{models.MarkerSegmentIntro, 12, 60, models.MarkerSourceScanner})
	assertOverlaySegment(t, OverlayOnline(replaced, view), models.MarkerSegmentIntro, 12, models.MarkerSourceScanner)

	cleared := OverlayOnline(overlayFile(), view)
	if cleared.IntroStart != nil {
		t.Fatalf("a cleared legacy intro came back: %v", *cleared.IntroStart)
	}
	assertOverlaySegment(t, cleared, models.MarkerSegmentCredits, 1700, models.MarkerSourceOnline)
}

// A stored marker written before per-segment provenance ranks by the file's
// shared source.
func TestOverlayOnlineUsesLegacyFileSource(t *testing.T) {
	view := overlayFile(overlaySegment{models.MarkerSegmentIntro, 20, 80, models.MarkerSourceOnline})
	start, end, manual := 5.0, 45.0, models.MarkerSourceManual
	stored := &models.MediaFile{ID: 7, IntroStart: &start, IntroEnd: &end, MarkersSource: &manual}

	if got := OverlayOnline(stored, view); *got.IntroStart != 5 {
		t.Fatalf("intro = %v, want the legacy manual intro", *got.IntroStart)
	}
}

func TestOverlayOnlineWithoutView(t *testing.T) {
	view := overlayFile(overlaySegment{models.MarkerSegmentIntro, 20, 80, models.MarkerSourceOnline})
	view.ID = 8
	stored := overlayFile()
	if got := OverlayOnline(stored, view); got != stored || got.IntroStart != nil {
		t.Fatal("markers from another file were laid over")
	}
	if got := OverlayOnline(stored, nil); got != stored {
		t.Fatal("a nil view changed the stored row")
	}
	if got := OverlayOnline(nil, view); got != nil {
		t.Fatalf("OverlayOnline(nil, view) = %+v, want nil", got)
	}
}

// A refreshed provider that no longer has a marker withdraws its stored one
// from the view. The overlay carries that withdrawal, while a manual marker
// and a local one saved after the view was read stay.
func TestOverlayOnlineCarriesProviderWithdrawals(t *testing.T) {
	provider := "provider"
	stored := overlayFile(
		overlaySegment{models.MarkerSegmentIntro, 20, 80, models.MarkerSourceOnline},
		overlaySegment{models.MarkerSegmentCredits, 1700, 1780, models.MarkerSourceOnline},
	)
	stored.IntroMarkersProvider, stored.CreditsMarkersProvider = &provider, &provider
	view := ApplyResult(stored, Result{RefreshedProviders: []string{provider}})
	if view.IntroStart != nil || view.CreditsStart != nil {
		t.Fatalf("view kept withdrawn markers: intro %v, credits %v", view.IntroStart, view.CreditsStart)
	}

	if got := OverlayOnline(stored, view); got.IntroStart != nil || got.CreditsStart != nil || len(got.MarkerSegments) != 0 || got.MarkersSource != nil {
		t.Fatalf("withdrawn markers came back: intro %v, credits %v, segments %+v", got.IntroStart, got.CreditsStart, got.MarkerSegments)
	}

	later := overlayFile(
		overlaySegment{models.MarkerSegmentIntro, 5, 45, models.MarkerSourceManual},
		overlaySegment{models.MarkerSegmentCredits, 1690, 1770, models.MarkerSourceScanner},
	)
	later.IntroMarkersProvider = &provider
	got := OverlayOnline(later, view)
	assertOverlaySegment(t, got, models.MarkerSegmentIntro, 5, models.MarkerSourceManual)
	assertOverlaySegment(t, got, models.MarkerSegmentCredits, 1690, models.MarkerSourceScanner)
}

func TestOverlayOnlineCannotRestoreManualDeletion(t *testing.T) {
	stored := &models.MediaFile{ID: 42, Duration: 1000, IntroMarkersSource: new(models.MarkerSourceManual)}
	view := &models.MediaFile{ID: 42, Duration: 1000, IntroStart: new(10.0), IntroEnd: new(30.0), IntroMarkersSource: new(models.MarkerSourceOnline)}
	got := OverlayOnline(stored, view)
	if got.IntroStart != nil || got.IntroEnd != nil || len(models.EffectiveMarkerSegments(got)) != 0 {
		t.Fatal("old provider view restored a manually deleted intro")
	}
}
