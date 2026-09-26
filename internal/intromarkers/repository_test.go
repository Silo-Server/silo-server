package intromarkers

import (
	"testing"

	"github.com/Silo-Server/silo-server/internal/markers"
	"github.com/Silo-Server/silo-server/internal/models"
)

func TestLocalMarkerWritePolicyAllowsSameAlgorithmRangeCorrection(t *testing.T) {
	start := 60.0
	end := 120.0
	source := models.MarkerSourceScanner
	confidence := 0.95
	algorithm := ChapterSilenceAlgorithm
	existing := markers.SegmentPayload{
		Start:      &start,
		End:        &end,
		Source:     source,
		Confidence: &confidence,
		Algorithm:  algorithm,
	}
	patch := IntroMarkerPatch{
		Start:      60,
		End:        132,
		Source:     models.MarkerSourceScanner,
		Confidence: 0.95,
		Algorithm:  ChapterSilenceAlgorithm,
	}

	if !markers.CanWriteMarkerUpdate(existing, markers.SegmentPayload{
		Start: &patch.Start, End: &patch.End, Source: patch.Source,
		Confidence: &patch.Confidence, Algorithm: patch.Algorithm,
	}) {
		t.Fatal("expected equal-confidence range correction to apply")
	}
}

func TestLocalMarkerWritePolicyRejectsLowerPrioritySource(t *testing.T) {
	start := 60.0
	end := 120.0
	source := models.MarkerSourceManual
	confidence := 1.0
	algorithm := "manual"
	existing := markers.SegmentPayload{
		Start:      &start,
		End:        &end,
		Source:     source,
		Confidence: &confidence,
		Algorithm:  algorithm,
	}
	patch := IntroMarkerPatch{
		Start:      60,
		End:        132,
		Source:     models.MarkerSourceScanner,
		Confidence: 0.99,
		Algorithm:  ChapterSilenceAlgorithm,
	}

	if markers.CanWriteMarkerUpdate(existing, markers.SegmentPayload{
		Start: &patch.Start, End: &patch.End, Source: patch.Source,
		Confidence: &patch.Confidence, Algorithm: patch.Algorithm,
	}) {
		t.Fatal("scanner patch should not overwrite manual marker")
	}
}

func TestLocalMarkerWritePolicyRejectsLowerConfidenceAlgorithmChange(t *testing.T) {
	start := 331.5
	end := 362.5
	source := models.MarkerSourceScanner
	confidence := 0.95
	algorithm := ChapterAlgorithm
	existing := markers.SegmentPayload{
		Start:      &start,
		End:        &end,
		Source:     source,
		Confidence: &confidence,
		Algorithm:  algorithm,
	}
	patch := IntroMarkerPatch{
		Start:      322.014,
		End:        363.465,
		Source:     models.MarkerSourceScanner,
		Confidence: 0.85,
		Algorithm:  ChromaprintAlgorithm,
	}

	if markers.CanWriteMarkerUpdate(existing, markers.SegmentPayload{
		Start: &patch.Start, End: &patch.End, Source: patch.Source,
		Confidence: &patch.Confidence, Algorithm: patch.Algorithm,
	}) {
		t.Fatal("lower-confidence chromaprint patch should not overwrite chapter marker")
	}
}

func TestLocalMarkerWritePolicyRejectsEqualConfidenceLowerRankAlgorithm(t *testing.T) {
	start := 331.5
	end := 362.5
	source := models.MarkerSourceScanner
	confidence := 0.85
	algorithm := EpisodeVersionCopyAlgorithm
	existing := markers.SegmentPayload{
		Start:      &start,
		End:        &end,
		Source:     source,
		Confidence: &confidence,
		Algorithm:  algorithm,
	}
	patch := IntroMarkerPatch{
		Start:      322.014,
		End:        363.465,
		Source:     models.MarkerSourceScanner,
		Confidence: 0.85,
		Algorithm:  ChromaprintAlgorithm,
	}

	if markers.CanWriteMarkerUpdate(existing, markers.SegmentPayload{
		Start: &patch.Start, End: &patch.End, Source: patch.Source,
		Confidence: &patch.Confidence, Algorithm: patch.Algorithm,
	}) {
		t.Fatal("equal-confidence chromaprint patch should not overwrite copied chapter marker")
	}
}

func TestLocalMarkerWritePolicyRejectsHigherConfidenceLowerRankAlgorithm(t *testing.T) {
	start := 331.5
	end := 362.5
	source := models.MarkerSourceScanner
	confidence := 0.85
	algorithm := EpisodeVersionCopyAlgorithm
	existing := markers.SegmentPayload{
		Start:      &start,
		End:        &end,
		Source:     source,
		Confidence: &confidence,
		Algorithm:  algorithm,
	}
	patch := IntroMarkerPatch{
		Start:      322.014,
		End:        363.465,
		Source:     models.MarkerSourceScanner,
		Confidence: 0.90,
		Algorithm:  ChromaprintAlgorithm,
	}

	if markers.CanWriteMarkerUpdate(existing, markers.SegmentPayload{
		Start: &patch.Start, End: &patch.End, Source: patch.Source,
		Confidence: &patch.Confidence, Algorithm: patch.Algorithm,
	}) {
		t.Fatal("higher-confidence chromaprint patch should not replace copied marker")
	}
}

func TestLocalMarkerWritePolicyAllowsHigherRankLowerConfidenceAlgorithm(t *testing.T) {
	start := 331.5
	end := 362.5
	source := models.MarkerSourceScanner
	confidence := 0.95
	algorithm := ChromaprintAlgorithm
	existing := markers.SegmentPayload{
		Start:      &start,
		End:        &end,
		Source:     source,
		Confidence: &confidence,
		Algorithm:  algorithm,
	}
	patch := IntroMarkerPatch{
		Start:      322.014,
		End:        363.465,
		Source:     models.MarkerSourceScanner,
		Confidence: 0.85,
		Algorithm:  ChapterAlgorithm,
	}

	if !markers.CanWriteMarkerUpdate(existing, markers.SegmentPayload{
		Start: &patch.Start, End: &patch.End, Source: patch.Source,
		Confidence: &patch.Confidence, Algorithm: patch.Algorithm,
	}) {
		t.Fatal("higher-rank chapter patch should replace lower-rank chromaprint marker")
	}
}
