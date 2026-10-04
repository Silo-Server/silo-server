package recommendations

import (
	"fmt"
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

// A cluster's label names the genres that set it apart from the profile,
// most distinctive first, with its most common genre always among them, at
// most two, joined with ", ".
func TestClusterLabelNamesWhatSetsAClusterApart(t *testing.T) {
	titles := func(n int, weight float64, genres ...string) []clusterItem {
		out := make([]clusterItem, n)
		for i := range out {
			out[i] = clusterItem{itemID: fmt.Sprint(i), weight: weight, genres: genres}
		}
		return out
	}
	join := func(groups ...[]clusterItem) []clusterItem { return slices.Concat(groups...) }
	profileOf := func(items []clusterItem) (map[string]int, int) {
		counts := map[string]int{}
		for _, item := range items {
			for _, g := range distinctGenres(item.genres) {
				counts[g]++
			}
		}
		return counts, len(items)
	}

	// A thriller profile split into a horror side and a drama side: Thriller
	// is on every title, so it sets neither cluster apart but names both.
	horror := join(titles(8, 1, "Thriller", "Horror"), titles(2, 1, "Thriller"))
	drama := join(titles(6, 1, "Thriller", "Drama"), titles(4, 1, "Thriller", "Crime"))
	profile, n := profileOf(join(horror, drama))
	for _, tc := range []struct {
		name    string
		members []clusterItem
		want    string
	}{
		{"distinctive genre with the common one", horror, "Horror, Thriller"},
		// Drama and Crime are both twice as common there as in the profile;
		// Drama, on more of the cluster, takes the one place beside Thriller.
		{"most distinctive first", drama, "Drama, Thriller"},
		{"the profile's only cluster", join(horror, drama), "Thriller"},
		{"no genres", titles(3, 1), ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := clusterLabel(tc.members, profile, n); got != tc.want {
				t.Fatalf("label = %q, want %q", got, tc.want)
			}
		})
	}

	// A TMDB genre containing "&" stays one genre in the label.
	scifi := titles(5, 1, "Sci-Fi & Fantasy", "Drama")
	profile, n = profileOf(join(scifi, titles(15, 1, "Drama")))
	if got := clusterLabel(scifi, profile, n); got != "Sci-Fi & Fantasy, Drama" {
		t.Fatalf("label = %q, want \"Sci-Fi & Fantasy, Drama\"", got)
	}

	// A genre on under 40% of the cluster is not named, however rare it is
	// elsewhere.
	western := join(titles(2, 1, "Horror", "Western"), titles(4, 1, "Horror"))
	profile, n = profileOf(join(western, titles(14, 1, "Drama")))
	if got := clusterLabel(western, profile, n); got != "Horror" {
		t.Fatalf("label = %q, want Horror", got)
	}

	// A tie for the most common genre goes to the heavier titles, which pull
	// the row toward them, not to the first name.
	tied := join(titles(3, 0.1, "Comedy"), titles(3, 0.9, "Crime"))
	profile, n = profileOf(tied)
	if got := clusterLabel(tied, profile, n); got != "Crime" {
		t.Fatalf("label = %q, want Crime", got)
	}
}
