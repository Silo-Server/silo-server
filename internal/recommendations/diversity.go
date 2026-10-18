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
// Candidates without an entry in embeddings are scored by relevance alone.
//
// Each remaining candidate keeps its highest similarity to the picks so far,
// updated against only the newest pick, so a call costs O(n·k) cosines for n
// candidates and k picks rather than O(n·k²).
func applyMMR(candidates []ScoredItem, embeddings map[string][]float32, lambda float64, limit int) []ScoredItem {
	if len(candidates) == 0 || limit <= 0 {
		return nil
	}
	if limit > len(candidates) {
		limit = len(candidates)
	}

	// Find max relevance score for normalization.
	maxScore := candidates[0].Score
	for _, c := range candidates[1:] {
		if c.Score > maxScore {
			maxScore = c.Score
		}
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

	take := func(at int) {
		pick := candidates[remaining[at]]
		selected = append(selected, pick)
		remaining = append(remaining[:at], remaining[at+1:]...)
		maxSim = append(maxSim[:at], maxSim[at+1:]...)
		pickEmb, ok := embeddings[pick.MediaItemID]
		if !ok {
			return
		}
		for i, ri := range remaining {
			candidateEmb := embeddings[candidates[ri].MediaItemID]
			if candidateEmb == nil {
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
		bestMMRIdx := -1
		bestMMRScore := math.Inf(-1)

		for i, ri := range remaining {
			normalizedRelevance := candidates[ri].Score / maxScore

			var mmrScore float64
			if embeddings[candidates[ri].MediaItemID] == nil {
				// No embedding available — use relevance only.
				mmrScore = normalizedRelevance
			} else {
				mmrScore = lambda*normalizedRelevance - (1-lambda)*maxSim[i]
			}

			if mmrScore > bestMMRScore {
				bestMMRScore = mmrScore
				bestMMRIdx = i
			}
		}

		take(bestMMRIdx)
	}

	return selected
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
