package scanner

import (
	"testing"

	"github.com/Silo-Server/silo-server/internal/models"
)

func TestRecomputeSharedMarkerAttributionPreservesHigherPrioritySegment(t *testing.T) {
	manual := models.MarkerSourceManual
	scannerSource := models.MarkerSourceScanner
	manualConfidence := 1.0
	scannerConfidence := 0.5

	nextSource, nextConfidence := recomputeSharedMarkerAttribution(
		nil,
		nil,
		segmentState{
			start:      floatPtr(0),
			end:        floatPtr(10),
			source:     &manual,
			confidence: &manualConfidence,
		},
		segmentState{
			start:      floatPtr(20),
			end:        floatPtr(30),
			source:     &scannerSource,
			confidence: &scannerConfidence,
		},
	)

	if nextSource == nil || *nextSource != models.MarkerSourceManual {
		t.Fatalf("next source = %v, want manual", nextSource)
	}
	if nextConfidence == nil || *nextConfidence != manualConfidence {
		t.Fatalf("next confidence = %v, want %v", nextConfidence, manualConfidence)
	}
}

func TestRecomputeSharedMarkerAttributionUpdatesSamePriorityConfidence(t *testing.T) {
	scannerSource := models.MarkerSourceScanner
	lowerConfidence := 0.5
	higherConfidence := 0.8

	nextSource, nextConfidence := recomputeSharedMarkerAttribution(
		nil,
		nil,
		segmentState{
			start:      floatPtr(0),
			end:        floatPtr(10),
			source:     &scannerSource,
			confidence: &lowerConfidence,
		},
		segmentState{
			start:      floatPtr(20),
			end:        floatPtr(30),
			source:     &scannerSource,
			confidence: &higherConfidence,
		},
	)

	if nextSource == nil || *nextSource != models.MarkerSourceScanner {
		t.Fatalf("next source = %v, want scanner", nextSource)
	}
	if nextConfidence == nil || *nextConfidence != higherConfidence {
		t.Fatalf("next confidence = %v, want %v", nextConfidence, higherConfidence)
	}
}

func TestRecomputeSharedMarkerAttributionPromotesHigherPrioritySource(t *testing.T) {
	scannerSource := models.MarkerSourceScanner
	manual := models.MarkerSourceManual
	scannerConfidence := 0.5
	manualConfidence := 0.9

	nextSource, nextConfidence := recomputeSharedMarkerAttribution(
		nil,
		nil,
		segmentState{
			start:      floatPtr(0),
			end:        floatPtr(10),
			source:     &scannerSource,
			confidence: &scannerConfidence,
		},
		segmentState{
			start:      floatPtr(20),
			end:        floatPtr(30),
			source:     &manual,
			confidence: &manualConfidence,
		},
	)

	if nextSource == nil || *nextSource != models.MarkerSourceManual {
		t.Fatalf("next source = %v, want manual", nextSource)
	}
	if nextConfidence == nil || *nextConfidence != manualConfidence {
		t.Fatalf("next confidence = %v, want %v", nextConfidence, manualConfidence)
	}
}

func TestRecomputeSharedMarkerAttributionUsesLegacyAttributionForUnattributedSegment(t *testing.T) {
	existingSource := models.MarkerSourceScanner
	existingConfidence := 0.9

	nextSource, nextConfidence := recomputeSharedMarkerAttribution(
		&existingSource,
		&existingConfidence,
		segmentState{
			start: floatPtr(0),
			end:   floatPtr(10),
		},
	)

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
