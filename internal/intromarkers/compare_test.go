package intromarkers

import (
	"math/rand/v2"
	"testing"
)

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
