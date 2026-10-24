package markers

import (
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/models"
)

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

// An on-demand absent answer hides a local intro, including when laid over a
// fresh read of the stored row, and goes once its refreshed provider drops it.
func TestApplyResultProjectsAbsentAnswer(t *testing.T) {
	stored := &models.MediaFile{ID: 3, Duration: 1800, IntroStart: new(10.0), IntroEnd: new(70.0),
		IntroMarkersSource: new(models.MarkerSourceScanner), IntroMarkersAlgorithm: new("chromaprint:v4"), IntroMarkersConfidence: new(0.9)}
	stored.MarkerSegments = models.EffectiveMarkerSegments(stored)
	absent := Result{SourceClass: models.MarkerSourcePlugin, ProviderID: "provider", RefreshedProviders: []string{"provider"},
		Markers: []Marker{{Kind: MarkerKindIntro, Absent: true, Confidence: 0.9}}}
	tombstone := func(name string, file *models.MediaFile) {
		t.Helper()
		if file.IntroStart != nil || file.IntroEnd != nil || len(file.MarkerSegments) != 0 ||
			file.IntroMarkersSource == nil || *file.IntroMarkersSource != models.MarkerSourcePlugin {
			t.Fatalf("%s: intro = %v-%v source %v segments %+v, want a plugin tombstone", name, file.IntroStart, file.IntroEnd, file.IntroMarkersSource, file.MarkerSegments)
		}
	}
	view := ApplyResult(stored, absent)
	tombstone("projection", view)
	tombstone("overlay", OverlayOnline(stored, view))
	withdrawn := ApplyResult(view, Result{RefreshedProviders: []string{"provider"}})
	if withdrawn.IntroMarkersSource != nil || withdrawn.IntroMarkersProvider != nil {
		t.Fatalf("withdrawn absent answer kept: source %v provider %v", withdrawn.IntroMarkersSource, withdrawn.IntroMarkersProvider)
	}
}

func TestApplyResultDropsCreditsOutsideCreditsWindow(t *testing.T) {
	// A 40-minute episode, whose credits window opens at 32:30.
	file := &models.MediaFile{Duration: 2400, EpisodeID: "episode"}
	result := Result{ProviderID: "provider", SourceClass: models.MarkerSourceOnline,
		Markers: []Marker{
			{Kind: MarkerKindIntro, Start: 94 * time.Second, End: 114 * time.Second, Confidence: 0.9},
			{Kind: MarkerKindCredits, Start: 94 * time.Second, End: 112 * time.Second, Confidence: 0.9},
			{Kind: MarkerKindCredits, Start: 1300 * time.Second, End: 1400 * time.Second, Confidence: 0.9},
		}}

	next := ApplyResult(file, result)

	if next.IntroStart == nil || *next.IntroStart != 94 {
		t.Fatalf("intro start = %v, want 94", next.IntroStart)
	}
	if next.CreditsStart != nil {
		t.Fatalf("credits start = %v, want none", *next.CreditsStart)
	}
	for _, segment := range next.MarkerSegments {
		if segment.Kind == models.MarkerSegmentCredits {
			t.Fatalf("projected early credits %+v", segment)
		}
	}
}

func TestApplyResultRefreshClearsProviderCreditsWhenOnlyEarlyCreditsReturn(t *testing.T) {
	file := &models.MediaFile{Duration: 2400, EpisodeID: "episode", CreditsStart: new(94.0), CreditsEnd: new(112.0),
		CreditsMarkersSource: new(models.MarkerSourceOnline), CreditsMarkersProvider: new("provider")}
	result := Result{ProviderID: "provider", SourceClass: models.MarkerSourceOnline, RefreshedProviders: []string{"provider"},
		Markers: []Marker{{Kind: MarkerKindCredits, Start: 94 * time.Second, End: 112 * time.Second, Confidence: 0.9}}}

	next := ApplyResult(file, result)

	if next.CreditsStart != nil || len(next.MarkerSegments) != 0 {
		t.Fatalf("early credits survived the refresh: start=%v segments=%+v", next.CreditsStart, next.MarkerSegments)
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

// Scanner algorithms rank by detector and version. A new detector version
// must be able to overwrite what the version it replaces wrote, even at
// lower confidence; a re-analysis that rescores a Chromaprint intro lower
// replaces the stored score, while other detectors keep the higher result.
// Credits detectors rank like their intro counterparts: chapters above
// version copies above season-scored audio above video alone.
func TestCanWriteMarkerUpdateRanksScannerAlgorithms(t *testing.T) {
	payload := func(algorithm string, confidence, start, end float64) SegmentPayload {
		return SegmentPayload{Start: new(start), End: new(end), Source: models.MarkerSourceScanner,
			Confidence: new(confidence), Algorithm: algorithm}
	}
	provider := func(source string, confidence *float64) SegmentPayload {
		return SegmentPayload{Start: new(60.0), End: new(125.0), Source: source, Provider: new("provider"),
			Confidence: confidence, Algorithm: "provider:v1"}
	}
	// absent is a provider's answer that the file has none of the kind.
	absent := func(confidence float64) SegmentPayload {
		return SegmentPayload{Source: models.MarkerSourcePlugin, Provider: new("provider"), Confidence: new(confidence), Algorithm: "provider:v1"}
	}
	manual := SegmentPayload{Start: new(60.0), End: new(120.0), Source: models.MarkerSourceManual, Confidence: new(1.0), Algorithm: "manual:v1"}
	manualClear := SegmentPayload{Source: models.MarkerSourceManual, Confidence: new(1.0), Algorithm: "manual:v1"}
	ranks := []string{"credits-chapter:v1", "credits-version-copy:v1", "credits-audio:video:v1", "credits-audio:v1", "credits-video:v1"}
	for i := 1; i < len(ranks); i++ {
		if scannerAlgorithmPriority(ranks[i-1]) <= scannerAlgorithmPriority(ranks[i]) {
			t.Fatalf("%s must outrank %s", ranks[i-1], ranks[i])
		}
	}
	cases := []struct {
		name               string
		existing, incoming SegmentPayload
		want               bool
	}{
		{"silence v2 corrects an equal-confidence range", payload("chapter:silence:v2", 0.98, 60, 120), payload("chapter:silence:v2", 0.98, 60, 122), true},
		{"chapter replaces chromaprint v4 at lower confidence", payload("chromaprint:v4", 0.98, 60, 120), payload("chapter:v1", 0.95, 60, 120), true},                           //nolint:misspell // Persisted algorithm identifier.
		{"chromaprint v4 cannot replace chapter", payload("chapter:v1", 0.95, 60, 120), payload("chromaprint:v4", 0.85, 60, 120), false},                                        //nolint:misspell // Persisted algorithm identifier.
		{"chromaprint v4 cannot replace episode copy at higher confidence", payload("episode-version-copy:v1", 0.85, 60, 120), payload("chromaprint:v4", 0.98, 60, 120), false}, //nolint:misspell // Persisted algorithm identifier.
		{"chromaprint v2 over dialogue v1", payload("chromaprint:dialogue:v1", 0.9, 10, 70), payload("chromaprint:v2", 0.75, 12, 70), true},                                     //nolint:misspell // Persisted algorithm identifier.
		{"dialogue v2 over chromaprint v2", payload("chromaprint:v2", 0.9, 10, 70), payload("chromaprint:dialogue:v2", 0.75, 12, 70), true},                                     //nolint:misspell // Persisted algorithm identifier.
		{"chapter over over-extended legacy silence", payload("chapter:silence:v1", 0.98, 60, 140), payload("chapter:v1", 0.95, 60, 120), true},
		{"silence v2 over chapter", payload("chapter:v1", 0.95, 60, 120), payload("chapter:silence:v2", 0.98, 60, 122), true},
		{"chromaprint v3 over dialogue v2", payload("chromaprint:dialogue:v2", 0.9, 10, 70), payload("chromaprint:v3", 0.3, 12, 70), true},            //nolint:misspell // Persisted algorithm identifier.
		{"dialogue v3 over chromaprint v3", payload("chromaprint:v3", 0.9, 10, 70), payload("chromaprint:dialogue:v3", 0.65, 12, 70), true},           //nolint:misspell // Persisted algorithm identifier.
		{"episode copy over chromaprint v3", payload("chromaprint:dialogue:v3", 0.9, 10, 70), payload("episode-version-copy:v1", 0.85, 12, 70), true}, //nolint:misspell // Persisted algorithm identifier.
		{"chromaprint v4 over dialogue v3", payload("chromaprint:dialogue:v3", 0.9, 10, 70), payload("chromaprint:v4", 0.65, 12, 70), true},           //nolint:misspell // Persisted algorithm identifier.
		{"legacy chromaprint cannot replace v2", payload("chromaprint:v2", 0.75, 12, 70), payload("chromaprint:dialogue:v1", 0.9, 10, 70), false},     //nolint:misspell // Persisted algorithm identifier.
		{"chromaprint rescored lower over the same range", payload("chromaprint:v3", 0.9, 10, 70), payload("chromaprint:v3", 0.65, 10, 70), true},
		{"identical chromaprint result is a no-op", payload("chromaprint:v3", 0.9, 10, 70), payload("chromaprint:v3", 0.9, 10.2, 70.2), false},
		{"same-version plain rescore replaces a refined marker whose subtitle is gone", payload("chromaprint:dialogue:v3", 0.9, 14, 70), payload("chromaprint:v3", 0.65, 10, 70), true}, //nolint:misspell // Persisted algorithm identifier.
		{"older chromaprint cannot replace a newer refined marker", payload("chromaprint:dialogue:v3", 0.9, 14, 70), payload("chromaprint:v2", 0.9, 10, 70), false},                     //nolint:misspell // Persisted algorithm identifier.
		{"lower-confidence chapter cannot replace a higher one", payload("chapter:v1", 0.95, 60, 120), payload("chapter:v1", 0.85, 60, 125), false},
		{"chapter over audio", payload("credits-audio:v1", 0.9, 1300, 1400), payload("credits-chapter:v1", 0.95, 1310, 1400), true},
		{"audio cannot replace chapter", payload("credits-chapter:v1", 0.95, 1310, 1400), payload("credits-audio:v1", 0.9, 1300, 1400), false},
		{"audio cannot replace version copy", payload("credits-version-copy:v1", 0.85, 1310, 1400), payload("credits-audio:video:v1", 0.95, 1300, 1400), false},
		{"video alone cannot replace audio", payload("credits-audio:v1", 0.65, 1300, 1400), payload("credits-video:v1", 0.6, 1290, 1400), false},
		{"audio rescored lower", payload("credits-audio:v1", 0.9, 1300, 1400), payload("credits-audio:v1", 0.65, 1300, 1400), true},
		{"plain audio replaces video-refined audio", payload("credits-audio:video:v1", 0.95, 1305, 1400), payload("credits-audio:v1", 0.65, 1300, 1400), true},
		{"identical audio result is a no-op", payload("credits-audio:v1", 0.9, 1300, 1400), payload("credits-audio:v1", 0.9, 1300.2, 1400), false},
		{"less confident provider cannot replace chapter", payload("chapter:v1", 0.95, 60, 120), provider(models.MarkerSourcePlugin, new(0.3)), false},
		{"provider without confidence cannot replace chapter", payload("chapter:silence:v2", 0.98, 60, 122), provider(models.MarkerSourceOnline, nil), false},
		{"less confident provider cannot replace credits chapter", payload("credits-chapter:v1", 0.95, 1310, 1400), provider(models.MarkerSourceOnline, new(0.9)), false},
		{"provider as confident as chapter replaces it", payload("chapter:v1", 0.95, 60, 120), provider(models.MarkerSourcePlugin, new(0.95)), true},
		{"less confident provider replaces episode copy", payload("episode-version-copy:v1", 0.9, 60, 120), provider(models.MarkerSourcePlugin, new(0.3)), true},
		{"less confident provider replaces chromaprint", payload("chromaprint:v4", 0.9, 60, 120), provider(models.MarkerSourceOnline, new(0.1)), true},
		{"chapter replaces less confident provider", provider(models.MarkerSourcePlugin, new(0.3)), payload("chapter:v1", 0.95, 60, 120), true},
		{"credits chapter replaces provider without confidence", provider(models.MarkerSourceOnline, nil), payload("credits-chapter:v1", 0.95, 1310, 1400), true},
		{"chapter cannot replace equally confident provider", provider(models.MarkerSourcePlugin, new(0.95)), payload("chapter:v1", 0.95, 60, 120), false},
		{"chromaprint cannot replace less confident provider", provider(models.MarkerSourceOnline, new(0.1)), payload("chromaprint:v4", 0.9, 60, 120), false},
		{"absent answer replaces chromaprint", payload("chromaprint:v4", 0.9, 60, 120), absent(0.5), true},
		{"chromaprint cannot replace an absent answer", absent(0.1), payload("chromaprint:v4", 0.9, 60, 120), false},
		{"less confident absent answer cannot replace chapter", payload("chapter:v1", 0.95, 60, 120), absent(0.9), false},
		{"absent answer as confident as chapter replaces it", payload("credits-chapter:v1", 0.95, 1310, 1400), absent(0.95), true},
		{"chapter replaces less confident absent answer", absent(0.3), payload("chapter:v1", 0.95, 60, 120), true},
		{"chapter cannot replace equally confident absent answer", absent(0.95), payload("chapter:v1", 0.95, 60, 120), false},
		{"absent answer cannot replace manual marker", manual, absent(1), false},
		{"absent answer cannot replace manual clear", manualClear, absent(1), false},
		{"manual marker replaces absent answer", absent(1), manual, true},
		{"provider marker replaces absent answer", absent(0.9), provider(models.MarkerSourcePlugin, new(0.3)), true},
	}
	for _, tc := range cases {
		if got := CanWriteMarkerUpdate(tc.existing, tc.incoming); got != tc.want {
			t.Errorf("%s: CanWriteMarkerUpdate = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestSameSeasonScoredVersion(t *testing.T) {
	cases := []struct {
		a, b string
		want bool
	}{
		{"chromaprint:v4", "chromaprint:dialogue:v4", true}, //nolint:misspell // Persisted algorithm identifier.
		{"chromaprint:v4", "chromaprint:v3", false},
		{"credits-audio:v1", "credits-audio:video:v1", true},
		{"credits-audio:v1", "credits-audio:v2", false},
		{"credits-audio:v1", "chromaprint:v1", false},
		{"credits-chapter:v1", "credits-chapter:v1", false},
		{"credits-video:v1", "credits-audio:v1", false},
	}
	for _, tc := range cases {
		if got := sameSeasonScoredVersion(tc.a, tc.b); got != tc.want {
			t.Errorf("sameSeasonScoredVersion(%q, %q) = %v, want %v", tc.a, tc.b, got, tc.want)
		}
	}
}

func TestManualDeletionRejectsAutomaticProjection(t *testing.T) {
	file := &models.MediaFile{Duration: 1000, IntroMarkersSource: new(models.MarkerSourceManual)}
	for _, source := range []string{models.MarkerSourceOnline, models.MarkerSourcePlugin, models.MarkerSourceScanner, models.MarkerSourceS3} {
		result := Result{ProviderID: "provider", SourceClass: source, Markers: []Marker{
			{Kind: MarkerKindIntro, Start: 10 * time.Second, End: 30 * time.Second},
			{Kind: MarkerKindRecap, Start: 40 * time.Second, End: 60 * time.Second},
		}}
		next := ApplyResult(file, result)
		if next.IntroStart != nil || next.IntroEnd != nil || next.RecapStart == nil || *next.RecapStart != 40 {
			t.Fatalf("%s restored deleted intro or suppressed unrelated recap: %+v", source, next.MarkerSegments)
		}
		if file.RecapStart != nil {
			t.Fatal("projection mutated stored snapshot")
		}
	}
	if !CanWriteMarkerUpdate(SegmentPayload{Source: models.MarkerSourceManual}, SegmentPayload{Source: models.MarkerSourceManual, Start: new(12.0), End: new(34.0)}) {
		t.Fatal("explicit manual replacement must be allowed")
	}
}
