package intromarkers

import (
	"math"
	"testing"
)

// spanPoints builds the fingerprint points of a window: a random filler with
// the shared theme's points written into the middle.
func spanPoints(filler, theme []uint32, at int) []uint32 {
	points := append([]uint32(nil), filler...)
	copy(points[at:], theme)
	return points
}

// confirmSpans builds the map decideSpanConfirmations reads: each file's span
// points, its placed segment, and its candidate, all starting at the same
// offset so the pair comparison sees the theme where the segments say it is.
func confirmSpans(t *testing.T, segments map[int]Segment, shared map[int][]uint32) map[int]Segment {
	t.Helper()
	candidates := map[int]Candidate{}
	for fileID := range segments {
		candidates[fileID] = Candidate{FileID: fileID, EpisodeID: string(rune('a' + fileID)), DurationSeconds: 3000}
	}
	return decideSpanConfirmations(shared, segments, candidates, func(left, right int) matchProfile {
		return spanProfile(math.Max(segments[left].End-segments[left].Start, segments[right].End-segments[right].Start))
	})
}

func TestSpanConfirmationTightensAConfirmedIntro(t *testing.T) {
	// Two files carry the same 30-second opening; the envelope placed one of
	// them two seconds wide on each side.
	theme := randomPoints(240, 7, 7)
	other := randomPoints(240, 9, 9)
	segments := map[int]Segment{
		1: {Start: 600, End: 630, Algorithm: LevelAlgorithm, Confidence: 0.9},
		2: {Start: 700, End: 734, Algorithm: LevelAlgorithm, Confidence: 0.9},
	}
	// Window 1 starts at 598; the theme begins 2 s in. Window 2 starts at
	// 698; the theme begins 2 s in as well.
	shared := map[int][]uint32{
		1: spanPoints(randomPoints(300, 1, 1), theme, 20),
		2: spanPoints(other, theme, 20),
	}
	got := confirmSpans(t, segments, shared)
	if len(got) != 2 {
		t.Fatalf("confirmed %d spans, want both: %+v", len(got), got)
	}
	placed := map[int]Segment{1: {Start: 600, End: 630}, 2: {Start: 700, End: 734}}
	for fileID, segment := range got {
		if segment.Algorithm != SpanConfirmedAlgorithm {
			t.Fatalf("file %d: algorithm %q, want the confirmed identifier", fileID, segment.Algorithm)
		}
		// The fingerprint's own leads trim a second or so off each edge; the
		// span stays inside what the envelope placed.
		want := placed[fileID]
		if segment.Start < want.Start || segment.Start > want.Start+6 || segment.End > want.End || segment.End < want.End-10 {
			t.Fatalf("file %d: span %.2f-%.2f, want inside %s", fileID, segment.Start, segment.End, segmentRange(want.Start, want.End))
		}
	}
}

func TestSpanConfirmationDropsASpanTheSeasonContradicts(t *testing.T) {
	// Three files share one opening; the fourth's envelope matched a shared
	// logo that no fingerprint carries.
	theme := randomPoints(240, 7, 7)
	segments := map[int]Segment{
		1: {Start: 600, End: 630, Algorithm: LevelAlgorithm, Confidence: 0.9},
		2: {Start: 600, End: 630, Algorithm: LevelAlgorithm, Confidence: 0.9},
		3: {Start: 600, End: 630, Algorithm: LevelAlgorithm, Confidence: 0.9},
		4: {Start: 900, End: 930, Algorithm: LevelAlgorithm, Confidence: 0.9},
	}
	shared := map[int][]uint32{
		1: spanPoints(randomPoints(300, 1, 1), theme, 20),
		2: spanPoints(randomPoints(300, 2, 2), theme, 20),
		3: spanPoints(randomPoints(300, 3, 3), theme, 20),
		4: randomPoints(300, 4, 4),
	}
	got := confirmSpans(t, segments, shared)
	if _, ok := got[4]; ok {
		t.Fatalf("an unconfirmed span survived a confirmed season: %+v", got[4])
	}
	if len(got) != 3 {
		t.Fatalf("confirmed %d spans, want the three that agree", len(got))
	}
}

func TestSpanConfirmationLeavesASeasonNothingConfirmedAlone(t *testing.T) {
	// Two files whose fingerprints do not agree: the check produced no
	// evidence at all, so the envelope's result stands untouched rather than
	// losing confidence for a fingerprint that may simply not have arrived.
	segments := map[int]Segment{
		1: {Start: 600, End: 630, Algorithm: LevelAlgorithm, Confidence: 0.9},
		2: {Start: 700, End: 730, Algorithm: LevelAlgorithm, Confidence: 0.9},
	}
	shared := map[int][]uint32{
		1: randomPoints(300, 1, 1),
		2: randomPoints(300, 2, 2),
	}
	if got := confirmSpans(t, segments, shared); got != nil {
		t.Fatalf("an unconfirmed pair was decided: %+v", got)
	}
}

func TestSpanWindowAddsMarginAndClipsAtTheFileEnd(t *testing.T) {
	candidate := Candidate{DurationSeconds: 3000}
	window := spanWindow(candidate, Segment{Start: 600, End: 630})
	if window.Start != 598 || window.End != 632 {
		t.Fatalf("window %.2f-%.2f, want 598.00-632.00", window.Start, window.End)
	}
	window = spanWindow(candidate, Segment{Start: 0, End: 2999})
	if window.Start != 0 || window.End != 3000 {
		t.Fatalf("window %.2f-%.2f, want it clipped to the file", window.Start, window.End)
	}
}
