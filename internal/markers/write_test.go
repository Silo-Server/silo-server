package markers

import (
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/models"
)

func TestBuildUpdatePayloadAggregatesConfidence(t *testing.T) {
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

	if payload.Confidence == nil {
		t.Fatal("confidence should be populated")
	}
	if *payload.Confidence != 0.9 {
		t.Errorf("confidence = %v, want 0.9 (max across markers)", *payload.Confidence)
	}
	if payload.Algorithm != "introdb:v3" {
		t.Errorf("algorithm = %q, want introdb:v3", payload.Algorithm)
	}
	if payload.IntroStart == nil || *payload.IntroStart != 10 {
		t.Errorf("intro start = %v, want 10", payload.IntroStart)
	}
	if payload.RecapStart == nil || *payload.RecapStart != 0 {
		t.Errorf("recap start = %v, want 0", payload.RecapStart)
	}
	if payload.PreviewStart != nil {
		t.Errorf("preview start = %v, want nil (no preview marker)", payload.PreviewStart)
	}
}

func TestBuildUpdatePayloadFallsBackAlgorithm(t *testing.T) {
	result := Result{
		ProviderID:  "custom",
		SourceClass: models.MarkerSourceOnline,
		Markers:     []Marker{{Kind: MarkerKindIntro, Start: 0, End: 10 * time.Second}},
	}
	payload := BuildUpdatePayload(result)
	if payload.Algorithm != "external:online" {
		t.Errorf("algorithm = %q, want external:online fallback", payload.Algorithm)
	}
}

func TestCanWriteMarkerEqualPriorityRequiresHigherConfidence(t *testing.T) {
	existing := models.MarkerSourceOnline
	low, high := 0.5, 0.9

	if CanWriteMarker(&existing, &high, models.MarkerSourceOnline, &low) {
		t.Error("equal priority with lower new confidence should not write")
	}
	if !CanWriteMarker(&existing, &low, models.MarkerSourceOnline, &high) {
		t.Error("equal priority with strictly higher new confidence should write")
	}
	if CanWriteMarker(&existing, &high, models.MarkerSourceOnline, &high) {
		t.Error("equal priority with equal confidence should not write")
	}
}

func TestCanWriteMarkerHigherPriorityWinsRegardless(t *testing.T) {
	existing := models.MarkerSourceScanner
	if !CanWriteMarker(&existing, nil, models.MarkerSourceOnline, nil) {
		t.Error("higher priority should win even without confidence")
	}
	online := models.MarkerSourceOnline
	if CanWriteMarker(&online, nil, models.MarkerSourceScanner, nil) {
		t.Error("lower priority should not overwrite higher")
	}
}
