package intromarkers

import (
	"testing"

	"github.com/Silo-Server/silo-server/internal/models"
)

// chapters builds chapters from (title, start, end) triples.
func chapters(entries ...[3]any) []models.MediaChapter {
	out := make([]models.MediaChapter, 0, len(entries))
	for i, entry := range entries {
		out = append(out, models.MediaChapter{
			Index:        i,
			Title:        entry[0].(string),
			StartSeconds: entry[1].(float64),
			EndSeconds:   entry[2].(float64),
		})
	}
	return out
}

func TestDetectChapterRecapReadsReleaseTitles(t *testing.T) {
	for _, title := range []string{"Previously on", "Previously On Silo", "Recap", "Last time on", "The Story So Far"} {
		span, ok := DetectChapterRecap(chapters([3]any{title, 0.0, 62.0}, [3]any{"Opening", 62.0, 92.0}), 3000)
		if !ok || span.Start != 0 || span.End != 62 {
			t.Fatalf("%q gave %v %v, want 0-62", title, span, ok)
		}
	}
	// A scene called "Previous Tenant" is not a recap.
	if _, ok := DetectChapterRecap(chapters([3]any{"Previous Tenant", 0.0, 62.0}), 3000); ok {
		t.Fatal("a scene title beginning with Previous was read as a recap")
	}
}

func TestDetectChapterRecapJoinsAdjacentChapters(t *testing.T) {
	span, ok := DetectChapterRecap(chapters(
		[3]any{"Previously On", 0.0, 20.0},
		[3]any{"Recap", 20.0, 48.0},
		[3]any{"Opening", 48.0, 78.0},
	), 3000)
	if !ok || span.End != 48 {
		t.Fatalf("recap run %v %v, want it to end at 48", span, ok)
	}
}

func TestDetectChapterRecapRequiresTheOpening(t *testing.T) {
	if _, ok := DetectChapterRecap(chapters([3]any{"Recap", 1500.0, 1560.0}), 3000); ok {
		t.Fatal("a recap named in the middle of the file was accepted")
	}
}

func TestDetectChapterPreviewReadsReleaseTitles(t *testing.T) {
	for _, title := range []string{"Next episode", "Next on Silo", "Next time on", "Preview", "Coming up"} {
		span, ok := DetectChapterPreview(chapters([3]any{"Credits", 2700.0, 2910.0}, [3]any{title, 2910.0, 2940.0}), 3004)
		if !ok || span.Start != 2910 || span.End != 2940 {
			t.Fatalf("%q gave %v %v, want 2910-2940", title, span, ok)
		}
	}
	if _, ok := DetectChapterPreview(chapters([3]any{"Preview", 200.0, 230.0}), 3004); ok {
		t.Fatal("a preview named in the first half of the file was accepted")
	}
}

func TestPreviewStartInsidePrefersTheEarlierBound(t *testing.T) {
	stored := 2905.0
	candidate := Candidate{
		DurationSeconds: 3004,
		PreviewStart:    &stored,
		Chapters:        chapters([3]any{"Next Episode", 2898.0, 2940.0}),
	}
	start, ok := previewStartInside(candidate, Segment{Start: 2700, End: 3004})
	if !ok || start != 2898 {
		t.Fatalf("preview bound %.2f %v, want the chapter's 2898", start, ok)
	}
}

func TestPreviewStartInsideIgnoresABoundOutsideTheCredits(t *testing.T) {
	stored := 2600.0
	candidate := Candidate{DurationSeconds: 3004, PreviewStart: &stored}
	if _, ok := previewStartInside(candidate, Segment{Start: 2700, End: 3004}); ok {
		t.Fatal("a preview before the credits moved their end")
	}
}

func TestApplyCreditsGuardsEndsCreditsAtAChapterPreview(t *testing.T) {
	candidate := Candidate{
		DurationSeconds: 3004,
		Chapters:        chapters([3]any{"Credits", 2700.0, 2940.0}, [3]any{"Next Episode", 2940.0, 2990.0}),
	}
	segment, ok := applyCreditsGuards(Segment{Start: 2700, End: 3004, Confidence: 0.9, Algorithm: CreditsAudioAlgorithm}, candidate, creditsLimitsFor(false))
	if !ok {
		t.Fatal("guards refused the segment")
	}
	if segment.End != 2940 {
		t.Fatalf("credits ended at %.2f, want the preview's start 2940", segment.End)
	}
}

func TestDetectPreviewsNeedsAgreementAndACard(t *testing.T) {
	// Three episodes end with the same 30-second preview after their credits;
	// the fourth ends with no credits at all.
	var inputs []previewInput
	for i := 1; i <= 4; i++ {
		candidate := Candidate{FileID: i, EpisodeID: string(rune('a' + i)), DurationSeconds: 3004}
		input := previewInput{Candidate: candidate, CreditsEnd: 2974, HasCredits: true}
		if i <= 3 {
			input.Keyframes = []creditsKeyframe{
				{Seconds: 2975, Class: keyframeContent},
				{Seconds: 2999, Class: keyframeCard},
			}
		}
		if i == 4 {
			input.HasCredits = false
		}
		inputs = append(inputs, input)
	}
	previews := detectPreviews(inputs)
	if len(previews) != 3 {
		t.Fatalf("found %d previews, want 3", len(previews))
	}
	for i := 1; i <= 3; i++ {
		got, ok := previews[i]
		if !ok || got.Start != 2974 || got.End != 3004 {
			t.Fatalf("file %d: preview %v %v, want 2974-3004", i, got, ok)
		}
		if got.Algorithm != PreviewAlgorithm {
			t.Fatalf("file %d: preview algorithm %q", i, got.Algorithm)
		}
	}
}

func TestDetectPreviewsWithoutAnEndCard(t *testing.T) {
	var inputs []previewInput
	for i := 1; i <= 3; i++ {
		inputs = append(inputs, previewInput{
			Candidate:  Candidate{FileID: i, EpisodeID: string(rune('a' + i)), DurationSeconds: 3004},
			CreditsEnd: 2974,
			HasCredits: true,
			Keyframes:  []creditsKeyframe{{Seconds: 2980, Class: keyframeContent}},
		})
	}
	if previews := detectPreviews(inputs); len(previews) != 0 {
		t.Fatalf("previews without a card: %+v", previews)
	}
}

func TestDetectPreviewsRefusesSceneLeftovers(t *testing.T) {
	// A tail that is three minutes of story after the credits is not a
	// preview, however alike the files are.
	var inputs []previewInput
	for i := 1; i <= 3; i++ {
		inputs = append(inputs, previewInput{
			Candidate:  Candidate{FileID: i, EpisodeID: string(rune('a' + i)), DurationSeconds: 3004},
			CreditsEnd: 2800,
			HasCredits: true,
			Keyframes:  []creditsKeyframe{{Seconds: 3000, Class: keyframeCard}},
		})
	}
	if previews := detectPreviews(inputs); len(previews) != 0 {
		t.Fatalf("long tails read as previews: %+v", previews)
	}
}
