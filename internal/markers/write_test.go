package markers

import (
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/models"
)

func TestBuildUpdatePayloadPreservesPerSegmentConfidence(t *testing.T) {
	result := Result{
		ProviderID:  "introdb",
		SourceClass: models.MarkerSourceOnline,
		Algorithm:   "introdb:v3",
		Markers: []Marker{
			{Kind: MarkerKindIntro, Start: 10 * time.Second, End: 60 * time.Second, Confidence: 0.7},
			{Kind: MarkerKindCredits, Start: 1500 * time.Second, End: 1790 * time.Second, Confidence: 0.9},
			{Kind: MarkerKindRecap, Start: 0, End: 30 * time.Second, Confidence: 0.5},
		},
	}

	payload := BuildUpdatePayload(result)

	if payload.Intro.Confidence == nil || *payload.Intro.Confidence != 0.7 {
		t.Errorf("intro confidence = %v, want 0.7", payload.Intro.Confidence)
	}
	if payload.Credits.Confidence == nil || *payload.Credits.Confidence != 0.9 {
		t.Errorf("credits confidence = %v, want 0.9", payload.Credits.Confidence)
	}
	if payload.Recap.Confidence == nil || *payload.Recap.Confidence != 0.5 {
		t.Errorf("recap confidence = %v, want 0.5", payload.Recap.Confidence)
	}
	if sc := payload.SummaryConfidence(); sc == nil || *sc != 0.9 {
		t.Errorf("summary confidence = %v, want 0.9 (max)", sc)
	}
	if payload.Intro.Algorithm != "introdb:v3" {
		t.Errorf("intro algorithm = %q, want introdb:v3", payload.Intro.Algorithm)
	}
	if payload.Intro.Start == nil || *payload.Intro.Start != 10 {
		t.Errorf("intro start = %v, want 10", payload.Intro.Start)
	}
	if payload.Recap.Start == nil || *payload.Recap.Start != 0 {
		t.Errorf("recap start = %v, want 0", payload.Recap.Start)
	}
	if payload.Preview.Present() {
		t.Errorf("preview should be absent (no preview marker)")
	}
}

func TestBuildUpdatePayloadPerMarkerProvider(t *testing.T) {
	// A merged result: each marker carries its own provider/algorithm.
	result := Result{
		SourceClass: models.MarkerSourceOnline,
		Markers: []Marker{
			{Kind: MarkerKindIntro, Start: 0, End: 30 * time.Second, Confidence: 0.8, ProviderID: "introdb", Algorithm: "introdb:v3"},
			{Kind: MarkerKindCredits, Start: 100 * time.Second, End: 120 * time.Second, Confidence: 0.7, SourceClass: models.MarkerSourcePlugin, ProviderID: "plugin:1:markers", Algorithm: "other:v1"},
		},
	}
	payload := BuildUpdatePayload(result)
	if payload.Intro.Provider == nil || *payload.Intro.Provider != "introdb" {
		t.Errorf("intro provider = %v, want introdb", payload.Intro.Provider)
	}
	if payload.Credits.Provider == nil || *payload.Credits.Provider != "plugin:1:markers" {
		t.Errorf("credits provider = %v, want plugin:1:markers", payload.Credits.Provider)
	}
	if payload.Credits.Source != models.MarkerSourcePlugin {
		t.Errorf("credits source = %q, want plugin", payload.Credits.Source)
	}
	if payload.Credits.Algorithm != "other:v1" {
		t.Errorf("credits algorithm = %q, want other:v1", payload.Credits.Algorithm)
	}
	if payload.Intro.Source != models.MarkerSourceOnline {
		t.Errorf("intro source = %q, want online fallback", payload.Intro.Source)
	}
}

func TestBuildUpdatePayloadFallsBackAlgorithmAndProvider(t *testing.T) {
	result := Result{
		ProviderID:  "custom",
		SourceClass: models.MarkerSourceOnline,
		Markers:     []Marker{{Kind: MarkerKindIntro, Start: 0, End: 10 * time.Second}},
	}
	payload := BuildUpdatePayload(result)
	if payload.Intro.Algorithm != "external:online" {
		t.Errorf("intro algorithm = %q, want external:online fallback", payload.Intro.Algorithm)
	}
	if payload.Intro.Provider == nil || *payload.Intro.Provider != "custom" {
		t.Errorf("intro provider = %v, want custom (result-level fallback)", payload.Intro.Provider)
	}
}

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

func TestCanWriteMarkerUpdateAcceptsSelectedProviderRefresh(t *testing.T) {
	existing := SegmentPayload{Start: new(10.0), End: new(20.0), Source: models.MarkerSourceOnline,
		Provider: new("old"), Confidence: new(0.9)}
	incoming := existing
	incoming.Start = new(12.0)
	if !CanWriteMarkerUpdate(existing, incoming) {
		t.Fatal("same provider correction at unchanged confidence was rejected")
	}
	incoming.Provider, incoming.Confidence = new("preferred"), new(0.8)
	if !CanWriteMarkerUpdate(existing, incoming) {
		t.Fatal("registry's selected provider was overridden by incomparable confidence")
	}
	existing.Source = models.MarkerSourceManual
	if CanWriteMarkerUpdate(existing, incoming) {
		t.Fatal("online result replaced a manual edit")
	}
}

func TestCanWriteMarkerUpdatePreservesSourcePriority(t *testing.T) {
	scanner := SegmentPayload{Start: new(10.0), End: new(20.0), Source: models.MarkerSourceScanner}
	online := scanner
	online.Source = models.MarkerSourceOnline
	manual := scanner
	manual.Source = models.MarkerSourceManual
	manual.Confidence = new(1.0)
	if !CanWriteMarkerUpdate(scanner, online) || CanWriteMarkerUpdate(online, scanner) {
		t.Fatal("online/scanner priority is incorrect")
	}
	if !CanWriteMarkerUpdate(online, manual) || CanWriteMarkerUpdate(manual, online) {
		t.Fatal("manual priority is incorrect")
	}
	if !CanWriteMarkerUpdate(manual, manual) {
		t.Fatal("manual edits must allow corrections at unchanged confidence")
	}
}
