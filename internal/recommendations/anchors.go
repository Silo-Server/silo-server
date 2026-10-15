package recommendations

import (
	"cmp"
	"context"
	"fmt"
	"slices"

	"github.com/Silo-Server/silo-server/internal/catalog"
)

// A profile with several interests gets a main row composed per interest. One
// query around the averaged taste vector hands most of the row to the
// heaviest interest, or to titles between the interests; anchoring on each
// taste cluster gives every interest its share of the row.
const (
	// minAnchorTitles is how many positive titles a profile needs before its
	// clusters anchor the main row. Under it the profile has one cluster.
	minAnchorTitles = 10
	// anchorMinSlots is the fewest slots an anchor gets when the row has room
	// for that many per anchor.
	anchorMinSlots = 3
	// anchorPoolFactor is how many candidates an anchor's query fetches per
	// slot, for MMR to choose from.
	anchorPoolFactor = 3
)

// mainRowAnchor is one interest the main row is composed from.
type mainRowAnchor struct {
	embedding []float32
	weight    float64
}

// mainRowAnchors returns the anchors of a profile's main row: its clusters,
// heaviest first. A profile under minAnchorTitles positive titles, or with
// fewer than two clusters, gets none: its main row comes from the averaged
// vector.
//
// Clusters are not merged by how close their centroids are. A centroid
// averages away its titles' differences, so the more titles two clusters
// hold, the closer their centroids sit, whatever their genres:
// buildTasteClusters already decides which titles are one interest, against
// the profile's own spread.
func mainRowAnchors(clusters []TasteCluster) []mainRowAnchor {
	titles := 0
	var anchors []mainRowAnchor
	for _, c := range clustersHeaviestFirst(clusters) {
		titles += c.MemberCount
		if len(c.Embedding) == 0 || c.TotalWeight <= 0 {
			continue
		}
		anchors = append(anchors, mainRowAnchor{embedding: c.Embedding, weight: c.TotalWeight})
	}
	if titles < minAnchorTitles || len(anchors) < 2 {
		return nil
	}
	return anchors
}

// allocateAnchorSlots splits limit slots across anchors in proportion to
// their weights, by largest remainder, then lifts any anchor below
// min(anchorMinSlots, limit/len(weights)) to that minimum with slots from the
// anchors holding the most.
func allocateAnchorSlots(weights []float64, limit int) []int {
	n := len(weights)
	slots := make([]int, n)
	if n == 0 || limit <= 0 {
		return slots
	}
	var total float64
	for _, w := range weights {
		total += w
	}
	remainders := make([]float64, n)
	given := 0
	for i, w := range weights {
		exact := float64(limit) / float64(n)
		if total > 0 {
			exact = float64(limit) * w / total
		}
		slots[i] = int(exact)
		remainders[i] = exact - float64(slots[i])
		given += slots[i]
	}
	order := make([]int, n)
	for i := range order {
		order[i] = i
	}
	slices.SortStableFunc(order, func(a, b int) int { return cmp.Compare(remainders[b], remainders[a]) })
	for k := 0; given < limit; k++ {
		slots[order[k%n]]++
		given++
	}

	minSlots := min(anchorMinSlots, limit/n)
	for i := range slots {
		for slots[i] < minSlots {
			donor := -1
			for j := range slots {
				if slots[j] > minSlots && (donor < 0 || slots[j] > slots[donor]) {
					donor = j
				}
			}
			if donor < 0 {
				break
			}
			slots[donor]--
			slots[i]++
		}
	}
	return slots
}

// interleaveAnchors merges the anchors' ranked lists into one row of at most
// limit items by smooth weighted round-robin over their slots, so every prefix
// of the row holds each anchor's share. An item another anchor already placed
// is skipped. An anchor that runs out before filling its slots leaves them to
// the others, which then continue past their own slots in the same rotation.
func interleaveAnchors(lists [][]ScoredItem, slots []int, limit int) []ScoredItem {
	n := len(lists)
	next := make([]int, n)
	placed := make([]int, n)
	seen := make(map[string]struct{}, limit)
	row := make([]ScoredItem, 0, limit)

	take := func(i int) bool {
		for next[i] < len(lists[i]) {
			item := lists[i][next[i]]
			next[i]++
			if _, dup := seen[item.MediaItemID]; dup {
				continue
			}
			seen[item.MediaItemID] = struct{}{}
			row = append(row, item)
			placed[i]++
			return true
		}
		return false
	}

	// The first pass fills each anchor's slots; the second hands the slots an
	// anchor could not fill to the anchors with items left.
	for _, withinSlots := range []bool{true, false} {
		active := make([]bool, n)
		current := make([]int, n)
		for i := range active {
			active[i] = slots[i] > 0 && (!withinSlots || placed[i] < slots[i])
		}
		for len(row) < limit {
			pick, total := -1, 0
			for i := range n {
				if !active[i] {
					continue
				}
				current[i] += slots[i]
				total += slots[i]
				if pick < 0 || current[i] > current[pick] {
					pick = i
				}
			}
			if pick < 0 {
				break
			}
			current[pick] -= total
			if !take(pick) || (withinSlots && placed[pick] >= slots[pick]) {
				active[pick] = false
			}
		}
	}
	return row
}

// anchoredRow composes a main row of up to limit items from anchors. Each
// anchor fetches anchorPoolFactor candidates per slot with no genre filter,
// MMR ranks them, and interleaveAnchors merges the anchors' rankings. It
// returns the row and every candidate fetched. An anchor whose candidates or
// embeddings cannot be read fails the row: the others would take its slots,
// and a row missing an interest would be cached as if it were whole.
//
// When the profile's turned-away titles take anchors out of the row (see
// shrinkAvertedAnchors), the anchors left fetch again for the slots they
// gained, so the row keeps its length. allAverted reports that every anchor
// left: the profile turned away each of its interests, and the row is empty.
func (b rowBuilder) anchoredRow(ctx context.Context, anchors []mainRowAnchor, limit int, excludeIDs []string, filter catalog.AccessFilter) (row, pool []ScoredItem, allAverted bool, err error) {
	weights := make([]float64, len(anchors))
	for i, a := range anchors {
		weights[i] = a.weight
	}
	slots := allocateAnchorSlots(weights, limit)
	lists := make([][]ScoredItem, len(anchors))
	shares := make([]float64, len(anchors))
	fetched := make([]int, len(anchors))
	for i := range anchors {
		fetched[i] = anchorPoolFactor * slots[i]
		if lists[i], shares[i], err = b.anchorList(ctx, anchors[i], i, fetched[i], excludeIDs, filter); err != nil {
			return nil, nil, false, err
		}
	}
	// A refill widens an anchor's pool, which moves the median its share is
	// measured against (see aversion.apply), so the split is taken again
	// until no anchor gains slots it has not fetched for. Fetches only grow,
	// up to anchorPoolFactor×limit each, so this ends.
	for {
		kept, keptSlots := shrinkAvertedAnchors(weights, shares, slots, limit)
		if len(kept) == 0 {
			return nil, nil, true, nil
		}
		refilled := false
		for k, i := range kept {
			if n := anchorPoolFactor * keptSlots[k]; n > fetched[i] {
				fetched[i] = n
				if lists[i], shares[i], err = b.anchorList(ctx, anchors[i], i, n, excludeIDs, filter); err != nil {
					return nil, nil, false, err
				}
				refilled = true
			}
		}
		if refilled {
			continue
		}
		keptLists := make([][]ScoredItem, len(kept))
		for k, i := range kept {
			keptLists[k] = lists[i]
			// An anchor that leaves the row leaves the pool too, so type
			// supplements cannot bring its titles back.
			pool = mergeScoredCandidates(pool, lists[i])
		}
		return interleaveAnchors(keptLists, keptSlots, limit), pool, false, nil
	}
}

// anchorList fetches n candidates around anchor a (the i-th), ranks them
// against the profile's turned-away titles and by MMR, and returns the
// ranking with the share of the anchor's interest the turned-away titles
// leave. MMR's ranking is greedy, so its first items are the anchor's picks;
// the rest stand in when another anchor runs out.
func (b rowBuilder) anchorList(ctx context.Context, a mainRowAnchor, i, n int, excludeIDs []string, filter catalog.AccessFilter) ([]ScoredItem, float64, error) {
	candidates, _, err := b.store.FindTasteProfileCandidates(ctx, a.embedding, excludeIDs, nil, n, filter)
	if err != nil {
		return nil, 0, fmt.Errorf("main row anchor %d candidates: %w", i, err)
	}
	if len(candidates) == 0 {
		return nil, 1, nil
	}
	candidates = b.withRecencyBoost(ctx, b.withQualityPrior(ctx, candidates, 0))
	embMap, err := b.store.GetBatchEmbeddings(ctx, scoredItemIDs(candidates))
	if err != nil {
		return nil, 0, fmt.Errorf("main row anchor %d embeddings: %w", i, err)
	}
	candidates, averse := b.withAversion(candidates, embMap, a.embedding)
	return applyMMR(candidates, embMap, b.lambda, len(candidates)), averse.interestShare(a.weight), nil
}

// shrinkAvertedAnchors re-splits the row's slots after the profile's
// turned-away titles shrank some anchors' shares (see aversion.apply): each
// anchor's weight is scaled by its share, and an anchor whose share fell
// under aversionInterestFloor leaves the row. It returns the indexes of the
// anchors kept and their slots; with every share whole, every anchor and
// its slots unchanged.
func shrinkAvertedAnchors(weights, shares []float64, slots []int, limit int) (kept, keptSlots []int) {
	kept = make([]int, 0, len(weights))
	if !slices.ContainsFunc(shares, func(s float64) bool { return s < 1 }) {
		for i := range weights {
			kept = append(kept, i)
		}
		return kept, slots
	}
	keptWeights := make([]float64, 0, len(weights))
	for i, share := range shares {
		if share < aversionInterestFloor {
			continue
		}
		kept = append(kept, i)
		keptWeights = append(keptWeights, weights[i]*share)
	}
	return kept, allocateAnchorSlots(keptWeights, limit)
}
