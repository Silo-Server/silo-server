package intromarkers

import (
	"testing"

	"github.com/Silo-Server/silo-server/internal/models"
)

func TestShouldApplyIntroPatchAllowsSameAlgorithmRangeCorrection(t *testing.T) {
	start := 60.0
	end := 120.0
	source := models.MarkerSourceScanner
	confidence := 0.95
	algorithm := ChapterSilenceAlgorithm
	row := markerRow{
		IntroStart:             &start,
		IntroEnd:               &end,
		IntroMarkersSource:     &source,
		IntroMarkersConfidence: &confidence,
		IntroMarkersAlgorithm:  &algorithm,
	}
	patch := IntroMarkerPatch{
		Start:      60,
		End:        132,
		Source:     models.MarkerSourceScanner,
		Confidence: 0.95,
		Algorithm:  ChapterSilenceAlgorithm,
	}

	if !shouldApplyIntroPatch(row, patch) {
		t.Fatal("expected equal-confidence range correction to apply")
	}
}

func TestShouldApplyIntroPatchRejectsLowerPrioritySource(t *testing.T) {
	start := 60.0
	end := 120.0
	source := models.MarkerSourceManual
	confidence := 1.0
	algorithm := "manual"
	row := markerRow{
		IntroStart:             &start,
		IntroEnd:               &end,
		IntroMarkersSource:     &source,
		IntroMarkersConfidence: &confidence,
		IntroMarkersAlgorithm:  &algorithm,
	}
	patch := IntroMarkerPatch{
		Start:      60,
		End:        132,
		Source:     models.MarkerSourceScanner,
		Confidence: 0.99,
		Algorithm:  ChapterSilenceAlgorithm,
	}

	if shouldApplyIntroPatch(row, patch) {
		t.Fatal("scanner patch should not overwrite manual marker")
	}
}

func TestShouldApplyIntroPatchRejectsLowerConfidenceAlgorithmChange(t *testing.T) {
	start := 331.5
	end := 362.5
	source := models.MarkerSourceScanner
	confidence := 0.95
	algorithm := ChapterAlgorithm
	row := markerRow{
		IntroStart:             &start,
		IntroEnd:               &end,
		IntroMarkersSource:     &source,
		IntroMarkersConfidence: &confidence,
		IntroMarkersAlgorithm:  &algorithm,
	}
	patch := IntroMarkerPatch{
		Start:      322.014,
		End:        363.465,
		Source:     models.MarkerSourceScanner,
		Confidence: 0.85,
		Algorithm:  ChromaprintAlgorithm,
	}

	if shouldApplyIntroPatch(row, patch) {
		t.Fatal("lower-confidence chromaprint patch should not overwrite chapter marker")
	}
}

func TestShouldApplyIntroPatchRejectsEqualConfidenceLowerRankAlgorithm(t *testing.T) {
	start := 331.5
	end := 362.5
	source := models.MarkerSourceScanner
	confidence := 0.85
	algorithm := EpisodeVersionCopyAlgorithm
	row := markerRow{
		IntroStart:             &start,
		IntroEnd:               &end,
		IntroMarkersSource:     &source,
		IntroMarkersConfidence: &confidence,
		IntroMarkersAlgorithm:  &algorithm,
	}
	patch := IntroMarkerPatch{
		Start:      322.014,
		End:        363.465,
		Source:     models.MarkerSourceScanner,
		Confidence: 0.85,
		Algorithm:  ChromaprintAlgorithm,
	}

	if shouldApplyIntroPatch(row, patch) {
		t.Fatal("equal-confidence chromaprint patch should not overwrite copied chapter marker")
	}
}

func TestShouldApplyIntroPatchRejectsHigherConfidenceLowerRankAlgorithm(t *testing.T) {
	start := 331.5
	end := 362.5
	source := models.MarkerSourceScanner
	confidence := 0.85
	algorithm := EpisodeVersionCopyAlgorithm
	row := markerRow{
		IntroStart:             &start,
		IntroEnd:               &end,
		IntroMarkersSource:     &source,
		IntroMarkersConfidence: &confidence,
		IntroMarkersAlgorithm:  &algorithm,
	}
	patch := IntroMarkerPatch{
		Start:      322.014,
		End:        363.465,
		Source:     models.MarkerSourceScanner,
		Confidence: 0.90,
		Algorithm:  ChromaprintAlgorithm,
	}

	if shouldApplyIntroPatch(row, patch) {
		t.Fatal("higher-confidence chromaprint patch should not replace copied marker")
	}
}

func TestShouldApplyIntroPatchAllowsHigherRankLowerConfidenceAlgorithm(t *testing.T) {
	start := 331.5
	end := 362.5
	source := models.MarkerSourceScanner
	confidence := 0.95
	algorithm := ChromaprintAlgorithm
	row := markerRow{
		IntroStart:             &start,
		IntroEnd:               &end,
		IntroMarkersSource:     &source,
		IntroMarkersConfidence: &confidence,
		IntroMarkersAlgorithm:  &algorithm,
	}
	patch := IntroMarkerPatch{
		Start:      322.014,
		End:        363.465,
		Source:     models.MarkerSourceScanner,
		Confidence: 0.85,
		Algorithm:  ChapterAlgorithm,
	}

	if !shouldApplyIntroPatch(row, patch) {
		t.Fatal("higher-rank chapter patch should replace lower-rank chromaprint marker")
	}
}
