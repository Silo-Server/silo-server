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

// A kind can occur more than once. An update that keeps the first occurrence but
// changes a later one is a real correction, not a no-op, so the equal-confidence
// path has to compare the whole range set.
func TestCanWriteMarkerUpdateComparesEveryOccurrence(t *testing.T) {
	confidence := new(0.9)
	ranges := func(bounds ...[2]float64) []models.MarkerSegment {
		out := make([]models.MarkerSegment, 0, len(bounds))
		for _, bound := range bounds {
			out = append(out, models.MarkerSegment{Kind: models.MarkerSegmentIntro, StartSeconds: bound[0], EndSeconds: bound[1]})
		}
		return out
	}
	payload := func(source string, bounds ...[2]float64) SegmentPayload {
		segments := ranges(bounds...)
		return SegmentPayload{Start: new(bounds[0][0]), End: new(bounds[0][1]), Ranges: segments,
			Source: source, Confidence: confidence, Algorithm: "chapter:v1"}
	}

	existing := payload(models.MarkerSourceScanner, [2]float64{10, 40}, [2]float64{100, 130})

	movedLaterRange := payload(models.MarkerSourceScanner, [2]float64{10, 40}, [2]float64{500, 540})
	if !CanWriteMarkerUpdate(existing, movedLaterRange) {
		t.Error("a corrected second occurrence at unchanged confidence was rejected")
	}

	withdrawnLaterRange := payload(models.MarkerSourceScanner, [2]float64{10, 40})
	if !CanWriteMarkerUpdate(existing, withdrawnLaterRange) {
		t.Error("a withdrawn second occurrence at unchanged confidence was rejected")
	}

	addedLaterRange := payload(models.MarkerSourceScanner, [2]float64{10, 40}, [2]float64{100, 130}, [2]float64{700, 730})
	if !CanWriteMarkerUpdate(existing, addedLaterRange) {
		t.Error("an added third occurrence at unchanged confidence was rejected")
	}

	identical := payload(models.MarkerSourceScanner, [2]float64{10, 40}, [2]float64{100, 130})
	if CanWriteMarkerUpdate(existing, identical) {
		t.Error("an identical equal-confidence update should stay a no-op")
	}

	withinTolerance := payload(models.MarkerSourceScanner, [2]float64{10.4, 40.4}, [2]float64{100.4, 130.4})
	if CanWriteMarkerUpdate(existing, withinTolerance) {
		t.Error("sub-half-second drift should count as the same occurrence")
	}
}

// Ranges are compared as a set, so a payload whose ranges arrive unsorted must
// not read as a change, and two occurrences close enough to be within tolerance
// must pair up whichever way round they arrive.
func TestCanWriteMarkerUpdateMatchesRangesWithTolerance(t *testing.T) {
	confidence := new(0.7)
	payload := func(bounds ...[2]float64) SegmentPayload {
		ranges := make([]models.MarkerSegment, 0, len(bounds))
		for _, bound := range bounds {
			ranges = append(ranges, models.MarkerSegment{Kind: models.MarkerSegmentCredits, StartSeconds: bound[0], EndSeconds: bound[1]})
		}
		first := ranges[0]
		return SegmentPayload{Start: new(first.StartSeconds), End: new(first.EndSeconds), Ranges: ranges,
			Source: models.MarkerSourceS3, Confidence: confidence}
	}

	existing := payload([2]float64{10, 40}, [2]float64{900, 950})
	unsorted := payload([2]float64{900, 950}, [2]float64{10, 40})
	if CanWriteMarkerUpdate(existing, unsorted) {
		t.Error("the same occurrence set in a different order should stay a no-op")
	}

	// Range validation permits two occurrences with the same start and different
	// ends, so ordering them by start alone leaves them in arrival order and the
	// same set reads as a change.
	tied := payload([2]float64{10, 20}, [2]float64{10, 40})
	tiedReordered := payload([2]float64{10, 40}, [2]float64{10, 20})
	if CanWriteMarkerUpdate(tied, tiedReordered) {
		t.Error("two occurrences sharing a start compared as a change when only their order differed")
	}

	// Two starts within tolerance of each other: sorting by exact value pairs the
	// short occurrence with the long one and reports a change for the same set.
	closeStarts := payload([2]float64{10.0, 20.0}, [2]float64{10.4, 40.0})
	closeStartsNoisy := payload([2]float64{10.4, 20.4}, [2]float64{10.0, 40.4})
	if CanWriteMarkerUpdate(closeStarts, closeStartsNoisy) {
		t.Error("occurrences within tolerance compared as a change when noise reordered their starts")
	}

	// A genuine change still writes: the second occurrence ends elsewhere.
	movedEnd := payload([2]float64{10, 40}, [2]float64{900, 1200})
	if !CanWriteMarkerUpdate(existing, movedEnd) {
		t.Error("a moved end should still count as a change")
	}

	// Equal confidence on an unranked source: only a real range change applies.
	withdrawn := payload([2]float64{900, 950})
	if !CanWriteMarkerUpdate(existing, withdrawn) {
		t.Error("an equal-confidence ranged source could not withdraw an occurrence")
	}
}
