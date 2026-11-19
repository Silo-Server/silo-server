package recommendations

import (
	"math"
	"slices"
	"testing"
)

func externalSims(n int, sim func(i int) float64, votes func(i int) int) []ExternalSimilarity {
	out := make([]ExternalSimilarity, n)
	for i := range out {
		out[i] = ExternalSimilarity{Key: ExternalTitleKey{MediaType: ExternalMediaMovie, TMDBID: i + 1}, Similarity: sim(i), VoteCount: votes(i)}
	}
	return out
}

// Both measures are standardized over the candidates and added with equal
// weight, so a title both close and well known leads.
func TestRankExternalCandidatesScores(t *testing.T) {
	sims := externalSims(externalMinCandidates, func(int) float64 { return 0.5 }, func(int) int { return 100 })
	sims[0].Similarity, sims[0].VoteCount = 0.9, 0      // close, unknown
	sims[1].Similarity, sims[1].VoteCount = 0.1, 10_000 // far, famous
	sims[2].Similarity, sims[2].VoteCount = 0.9, 10_000 // close, famous

	items := rankExternalCandidates(sims, nil, nil)
	if len(items) != len(sims) || items[0].TMDBID != 3 {
		t.Fatalf("ranked %+v, want title 3 first", items)
	}
	score := map[int]float64{}
	var sum float64
	for _, it := range items {
		score[it.TMDBID] = it.Score
		sum += it.Score
	}
	if math.Abs(sum) > 1e-9 {
		t.Fatalf("scores sum to %v; two z-scores over the same candidates sum to 0", sum)
	}
	z := standardize([]float64{0.9, 0.1, 0.9, 0.5, 0.5, 0.5, 0.5, 0.5, 0.5, 0.5})
	pop := standardize([]float64{0, math.Log1p(10_000), math.Log1p(10_000), math.Log1p(100), math.Log1p(100), math.Log1p(100), math.Log1p(100), math.Log1p(100), math.Log1p(100), math.Log1p(100)})
	for i := range 3 {
		if want := z[i] + pop[i]; math.Abs(score[i+1]-want) > 1e-9 {
			t.Fatalf("title %d score %v, want %v", i+1, score[i+1], want)
		}
	}
	// Equal scores keep TMDB ID order, so a rebuild is stable.
	var rest []int
	for _, it := range items {
		if it.TMDBID > 3 {
			rest = append(rest, it.TMDBID)
		}
	}
	if !slices.IsSorted(rest) {
		t.Fatalf("tied titles in order %v", rest)
	}
}

// A measure with no spread adds nothing, rather than dividing by zero.
func TestRankExternalCandidatesFlatMeasure(t *testing.T) {
	sims := externalSims(12, func(i int) float64 { return float64(i) / 12 }, func(int) int { return 50 })
	items := rankExternalCandidates(sims, nil, nil)
	for _, it := range items {
		if math.IsNaN(it.Score) || math.IsInf(it.Score, 0) {
			t.Fatalf("score %v", it.Score)
		}
	}
	if items[0].TMDBID != 12 || items[len(items)-1].TMDBID != 1 {
		t.Fatalf("with equal votes the order is by similarity; got %+v", items)
	}
}

// Fewer than the minimum candidates make no row, and a row keeps the best
// ExternalRowSize.
func TestRankExternalCandidatesSize(t *testing.T) {
	few := externalSims(externalMinCandidates-1, func(i int) float64 { return float64(i) }, func(int) int { return 1 })
	if items := rankExternalCandidates(few, nil, nil); items != nil {
		t.Fatalf("%d candidates gave a row of %d", len(few), len(items))
	}
	many := externalSims(ExternalRowSize+15, func(i int) float64 { return float64(i) }, func(int) int { return 1 })
	items := rankExternalCandidates(many, nil, nil)
	if len(items) != ExternalRowSize || items[0].TMDBID != ExternalRowSize+15 {
		t.Fatalf("row of %d led by %d", len(items), items[0].TMDBID)
	}
}

// Reasons are the seeds that named the title, best rank first as the pool
// orders them, as library content IDs: at most two, each once, leaving out
// seeds the profile no longer has.
func TestRankExternalCandidatesReasons(t *testing.T) {
	sims := externalSims(externalMinCandidates, func(i int) float64 { return float64(i) }, func(int) int { return 1 })
	key := sims[0].Key
	pool := map[ExternalTitleKey]ExternalPoolTitle{key: {Key: key, Seeds: []ExternalPoolSeed{
		{SourceKey: "seed:movie:9", BestRank: 1},
		{SourceKey: "seed:movie:7", BestRank: 2},
		{SourceKey: "seed:movie:8", BestRank: 3},
		{SourceKey: "seed:movie:6", BestRank: 4},
	}}}
	contentIDs := map[string]string{"seed:movie:7": "b", "seed:movie:8": "b", "seed:movie:6": "c", "seed:movie:1": "z"}
	items := rankExternalCandidates(sims, pool, contentIDs)
	for _, it := range items {
		switch {
		case it.TMDBID == key.TMDBID && !slices.Equal(it.Reasons, []string{"b", "c"}):
			t.Fatalf("reasons %v, want [b c]", it.Reasons)
		case it.TMDBID != key.TMDBID && (it.Reasons == nil || len(it.Reasons) != 0):
			t.Fatalf("title %d reasons %#v, want an empty list", it.TMDBID, it.Reasons)
		}
	}
}
