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
// A candidate whose vector has no cosine (an all-zero vector) is left out
// and the others keep finite scores, so the row can still be stored.
func TestRankExternalCandidatesSkipsNaNSimilarity(t *testing.T) {
	sims := externalSims(externalMinCandidates+1, func(i int) float64 { return float64(i) / 20 }, func(i int) int { return 10 * i })
	sims[3].Similarity = math.NaN()
	items := rankExternalCandidates(sims, nil, nil)
	if len(items) != externalMinCandidates {
		t.Fatalf("%d items, want %d", len(items), externalMinCandidates)
	}
	for _, it := range items {
		if it.TMDBID == 4 || math.IsNaN(it.Score) {
			t.Fatalf("item %+v: the NaN candidate must go and scores stay finite", it)
		}
	}
}

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

func TestFilterExternalByCeiling(t *testing.T) {
	sims := []ExternalSimilarity{
		{Key: ExternalTitleKey{MediaType: ExternalMediaMovie, TMDBID: 1}, USCertification: "G"},
		{Key: ExternalTitleKey{MediaType: ExternalMediaMovie, TMDBID: 2}, USCertification: "PG-13"},
		{Key: ExternalTitleKey{MediaType: ExternalMediaMovie, TMDBID: 3}, USCertification: "R"},
		{Key: ExternalTitleKey{MediaType: ExternalMediaMovie, TMDBID: 4}, USCertification: ""},
		{Key: ExternalTitleKey{MediaType: ExternalMediaMovie, TMDBID: 5}, USCertification: "UNKNOWN"},
	}
	if got := filterExternalByCeiling(sims, ""); len(got) != len(sims) {
		t.Fatalf("unrestricted ceiling kept %d, want %d", len(got), len(sims))
	}
	got := filterExternalByCeiling(sims, "PG-13")
	var ids []int
	for _, s := range got {
		ids = append(ids, s.Key.TMDBID)
	}
	if !slices.Equal(ids, []int{1, 2}) {
		t.Fatalf("PG-13 ceiling kept %v, want [1 2] (R and uncertified dropped)", ids)
	}
}

// A restricted profile with too few admitted titles to rank on their own
// keeps a short row of them rather than none.
func TestScoreEligibleExternalShortRow(t *testing.T) {
	sims := externalSims(externalMinCandidates+5, func(i int) float64 { return float64(i) / 20 }, func(int) int { return 100 })
	for i := range sims {
		sims[i].USCertification = "R"
	}
	sims[3].USCertification, sims[8].USCertification = "PG", "G"

	got := scoreEligibleExternal(sims, "PG-13", nil, nil)
	var ids []int
	for _, it := range got {
		ids = append(ids, it.TMDBID)
	}
	if !slices.Equal(ids, []int{9, 4}) {
		t.Fatalf("restricted short row %v, want [9 4]", ids)
	}
	if got := scoreEligibleExternal(sims[:externalMinCandidates-1], "", nil, nil); len(got) != 0 {
		t.Fatalf("unrestricted pool below the minimum ranked %d titles, want none", len(got))
	}
}

func TestApplyExternalQualityPrior(t *testing.T) {
	sims := externalSims(qualityMinRated+2, func(int) float64 { return 0.7 }, func(int) int { return 500 })
	// Give spread to base scores so scoreSD > 0, while titles 1 and 2 tie on
	// similarity and vote count and differ only in vote_average.
	for i := range sims {
		sims[i].Similarity = 0.5 + float64(i)*0.02
		sims[i].VoteAverage = 6.5
	}
	sims[0].Similarity, sims[1].Similarity = 0.7, 0.7
	sims[0].VoteAverage = 5.0
	sims[1].VoteAverage = 8.8
	// Title 3 has a 9.9 rating but too few votes to qualify.
	sims[2].Similarity, sims[2].VoteCount, sims[2].VoteAverage = 0.7, externalQualityMinVotes-1, 9.9
	sims[3].Similarity = 0.7

	items := scoreExternalCandidates(sims, nil, nil)
	pos := map[int]int{}
	for i, it := range items {
		pos[it.TMDBID] = i
	}
	if pos[2] >= pos[1] {
		t.Fatalf("high-rated title 2 (pos %d) should outrank low-rated title 1 (pos %d): %+v", pos[2], pos[1], items)
	}
	if pos[3] <= pos[2] {
		t.Fatalf("under-threshold title 3 (pos %d) should not outrank qualified title 2 (pos %d): %+v", pos[3], pos[2], items)
	}
}

func TestRerankExternalCandidatesAversionAndMMR(t *testing.T) {
	// Include negative z-scores so we verify MMR shifting and aversion work
	// cleanly when scores span negative to positive.
	items := []ExternalRowItem{
		{TMDBID: 1, Score: 1.5},
		{TMDBID: 2, Score: 1.4},
		{TMDBID: 3, Score: 1.1},
		{TMDBID: 4, Score: -0.5},
		{TMDBID: 5, Score: -1.2},
	}
	// Title 1 and 2 are near-duplicates along axis 0; title 3 is orthogonal
	// along axis 1; title 4 sits on an avoided axis 2.
	embs := map[int][]float32{
		1: {1, 0, 0, 0},
		2: {0.999, 0.04, 0, 0},
		3: {0, 1, 0, 0},
		4: {0, 0, 1, 0},
		5: {0, 0, 0, 1},
	}
	// MMR alone: #1 leads, then #3 (orthogonal) beats near-duplicate #2.
	mmrOnly := rerankExternalCandidates(items, embs, nil, nil, 0.5)
	if len(mmrOnly) != len(items) || mmrOnly[0].TMDBID != 1 || mmrOnly[1].TMDBID != 3 {
		t.Fatalf("MMR order = %+v, want title 1 then orthogonal title 3 ahead of near-duplicate 2", mmrOnly)
	}
	scoreByID := map[int]float64{}
	for _, it := range mmrOnly {
		scoreByID[it.TMDBID] = it.Score
	}
	if scoreByID[1] != 1.5 || scoreByID[5] != -1.2 {
		t.Fatalf("MMR should keep original scores rather than shifted [0,1] values: %+v", mmrOnly)
	}

	// Aversion alone (lambda = 1): with enough candidates to establish the
	// similarity distribution, a turned-away title near the taste vector pushes
	// down a candidate nearly identical to it.
	avItems := make([]ExternalRowItem, 12)
	avEmbs := make(map[int][]float32, 12)
	for i := range 12 {
		id := i + 1
		avItems[i] = ExternalRowItem{TMDBID: id, Score: float64(12-i)*0.2 - 1.0}
		// Spread candidates along axes 1..3 with small axis-0 component so the
		// median similarity to taste (axis 0) is low (~0.2).
		angle := float64(i) * 0.4
		avEmbs[id] = []float32{0.2, float32(math.Cos(angle)), float32(math.Sin(angle)), 0}
	}
	// Candidate 1 sits near the taste vector AND near the turned-away title
	// along axis 0 + axis 3.
	avEmbs[1] = []float32{0.7, 0, 0, 0.7}
	av := &aversion{
		titles: []aversionTitle{{embedding: []float32{0.7, 0, 0, 0.7}, weight: 1.0}},
	}
	taste := []float32{1, 0, 0, 0}
	avOnly := rerankExternalCandidates(avItems, avEmbs, av, taste, 1.0)
	if avOnly[0].TMDBID == 1 {
		t.Fatalf("averted candidate 1 stayed at position 0: %+v", avOnly[:4])
	}
}
