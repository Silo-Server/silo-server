package recommendations

import (
	"math"
	"slices"
)

// applyMMR re-ranks candidates using Maximal Marginal Relevance to balance
// relevance against diversity. It selects up to limit items from candidates,
// choosing each successive item to maximize:
//
//	score = λ × normalizedRelevance - (1-λ) × maxSimilarityToSelected
//
// A candidate without an embedding (no entry, or an empty one) has no
// similarity to measure, so it takes the round's typical redundancy instead:
// the mean maxSimilarityToSelected of the embedded candidates still in the
// pool. It is weighed by λ like every other candidate, so it neither jumps
// ahead of embedded titles for lack of a penalty nor sinks below them for
// carrying the worst one. Picks without an embedding raise no candidate's
// similarity.
// Candidates whose score is NaN or infinite cannot be ranked and are left out;
// pgvector's cosine distance to a zero-norm embedding is NaN.
//
// Each remaining candidate keeps its highest similarity to the picks so far,
// updated against only the newest pick, so a call costs O(n·k) cosines for n
// candidates and k picks rather than O(n·k²).
func applyMMR(candidates []ScoredItem, embeddings map[string][]float32, lambda float64, limit int) []ScoredItem {
	if slices.ContainsFunc(candidates, nonFiniteScore) {
		candidates = slices.DeleteFunc(slices.Clone(candidates), nonFiniteScore)
	}
	if len(candidates) == 0 || limit <= 0 {
		return nil
	}
	if limit > len(candidates) {
		limit = len(candidates)
	}

	// Relevance is scaled by the largest score magnitude. Dividing by the
	// highest score instead would reverse the order of a pool whose scores
	// are all negative.
	maxScore := 0.0
	for _, c := range candidates {
		maxScore = max(maxScore, math.Abs(c.Score))
	}
	if maxScore == 0 {
		maxScore = 1 // avoid division by zero
	}

	// remaining holds the candidates still available; maxSim[i] is
	// remaining[i]'s highest similarity to a pick with an embedding. Picks
	// remove their entry from both slices together.
	remaining := make([]int, len(candidates))
	for i := range remaining {
		remaining[i] = i
	}
	maxSim := make([]float64, len(candidates))
	selected := make([]ScoredItem, 0, limit)
	// A zero vector has no direction, so it counts as no embedding: it would
	// otherwise be similar to nothing and escape the redundancy penalty.
	usable := make([][]float32, len(candidates))
	for i, c := range candidates {
		if emb := embeddings[c.MediaItemID]; hasDirection(emb) {
			usable[i] = emb
		}
	}
	embeddingOf := func(ri int) []float32 { return usable[ri] }

	take := func(at int) {
		pickEmb := embeddingOf(remaining[at])
		selected = append(selected, candidates[remaining[at]])
		remaining = append(remaining[:at], remaining[at+1:]...)
		maxSim = append(maxSim[:at], maxSim[at+1:]...)
		if len(pickEmb) == 0 {
			return
		}
		for i, ri := range remaining {
			candidateEmb := embeddingOf(ri)
			if len(candidateEmb) == 0 {
				continue
			}
			if sim := cosineSimilarity(candidateEmb, pickEmb); sim > maxSim[i] {
				maxSim[i] = sim
			}
		}
	}

	// First pick: highest relevance score.
	bestIdx := 0
	for i, ri := range remaining {
		if candidates[ri].Score > candidates[remaining[bestIdx]].Score {
			bestIdx = i
		}
	}
	take(bestIdx)

	// Subsequent picks via MMR scoring.
	for len(selected) < limit && len(remaining) > 0 {
		// remaining[0] stands in when no score beats -Inf, as when dividing
		// by a tiny maxScore overflows, so every round picks.
		bestMMRIdx := 0
		bestMMRScore := math.Inf(-1)
		neutralSim := meanEmbeddedMaxSim(remaining, maxSim, embeddingOf)

		for i, ri := range remaining {
			sim := maxSim[i]
			if len(embeddingOf(ri)) == 0 {
				sim = neutralSim
			}
			mmrScore := lambda*candidates[ri].Score/maxScore - (1-lambda)*sim

			if mmrScore > bestMMRScore {
				bestMMRScore = mmrScore
				bestMMRIdx = i
			}
		}

		take(bestMMRIdx)
	}

	return selected
}

// meanEmbeddedMaxSim returns the mean of maxSim over the remaining
// candidates that have an embedding, or 0 when none has.
func meanEmbeddedMaxSim(remaining []int, maxSim []float64, embeddingOf func(int) []float32) float64 {
	var sum float64
	var n int
	for i, ri := range remaining {
		if len(embeddingOf(ri)) > 0 {
			sum += maxSim[i]
			n++
		}
	}
	if n == 0 {
		return 0
	}
	return sum / float64(n)
}

// hasDirection reports whether v is a non-empty vector with a nonzero
// component, one a cosine can be taken against.
func hasDirection(v []float32) bool {
	return slices.ContainsFunc(v, func(x float32) bool { return x != 0 })
}

// nonFiniteScore reports whether item's Score is NaN or infinite. NaN fails
// every comparison, so MMR can neither scale relevance by it nor pick it.
func nonFiniteScore(item ScoredItem) bool {
	return math.IsNaN(item.Score) || math.IsInf(item.Score, 0)
}

// servedGenreShare is the largest share of the served window one genre may
// hold while the row has items of other genres to offer.
const servedGenreShare = 0.5

// applyGenreCap reorders a row ranked by MMR to keep any one genre to
// servedGenreShare of its served window, its first ServedRowSize positions or
// the whole row when shorter. It is greedy and keeps relevance order: it walks
// the row in order, an item enters the window only if none of its genres
// already holds that many places there, and the items it passes over fill the
// window's remaining places, in order, when the row runs out of others. It
// never revisits a title it let in, so with multi-genre titles a genre can
// exceed the share even when another choice of titles would meet it; the
// higher-ranked titles win. Everything after the window keeps its order, and
// no item is removed. Items without genre data are never counted.
func applyGenreCap(items []ScoredItem, genres map[string][]string) []ScoredItem {
	window := min(len(items), ServedRowSize)
	maxPerGenre := max(1, int(servedGenreShare*float64(window)))
	counts := make(map[string]int)
	out := make([]ScoredItem, 0, len(items))
	var skipped []ScoredItem
	next := 0
	for ; next < len(items) && len(out) < window; next++ {
		item := items[next]
		itemGenres := distinctGenres(genres[item.MediaItemID])
		if slices.ContainsFunc(itemGenres, func(g string) bool { return counts[g] >= maxPerGenre }) {
			skipped = append(skipped, item)
			continue
		}
		for _, g := range itemGenres {
			counts[g]++
		}
		out = append(out, item)
	}
	// The skipped items fill the window first, then precede the untouched
	// rest, which they came before.
	out = append(out, skipped...)
	return append(out, items[next:]...)
}

// distinctGenres returns genres without blanks and repeats.
func distinctGenres(genres []string) []string {
	out := make([]string, 0, len(genres))
	for _, g := range genres {
		if g != "" && !slices.Contains(out, g) {
			out = append(out, g)
		}
	}
	return out
}

// cosineSimilarity computes the cosine similarity between two float32 vectors.
// Returns 0 if either vector is nil or empty.
func cosineSimilarity(a, b []float32) float64 {
	if len(a) == 0 || len(b) == 0 {
		return 0
	}

	n := len(a)
	if len(b) < n {
		n = len(b)
	}

	var dot, normA, normB float64
	for i := 0; i < n; i++ {
		ai := float64(a[i])
		bi := float64(b[i])
		dot += ai * bi
		normA += ai * ai
		normB += bi * bi
	}

	if normA == 0 || normB == 0 {
		return 0
	}

	return dot / (math.Sqrt(normA) * math.Sqrt(normB))
}
