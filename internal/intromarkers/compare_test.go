package intromarkers

import (
	"fmt"
	"math"
	"math/rand/v2"
	"sort"
	"testing"
)

func TestCompareFingerprintsFindsSharedRange(t *testing.T) {
	left := make([]uint32, 400)
	right := make([]uint32, 400)
	for i := range left {
		left[i] = uint32(i + 1000)
		right[i] = uint32(i + 5000)
	}
	for i := 40; i < 300; i++ {
		left[i] = uint32(i)
		right[i] = uint32(i)
	}

	segments := CompareFingerprints([]fingerprintInput{
		{Candidate: Candidate{FileID: 1, EpisodeID: "ep1", DurationSeconds: 1200}, Points: left},
		{Candidate: Candidate{FileID: 2, EpisodeID: "ep2", DurationSeconds: 1200}, Points: right},
	}, DefaultConfig("ffmpeg"))

	if len(segments) != 2 {
		t.Fatalf("expected two file segments, got %d", len(segments))
	}
	if got := segments[1].End - segments[1].Start; got < 30 {
		t.Fatalf("expected at least 30s segment, got %.3f", got)
	}
	if segments[1].Algorithm != ChromaprintAlgorithm {
		t.Fatalf("unexpected algorithm %q", segments[1].Algorithm)
	}
}

func TestCompareFingerprintsSkipsSameEpisodePairs(t *testing.T) {
	points := make([]uint32, 400)
	for i := range points {
		points[i] = uint32(i)
	}
	segments := CompareFingerprints([]fingerprintInput{
		{Candidate: Candidate{FileID: 1, EpisodeID: "ep1", DurationSeconds: 1200}, Points: points},
		{Candidate: Candidate{FileID: 2, EpisodeID: "ep1", DurationSeconds: 1200}, Points: points},
	}, DefaultConfig("ffmpeg"))
	if len(segments) != 0 {
		t.Fatalf("same-episode fingerprints must not produce segments: %#v", segments)
	}
}

func TestCompareFingerprintsFindsSharedRangeWithOffset(t *testing.T) {
	left := make([]uint32, 700)
	right := make([]uint32, 700)
	for i := range left {
		left[i] = 0xAAAAAAAA ^ uint32(i)
		right[i] = 0x55555555 ^ uint32(i*3)
	}
	for i := 40; i < 320; i++ {
		point := uint32((i * 17) + 12345)
		left[i] = point
		right[i+160] = point
	}

	segments := CompareFingerprints([]fingerprintInput{
		{Candidate: Candidate{FileID: 1, EpisodeID: "ep1", DurationSeconds: 1200}, Points: left},
		{Candidate: Candidate{FileID: 2, EpisodeID: "ep2", DurationSeconds: 1200}, Points: right},
	}, DefaultConfig("ffmpeg"))

	if len(segments) != 2 {
		t.Fatalf("expected two file segments, got %d", len(segments))
	}
	// 40 points in, shifted back by the Chromaprint start lead.
	if want := 40*DefaultPointHopSeconds + chromaprintStartLeadSeconds; math.Abs(segments[1].Start-want) > 0.01 {
		t.Fatalf("left start = %.3f, want %.3f", segments[1].Start, want)
	}
	if segments[2].Start < 24 || segments[2].Start > 27 {
		t.Fatalf("unexpected right start %.3f", segments[2].Start)
	}
	if got := segments[1].End - segments[1].Start; got < 30 {
		t.Fatalf("expected at least 30s segment, got %.3f", got)
	}
}

func TestComparePairAtShiftBreaksRunsOnBackwardRightJump(t *testing.T) {
	left := make([]uint32, 160)
	right := make([]uint32, 160)
	for i := range left {
		left[i] = 0xAAAAAAAA ^ uint32(i*17)
		right[i] = 0x55555555 ^ uint32(i*31)
	}
	left[0] = 0x11111111
	right[30] = 0x11111111
	for i := 1; i < 120; i++ {
		point := 0x22220000 + uint32(i)
		left[i] = point
		right[i-1] = point
	}

	cfg := DefaultConfig("ffmpeg")
	cfg.MinimumIntroDurationSeconds = 1
	leftSegment, rightSegment, ok := comparePairAtShift(left, right, cfg, 0)
	if !ok {
		t.Fatal("expected monotonic run after backward jump")
	}
	if leftSegment.Start == 0 {
		t.Fatalf("backward right jump should start a new run, got left segment %+v right segment %+v", leftSegment, rightSegment)
	}
}

func TestComparePairAtShiftAllowsSmallBackwardJitter(t *testing.T) {
	left := make([]uint32, 180)
	right := make([]uint32, 180)
	for i := range left {
		left[i] = 0xAAAAAAAA ^ uint32(i*17)
		right[i] = 0x55555555 ^ uint32(i*31)
	}
	for i := 10; i < 150; i++ {
		point := 0x33330000 + uint32(i)
		left[i] = point
		right[i] = point
	}
	right[70] = 0x55555555
	right[65] = left[71]

	cfg := DefaultConfig("ffmpeg")
	cfg.MinimumIntroDurationSeconds = 1
	leftSegment, _, ok := comparePairAtShift(left, right, cfg, 0)
	if !ok {
		t.Fatal("expected small backward jitter to remain in the same run")
	}
	if got := leftSegment.End - leftSegment.Start; got < 15 {
		t.Fatalf("expected jitter-tolerant run, got duration %.3f", got)
	}
}

func TestConsensusSegmentUsesMedianOfAgreeingPairs(t *testing.T) {
	segment, confirmations := consensusSegment(map[string][]Segment{
		"e2": {{Start: 60, End: 150}}, // One pair ran long into shared music after the intro.
		"e3": {{Start: 60, End: 120}},
		"e4": {{Start: 61, End: 121}},
		"e5": {{Start: 59, End: 119}},
		"e6": {{Start: 600, End: 640}}, // An unrelated shared cue elsewhere in the episode.
	})
	if confirmations != 4 {
		t.Fatalf("confirmations = %d, want the four overlapping results", confirmations)
	}
	if segment.Start != 60 || segment.End != 120.5 {
		t.Fatalf("consensus = %+v, want median 60-120.5 rather than the longest result", segment)
	}
}

func TestAdjustSegmentSnapsOnlyNearZeroStarts(t *testing.T) {
	candidate := Candidate{DurationSeconds: 1800}
	nearZero := adjustSegment(Segment{Start: 0.4, End: 60}, candidate)
	if nearZero.Start != 0 {
		t.Fatalf("start %.2f within the snap window should become 0", nearZero.Start)
	}
	afterLogo := adjustSegment(Segment{Start: 4, End: 60}, candidate)
	if want := 4 + chromaprintStartLeadSeconds; afterLogo.Start != want {
		t.Fatalf("start after a short logo = %.2f, want %.2f", afterLogo.Start, want)
	}
	if want := 60 + chromaprintEndLeadSeconds; afterLogo.End != want {
		t.Fatalf("end = %.2f, want %.2f", afterLogo.End, want)
	}
}

func TestCompareFingerprintsLimitsComparisonsToNeighbors(t *testing.T) {
	// Episodes 1-10 share one intro; only neighbors within
	// compareNeighborEpisodes are compared, and every file still matches.
	const episodes = 10
	intro := make([]uint32, 300)
	introRNG := rand.New(rand.NewPCG(0, 1))
	for i := range intro {
		intro[i] = introRNG.Uint32()
	}
	inputs := make([]fingerprintInput, 0, episodes)
	for e := 1; e <= episodes; e++ {
		points := make([]uint32, 500)
		rng := rand.New(rand.NewPCG(uint64(e), 1))
		for i := range points {
			points[i] = rng.Uint32()
		}
		copy(points[100:], intro)
		inputs = append(inputs, fingerprintInput{
			Candidate: Candidate{FileID: 100 + e, EpisodeID: string(rune('a' + e)), EpisodeNumber: episodes + 1 - e, DurationSeconds: 1800},
			Points:    points,
		})
	}
	segments := CompareFingerprints(inputs, DefaultConfig("ffmpeg"))
	if len(segments) != episodes {
		t.Fatalf("matched %d files, want %d", len(segments), episodes)
	}
	for fileID, segment := range segments {
		if want := 100*DefaultPointHopSeconds + chromaprintStartLeadSeconds; math.Abs(segment.Start-want) > 0.2 {
			t.Fatalf("file %d start = %.2f, want %.2f", fileID, segment.Start, want)
		}
	}
}

func TestCompareFingerprintsWidensSearchForUnmatchedFiles(t *testing.T) {
	// Episode 1 shares its intro only with episode 12, beyond its eight
	// neighbors; the other episodes share nothing.
	intro := make([]uint32, 300)
	introRNG := rand.New(rand.NewPCG(0, 1))
	for i := range intro {
		intro[i] = introRNG.Uint32()
	}
	inputs := make([]fingerprintInput, 0, 12)
	for e := 1; e <= 12; e++ {
		points := make([]uint32, 500)
		rng := rand.New(rand.NewPCG(uint64(e), 3))
		for i := range points {
			points[i] = rng.Uint32()
		}
		if e == 1 || e == 12 {
			copy(points[100:], intro)
		}
		inputs = append(inputs, fingerprintInput{
			Candidate: Candidate{FileID: e, EpisodeID: string(rune('a' + e)), EpisodeNumber: e, DurationSeconds: 1800},
			Points:    points,
		})
	}
	segments := CompareFingerprints(inputs, DefaultConfig("ffmpeg"))
	if _, ok := segments[1]; !ok {
		t.Fatal("episode 1 should match episode 12 through the wider search")
	}
	if _, ok := segments[12]; !ok {
		t.Fatal("episode 12 should match episode 1")
	}
	if len(segments) != 2 {
		t.Fatalf("matched %d files, want only the two that share an intro", len(segments))
	}
}

func TestCompareFingerprintsCountsEpisodeVersionsOnce(t *testing.T) {
	// Episode 2 has five versions whose shared audio runs 40 points past the
	// intro; episodes 3 and 4 carry only the intro. Five votes from one
	// episode must not outweigh two from others.
	intro := make([]uint32, 340)
	introRNG := rand.New(rand.NewPCG(0, 1))
	for i := range intro {
		intro[i] = introRNG.Uint32()
	}
	build := func(fileID int, episode string, number, length int) fingerprintInput {
		points := make([]uint32, 600)
		rng := rand.New(rand.NewPCG(uint64(fileID), 5))
		for i := range points {
			points[i] = rng.Uint32()
		}
		copy(points[100:], intro[:length])
		return fingerprintInput{
			Candidate: Candidate{FileID: fileID, EpisodeID: episode, EpisodeNumber: number, DurationSeconds: 1800},
			Points:    points,
		}
	}
	inputs := []fingerprintInput{build(1, "e1", 1, 340)}
	for v := 0; v < 5; v++ {
		inputs = append(inputs, build(20+v, "e2", 2, 340))
	}
	inputs = append(inputs, build(3, "e3", 3, 300), build(4, "e4", 4, 300))

	segments := CompareFingerprints(inputs, DefaultConfig("ffmpeg"))
	got := segments[1].End - segments[1].Start
	want := 300*DefaultPointHopSeconds + chromaprintEndLeadSeconds - chromaprintStartLeadSeconds
	longer := 340*DefaultPointHopSeconds + chromaprintEndLeadSeconds - chromaprintStartLeadSeconds
	if math.Abs(got-want) > math.Abs(got-longer) && math.Abs(got-want) > 1 {
		t.Fatalf("episode 1 intro = %.1fs, want the median of three episodes (%.1fs), not one episode's five versions (%.1fs)", got, want, longer)
	}
}

func TestCompareFingerprintsWindowCountsEpisodesNotVersions(t *testing.T) {
	// Episode 1's only partner is episode 10. Episode 2 has nine versions;
	// counted as one neighbor, the window still reaches episodes 3-9 and the
	// fallback reaches episode 10.
	intro := make([]uint32, 300)
	introRNG := rand.New(rand.NewPCG(0, 1))
	for i := range intro {
		intro[i] = introRNG.Uint32()
	}
	var inputs []fingerprintInput
	add := func(fileID, number int, shared bool) {
		points := make([]uint32, 500)
		rng := rand.New(rand.NewPCG(uint64(fileID), 9))
		for i := range points {
			points[i] = rng.Uint32()
		}
		if shared {
			copy(points[100:], intro)
		}
		inputs = append(inputs, fingerprintInput{
			Candidate: Candidate{FileID: fileID, EpisodeID: fmt.Sprintf("e%d", number), EpisodeNumber: number, DurationSeconds: 1800},
			Points:    points,
		})
	}
	add(1, 1, true)
	for v := 0; v < 9; v++ {
		add(200+v, 2, false)
	}
	for e := 3; e <= 9; e++ {
		add(e, e, false)
	}
	add(10, 10, true)
	segments := CompareFingerprints(inputs, DefaultConfig("ffmpeg"))
	if _, ok := segments[1]; !ok {
		t.Fatal("episode 1 should match episode 10")
	}
}

func TestConsensusSegmentPicksEachPartnersAgreeingVersion(t *testing.T) {
	// Episode e2's first version matched an unrelated cue and its second the
	// intro; the intro version is the one that agrees with the other partners.
	segment, confirmations := consensusSegment(map[string][]Segment{
		"e2": {{Start: 600, End: 640}, {Start: 60, End: 120}},
		"e3": {{Start: 61, End: 121}},
		"e4": {{Start: 59, End: 119}},
	})
	if confirmations != 3 || segment.Start != 60 || segment.End != 120 {
		t.Fatalf("consensus = %+v with %d partners, want 60-120 from all three", segment, confirmations)
	}
}

func TestCompareFingerprintsFallbackCountsEpisodesNotVersions(t *testing.T) {
	// Episode 1's only partner is episode 11. Episodes 2-9 fill the neighbor
	// window and episode 10 has 60 versions, more than the fallback budget
	// if versions were counted separately.
	intro := make([]uint32, 300)
	introRNG := rand.New(rand.NewPCG(0, 1))
	for i := range intro {
		intro[i] = introRNG.Uint32()
	}
	var inputs []fingerprintInput
	add := func(fileID, number int, shared bool) {
		points := make([]uint32, 500)
		rng := rand.New(rand.NewPCG(uint64(fileID), 11))
		for i := range points {
			points[i] = rng.Uint32()
		}
		if shared {
			copy(points[100:], intro)
		}
		inputs = append(inputs, fingerprintInput{
			Candidate: Candidate{FileID: fileID, EpisodeID: fmt.Sprintf("e%d", number), EpisodeNumber: number, DurationSeconds: 1800},
			Points:    points,
		})
	}
	add(1, 1, true)
	for e := 2; e <= 9; e++ {
		add(e, e, false)
	}
	for v := 0; v < 60; v++ {
		add(1000+v, 10, false)
	}
	add(11, 11, true)
	segments := CompareFingerprints(inputs, DefaultConfig("ffmpeg"))
	if _, ok := segments[1]; !ok {
		t.Fatal("episode 1 should reach episode 11 through the fallback")
	}
}

func TestCompareFingerprintsWidensSearchForFilesOnlyAnotherSearchReached(t *testing.T) {
	// Episodes 1 and 12 share a short cue; episodes 12, 30, and 31 share the
	// intro. Episodes 30 and 31 match each other as neighbors, so only
	// episode 12's own wider search can reach them. Episode 1's wider search
	// records the cue for episode 12 first, which must not cancel it.
	rng := rand.New(rand.NewPCG(0, 13))
	shared := func(n int) []uint32 {
		out := make([]uint32, n)
		for i := range out {
			out[i] = rng.Uint32()
		}
		return out
	}
	cue, intro := shared(140), shared(300)
	var inputs []fingerprintInput
	for e := 1; e <= 31; e++ {
		points := make([]uint32, 900)
		prng := rand.New(rand.NewPCG(uint64(e), 17))
		for i := range points {
			points[i] = prng.Uint32()
		}
		switch e {
		case 1:
			copy(points[600:], cue)
		case 12:
			copy(points[600:], cue)
			copy(points[100:], intro)
		case 30, 31:
			copy(points[100:], intro)
		}
		inputs = append(inputs, fingerprintInput{
			Candidate: Candidate{FileID: e, EpisodeID: fmt.Sprintf("e%d", e), EpisodeNumber: e, DurationSeconds: 3600},
			Points:    points,
		})
	}
	segments := CompareFingerprints(inputs, DefaultConfig("ffmpeg"))
	if _, ok := segments[30]; !ok {
		t.Fatal("episode 30 should match episode 12's intro")
	}
	if got := segments[12]; got.End-got.Start < 30 {
		t.Fatalf("episode 12 = %+v, want its 37s intro to win over the 17s cue", got)
	}
}

func TestCompareFingerprintsFallbackSearchesNearestEpisodesFirst(t *testing.T) {
	// Episode 10's only partner is episode 1, nine episodes to its left.
	// Episode 9 has 60 versions, so the 59 right-hand episodes the neighbor
	// pass skipped are fewer files away than episode 1. Walking files would
	// spend the fallback budget there first; walking episodes reaches episode
	// 1 at distance nine. Episode 1 matches episode 2 on a separate cue, so it
	// never runs a fallback of its own that could find episode 10.
	rng := rand.New(rand.NewPCG(0, 19))
	shared := func(n int) []uint32 {
		out := make([]uint32, n)
		for i := range out {
			out[i] = rng.Uint32()
		}
		return out
	}
	cue, intro := shared(140), shared(300)
	var inputs []fingerprintInput
	add := func(fileID, number int) {
		points := make([]uint32, 900)
		prng := rand.New(rand.NewPCG(uint64(fileID), 23))
		for i := range points {
			points[i] = prng.Uint32()
		}
		switch fileID {
		case 1:
			copy(points[100:], intro)
			copy(points[600:], cue)
		case 2:
			copy(points[600:], cue)
		case 10:
			copy(points[100:], intro)
		}
		inputs = append(inputs, fingerprintInput{
			Candidate: Candidate{FileID: fileID, EpisodeID: fmt.Sprintf("e%d", number), EpisodeNumber: number, DurationSeconds: 3600},
			Points:    points,
		})
	}
	for e := 1; e <= 8; e++ {
		add(e, e)
	}
	for v := 0; v < 60; v++ {
		add(1000+v, 9)
	}
	for e := 10; e <= 80; e++ {
		add(e, e)
	}
	segments := CompareFingerprints(inputs, DefaultConfig("ffmpeg"))
	got, ok := segments[10]
	if !ok {
		t.Fatal("episode 10 should reach episode 1 through the fallback")
	}
	if want := 100*DefaultPointHopSeconds + chromaprintStartLeadSeconds; math.Abs(got.Start-want) > 0.2 {
		t.Fatalf("episode 10 start = %.2f, want the shared intro at %.2f", got.Start, want)
	}
}

// seasonInputs builds episodes whose fingerprints share intro[:introLen[i]] at
// point 100, over random per-episode audio.
func seasonInputs(introLen []int) []fingerprintInput {
	intro := make([]uint32, 400)
	introRNG := rand.New(rand.NewPCG(0, 1))
	for i := range intro {
		intro[i] = introRNG.Uint32()
	}
	inputs := make([]fingerprintInput, 0, len(introLen))
	for e, length := range introLen {
		points := make([]uint32, 700)
		rng := rand.New(rand.NewPCG(uint64(e+1), 7))
		for i := range points {
			points[i] = rng.Uint32()
		}
		copy(points[100:], intro[:length])
		inputs = append(inputs, fingerprintInput{
			Candidate: Candidate{FileID: e + 1, EpisodeID: fmt.Sprintf("e%d", e+1), EpisodeNumber: e + 1, DurationSeconds: 1800},
			Points:    points,
		})
	}
	return inputs
}

func TestCompareFingerprintsRatesSeasonConsistentIntrosHighest(t *testing.T) {
	// Five episodes share a 300-point intro; the sixth carries only its first
	// 220 points, so its intro is shorter than the season's.
	segments := CompareFingerprints(seasonInputs([]int{300, 300, 300, 300, 300, 220}), DefaultConfig("ffmpeg"))
	if len(segments) != 6 {
		t.Fatalf("matched %d files, want 6", len(segments))
	}
	for fileID := 1; fileID <= 5; fileID++ {
		if got := segments[fileID].Confidence; got != chromaprintConsistentConfidence {
			t.Errorf("file %d confidence = %.2f, want season-consistent %.2f", fileID, got, chromaprintConsistentConfidence)
		}
	}
	if got := segments[6].Confidence; got != chromaprintInconsistentConfidence {
		t.Errorf("off-length file confidence = %.2f, want %.2f", got, chromaprintInconsistentConfidence)
	}
}

func TestCompareFingerprintsRatesShortMatchesLowest(t *testing.T) {
	// 140 points is about 17 seconds: long enough to match, short enough to be
	// a recurring music cue.
	segments := CompareFingerprints(seasonInputs([]int{140, 140, 140, 140}), DefaultConfig("ffmpeg"))
	if len(segments) != 4 {
		t.Fatalf("matched %d files, want 4", len(segments))
	}
	for fileID, segment := range segments {
		if segment.Confidence != chromaprintShortConfidence {
			t.Errorf("file %d confidence = %.2f, want %.2f", fileID, segment.Confidence, chromaprintShortConfidence)
		}
	}
}

func TestUsualIntroDurationPrefersLargestThenLongestCluster(t *testing.T) {
	durations := func(values ...float64) []episodeDuration {
		out := make([]episodeDuration, len(values))
		for i, v := range values {
			out[i] = episodeDuration{episode: fmt.Sprintf("e%d", i), seconds: v}
		}
		return out
	}
	usual, sharing := usualIntroDuration(durations(40, 40.5, 41, 90, 90.2, 90.4))
	if sharing != 3 || usual < 90 {
		t.Fatalf("usualIntroDuration = (%.1f, %d), want the longer of two equal clusters", usual, sharing)
	}
	usual, sharing = usualIntroDuration(durations(40, 40.5, 41, 41.2, 90))
	if sharing != 4 || usual < 40 || usual > 41.5 {
		t.Fatalf("usualIntroDuration = (%.1f, %d), want the 40-41s cluster of four", usual, sharing)
	}
	// Four versions of one episode count once against two other episodes.
	versions := []episodeDuration{
		{"e1", 90}, {"e1", 90.1}, {"e1", 90.2}, {"e1", 90.3},
		{"e2", 40}, {"e3", 40.4},
	}
	if usual, sharing = usualIntroDuration(versions); sharing != 2 || usual > 41 {
		t.Fatalf("usualIntroDuration = (%.1f, %d), want the two-episode 40s cluster", usual, sharing)
	}
}

func TestUsualIntroDurationMatchesPairwiseScan(t *testing.T) {
	// The sliding window must agree with the direct definition: for each
	// duration, count the distinct episodes within tolerance.
	rng := rand.New(rand.NewPCG(3, 7))
	for trial := 0; trial < 200; trial++ {
		var durations []episodeDuration
		for i := 0; i < 1+rng.IntN(40); i++ {
			durations = append(durations, episodeDuration{
				episode: fmt.Sprintf("e%d", rng.IntN(15)),
				seconds: 20 + float64(rng.IntN(60))/4,
			})
		}
		wantUsual, wantSharing := 0.0, 0
		sorted := append([]episodeDuration(nil), durations...)
		sort.Slice(sorted, func(i, j int) bool { return sorted[i].seconds < sorted[j].seconds })
		for _, candidate := range sorted {
			episodes := map[string]struct{}{}
			for _, other := range sorted {
				if math.Abs(other.seconds-candidate.seconds) <= seasonDurationToleranceSeconds {
					episodes[other.episode] = struct{}{}
				}
			}
			if len(episodes) >= wantSharing {
				wantUsual, wantSharing = candidate.seconds, len(episodes)
			}
		}
		if usual, sharing := usualIntroDuration(durations); usual != wantUsual || sharing != wantSharing {
			t.Fatalf("trial %d: usualIntroDuration = (%.2f, %d), want (%.2f, %d)", trial, usual, sharing, wantUsual, wantSharing)
		}
	}
}
