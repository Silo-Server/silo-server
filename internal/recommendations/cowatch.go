package recommendations

import (
	"container/heap"
	"context"
	"sort"
)

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
// most similar neighbors (by Jaccard score, then similar item ID) are
// retained, and only those are held while scanning. It stops with ctx's error
// when ctx ends.
func computeCowatchMatrix(ctx context.Context, watchers map[string][]string, minWatchers, minShared, topN int) ([]CowatchPair, error) {
	if topN <= 0 {
		return nil, nil
	}

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

	// Per-item best neighbors, keyed by itemID.
	neighbors := make(map[string]*cowatchNeighbors, len(eligible))
	keep := func(itemID string, n cowatchNeighbor) {
		kept := neighbors[itemID]
		if kept == nil {
			kept = &cowatchNeighbors{}
			neighbors[itemID] = kept
		}
		kept.offer(n, topN)
	}

	// Compare every pair once (i < j), then record from both sides.
	for i := 0; i < len(eligible); i++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
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

			keep(a, cowatchNeighbor{similarID: b, score: jaccard, shared: shared})
			keep(b, cowatchNeighbor{similarID: a, score: jaccard, shared: shared})
		}
	}

	// Emit each item's neighbors best first.
	var result []CowatchPair
	for _, itemID := range eligible {
		kept := neighbors[itemID]
		if kept == nil {
			continue
		}
		nbrs := *kept
		sort.Slice(nbrs, func(i, j int) bool { return nbrs[i].ranksAbove(nbrs[j]) })
		for _, n := range nbrs {
			result = append(result, CowatchPair{
				ItemID:        itemID,
				SimilarItemID: n.similarID,
				JaccardScore:  n.score,
				CowatchCount:  n.shared,
			})
		}
	}

	return result, nil
}

// cowatchNeighbor is one co-watched item and its similarity to another.
type cowatchNeighbor struct {
	similarID string
	score     float64
	shared    int
}

// ranksAbove orders neighbors by Jaccard score, highest first, then by
// similar item ID. An item sees each neighbor once, so the order is total.
func (n cowatchNeighbor) ranksAbove(other cowatchNeighbor) bool {
	if n.score != other.score {
		return n.score > other.score
	}
	return n.similarID < other.similarID
}

// cowatchNeighbors keeps an item's best neighbors as a heap whose root is
// the lowest-ranked one kept, so a better neighbor replaces it in O(log n).
type cowatchNeighbors []cowatchNeighbor

func (h cowatchNeighbors) Len() int           { return len(h) }
func (h cowatchNeighbors) Less(i, j int) bool { return h[j].ranksAbove(h[i]) }
func (h cowatchNeighbors) Swap(i, j int)      { h[i], h[j] = h[j], h[i] }
func (h *cowatchNeighbors) Push(x any) {
	if n, ok := x.(cowatchNeighbor); ok {
		*h = append(*h, n)
	}
}
func (h *cowatchNeighbors) Pop() any {
	old := *h
	last := old[len(old)-1]
	*h = old[:len(old)-1]
	return last
}

// offer keeps n if fewer than limit neighbors are kept or n ranks above the
// lowest kept one, which it then replaces.
func (h *cowatchNeighbors) offer(n cowatchNeighbor, limit int) {
	if h.Len() < limit {
		heap.Push(h, n)
		return
	}
	if n.ranksAbove((*h)[0]) {
		(*h)[0] = n
		heap.Fix(h, 0)
	}
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
