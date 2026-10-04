package recommendations

import (
	"math"
	"sort"
)

const minGenreCapRetainedFraction = 0.5

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

// applyGenreCap enforces that no single genre exceeds maxPct of the result set.
// When a genre is over-represented, the lowest-scored items from that genre are
// removed until the genre falls within the cap.
func applyGenreCap(items []ScoredItem, genres map[string][]string, maxPct float64) []ScoredItem {
	if len(items) == 0 || maxPct <= 0 || maxPct >= 1 {
		return items
	}
	minRetained := int(math.Ceil(float64(len(items)) * minGenreCapRetainedFraction))
	if minRetained < 1 {
		minRetained = 1
	}

	// Sort a copy by score descending so removals take lowest-scored first.
	sorted := make([]ScoredItem, len(items))
	copy(sorted, items)
	sort.Slice(sorted, func(i, j int) bool {
		if sorted[i].Score != sorted[j].Score {
			return sorted[i].Score > sorted[j].Score
		}
		return sorted[i].MediaItemID < sorted[j].MediaItemID
	})

	// Iteratively remove until all genres are within cap. Re-check after each
	// removal because the total count changes.
	for {
		// Count items per genre.
		genreCounts := make(map[string]int)
		for _, item := range sorted {
			for _, genre := range genres[item.MediaItemID] {
				if genre != "" {
					genreCounts[genre]++
				}
			}
		}

		// Find a genre that exceeds the cap.
		total := len(sorted)
		maxAllowed := int(math.Floor(maxPct * float64(total)))
		if maxAllowed < 1 {
			maxAllowed = 1
		}

		overGenre := ""
		for g, count := range genreCounts {
			if count > maxAllowed {
				if overGenre == "" ||
					count > genreCounts[overGenre] ||
					(count == genreCounts[overGenre] && g < overGenre) {
					overGenre = g
				}
			}
		}
		if overGenre == "" {
			break // all genres within cap
		}
		if len(sorted) <= minRetained {
			break
		}

		// Remove the lowest-scored item of the over-represented genre.
		// Items are sorted descending, so scan from the end.
		removed := false
		for i := len(sorted) - 1; i >= 0; i-- {
			if itemHasGenre(genres[sorted[i].MediaItemID], overGenre) {
				sorted = append(sorted[:i], sorted[i+1:]...)
				removed = true
				break
			}
		}
		if !removed {
			break
		}
	}

	return sorted
}

func itemHasGenre(genres []string, target string) bool {
	for _, genre := range genres {
		if genre == target {
			return true
		}
	}
	return false
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
