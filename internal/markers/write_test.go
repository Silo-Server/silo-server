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
	if len(payload.Credits.Ranges) != 2 || *payload.Credits.Start != 800 || *payload.Credits.End != 850 {
		t.Fatalf("lost credit occurrences or wrong legacy range: %+v", payload.Credits)
	}
	next := ApplyResult(file, result)
	if *next.IntroStart != 10 || len(next.MarkerSegments) != 3 || *next.CreditsStart != 800 {
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
