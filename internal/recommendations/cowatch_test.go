package recommendations

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"reflect"
	"sort"
	"testing"
)

// computeCowatchMatrixUnbounded is computeCowatchMatrix before it bounded each
// item's neighbors while scanning: it collects every qualifying pair, then
// sorts and cuts each item's list to topN. It is the oracle the bounded
// version must match exactly.
func computeCowatchMatrixUnbounded(watchers map[string][]string, minWatchers, minShared, topN int) []CowatchPair {
	eligible := make([]string, 0, len(watchers))
	for itemID, users := range watchers {
		if len(users) >= minWatchers {
			eligible = append(eligible, itemID)
		}
	}
	sort.Strings(eligible)

	watcherSets := make(map[string]map[string]struct{}, len(eligible))
	for _, itemID := range eligible {
		set := make(map[string]struct{}, len(watchers[itemID]))
		for _, uid := range watchers[itemID] {
			set[uid] = struct{}{}
		}
		watcherSets[itemID] = set
	}

	type neighbor struct {
		similarID string
		score     float64
		shared    int
	}
	neighbors := make(map[string][]neighbor, len(eligible))
	for i := 0; i < len(eligible); i++ {
		a := eligible[i]
		setA := watcherSets[a]
		for j := i + 1; j < len(eligible); j++ {
			b := eligible[j]
			setB := watcherSets[b]
			shared := 0
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
			neighbors[a] = append(neighbors[a], neighbor{similarID: b, score: jaccard, shared: shared})
			neighbors[b] = append(neighbors[b], neighbor{similarID: a, score: jaccard, shared: shared})
		}
	}

	var result []CowatchPair
	for _, itemID := range eligible {
		nbrs := neighbors[itemID]
		if len(nbrs) == 0 {
			continue
		}
		sort.Slice(nbrs, func(i, j int) bool {
			if nbrs[i].score != nbrs[j].score {
				return nbrs[i].score > nbrs[j].score
			}
			return nbrs[i].similarID < nbrs[j].similarID
		})
		limit := min(topN, len(nbrs))
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

// The bounded matrix keeps exactly the neighbors the unbounded one keeps,
// in the same order, including Jaccard ties broken by similar item ID.
func TestCowatchMatrixMatchesUnboundedOracle(t *testing.T) {
	rng := rand.New(rand.NewPCG(20261003, 53))
	for trial := range 200 {
		items := 2 + rng.IntN(70)
		population := 3 + rng.IntN(25)
		watchers := make(map[string][]string, items)
		for i := range items {
			// Small watcher pools make identical sets, and so tied scores, common.
			pool := population
			if rng.IntN(3) == 0 {
				pool = min(population, 6)
			}
			var list []string
			for w := range pool {
				if rng.IntN(3) > 0 {
					list = append(list, fmt.Sprintf("%d:p", w))
				}
			}
			watchers[fmt.Sprintf("item-%02d", i)] = list
		}
		minWatchers := 1 + rng.IntN(5)
		minShared := 1 + rng.IntN(4)
		topN := 1 + rng.IntN(12)

		want := computeCowatchMatrixUnbounded(watchers, minWatchers, minShared, topN)
		got, err := computeCowatchMatrix(t.Context(), watchers, minWatchers, minShared, topN)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("trial %d (items=%d minWatchers=%d minShared=%d topN=%d):\n got %v\nwant %v", trial, items, minWatchers, minShared, topN, got, want)
		}
	}
}

func TestCowatchMatrixStopsWhenContextEnds(t *testing.T) {
	watchers := map[string][]string{
		"a": {"1:p", "2:p", "3:p"},
		"b": {"1:p", "2:p", "3:p"},
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	pairs, err := computeCowatchMatrix(ctx, watchers, 1, 1, 10)
	if !errors.Is(err, context.Canceled) || pairs != nil {
		t.Fatalf("pairs=%v err=%v, want context.Canceled and no pairs", pairs, err)
	}
}
