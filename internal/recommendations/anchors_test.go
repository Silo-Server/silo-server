package recommendations

import (
	"fmt"
	"math"
	"slices"
	"testing"

	"github.com/Silo-Server/silo-server/internal/catalog"
)

// Slots follow the anchors' weights, always add up to the row, and give no
// anchor fewer than min(anchorMinSlots, limit/anchors).
func TestAllocateAnchorSlots(t *testing.T) {
	for _, tc := range []struct {
		name    string
		weights []float64
		limit   int
		want    []int
	}{
		{"two near-even interests", []float64{0.52, 0.48}, 60, []int{31, 29}},
		{"a heavy and a light interest", []float64{0.68, 0.32}, 60, []int{41, 19}},
		{"a light interest lifted to the minimum", []float64{0.6, 0.38, 0.02}, 60, []int{34, 23, 3}},
		{"the minimum shrinks with the row", []float64{0.9, 0.05, 0.05}, 8, []int{4, 2, 2}},
		{"no weight splits evenly", []float64{0, 0}, 5, []int{3, 2}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := allocateAnchorSlots(tc.weights, tc.limit); !slices.Equal(got, tc.want) {
				t.Fatalf("allocateAnchorSlots(%v, %d) = %v, want %v", tc.weights, tc.limit, got, tc.want)
			}
		})
	}
}

// anchorList returns n items named prefix0, prefix1, … .
func anchorList(prefix string, n int) []ScoredItem {
	items := make([]ScoredItem, n)
	for i := range items {
		items[i] = ScoredItem{MediaItemID: fmt.Sprintf("%s%d", prefix, i)}
	}
	return items
}

// Every prefix of the interleaved row holds each anchor's share of its slots,
// to within one item.
func TestInterleaveAnchorsKeepsSharesInEveryPrefix(t *testing.T) {
	slots := []int{35, 22, 3}
	lists := [][]ScoredItem{anchorList("a", 105), anchorList("b", 66), anchorList("c", 9)}

	row := interleaveAnchors(lists, slots, 60)

	if len(row) != 60 {
		t.Fatalf("row length = %d, want 60", len(row))
	}
	counts := map[byte]int{}
	for n, item := range row {
		counts[item.MediaItemID[0]]++
		for i, prefix := range []byte("abc") {
			ideal := float64(slots[i]) * float64(n+1) / 60
			if math.Abs(float64(counts[prefix])-ideal) > 1 {
				t.Fatalf("after %d items anchor %c holds %d, want %.1f±1", n+1, prefix, counts[prefix], ideal)
			}
		}
	}
	for i, prefix := range []byte("abc") {
		if counts[prefix] != slots[i] {
			t.Fatalf("anchor %c holds %d, want its %d slots", prefix, counts[prefix], slots[i])
		}
	}
}

// A title two anchors share is placed once, and an anchor that runs out
// leaves its slots to the others instead of shortening the row.
func TestInterleaveAnchorsDedupesAndPassesUnusedSlots(t *testing.T) {
	shared := ScoredItem{MediaItemID: "shared"}
	lists := [][]ScoredItem{
		append([]ScoredItem{shared}, anchorList("a", 20)...),
		append([]ScoredItem{shared}, anchorList("b", 2)...),
	}

	row := interleaveAnchors(lists, []int{5, 5}, 10)

	ids := mmrItemIDs(row)
	if len(ids) != 10 {
		t.Fatalf("row = %v, want 10 items", ids)
	}
	seen := map[string]bool{}
	for _, id := range ids {
		if seen[id] {
			t.Fatalf("row = %v repeats %s", ids, id)
		}
		seen[id] = true
	}
	if !seen["b0"] || !seen["b1"] {
		t.Fatalf("row = %v, want both of b's own titles", ids)
	}
}

func anchorCluster(idx int, vec []float32, members int, weight float64) TasteCluster {
	return TasteCluster{ClusterIdx: idx, Embedding: vec, MemberCount: members, TotalWeight: weight}
}

// Clusters anchor the main row only for a profile with enough titles and at
// least two clusters. Every cluster is its own anchor, heaviest first, even
// one whose centroid lies near another's: buildTasteClusters has already
// decided which titles are one interest.
func TestMainRowAnchors(t *testing.T) {
	horror, romance := []float32{1, 0, 0}, []float32{0, 1, 0}
	nearHorror := []float32{0.95, 0.05, 0.2}

	anchors := mainRowAnchors([]TasteCluster{
		anchorCluster(0, horror, 8, 4),
		anchorCluster(1, romance, 6, 5),
		anchorCluster(2, nearHorror, 4, 2),
	})
	var weights []float64
	for _, a := range anchors {
		weights = append(weights, a.weight)
	}
	if !slices.Equal(weights, []float64{5, 4, 2}) {
		t.Fatalf("anchor weights = %v, want romance 5, horror 4, near-horror 2", weights)
	}
	if !slices.Equal(anchors[1].embedding, horror) || !slices.Equal(anchors[2].embedding, nearHorror) {
		t.Fatalf("anchors = %+v, want each cluster's own centroid", anchors)
	}

	for name, clusters := range map[string][]TasteCluster{
		"under ten titles": {anchorCluster(0, horror, 5, 4), anchorCluster(1, romance, 4, 3)},
		"one cluster":      {anchorCluster(0, horror, 30, 9)},
	} {
		if got := mainRowAnchors(clusters); got != nil {
			t.Fatalf("%s: anchors = %+v, want none", name, got)
		}
	}
}

// Every title the main row can serve, its first 30 under the daily
// rotation, leaves the cluster rows; a row left thin is emptied, and one that
// shared nothing is kept as it was.
func TestWithoutMainRowItems(t *testing.T) {
	main := anchorList("m", CacheCandidateLimit)
	reach := rotationReach(ServedRowSize)
	overlapping := append(slices.Clone(main[:5]), anchorList("x", 12)...)
	drawable := append(slices.Clone(main[ServedRowSize:reach]), anchorList("v", 12)...)
	thin := append(slices.Clone(main[5:15]), anchorList("y", 9)...)
	separate := anchorList("z", 4)
	tailOnly := append(anchorList("w", 3), main[reach:]...)

	rows := withoutMainRowItems([]ForYouRow{
		{ClusterIndex: 0, Items: overlapping},
		{ClusterIndex: 1, Items: thin},
		{ClusterIndex: 2, Items: separate},
		{ClusterIndex: 3, Items: tailOnly},
		{ClusterIndex: 4, Items: drawable},
	}, main)

	if got := mmrItemIDs(rows[0].Items); !slices.Equal(got, mmrItemIDs(anchorList("x", 12))) {
		t.Fatalf("overlapping row = %v, want its own 12 titles", got)
	}
	if got := mmrItemIDs(rows[4].Items); !slices.Equal(got, mmrItemIDs(anchorList("v", 12))) {
		t.Fatalf("row sharing the rotation's draw ranks = %v, want its own 12 titles", got)
	}
	if rows[1].Items == nil || len(rows[1].Items) != 0 {
		t.Fatalf("thin row = %#v, want it cached empty", rows[1].Items)
	}
	if len(rows[2].Items) != 4 || len(rows[3].Items) != len(tailOnly) {
		t.Fatalf("rows sharing nothing served = %d and %d items, want them whole", len(rows[2].Items), len(rows[3].Items))
	}
}

// A cluster row cached empty was built: reads skip it without asking for a
// refresh, while a cluster with no cached row still asks.
func TestReaderSkipsClusterRowsCachedEmpty(t *testing.T) {
	refresher := &countingReadRefresher{}
	repo := &fakeReaderRepo{
		meta: &TasteProfileMeta{SignalCounts: map[string]int{"watch_high": ColdStartFullPersonalized}},
		clusters: []TasteCluster{
			{ClusterIdx: 0, Label: "Drama", TotalWeight: 3},
			{ClusterIdx: 1, Label: "Comedy", TotalWeight: 2},
		},
		personal: map[string][]ScoredItem{
			RecTypeForYouMain + "|":                {{MediaItemID: "main"}},
			RecTypeForYouClusterPrefix + "0" + "|": {},
			RecTypeForYouClusterPrefix + "1" + "|": {{MediaItemID: "comedy"}},
		},
	}
	r := &Reader{repo: repo, refresh: refresher, signals: NewSignalReader(&fakeSignalRepo{}, fakeSignalProvider{store: &fakeSignalStore{}})}
	ctx := t.Context()

	page, err := r.GetForYouPage(ctx, 7, "p1", 20, catalog.AccessFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if labels := rowLabels(page); !slices.Equal(labels, []string{"For You", clusterTitle("Comedy")}) {
		t.Fatalf("rows = %v, want the main row and the Comedy cluster", labels)
	}
	row, err := r.SectionTasteMatchRow(ctx, 7, "p1", "", catalog.AccessFilter{})
	if err != nil || row == nil || row.ClusterIndex != 1 {
		t.Fatalf("taste match = %+v, %v; want the Comedy cluster", row, err)
	}
	if refresher.calls != 0 {
		t.Fatalf("refreshes = %d, want none for a row cached empty", refresher.calls)
	}

	delete(repo.personal, RecTypeForYouClusterPrefix+"0|")
	if _, err := r.GetForYouPage(ctx, 7, "p1", 20, catalog.AccessFilter{}); err != nil {
		t.Fatal(err)
	}
	if refresher.calls != 1 {
		t.Fatalf("refreshes = %d, want one for a missing cluster row", refresher.calls)
	}
}
