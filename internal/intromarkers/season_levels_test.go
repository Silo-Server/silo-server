package intromarkers

import (
	"math"
	"testing"
)

// season builds a season of episodes that all carry the same theme at the
// offsets given: each offset is where that episode's theme starts.
func season(offsets ...float64) []levelsInput {
	var inputs []levelsInput
	for i, offset := range offsets {
		b := &traceBuilder{}
		if offset > 0 {
			b.dialog(int64(10+i), offset)
		}
		b.musicJittered(99, 25, 0.6)
		rest := 1400 - b.seconds()
		if rest > 0 {
			b.dialog(int64(20+i), rest)
		}
		inputs = append(inputs, syntheticEpisode{
			fileID:   i + 1,
			episode:  string(rune('a' + i)),
			duration: 1400,
			levels:   b.build(),
		}.input())
	}
	return inputs
}

func wantSegment(t *testing.T, segments map[int]Segment, fileID int, start, end, tolerance float64) {
	t.Helper()
	got, ok := segments[fileID]
	if !ok {
		t.Fatalf("file %d: no intro, want %s", fileID, segmentRange(start, end))
	}
	if math.Abs(got.Start-start) > tolerance || math.Abs(got.End-end) > tolerance {
		t.Fatalf("file %d: intro %s, want %s within %.1fs", fileID, segmentRange(got.Start, got.End), segmentRange(start, end), tolerance)
	}
}

func TestCompareLevelsFindsIntroAtDifferentOffsets(t *testing.T) {
	offsets := []float64{20, 45, 5, 60}
	segments := CompareLevels(season(offsets...), DefaultConfig("ffmpeg"))
	if len(segments) != len(offsets) {
		t.Fatalf("found %d intros, want %d: %+v", len(segments), len(offsets), segments)
	}
	for i, offset := range offsets {
		wantSegment(t, segments, i+1, offset, offset+25, 1.5)
	}
}

func TestCompareLevelsPrefersTheHeadOpeningOverALaterReprise(t *testing.T) {
	// Every episode plays the same theme again 15 minutes in, longer than the
	// opening, as a show that reprises its theme over a montage does. The
	// intro is the one at the head.
	var inputs []levelsInput
	for i := 1; i <= 4; i++ {
		b := &traceBuilder{}
		b.dialog(int64(30+i), 20)
		b.musicJittered(99, 25, 0.6)
		b.dialog(int64(40+i), 800)
		b.musicJittered(99, 40, 0.6)
		b.dialog(int64(50+i), 1400-b.seconds())
		inputs = append(inputs, syntheticEpisode{
			fileID: i, episode: string(rune('a' + i)), duration: 1400, levels: b.build(),
		}.input())
	}
	segments := CompareLevels(inputs, DefaultConfig("ffmpeg"))
	for fileID := 1; fileID <= 4; fileID++ {
		wantSegment(t, segments, fileID, 20, 45, 1.5)
	}
}

func TestCompareLevelsDoesNotStartAtAStudioLogo(t *testing.T) {
	// The same four-second logo opens every episode, then a second of
	// silence, then the theme. A start at zero is the fault this comparison
	// replaces.
	var inputs []levelsInput
	for i := 1; i <= 4; i++ {
		b := &traceBuilder{}
		b.musicJittered(7, 4, 0.4)
		b.silence(1.2)
		b.musicJittered(99, 22, 0.6)
		b.dialog(int64(60+i), 1400-b.seconds())
		inputs = append(inputs, syntheticEpisode{
			fileID: i, episode: string(rune('a' + i)), duration: 1400, levels: b.build(),
		}.input())
	}
	segments := CompareLevels(inputs, DefaultConfig("ffmpeg"))
	for fileID := 1; fileID <= 4; fileID++ {
		got, ok := segments[fileID]
		if !ok {
			t.Fatalf("file %d: no intro found", fileID)
		}
		if got.Start < 4.5 {
			t.Fatalf("file %d: intro starts at %.2f, on the logo; want about 5.2", fileID, got.Start)
		}
	}
}

func TestCompareLevelsNeedsAgreementFromOtherEpisodes(t *testing.T) {
	// Episode 4's opening is its own; no other episode carries it.
	b := &traceBuilder{}
	b.musicJittered(77, 20, 0.5)
	b.dialog(41, 1380)
	own := syntheticEpisode{fileID: 4, episode: "d", duration: 1400, levels: b.build()}

	inputs := season(15, 15, 15)
	inputs = append(inputs, own.input())
	segments := CompareLevels(inputs, DefaultConfig("ffmpeg"))
	if _, ok := segments[4]; ok {
		t.Fatalf("episode 4's unmatched opening was reported as an intro: %s", describe(segments[4]))
	}
	if len(segments) != 3 {
		t.Fatalf("found %d intros, want the three that agree", len(segments))
	}
}

func TestCompareLevelsDoesNotCompareAVersionWithItself(t *testing.T) {
	// Two files of one episode are no season: there is no second episode to
	// agree with.
	inputs := season(20)
	second := inputs[0]
	second.Candidate.FileID = 2
	inputs = append(inputs, second)
	if segments := CompareLevels(inputs, DefaultConfig("ffmpeg")); len(segments) != 0 {
		t.Fatalf("a season of one episode produced %+v", segments)
	}
}

func TestCompareLevelsWideSearchFindsADistantPartner(t *testing.T) {
	// Episodes 1 and 12 share their opening; the ten in between carry
	// different ones. The neighbor window does not reach that far, so the
	// wide search has to.
	var inputs []levelsInput
	for i := 1; i <= 12; i++ {
		seed := int64(100 + i)
		if i == 1 || i == 12 {
			seed = 99
		}
		b := &traceBuilder{}
		b.dialog(int64(200+i), 30)
		b.musicJittered(seed, 20, 0.5)
		b.dialog(int64(300+i), 1400-b.seconds())
		inputs = append(inputs, syntheticEpisode{
			fileID: i, episode: string(rune('a' + i)), duration: 1400, levels: b.build(),
		}.input())
	}
	segments := CompareLevels(inputs, DefaultConfig("ffmpeg"))
	if _, ok := segments[1]; !ok {
		t.Fatal("episode 1's opening was not found through the wide search")
	}
	if _, ok := segments[12]; !ok {
		t.Fatal("episode 12's opening was not found through the wide search")
	}
}

func TestCompareLevelsAcceptsASeasonOfTwo(t *testing.T) {
	segments := CompareLevels(season(30, 30), DefaultConfig("ffmpeg"))
	if len(segments) != 2 {
		t.Fatalf("a season of two produced %+v", segments)
	}
	wantSegment(t, segments, 1, 30, 55, 1.5)
	wantSegment(t, segments, 2, 30, 55, 1.5)
}

func TestCompareLevelsRefusesAnOpeningCutOffByTheWindow(t *testing.T) {
	// The opening runs to the end of the analysis window, so where it ends is
	// unknown: the comparison reports nothing rather than a guess.
	var inputs []levelsInput
	for i := 1; i <= 4; i++ {
		b := &traceBuilder{}
		b.dialog(int64(400+i), 300)
		b.musicJittered(99, 60, 0.5)
		if b.seconds() < 360 {
			b.musicJittered(99, 360-b.seconds(), 0.5)
		}
		inputs = append(inputs, syntheticEpisode{
			fileID: i, episode: string(rune('a' + i)), duration: 1400, levels: b.build(),
		}.input())
	}
	if segments := CompareLevels(inputs, DefaultConfig("ffmpeg")); len(segments) != 0 {
		t.Fatalf("a clipped opening produced %+v", segments)
	}
}
