package intromarkers

import (
	"fmt"
	"math"
	"math/rand/v2"
	"testing"
)

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
	for fileID, segment := range segments {
		if segment.Algorithm != ChromaprintAlgorithm {
			t.Fatalf("file %d algorithm = %q, want %q", fileID, segment.Algorithm, ChromaprintAlgorithm)
		}
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

func TestCompareFingerprintsCountsEpisodeVersionsOnce(t *testing.T) {
	// Episode 2 has five versions whose shared audio runs 40 points past the
	// intro; episodes 3 and 4 carry only the intro. Five votes from one
	// episode must not outweigh two from others.
	intro := randomPoints(340, 0, 1)
	build := func(fileID int, episode string, number, length int) fingerprintInput {
		points := randomPoints(600, uint64(fileID), 5)
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
	intro := randomPoints(300, 0, 1)
	var inputs []fingerprintInput
	add := func(fileID, number int, shared bool) {
		points := randomPoints(500, uint64(fileID), 9)
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
	intro := randomPoints(300, 0, 1)
	var inputs []fingerprintInput
	add := func(fileID, number int, shared bool) {
		points := randomPoints(500, uint64(fileID), 11)
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

// randomPoints returns n fingerprint points drawn in order from a PCG seeded
// with seed and stream, so a fixture is the same on every run.
func randomPoints(n int, seed, stream uint64) []uint32 {
	rng := rand.New(rand.NewPCG(seed, stream))
	points := make([]uint32, n)
	for i := range points {
		points[i] = rng.Uint32()
	}
	return points
}
