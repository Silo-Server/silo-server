package recommendations

import "sort"

// Default co-watch matrix parameters.
const (
	DefaultMinWatchers       = 5
	DefaultMinShared         = 3
	DefaultTopN              = 50
	DefaultMaxWatchesPerUser = 500
)

// computeCowatchMatrix builds a co-watch similarity matrix from per-item watcher
// lists. For every pair of items that each have at least minWatchers viewers,
// it computes the Jaccard similarity of their watcher sets. Pairs with fewer
// than minShared shared watchers are discarded. For each item only the topN
// most similar neighbours (by Jaccard score) are retained.
func computeCowatchMatrix(watchers map[string][]string, minWatchers, minShared, topN int) []CowatchPair {
	// Collect item IDs that meet the minimum-watchers threshold.
	eligible := make([]string, 0, len(watchers))
	for itemID, users := range watchers {
		if len(users) >= minWatchers {
			eligible = append(eligible, itemID)
		}
	}

	// Sort for deterministic iteration order.
	sort.Strings(eligible)

	// Pre-build watcher sets for O(1) membership tests.
	watcherSets := make(map[string]map[string]struct{}, len(eligible))
	for _, itemID := range eligible {
		set := make(map[string]struct{}, len(watchers[itemID]))
		for _, uid := range watchers[itemID] {
			set[uid] = struct{}{}
		}
		watcherSets[itemID] = set
	}

	// Per-item neighbour lists, keyed by itemID.
	type neighbour struct {
		similarID string
		score     float64
		shared    int
	}
	neighbours := make(map[string][]neighbour, len(eligible))

	// Compare every pair once (i < j), then record from both sides.
	for i := 0; i < len(eligible); i++ {
		a := eligible[i]
		setA := watcherSets[a]

		for j := i + 1; j < len(eligible); j++ {
			b := eligible[j]
			setB := watcherSets[b]

			// Count intersection.
			shared := 0
			// Iterate over the smaller set for efficiency.
			small, big := setA, setB
			if len(setA) > len(setB) {
				small, big = setB, setA
			}
			for uid := range small {
				if _, ok := big[uid]; ok {
					shared++
				}
			}

			if shared < minShared {
				continue
			}

			union := len(setA) + len(setB) - shared
			if union == 0 {
				continue
			}
			jaccard := float64(shared) / float64(union)

			neighbours[a] = append(neighbours[a], neighbour{similarID: b, score: jaccard, shared: shared})
			neighbours[b] = append(neighbours[b], neighbour{similarID: a, score: jaccard, shared: shared})
		}
	}

	// For each item, keep only the topN neighbours by Jaccard score descending.
	var result []CowatchPair
	for _, itemID := range eligible {
		nbrs := neighbours[itemID]
		if len(nbrs) == 0 {
			continue
		}

		sort.Slice(nbrs, func(i, j int) bool {
			if nbrs[i].score != nbrs[j].score {
				return nbrs[i].score > nbrs[j].score
			}
			return nbrs[i].similarID < nbrs[j].similarID
		})

		limit := topN
		if limit > len(nbrs) {
			limit = len(nbrs)
		}

		for _, n := range nbrs[:limit] {
			result = append(result, CowatchPair{
				ItemID:        itemID,
				SimilarItemID: n.similarID,
				JaccardScore:  n.score,
				CowatchCount:  n.shared,
			})
		}
	}

	return result
}

// blendScores merges embedding-based item scores with co-watch Jaccard scores
// using the given weights (typically 0.7 embedding, 0.3 co-watch). Items that
// appear in only one source still surface with their single weighted score.
// The result is sorted by blended score descending.
func blendScores(embeddingItems []ScoredItem, cowatchItems map[string]float64, embeddingWeight, cowatchWeight float64) []ScoredItem {
	blended := make(map[string]ScoredItem, len(embeddingItems)+len(cowatchItems))

	// Start with embedding items.
	for _, item := range embeddingItems {
		score := embeddingWeight * item.Score
		if jaccardScore, ok := cowatchItems[item.MediaItemID]; ok {
			score += cowatchWeight * jaccardScore
		}
		blended[item.MediaItemID] = ScoredItem{
			MediaItemID: item.MediaItemID,
			Score:       score,
			Reason:      item.Reason,
		}
	}

	// Add co-watch-only items that were not in the embedding set.
	for itemID, jaccardScore := range cowatchItems {
		if _, exists := blended[itemID]; exists {
			continue
		}
		blended[itemID] = ScoredItem{
			MediaItemID: itemID,
			Score:       cowatchWeight * jaccardScore,
			Reason:      "cowatch",
		}
	}

	// Flatten to a slice and sort by score descending.
	result := make([]ScoredItem, 0, len(blended))
	for _, item := range blended {
		result = append(result, item)
	}

	sort.Slice(result, func(i, j int) bool {
		if result[i].Score != result[j].Score {
			return result[i].Score > result[j].Score
		}
		return result[i].MediaItemID < result[j].MediaItemID
	})

	return result
}
