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
			{Kind: MarkerKindCredits, Start: 100 * time.Second, End: 120 * time.Second, Confidence: 0.7, ProviderID: "other", Algorithm: "other:v1"},
		},
	}
	payload := BuildUpdatePayload(result)
	if payload.Intro.Provider == nil || *payload.Intro.Provider != "introdb" {
		t.Errorf("intro provider = %v, want introdb", payload.Intro.Provider)
	}
	if payload.Credits.Provider == nil || *payload.Credits.Provider != "other" {
		t.Errorf("credits provider = %v, want other", payload.Credits.Provider)
	}
	if payload.Credits.Algorithm != "other:v1" {
		t.Errorf("credits algorithm = %q, want other:v1", payload.Credits.Algorithm)
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
