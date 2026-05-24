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

