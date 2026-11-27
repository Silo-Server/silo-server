package intromarkers

import (
	"math"
	"testing"

	"github.com/Silo-Server/silo-server/internal/models"
)

// testCue builds one cue.
func testCue(start, end float64, text string) subtitleCue {
	return subtitleCue{Start: start, End: end, Text: text}
}

func TestCueRecapReadsTheRecapCard(t *testing.T) {
	cues := []subtitleCue{
		testCue(60, 63, "Previously on Silo:"),
		testCue(63.2, 66, "the reactor went dark."),
		testCue(70, 72, "We have to go back."),
		testCue(200, 202, "Two months later."),
	}
	recap, ok := cueRecap(cues, 3000)
	if !ok {
		t.Fatal("the recap was not found")
	}
	if math.Abs(recap.Start-60) > 0.01 || math.Abs(recap.End-72) > 0.01 {
		t.Fatalf("recap %.2f-%.2f, want 60.00-72.00", recap.Start, recap.End)
	}
}

func TestCueRecapIgnoresMidEpisodeMentions(t *testing.T) {
	cues := []subtitleCue{testCue(900, 902, "Previously on Silo, we saw the reactor.")}
	if _, ok := cueRecap(cues, 3000); ok {
		t.Fatal("a line said in the episode was read as the recap")
	}
}

func TestCueRecapNeedsMoreThanAWord(t *testing.T) {
	cues := []subtitleCue{testCue(10, 11, "Recap:")}
	if _, ok := cueRecap(cues, 3000); ok {
		t.Fatal("a one-word card was read as a recap")
	}
}

func TestApplyIntroCueHintsStartsAfterTheRecap(t *testing.T) {
	candidate := Candidate{DurationSeconds: 3000}
	cues := []subtitleCue{
		testCue(30, 33, "Previously on Silo:"),
		testCue(33, 90, "the door opened."),
	}
	segment := Segment{Start: 40, End: 2900, Algorithm: LevelAlgorithm, Confidence: 0.9}
	got := applyIntroCueHints(candidate, segment, cues)
	if math.Abs(got.Start-90) > 0.01 {
		t.Fatalf("start %.2f, want the recap's end 90.00", got.Start)
	}
	// An intro that starts after the recap keeps its own start.
	segment = Segment{Start: 95, End: 125, Algorithm: LevelAlgorithm, Confidence: 0.9}
	if got := applyIntroCueHints(candidate, segment, cues); got.Start != 95 {
		t.Fatalf("start moved to %.2f with no recap in the way", got.Start)
	}
}

func TestCueRecapBoundsTheRecapWithItsFirstAndLastCue(t *testing.T) {
	cues := []subtitleCue{
		testCue(300, 302, "Last time on Silo"),
		testCue(302.5, 305, "the generator failed"),
		testCue(309, 312, "and the lights went out"),
	}
	recap, ok := cueRecap(cues, 3000)
	if !ok {
		t.Fatal("no recap")
	}
	if recap.Start != 300 || recap.End != 312 {
		t.Fatalf("recap %.2f-%.2f, want 300.00-312.00", recap.Start, recap.End)
	}
	// A cue a long silence after the recap is not part of it.
	cues = append(cues, testCue(340, 342, "Later that night."))
	if recap, ok := cueRecap(cues, 3000); !ok || recap.End != 312 {
		t.Fatalf("recap %+v, want it to end at 312", recap)
	}
}

func TestCueDialogEndFindsWhereDialogStopped(t *testing.T) {
	cues := []subtitleCue{
		testCue(100, 103, "one"),
		testCue(2580, 2584, "the last line before the credits"),
		testCue(2900, 2902, "a translator's card"),
	}
	end, ok := cueDialogEnd(cues, 2600, 30)
	if !ok || math.Abs(end-2584) > 0.01 {
		t.Fatalf("dialog end %.2f %v, want 2584.00", end, ok)
	}
	if _, ok := cueDialogEnd(cues, 2700, 30); ok {
		t.Fatal("a cue 100 seconds back was taken as the end of dialog")
	}
}

func TestApplyCreditCueHintMovesALateCreditsStart(t *testing.T) {
	// The music matched from 2590 but the dialog ran to 2601: the credits
	// do not start before the dialog stops.
	cues := []subtitleCue{testCue(2595, 2601, "Goodbye, then.")}
	segment := Segment{Start: 2590, End: 3004, Confidence: 0.9, Algorithm: CreditsAudioAlgorithm}
	got := applyCreditCueHint(segment, cues)
	if math.Abs(got.Start-2601.2) > 0.01 {
		t.Fatalf("credits start %.2f, want 2601.20", got.Start)
	}
	// A cue well past the placed start changes nothing.
	segment = Segment{Start: 2590, End: 3004, Confidence: 0.9, Algorithm: CreditsAudioAlgorithm}
	if got := applyCreditCueHint(segment, []subtitleCue{testCue(2700, 2702, "far later")}); got.Start != 2590 {
		t.Fatalf("start moved to %.2f by an unrelated cue", got.Start)
	}
}

func TestApplyCreditCueHintKeepsATranslatorCardInsideTheCredits(t *testing.T) {
	cues := []subtitleCue{
		testCue(2705, 2710, "Subtitles by Example Translations"),
	}
	segment := Segment{Start: 2690, End: 3004, Confidence: 0.9, Algorithm: CreditsVideoAlgorithm}
	got := applyCreditCueHint(segment, cues)
	if got.Start > 2705 {
		t.Fatalf("credits start %.2f is past the translator's card at 2705", got.Start)
	}
}

func TestCueTrackPicksAnEnglishTextTrack(t *testing.T) {
	tracks := []models.SubtitleTrack{
		{Index: 0, Language: "spa", Codec: "subrip"},
		{Index: 1, Language: "en", Codec: "hdmv_pgs_subtitle"},
		{Index: 2, Language: "eng", Codec: "subrip"},
		{Index: 3, Language: "eng", Codec: "ass", Forced: true},
	}
	track, ok := cueTrack(tracks)
	if !ok || track.Index != 2 {
		t.Fatalf("picked %+v, want the English text track at index 2", track)
	}
	if _, ok := cueTrack([]models.SubtitleTrack{{Index: 0, Language: "eng", Codec: "hdmv_pgs_subtitle"}}); ok {
		t.Fatal("an image subtitle was picked as a cue source")
	}
}

func TestCuePayloadRoundTrip(t *testing.T) {
	in := []subtitleCue{
		testCue(1.25, 3.5, "one"),
		testCue(1200, 1202.75, "two, with ünïcode and a comma"),
	}
	out, err := decodeCues(encodeCues(in))
	if err != nil {
		t.Fatalf("decoding: %v", err)
	}
	if len(out) != len(in) {
		t.Fatalf("decoded %d cues, want %d", len(out), len(in))
	}
	for i := range in {
		if out[i] != in[i] {
			t.Fatalf("cue %d is %+v, want %+v", i, out[i], in[i])
		}
	}
	if _, err := decodeCues([]byte{1, 2, 3}); err == nil {
		t.Fatal("a truncated payload decoded")
	}
}
