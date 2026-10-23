package recommendations

import (
	"fmt"
	"math"
	"math/rand/v2"
	"reflect"
	"testing"
)

// applyMMRQuadratic is applyMMR before it kept a running maximum similarity
// per candidate: every round re-scores each remaining candidate against every
// pick so far. It is the oracle the incremental version must match exactly.
func applyMMRQuadratic(candidates []ScoredItem, embeddings map[string][]float32, lambda float64, limit int) []ScoredItem {
	if len(candidates) == 0 || limit <= 0 {
		return nil
	}
	if limit > len(candidates) {
		limit = len(candidates)
	}

	maxScore := candidates[0].Score
	for _, c := range candidates[1:] {
		if c.Score > maxScore {
			maxScore = c.Score
		}
	}
	if maxScore == 0 {
		maxScore = 1
	}

	remaining := make([]int, len(candidates))
	for i := range remaining {
		remaining[i] = i
	}

	selected := make([]ScoredItem, 0, limit)
	selectedEmbeddings := make([][]float32, 0, limit)

	bestIdx := 0
	for i, ri := range remaining {
		if candidates[ri].Score > candidates[remaining[bestIdx]].Score {
			bestIdx = i
		}
	}
	first := remaining[bestIdx]
	selected = append(selected, candidates[first])
	if emb, ok := embeddings[candidates[first].MediaItemID]; ok {
		selectedEmbeddings = append(selectedEmbeddings, emb)
	}
	remaining = append(remaining[:bestIdx], remaining[bestIdx+1:]...)

	for len(selected) < limit && len(remaining) > 0 {
		bestMMRIdx := -1
		bestMMRScore := math.Inf(-1)

		for i, ri := range remaining {
			normalizedRelevance := candidates[ri].Score / maxScore
			candidateEmb := embeddings[candidates[ri].MediaItemID]

			var maxSim float64
			if candidateEmb != nil && len(selectedEmbeddings) > 0 {
				for _, selEmb := range selectedEmbeddings {
					sim := cosineSimilarity(candidateEmb, selEmb)
					if sim > maxSim {
						maxSim = sim
					}
				}
			}

			var mmrScore float64
			if candidateEmb == nil {
				mmrScore = normalizedRelevance
			} else {
				mmrScore = lambda*normalizedRelevance - (1-lambda)*maxSim
			}

			if mmrScore > bestMMRScore {
				bestMMRScore = mmrScore
				bestMMRIdx = i
			}
		}

		pick := remaining[bestMMRIdx]
		selected = append(selected, candidates[pick])
		if emb, ok := embeddings[candidates[pick].MediaItemID]; ok {
			selectedEmbeddings = append(selectedEmbeddings, emb)
		}
		remaining = append(remaining[:bestMMRIdx], remaining[bestMMRIdx+1:]...)
	}

	return selected
}

// The incremental applyMMR picks exactly what the quadratic version picks, in
// the same order, over random pools that include tied scores, candidates
// without an embedding, nil and empty embeddings, and every lambda edge.
func TestApplyMMRMatchesQuadraticOracle(t *testing.T) {
	rng := rand.New(rand.NewPCG(20261003, 47))
	for trial := range 400 {
		n := 1 + rng.IntN(90)
		dims := 1 + rng.IntN(12)
		candidates := make([]ScoredItem, n)
		embeddings := make(map[string][]float32, n)
		for i := range candidates {
			id := fmt.Sprintf("item-%03d", i)
			score := rng.Float64()
			if rng.IntN(5) == 0 {
				// Coarse scores make ties common.
				score = float64(rng.IntN(4)) / 4
			}
			candidates[i] = ScoredItem{MediaItemID: id, Score: score}
			switch rng.IntN(10) {
			case 0:
				// No embedding: scored by relevance alone.
			case 1:
				embeddings[id] = nil
			case 2:
				embeddings[id] = []float32{}
			default:
				vec := make([]float32, dims)
				for d := range vec {
					vec[d] = float32(rng.NormFloat64())
				}
				if rng.IntN(8) == 0 && i > 0 {
					// A near-duplicate of an earlier vector.
					copy(vec, embeddings[candidates[rng.IntN(i)].MediaItemID])
				}
				embeddings[id] = vec
			}
		}
		if rng.IntN(20) == 0 {
			// All-zero relevance takes the division-by-zero guard.
			for i := range candidates {
				candidates[i].Score = 0
			}
		}
		lambda := []float64{0, 0.3, 0.5, 0.7, 1, rng.Float64()}[rng.IntN(6)]
		limit := rng.IntN(n + 5)

		want := applyMMRQuadratic(candidates, embeddings, lambda, limit)
		got := applyMMR(candidates, embeddings, lambda, limit)
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("trial %d (n=%d limit=%d lambda=%v): applyMMR = %v, want %v", trial, n, limit, lambda, mmrItemIDs(got), mmrItemIDs(want))
		}
	}
}

// applyMMR leaves out candidates whose score is NaN or infinite and still
// picks in a round where no score beats -Inf. A NaN score fails every
// comparison, so it used to leave maxScore NaN or a round with no pick, and
// the pick at index -1 panicked the recommendations worker (#2926).
func TestApplyMMRLeavesOutNonFiniteScores(t *testing.T) {
	nan, negInf := math.NaN(), math.Inf(-1)
	embeddings := map[string][]float32{
		"a": {1, 0},
		"b": {0.9, 0.1},
		"c": {0, 1},
		"d": {0.1, 0.9},
		"z": {0, 0},
	}
	tests := []struct {
		name       string
		candidates []ScoredItem
		embeddings map[string][]float32
		want       []string
	}{
		{
			// A zero-norm source embedding makes every pgvector cosine
			// score NaN.
			name: "every score NaN",
			candidates: []ScoredItem{
				{MediaItemID: "a", Score: nan}, {MediaItemID: "b", Score: nan}, {MediaItemID: "c", Score: nan},
				{MediaItemID: "d", Score: nan}, {MediaItemID: "e", Score: nan}, {MediaItemID: "f", Score: nan},
			},
			want: []string{},
		},
		{
			name: "NaN first leaves maxScore NaN",
			candidates: []ScoredItem{
				{MediaItemID: "z", Score: nan}, {MediaItemID: "a", Score: 0.9}, {MediaItemID: "b", Score: 0.8},
				{MediaItemID: "c", Score: 0.7}, {MediaItemID: "d", Score: 0.6},
			},
			embeddings: embeddings,
			// c and d spread the row away from a before b, a's near
			// duplicate.
			want: []string{"a", "c", "b", "d"},
		},
		{
			name: "NaN is the last candidate left",
			candidates: []ScoredItem{
				{MediaItemID: "a", Score: 0.9}, {MediaItemID: "z", Score: nan}, {MediaItemID: "b", Score: 0.8},
				{MediaItemID: "c", Score: 0.7},
			},
			want: []string{"a", "b", "c"},
		},
		{
			name: "negative infinity is the last candidate left",
			candidates: []ScoredItem{
				{MediaItemID: "a", Score: 0.9}, {MediaItemID: "b", Score: 0.8}, {MediaItemID: "c", Score: negInf},
			},
			embeddings: embeddings,
			want:       []string{"a", "b"},
		},
		{
			// Dividing by the tiny maxScore overflows to -Inf.
			name: "relevance overflows to -Inf",
			candidates: []ScoredItem{
				{MediaItemID: "a", Score: 1e-310}, {MediaItemID: "b", Score: -1}, {MediaItemID: "c", Score: -2},
			},
			want: []string{"a", "b", "c"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := mmrItemIDs(applyMMR(tt.candidates, tt.embeddings, defaultMMRLambda, len(tt.candidates)))
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("applyMMR = %v, want %v", got, tt.want)
			}
		})
	}
}

func mmrItemIDs(items []ScoredItem) []string {
	out := make([]string, len(items))
	for i, item := range items {
		out[i] = item.MediaItemID
	}
	return out
}
