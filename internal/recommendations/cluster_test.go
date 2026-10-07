package recommendations

import (
	"slices"
	"testing"
)

// tasteItems returns two well-separated groups of items whose weights are
// scaled per item by weight(i), the way time decay changes them between
// rebuilds.
func tasteItems(weight func(i int) float64) []clusterItem {
	var items []clusterItem
	for i := range 8 {
		f := float32(i) * 0.01
		items = append(items,
			clusterTestItem("a"+string(rune('0'+i)), []float32{1 - f, f, 0}, weight(i), "Action"),
			clusterTestItem("d"+string(rune('0'+i)), []float32{0, 1 - f, f}, weight(i+8), "Drama"),
			clusterTestItem("h"+string(rune('0'+i)), []float32{f, 0, 1 - f}, weight(i+16), "Horror"),
		)
	}
	slices.SortFunc(items, func(a, b clusterItem) int {
		if a.itemID < b.itemID {
			return -1
		}
		return 1
	})
	return items
}

func TestKmeansSeedIgnoresWeights(t *testing.T) {
	today := tasteItems(func(i int) float64 { return 1 + float64(i)*0.1 })
	tomorrow := tasteItems(func(i int) float64 { return (1 + float64(i)*0.1) * (0.5 + float64(i%5)*0.07) })
	k := determinClusterCount(len(today))

	if kmeansSeed(today, k) != kmeansSeed(tomorrow, k) {
		t.Fatal("decayed weights changed the k-means seed")
	}
	if got, want := kmeansCluster(tomorrow, k), kmeansCluster(today, k); !slices.Equal(got, want) {
		t.Fatalf("assignments changed with weights: %v vs %v", got, want)
	}
	first, second := buildTasteClusters(today), buildTasteClusters(tomorrow)
	if len(first) != len(second) {
		t.Fatalf("cluster count changed with weights: %d vs %d", len(first), len(second))
	}
	for i := range first {
		if first[i].ClusterIdx != second[i].ClusterIdx || first[i].Label != second[i].Label || first[i].MemberCount != second[i].MemberCount {
			t.Fatalf("cluster %d changed with weights: %+v vs %+v", i, first[i], second[i])
		}
	}

	// A new title is a different profile and may reshuffle the clusters.
	added := append(slices.Clone(today), clusterTestItem("z9", []float32{1, 0, 0}, 1, "Action"))
	if kmeansSeed(added, k) == kmeansSeed(today, k) {
		t.Fatal("adding a title left the seed unchanged")
	}
	// IDs are delimited, so concatenations that match do not collide.
	ab := []clusterItem{{itemID: "ab"}, {itemID: "c"}}
	bc := []clusterItem{{itemID: "a"}, {itemID: "bc"}}
	if kmeansSeed(ab, 2) == kmeansSeed(bc, 2) {
		t.Fatal("different ID lists produced the same seed")
	}
}
