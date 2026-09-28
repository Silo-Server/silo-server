package intromarkers

import (
	"slices"
	"testing"

	"github.com/Silo-Server/silo-server/internal/models"
)

func strPtr(value string) *string {
	return &value
}

func floatPtr(value float64) *float64 {
	return &value
}

func TestOwnCandidatesJudgesEachKindOnItsOwn(t *testing.T) {
	online := models.MarkerSourceOnline
	scanner := models.MarkerSourceScanner
	candidates := []Candidate{
		// No markers: local analysis owns both kinds.
		{FileID: 1},
		// A higher-priority intro does not stop local credits.
		{FileID: 2, IntroStart: floatPtr(30), IntroEnd: floatPtr(90), IntroMarkersSource: strPtr(online)},
		// Local markers of both kinds can be re-analyzed.
		{
			FileID: 3, IntroStart: floatPtr(30), IntroEnd: floatPtr(90), IntroMarkersSource: strPtr(scanner),
			CreditsStart: floatPtr(1300), CreditsEnd: floatPtr(1400), CreditsMarkersSource: strPtr(scanner),
		},
		// Higher-priority credits do not stop a local intro.
		{FileID: 4, CreditsStart: floatPtr(1300), CreditsEnd: floatPtr(1400), CreditsMarkersSource: strPtr(online)},
		// A marker written before per-segment provenance carries only the
		// shared markers_source.
		{FileID: 5, CreditsStart: floatPtr(1300), CreditsEnd: floatPtr(1400), MarkersSource: strPtr(online)},
	}
	ids := func(candidates []Candidate) []int {
		out := make([]int, 0, len(candidates))
		for _, candidate := range candidates {
			out = append(out, candidate.FileID)
		}
		return out
	}
	if got, want := ids(ownCandidates(candidates, kindIntro)), []int{1, 3, 4, 5}; !slices.Equal(got, want) {
		t.Fatalf("intro candidates = %v, want %v", got, want)
	}
	if got, want := ids(ownCandidates(candidates, kindCredits)), []int{1, 2, 3}; !slices.Equal(got, want) {
		t.Fatalf("credits candidates = %v, want %v", got, want)
	}
}

func TestCandidateMarkerSelectsKind(t *testing.T) {
	c := Candidate{
		IntroStart: floatPtr(30), IntroEnd: floatPtr(90), IntroMarkersAlgorithm: strPtr(ChromaprintAlgorithm),
		CreditsStart: floatPtr(1300), CreditsEnd: floatPtr(1400), CreditsMarkersConfidence: floatPtr(0.9),
		MarkersSource: strPtr(models.MarkerSourceScanner),
	}
	intro, credits := c.marker(kindIntro), c.marker(kindCredits)
	if *intro.Start != 30 || *intro.End != 90 || *intro.Algorithm != ChromaprintAlgorithm {
		t.Fatalf("intro marker = %+v", intro)
	}
	if *credits.Start != 1300 || *credits.End != 1400 || *credits.Confidence != 0.9 {
		t.Fatalf("credits marker = %+v", credits)
	}
	if got := c.effectiveSource(kindCredits); got != models.MarkerSourceScanner {
		t.Fatalf("credits source = %q, want the shared markers_source", got)
	}
	if c.marker(markerKind(0)).present() || c.effectiveSource(markerKind(0)) != "" {
		t.Fatal("no kind should have no marker")
	}
}

func TestMarkerPatchWritesItsKind(t *testing.T) {
	patch := MarkerPatch{
		FileID:     7,
		Start:      1300,
		End:        1400,
		Source:     models.MarkerSourceScanner,
		Confidence: 0.9,
		Algorithm:  "credits-test:v1",
	}
	if _, err := patch.markerUpdate(); err == nil {
		t.Fatal("a patch without a kind must be rejected")
	}

	patch.Kind = kindCredits
	update, err := patch.markerUpdate()
	if err != nil {
		t.Fatal(err)
	}
	if update.IntroStart != nil || update.IntroEnd != nil {
		t.Fatalf("credits patch wrote an intro: %+v", update)
	}
	if update.CreditsStart == nil || *update.CreditsStart != 1300 || update.CreditsEnd == nil || *update.CreditsEnd != 1400 ||
		update.MarkersAlgorithm != "credits-test:v1" || update.MarkersSource != models.MarkerSourceScanner {
		t.Fatalf("credits update = %+v", update)
	}

	patch.Kind = kindIntro
	update, err = patch.markerUpdate()
	if err != nil {
		t.Fatal(err)
	}
	if update.CreditsStart != nil || update.IntroStart == nil || *update.IntroStart != 1300 {
		t.Fatalf("intro update = %+v", update)
	}
}
