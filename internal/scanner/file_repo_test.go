package scanner

import (
	"testing"
	"time"

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

func TestMarkerAuditSegmentForStatePreservesBeforeAfterShape(t *testing.T) {
	manual := models.MarkerSourceManual
	algorithm := "manual:v1"
	confidence := 1.0
	detectedAt := time.Unix(300, 0).UTC()

	segment := markerAuditSegmentForState(segmentState{
		start:      floatPtr(5),
		end:        floatPtr(65),
		source:     &manual,
		confidence: &confidence,
		algorithm:  &algorithm,
		detectedAt: &detectedAt,
	})
	if segment == nil {
		t.Fatal("expected audit segment")
	}
	if segment.Start == nil || *segment.Start != 5 || segment.End == nil || *segment.End != 65 {
		t.Fatalf("audit segment bounds = %v..%v, want 5..65", segment.Start, segment.End)
	}
	if segment.Source == nil || *segment.Source != manual || segment.Algorithm == nil || *segment.Algorithm != algorithm {
		t.Fatalf("audit provenance = source %v algorithm %v", segment.Source, segment.Algorithm)
	}
	if segment.DetectedAt == nil || !segment.DetectedAt.Equal(detectedAt) {
		t.Fatalf("detected_at = %v, want %v", segment.DetectedAt, detectedAt)
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

// Multipart ordering must survive rows written before the part-index column:
// SQL orders those legacy rows by file path, which puts part10 before part2.
func TestSortPresentationFilesOrdersLegacyRowsNaturally(t *testing.T) {
	files := []*models.MediaFile{
		{ID: 1, FilePath: "/book/part10.m4b"},
		{ID: 2, FilePath: "/book/part2.m4b", PresentationPartIndex: 2, PresentationPartTotal: 3},
		{ID: 3, FilePath: "/book/part1.m4b"},
		{ID: 4, FilePath: "/book/part1.m4b", PresentationPartIndex: 1, PresentationPartTotal: 3},
		{ID: 5, FilePath: "/book/part3.m4b", PresentationPartIndex: 3, PresentationPartTotal: 3},
	}
	sortPresentationFiles(files)

	got := make([]int, 0, len(files))
	for _, f := range files {
		got = append(got, f.ID)
	}
	want := []int{4, 2, 5, 3, 1}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("presentation order = %v, want %v", got, want)
		}
	}
}
