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

func TestMarkerUpdateDropsCreditsOutsideCreditsWindow(t *testing.T) {
	// A 40-minute episode, whose credits window opens at 32:30, from a
	// provider that copied its intro into the credits.
	for _, tc := range []struct {
		name        string
		update      MarkerUpdate
		wantCredits []models.MarkerSegment
	}{
		{
			name: "singular bounds",
			update: MarkerUpdate{
				IntroStart: floatPtr(94), IntroEnd: floatPtr(114),
				CreditsStart: floatPtr(94), CreditsEnd: floatPtr(112),
				MarkersSource: models.MarkerSourcePlugin,
			},
		},
		{
			name: "ranges",
			update: MarkerUpdate{
				Segments: []models.MarkerSegment{
					{Kind: models.MarkerSegmentIntro, StartSeconds: 94, EndSeconds: 114},
					{Kind: models.MarkerSegmentCredits, StartSeconds: 94, EndSeconds: 112},
					{Kind: models.MarkerSegmentCredits, StartSeconds: 1300, EndSeconds: 1400},
					{Kind: models.MarkerSegmentCredits, StartSeconds: 2300, EndSeconds: 2400},
				},
				MarkersSource: models.MarkerSourceOnline,
			},
			wantCredits: []models.MarkerSegment{{Kind: models.MarkerSegmentCredits, StartSeconds: 2300, EndSeconds: 2400}},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			state := markerMutationState{duration: 2400, file: models.MediaFile{EpisodeID: "episode"}}

			applied, err := applyMarkerUpdateToMutationState(&tc.update, &state, time.Now())
			if err != nil {
				t.Fatalf("apply: %v", err)
			}

			if !applied.intro || state.intro.start == nil || *state.intro.start != 94 {
				t.Fatalf("intro not written: applied=%v start=%v", applied.intro, state.intro.start)
			}
			if tc.wantCredits == nil {
				if applied.credits || state.credits.start != nil || len(state.credits.ranges) != 0 {
					t.Fatalf("early credits written: start=%v ranges=%v", state.credits.start, state.credits.ranges)
				}
				return
			}
			if !applied.credits || len(state.credits.ranges) != len(tc.wantCredits) || state.credits.ranges[0] != tc.wantCredits[0] {
				t.Fatalf("credits ranges = %v, want %v", state.credits.ranges, tc.wantCredits)
			}
			if state.credits.start == nil || *state.credits.start != tc.wantCredits[0].StartSeconds {
				t.Fatalf("credits start = %v, want %v", state.credits.start, tc.wantCredits[0].StartSeconds)
			}
		})
	}
}

func TestMarkerRefreshClearsProviderCreditsWhenOnlyEarlyCreditsReturn(t *testing.T) {
	plugin := models.MarkerSourcePlugin
	provider := "provider"
	state := markerMutationState{duration: 2400, file: models.MediaFile{EpisodeID: "episode"}, credits: segmentState{
		start: floatPtr(94), end: floatPtr(112), source: &plugin, provider: &provider,
		ranges: []models.MarkerSegment{{Kind: models.MarkerSegmentCredits, StartSeconds: 94, EndSeconds: 112}},
	}}
	update := MarkerUpdate{
		Segments:           []models.MarkerSegment{{Kind: models.MarkerSegmentCredits, StartSeconds: 94, EndSeconds: 112}},
		MarkersSource:      models.MarkerSourcePlugin,
		MarkersProvider:    &provider,
		RefreshedProviders: []string{provider},
	}

	applied, err := applyMarkerUpdateToMutationState(&update, &state, time.Now())
	if err != nil {
		t.Fatalf("apply: %v", err)
	}

	if !applied.credits || state.credits.start != nil || len(state.credits.ranges) != 0 {
		t.Fatalf("early credits survived the refresh: applied=%v start=%v ranges=%v", applied.credits, state.credits.start, state.credits.ranges)
	}
}

func TestMarkerMutationChecksCreditsAgainstTheIntroItLeaves(t *testing.T) {
	// A 25-minute episode whose wrong intro runs into the credits window: the
	// edit clears it and sets the credits in one request.
	manual := models.MarkerSourceManual
	state := markerMutationState{duration: 1500, file: models.MediaFile{EpisodeID: "episode"}, intro: segmentState{
		start: floatPtr(60), end: floatPtr(1400), source: &manual,
		ranges: []models.MarkerSegment{{Kind: models.MarkerSegmentIntro, StartSeconds: 60, EndSeconds: 1400}},
	}}
	update := MarkerUpdate{CreditsStart: floatPtr(1350), CreditsEnd: floatPtr(1500), MarkersSource: models.MarkerSourceManual}

	changed, err := applyMarkerMutation(&update, markerSegmentFlags{intro: true}, &state, time.Now())
	if err != nil {
		t.Fatalf("apply: %v", err)
	}

	if !changed.intro || state.intro.end != nil {
		t.Fatalf("intro not cleared: changed=%v end=%v", changed.intro, state.intro.end)
	}
	if !changed.credits || state.credits.start == nil || *state.credits.start != 1350 {
		t.Fatalf("credits not written: changed=%v start=%v", changed.credits, state.credits.start)
	}
}
