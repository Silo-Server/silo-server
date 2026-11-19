package recommendations

import (
	"slices"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/catalog"
)

var (
	rotationDay  = time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	rotationSeed = rotationSeedKey(7, "p1", RecTypeForYouMain)
)

// position maps each item of items to its index.
func position(items []ScoredItem) map[string]int {
	at := make(map[string]int, len(items))
	for i, item := range items {
		at[item.MediaItemID] = i
	}
	return at
}

// A page of any limit serves each personal row in the order its see-all
// page opens with: the served window is ServedRowSize whatever the limit, so
// a shorter page is a prefix of it and a longer one continues it.
func TestPagesOfAnyLimitMatchTheSeeAllOrder(t *testing.T) {
	pool := scoredRun("m", CacheCandidateLimit)
	cluster := scoredRun("c", CacheCandidateLimit)
	for i := range cluster {
		cluster[i].Reason = clusterTitle("Drama")
	}
	r := &Reader{
		repo: &fakeReaderRepo{
			meta:     &TasteProfileMeta{SignalCounts: map[string]int{signalCountPositiveTitles: ColdStartFullPersonalized}},
			clusters: []TasteCluster{{ClusterIdx: 0, Label: "Drama", TotalWeight: 1}},
			personal: map[string][]ScoredItem{
				RecTypeForYouMain + "|":                pool,
				RecTypeForYouClusterPrefix + "0" + "|": cluster,
			},
		},
		signals: NewSignalReader(&fakeSignalRepo{}, nil),
		now:     func() time.Time { return rotationDay },
	}
	ctx := t.Context()
	seeAll := map[string][]string{}
	for kind, label := range map[string]string{SectionKindForYouMain: ForYouLabel, SectionKindCluster: clusterTitle("Drama")} {
		key := map[string]string{SectionKindForYouMain: "", SectionKindCluster: "0"}[kind]
		row, err := r.GetSection(ctx, 7, "p1", kind, key, CacheCandidateLimit, catalog.AccessFilter{})
		if err != nil || row == nil {
			t.Fatalf("see all %s = %v, %v", kind, row, err)
		}
		seeAll[label] = scoredIDs(row.Items)
	}
	for _, limit := range []int{10, 15, ServedRowSize, 30} {
		rows, err := r.GetForYouPage(ctx, 7, "p1", limit, catalog.AccessFilter{})
		if err != nil {
			t.Fatal(err)
		}
		if len(rows) != 2 {
			t.Fatalf("limit %d: rows = %v, want the main and cluster rows", limit, rowLabels(rows))
		}
		for _, row := range rows {
			if got := scoredIDs(row.Items); !slices.Equal(got, seeAll[row.Label][:limit]) {
				t.Fatalf("limit %d: %s = %v, want the see-all page's first %d %v", limit, row.Label, got, limit, seeAll[row.Label][:limit])
			}
		}
	}
}
