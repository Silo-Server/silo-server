package recommendations

import (
	"cmp"
	"context"
	"log/slog"
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
	// anchorMergeCosine merges anchors whose centroids are more similar than
	// this, so one interest split across two clusters is one anchor.
	anchorMergeCosine = 0.9
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
// heaviest first, with those whose centroids are within anchorMergeCosine
// merged. A profile under minAnchorTitles positive titles, or left with fewer
// than two anchors, gets none: its main row comes from the averaged vector.
func mainRowAnchors(clusters []TasteCluster) []mainRowAnchor {
	sorted := slices.Clone(clusters)
	slices.SortStableFunc(sorted, func(a, b TasteCluster) int {
		if c := cmp.Compare(b.TotalWeight, a.TotalWeight); c != 0 {
			return c
		}
		return cmp.Compare(a.ClusterIdx, b.ClusterIdx)
	})
	titles := 0
	var anchors []mainRowAnchor
	for _, c := range sorted {
		titles += c.MemberCount
		if len(c.Embedding) == 0 || c.TotalWeight <= 0 {
			continue
		}
		merged := false
		for i, a := range anchors {
			if cosineSimilarity(a.embedding, c.Embedding) > anchorMergeCosine {
				anchors[i] = mainRowAnchor{
					embedding: weightedAverage([][]float32{a.embedding, c.Embedding}, []float64{a.weight, c.TotalWeight}),
					weight:    a.weight + c.TotalWeight,
				}
				merged = true
				break
			}
		}
		if !merged {
			anchors = append(anchors, mainRowAnchor{embedding: c.Embedding, weight: c.TotalWeight})
		}
	}
	if titles < minAnchorTitles || len(anchors) < 2 {
		return nil
	}
	slices.SortStableFunc(anchors, func(a, b mainRowAnchor) int { return cmp.Compare(b.weight, a.weight) })
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
// returns the row, every candidate fetched, and their genres. An anchor whose
// query fails is logged and left out.
func (b rowBuilder) anchoredRow(ctx context.Context, anchors []mainRowAnchor, limit int, excludeIDs []string, filter catalog.AccessFilter) (row, pool []ScoredItem, genres map[string][]string) {
	weights := make([]float64, len(anchors))
	for i, a := range anchors {
		weights[i] = a.weight
	}
	slots := allocateAnchorSlots(weights, limit)
	genres = make(map[string][]string)
	lists := make([][]ScoredItem, len(anchors))
	for i, a := range anchors {
		candidates, candidateGenres, err := b.store.FindTasteProfileCandidates(ctx, a.embedding, excludeIDs, nil, anchorPoolFactor*slots[i], filter)
		if err != nil {
			slog.WarnContext(ctx, "main row anchor candidates failed", "component", "recommendations", "anchor", i, "error", err)
			continue
		}
		if len(candidates) == 0 {
			continue
		}
		candidates = b.withRecencyBoost(ctx, b.withQualityPrior(ctx, candidates, 0))
		embMap, _ := b.store.GetBatchEmbeddings(ctx, scoredItemIDs(candidates))
		// MMR's ranking is greedy, so its first slots[i] items are the
		// anchor's picks; the rest stand in when another anchor runs out.
		lists[i] = applyMMR(candidates, embMap, b.lambda, len(candidates))
		pool = mergeScoredCandidates(pool, candidates)
		for id, g := range candidateGenres {
			genres[id] = g
		}
	}
	return interleaveAnchors(lists, slots, limit), pool, genres
}
