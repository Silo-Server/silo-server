package intromarkers

import (
	"math"
	"testing"

	"github.com/Silo-Server/silo-server/internal/models"
)

// Tests for the cases a review of the first version found: evidence that is
// not there must not act like evidence against a span, versions of one episode
// must not confirm each other, a weak stretch must not veto the partners that
// agree, and a chapter list must not walk an opening back onto the logo.

func TestSpanConfirmationIgnoresVersionsOfOneEpisode(t *testing.T) {
	// Files 1 and 2 are two versions of episode A and share their opening;
	// files 3 and 4 are episodes B and C, whose openings nothing shares. The
	// versions must not confirm each other: with nothing else confirming, the
	// season confirmed nothing and every span is left as the envelope placed
	// it, rather than the twins passing the season's share test between them.
	theme := randomPoints(240, 7, 7)
	segments := map[int]Segment{
		1: {Start: 600, End: 630, Algorithm: LevelAlgorithm, Confidence: 0.9},
		2: {Start: 601, End: 631, Algorithm: LevelAlgorithm, Confidence: 0.9},
		3: {Start: 700, End: 730, Algorithm: LevelAlgorithm, Confidence: 0.9},
		4: {Start: 800, End: 830, Algorithm: LevelAlgorithm, Confidence: 0.9},
	}
	spans := map[int][]uint32{
		1: spanPoints(randomPoints(240, 1, 1), theme, 2),
		2: spanPoints(randomPoints(240, 1, 1), theme, 3),
		3: spanPoints(randomPoints(240, 3, 3), randomPoints(240, 4, 4), 2),
		4: spanPoints(randomPoints(240, 5, 5), randomPoints(240, 6, 6), 2),
	}
	for _, episodes := range []map[int]string{
		{1: "a", 2: "a", 3: "b", 4: "c"},
		{1: "a", 2: "b", 3: "c", 4: "d"},
	} {
		candidates := map[int]Candidate{}
		for fileID, episode := range episodes {
			candidates[fileID] = Candidate{FileID: fileID, EpisodeID: episode, DurationSeconds: 3000}
		}
		out := decideSpanConfirmations(spans, segments, candidates, func(left, right int) matchProfile {
			return spanProfile(math.Max(segments[left].End-segments[left].Start, segments[right].End-segments[right].Start))
		})
		sameEpisode := episodes[1] == episodes[2]
		confirmed := out != nil && out[1].Algorithm == SpanConfirmedAlgorithm
		if sameEpisode && confirmed {
			t.Fatal("two versions of one episode confirmed each other's span")
		}
		if !sameEpisode && !confirmed {
			t.Fatal("two episodes sharing their opening did not confirm it")
		}
	}
}

func TestSpanConfirmationKeepsASpanWhoseFingerprintIsMissing(t *testing.T) {
	// Files 1 and 3 confirm each other; file 2 has no fingerprint at all,
	// because its extraction failed. Two of three is the season's share, and
	// file 2 must still be kept: no fingerprint is not a measured non-match.
	theme := randomPoints(240, 7, 7)
	segments := map[int]Segment{
		1: {Start: 600, End: 630, Algorithm: LevelAlgorithm, Confidence: 0.9},
		2: {Start: 700, End: 730, Algorithm: LevelAlgorithm, Confidence: 0.9},
		3: {Start: 800, End: 830, Algorithm: LevelAlgorithm, Confidence: 0.9},
	}
	spans := map[int][]uint32{
		1: spanPoints(randomPoints(240, 1, 1), theme, 2),
		3: spanPoints(randomPoints(240, 3, 3), theme, 2),
	}
	candidates := map[int]Candidate{
		1: {FileID: 1, EpisodeID: "a", DurationSeconds: 3000},
		2: {FileID: 2, EpisodeID: "b", DurationSeconds: 3000},
		3: {FileID: 3, EpisodeID: "c", DurationSeconds: 3000},
	}
	out := decideSpanConfirmations(spans, segments, candidates, func(left, right int) matchProfile {
		return spanProfile(math.Max(segments[left].End-segments[left].Start, segments[right].End-segments[right].Start))
	})
	if _, ok := out[2]; !ok {
		t.Fatal("a span whose fingerprint could not be read was dropped as if it had been contradicted")
	}
}

func TestSnapIntroToChaptersDoesNotWalkBackOntoTheLogo(t *testing.T) {
	// Chapters [0,4] and [4,34], opening at 4: one pass moves the start back
	// to 0, the logo, and a second pass cannot restore it because the forward
	// move it would need is wider than a snap. The opening must stay at 4.
	candidate := Candidate{
		DurationSeconds: 600,
		Chapters: []models.MediaChapter{
			{Title: "Logo", StartSeconds: 0, EndSeconds: 4},
			{Title: "Opening", StartSeconds: 4, EndSeconds: 34},
		},
	}
	start, end := snapIntroToChapters(candidate, Segment{Start: 4, End: 34})
	if start != 4 || end != 34 {
		t.Fatalf("chapter snap gave %.2f-%.2f, want 4.00-34.00", start, end)
	}
}

func TestCompareLevelsACreditCardDoesNotMoveACreditsStart(t *testing.T) {
	// The only cue after the placed start is a translator's card, which is
	// part of the credits: it must not be read as the last dialog.
	cues := []subtitleCue{
		{Start: 2600, End: 2688, Text: "You have to be kidding me."},
		{Start: 2705, End: 2710, Text: "Subtitles by Example Translations"},
	}
	segment := applyCreditCueHint(Segment{Start: 2690, End: 3000}, cues)
	if segment.Start != 2690 {
		t.Fatalf("credits start moved to %.2f on a translator's card, want 2690", segment.Start)
	}
}

func TestCompareLevelsAWeakStretchDoesNotVetoItsPartners(t *testing.T) {
	// Two partners carry the same opening cleanly; a third overlaps it with a
	// poor match. The weak stretch neither supports nor vetoes, so the two
	// that agree still place the intro.
	var inputs []levelsInput
	for i := 1; i <= 3; i++ {
		b := &traceBuilder{}
		b.dialog(int64(200+i), 30)
		if i == 3 {
			b.musicJittered(12345, 25, 12)
		} else {
			b.musicJittered(99, 25, 0.6)
		}
		rest := 1400 - b.seconds()
		if rest > 0 {
			b.dialog(int64(300+i), rest)
		}
		inputs = append(inputs, syntheticEpisode{
			fileID: i, episode: string(rune('a' + i)), duration: 1400, levels: b.build(),
		}.input())
	}
	segments := CompareLevels(inputs, DefaultConfig("ffmpeg"))
	if len(segments) < 2 {
		t.Fatalf("the two partners that agreed placed %d intros, want at least 2", len(segments))
	}
}

func TestCompareLevelsAnEarlierRepriseWinsOnEqualSupport(t *testing.T) {
	// Every episode plays the same theme at the head and again, longer, later
	// in the episode. Equally supported, the earlier occurrence is the intro.
	var inputs []levelsInput
	for i := 1; i <= 4; i++ {
		b := &traceBuilder{}
		b.dialog(int64(10+i), 20)
		b.musicJittered(99, 22, 0.6)
		b.dialog(int64(40+i), 600-b.seconds())
		b.musicJittered(99, 90, 0.6)
		rest := 1400 - b.seconds()
		if rest > 0 {
			b.dialog(int64(60+i), rest)
		}
		inputs = append(inputs, syntheticEpisode{
			fileID: i, episode: string(rune('a' + i)), duration: 1400, levels: b.build(),
		}.input())
	}
	segments := CompareLevels(inputs, DefaultConfig("ffmpeg"))
	for fileID := 1; fileID <= 4; fileID++ {
		segment, ok := segments[fileID]
		if !ok {
			t.Fatalf("file %d: no intro", fileID)
		}
		if segment.Start > 30 {
			t.Fatalf("file %d: the longer reprise at %.1f won over the opening", fileID, segment.Start)
		}
	}
}

func TestSinglePartnerIsHeldToThePairingGate(t *testing.T) {
	// Two episodes, one of them matching the other at a correlation under the
	// pairing's gate: a single partner must not place an intro at the
	// season's looser gate.
	byPartner := map[string][]levelStretch{
		"b": {{Start: 20, End: 42, PartnerStart: 20, Correlation: 0.80}},
	}
	if _, ok := bestLevelCluster(byPartner, Candidate{FileID: 1, EpisodeID: "a", DurationSeconds: 1400}, DefaultConfig("ffmpeg")); ok {
		t.Fatal("one partner at 0.80 was accepted as an intro; the pairing's gate is 0.85")
	}
	byPartner["b"][0].Correlation = 0.92
	if _, ok := bestLevelCluster(byPartner, Candidate{FileID: 1, EpisodeID: "a", DurationSeconds: 1400}, DefaultConfig("ffmpeg")); !ok {
		t.Fatal("one partner at 0.92 was rejected; a clear pairing is still an intro")
	}
}

func TestMergeAdjacentPairsKeepsDifferentAlignmentsApart(t *testing.T) {
	// Two runs ten seconds apart at the same alignment are one stretch split
	// by a quiet passage; two runs at different alignments are two
	// correspondences, and merging them would report a partner span that does
	// not cover the second.
	env := newEnvelope([]float64{60, 60, 60, 60, 50, 60, 60, 60, 60, 60, 60, 60}, LevelsHopSeconds)
	same := []levelPair{
		{Start: 10, End: 20, PartnerStart: 30, Correlation: 0.9},
		{Start: 20.5, End: 30, PartnerStart: 40.5, Correlation: 0.9},
	}
	if merged := mergeAdjacentPairs(env, same); len(merged) != 1 {
		t.Fatalf("one alignment split by a dip became %d runs, want 1", len(merged))
	}
	shifted := []levelPair{
		{Start: 10, End: 20, PartnerStart: 30, Correlation: 0.9},
		{Start: 20.5, End: 30, PartnerStart: 60, Correlation: 0.9},
	}
	if merged := mergeAdjacentPairs(env, shifted); len(merged) != 2 {
		t.Fatalf("two alignments became %d runs, want 2", len(merged))
	}
}
