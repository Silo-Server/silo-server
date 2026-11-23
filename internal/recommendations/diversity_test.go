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
	if emb := embeddings[candidates[first].MediaItemID]; len(emb) > 0 {
		selectedEmbeddings = append(selectedEmbeddings, emb)
	}
	remaining = append(remaining[:bestIdx], remaining[bestIdx+1:]...)

	for len(selected) < limit && len(remaining) > 0 {
		bestMMRIdx := -1
		bestMMRScore := math.Inf(-1)

		sims := make([]float64, len(remaining))
		var embeddedSimSum float64
		embedded := 0
		for i, ri := range remaining {
			candidateEmb := embeddings[candidates[ri].MediaItemID]
			if len(candidateEmb) == 0 {
				continue
			}
			for _, selEmb := range selectedEmbeddings {
				sims[i] = max(sims[i], cosineSimilarity(candidateEmb, selEmb))
			}
			embeddedSimSum += sims[i]
			embedded++
		}
		for i, ri := range remaining {
			if len(embeddings[candidates[ri].MediaItemID]) == 0 && embedded > 0 {
				sims[i] = embeddedSimSum / float64(embedded)
			}
			mmrScore := lambda*candidates[ri].Score/maxScore - (1-lambda)*sims[i]
			if mmrScore > bestMMRScore {
				bestMMRScore = mmrScore
				bestMMRIdx = i
			}
		}

		pick := remaining[bestMMRIdx]
		selected = append(selected, candidates[pick])
		if emb := embeddings[candidates[pick].MediaItemID]; len(emb) > 0 {
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
				// No embedding: it takes the round's mean similarity.
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

// A candidate without an embedding (missing or empty) is weighed by λ and
// takes the mean similarity of the embedded candidates still in the pool: a
// lower-relevance one no longer jumps ahead of a diverse embedded title, and
// it is not handed the worst redundancy either.
func TestApplyMMRUnembeddedCandidateTakesTypicalPenalty(t *testing.T) {
	x, y := []float32{1, 0}, []float32{0, 1}
	tests := []struct {
		name       string
		candidates []ScoredItem
		embeddings map[string][]float32
		want       []string
	}{{
		// u would score 0.8 by relevance alone and beat b's 0.7×0.9.
		name:       "missing embedding does not jump a diverse title",
		candidates: []ScoredItem{{MediaItemID: "a", Score: 1}, {MediaItemID: "b", Score: 0.9}, {MediaItemID: "u", Score: 0.8}},
		embeddings: map[string][]float32{"a": x, "b": y},
		want:       []string{"a", "b", "u"},
	}, {
		// After a, the pool's embedded titles average 2/3 similarity, so u
		// (0.7×0.85 − 0.3×2/3) still beats d (0.7×0.5) and the duplicates;
		// the worst penalty (1) would have put it below d.
		name: "missing embedding is not buried below weaker titles",
		candidates: []ScoredItem{
			{MediaItemID: "a", Score: 1}, {MediaItemID: "b", Score: 0.95}, {MediaItemID: "c", Score: 0.9},
			{MediaItemID: "u", Score: 0.85}, {MediaItemID: "d", Score: 0.5},
		},
		embeddings: map[string][]float32{"a": x, "b": x, "c": x, "d": y},
		want:       []string{"a", "u", "b", "d", "c"},
	}, {
		// Counted as embedded, u would carry no penalty and come second.
		name: "empty embedding counts as missing",
		candidates: []ScoredItem{
			{MediaItemID: "a", Score: 1}, {MediaItemID: "b", Score: 0.95}, {MediaItemID: "u", Score: 0.9}, {MediaItemID: "d", Score: 0.85},
		},
		embeddings: map[string][]float32{"a": x, "b": x, "u": {}, "d": y},
		want:       []string{"a", "d", "b", "u"},
	}}
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
