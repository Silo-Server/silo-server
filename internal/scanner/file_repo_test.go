package scanner

import (
	"testing"

	"github.com/Silo-Server/silo-server/internal/models"
)

func TestNextSharedMarkerAttributionPreservesHigherPriorityConfidence(t *testing.T) {
	existingSource := models.MarkerSourceManual
	existingConfidence := 1.0
	updateConfidence := 0.5

	nextSource, nextConfidence := nextSharedMarkerAttribution(&existingSource, &existingConfidence, MarkerUpdate{
		MarkersSource:     models.MarkerSourceScanner,
		MarkersConfidence: &updateConfidence,
	}, true)

	if nextSource == nil || *nextSource != models.MarkerSourceManual {
		t.Fatalf("next source = %v, want manual", nextSource)
	}
	if nextConfidence == nil || *nextConfidence != existingConfidence {
		t.Fatalf("next confidence = %v, want %v", nextConfidence, existingConfidence)
	}
}

func TestNextSharedMarkerAttributionUpdatesSamePriorityConfidence(t *testing.T) {
	existingSource := models.MarkerSourceScanner
	existingConfidence := 0.5
	updateConfidence := 0.8

	nextSource, nextConfidence := nextSharedMarkerAttribution(&existingSource, &existingConfidence, MarkerUpdate{
		MarkersSource:     models.MarkerSourceScanner,
		MarkersConfidence: &updateConfidence,
	}, true)

	if nextSource == nil || *nextSource != models.MarkerSourceScanner {
		t.Fatalf("next source = %v, want scanner", nextSource)
	}
	if nextConfidence == nil || *nextConfidence != updateConfidence {
		t.Fatalf("next confidence = %v, want %v", nextConfidence, updateConfidence)
	}
}

func TestNextSharedMarkerAttributionPromotesHigherPrioritySource(t *testing.T) {
	existingSource := models.MarkerSourceScanner
	existingConfidence := 0.5
	updateConfidence := 0.9

	nextSource, nextConfidence := nextSharedMarkerAttribution(&existingSource, &existingConfidence, MarkerUpdate{
		MarkersSource:     models.MarkerSourceManual,
		MarkersConfidence: &updateConfidence,
	}, true)

	if nextSource == nil || *nextSource != models.MarkerSourceManual {
		t.Fatalf("next source = %v, want manual", nextSource)
	}
	if nextConfidence == nil || *nextConfidence != updateConfidence {
		t.Fatalf("next confidence = %v, want %v", nextConfidence, updateConfidence)
	}
}

func TestNextSharedMarkerAttributionDoesNotDowngradeConfidenceWhenMarkerRejected(t *testing.T) {
	existingSource := models.MarkerSourceScanner
	existingConfidence := 0.9
	updateConfidence := 0.4

	nextSource, nextConfidence := nextSharedMarkerAttribution(&existingSource, &existingConfidence, MarkerUpdate{
		MarkersSource:     models.MarkerSourceScanner,
		MarkersConfidence: &updateConfidence,
	}, false)

	if nextSource == nil || *nextSource != models.MarkerSourceScanner {
		t.Fatalf("next source = %v, want scanner", nextSource)
	}
	if nextConfidence == nil || *nextConfidence != existingConfidence {
		t.Fatalf("next confidence = %v, want %v", nextConfidence, existingConfidence)
	}
}

func TestRecomputeSharedMarkerAttributionDropsClearedManualSegment(t *testing.T) {
	manual := models.MarkerSourceManual
	scannerSource := models.MarkerSourceScanner
	manualConfidence := 1.0
	scannerConfidence := 0.8

	nextSource, nextConfidence := recomputeSharedMarkerAttribution(
		&manual,
		&manualConfidence,
		segmentState{},
		segmentState{
			start:      floatPtr(120),
			end:        floatPtr(180),
			source:     &scannerSource,
			confidence: &scannerConfidence,
		},
	)

	if nextSource == nil || *nextSource != models.MarkerSourceScanner {
		t.Fatalf("next source = %v, want scanner after manual segment clear", nextSource)
	}
	if nextConfidence == nil || *nextConfidence != scannerConfidence {
		t.Fatalf("next confidence = %v, want %v", nextConfidence, scannerConfidence)
	}
}

func TestRecomputeSharedMarkerAttributionClearsWhenNoSegmentsRemain(t *testing.T) {
	manual := models.MarkerSourceManual
	manualConfidence := 1.0

	nextSource, nextConfidence := recomputeSharedMarkerAttribution(&manual, &manualConfidence, segmentState{})
	if nextSource != nil || nextConfidence != nil {
		t.Fatalf("shared attribution = %v/%v, want nil/nil with no remaining segments", nextSource, nextConfidence)
	}
}

func floatPtr(v float64) *float64 { return &v }
