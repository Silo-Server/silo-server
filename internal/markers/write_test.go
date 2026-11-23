package markers

import (
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/models"
)

func TestApplyResultKeepsOccurrencesAndManualEdits(t *testing.T) {
	file := &models.MediaFile{Duration: 1000, IntroStart: new(10.0), IntroEnd: new(50.0),
		IntroMarkersSource: new(models.MarkerSourceManual)}
	result := Result{ProviderID: "provider", SourceClass: models.MarkerSourceOnline, RefreshedProviders: []string{"provider"},
		Markers: []Marker{
			{Kind: MarkerKindCredits, Start: 900 * time.Second, End: 950 * time.Second, Confidence: 0.9},
			{Kind: MarkerKindIntro, Start: 20 * time.Second, End: 60 * time.Second, Confidence: 0.9},
			{Kind: MarkerKindCredits, Start: 800 * time.Second, End: 850 * time.Second, Confidence: 0.9},
		}}
	payload := BuildUpdatePayload(result)
	// Both credits ranges are placeable in the 1000s movie's tail window (it
	// opens at 12:30). The singular bounds name the end credits, the last
	// occurrence (900..950), not the earliest (800..850).
	if len(payload.Credits.Ranges) != 2 || *payload.Credits.Start != 900 || *payload.Credits.End != 950 {
		t.Fatalf("lost credit occurrences or wrong legacy range: %+v", payload.Credits)
	}
	next := ApplyResult(file, result)
	if *next.IntroStart != 10 || len(next.MarkerSegments) != 3 || *next.CreditsStart != 900 {
		t.Fatalf("incorrect marker projection: %+v", next.MarkerSegments)
	}
	if file.CreditsStart != nil || len(file.MarkerSegments) != 0 {
		t.Fatal("on-demand projection changed the stored file snapshot")
	}
	cleared := ApplyResult(next, Result{RefreshedProviders: []string{"provider"}})
	if cleared.CreditsStart != nil || len(cleared.MarkerSegments) != 1 || *cleared.IntroStart != 10 {
		t.Fatal("provider miss did not clear only that provider's ranges")
	}
}

// A provider can report an early credits block beside the end credits. Every
// occurrence is kept for the file, while the singular bounds name the end
// credits, the occurrence that drives the end-of-file affordance.
func TestApplyResultKeepsEveryCreditsOccurrenceBesideEndCredits(t *testing.T) {
	// A 40-minute episode, whose end-credits window opens at 32:30. The end
	// credits run to the end of the file; an early credits block sits well
	// before the window.
	file := &models.MediaFile{Duration: 2400, EpisodeID: "episode"}
	result := Result{ProviderID: "provider", SourceClass: models.MarkerSourceOnline, RefreshedProviders: []string{"provider"},
		Markers: []Marker{
			{Kind: MarkerKindCredits, Start: 2350 * time.Second, End: 2400 * time.Second, Confidence: 0.9},
			{Kind: MarkerKindCredits, Start: 300 * time.Second, End: 340 * time.Second, Confidence: 0.9},
		}}

	next := ApplyResult(file, result)

	var credits []models.MarkerSegment
	for _, segment := range next.MarkerSegments {
		if segment.Kind == models.MarkerSegmentCredits {
			credits = append(credits, segment)
		}
	}
	if len(credits) != 2 {
		t.Fatalf("credits occurrences = %d, want both kept: %+v", len(credits), next.MarkerSegments)
	}
	if next.CreditsStart == nil || *next.CreditsStart != 2350 || next.CreditsEnd == nil || *next.CreditsEnd != 2400 {
		t.Fatalf("singular credits = %v-%v, want the end credits 2350-2400", next.CreditsStart, next.CreditsEnd)
	}
}

func TestManualDeletionRejectsAutomaticProjection(t *testing.T) {
	file := &models.MediaFile{Duration: 1000, IntroMarkersSource: new(models.MarkerSourceManual)}
	for _, source := range []string{models.MarkerSourceOnline, models.MarkerSourcePlugin, models.MarkerSourceScanner, models.MarkerSourceS3} {
		result := Result{ProviderID: "provider", SourceClass: source, Markers: []Marker{
			{Kind: MarkerKindIntro, Start: 10 * time.Second, End: 30 * time.Second},
			{Kind: MarkerKindRecap, Start: 40 * time.Second, End: 60 * time.Second},
		}}
		next := ApplyResult(file, result)
		if next.IntroStart != nil || next.IntroEnd != nil || next.RecapStart == nil || *next.RecapStart != 40 {
			t.Fatalf("%s restored deleted intro or suppressed unrelated recap: %+v", source, next.MarkerSegments)
		}
		if file.RecapStart != nil {
			t.Fatal("projection mutated stored snapshot")
		}
	}
	if !CanWriteMarkerUpdate(SegmentPayload{Source: models.MarkerSourceManual}, SegmentPayload{Source: models.MarkerSourceManual, Start: new(12.0), End: new(34.0)}) {
		t.Fatal("explicit manual replacement must be allowed")
	}
}

func TestApplyResultKeepsInBoundsCreditsBesideAnOutOfBoundsOccurrence(t *testing.T) {
	// An occurrence past the end of the file cannot be stored, and the whole
	// update is rejected for it: the occurrences that do belong to the file must
	// still be written.
	file := &models.MediaFile{Duration: 2400, EpisodeID: "episode"}
	result := Result{ProviderID: "provider", SourceClass: models.MarkerSourceOnline,
		Markers: []Marker{
			{Kind: MarkerKindCredits, Start: 2350 * time.Second, End: 2400 * time.Second, Confidence: 0.9},
			// This occurrence starts inside the end-credits window and is short
			// enough to pass the placement rule on its own, so only the
			// bounds check rejects it. Upstream rejected the whole update for
			// it; the in-bounds end credits must still be written.
			{Kind: MarkerKindCredits, Start: 2360 * time.Second, End: 2500 * time.Second, Confidence: 0.9},
		}}

	next := ApplyResult(file, result)

	if next.CreditsStart == nil || *next.CreditsStart != 2350 {
		t.Fatalf("credits start = %v, want the in-bounds end credits", next.CreditsStart)
	}
	for _, segment := range next.MarkerSegments {
		if segment.Kind == models.MarkerSegmentCredits && segment.EndSeconds > 2401 {
			t.Fatalf("stored an out-of-bounds occurrence: %+v", segment)
		}
	}
}

func TestApplyResultTreatsCreditsBeforeTheIntroEndAsUnplaceable(t *testing.T) {
	// The placement rule, not the tail window alone, decides whether a credits
	// occurrence can belong to the file. One that starts inside the window but
	// before the intro ends is not an end credits, so a set made only of such
	// occurrences is a withdrawal rather than a set of ranges.
	file := &models.MediaFile{Duration: 2400, EpisodeID: "episode", IntroEnd: new(2390.0)}
	result := Result{ProviderID: "provider", SourceClass: models.MarkerSourceOnline,
		Markers: []Marker{
			{Kind: MarkerKindCredits, Start: 2370 * time.Second, End: 2400 * time.Second, Confidence: 0.9},
		}}

	next := ApplyResult(file, result)

	if next.CreditsStart != nil {
		t.Fatalf("credits start = %v, want a withdrawal", *next.CreditsStart)
	}
	for _, segment := range next.MarkerSegments {
		if segment.Kind == models.MarkerSegmentCredits {
			t.Fatalf("kept an unplaceable credits occurrence: %+v", segment)
		}
	}
}
