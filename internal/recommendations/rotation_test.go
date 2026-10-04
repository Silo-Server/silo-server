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

// A rotation keeps the head, draws the rest of the served window from the
// next ranks in rank order, and loses no item; it is the same all day.
func TestRotateTailKeepsTheHeadAndDrawsFromTheNextRanks(t *testing.T) {
	items := scoredRun("m", CacheCandidateLimit)
	input := slices.Clone(items)
	for _, tc := range []struct{ limit, pin, end int }{
		{20, 10, 30},
		{12, 6, 18},
		{50, 10, CacheCandidateLimit},
	} {
		got := rotateTail(items, tc.limit, rotationSeed, rotationDay)
		if !slices.Equal(got[:tc.pin], items[:tc.pin]) {
			t.Fatalf("limit %d: head %v, want the first %d unchanged", tc.limit, scoredIDs(got[:tc.pin]), tc.pin)
		}
		was := position(items)
		last := -1
		for _, item := range got[tc.pin:tc.limit] {
			at := was[item.MediaItemID]
			if at < tc.pin || at >= tc.end {
				t.Fatalf("limit %d: drew %s from rank %d, outside %d-%d", tc.limit, item.MediaItemID, at, tc.pin, tc.end-1)
			}
			if at <= last {
				t.Fatalf("limit %d: draws %v are not in rank order", tc.limit, scoredIDs(got[tc.pin:tc.limit]))
			}
			last = at
		}
		if !slices.Equal(slices.Sorted(slices.Values(scoredIDs(got))), scoredIDs(items)) {
			t.Fatalf("limit %d: rotation lost or repeated items: %v", tc.limit, scoredIDs(got))
		}
		if later := rotateTail(items, tc.limit, rotationSeed, rotationDay.Add(14*time.Hour)); !slices.Equal(later, got) {
			t.Fatalf("limit %d: the rotation changed within the day", tc.limit)
		}
	}
	if !slices.Equal(items, input) {
		t.Fatal("rotateTail changed its input")
	}
}

// A row no longer than the window, or a window of at most ten, is served in
// rank order.
func TestRotateTailLeavesShortRowsAndSmallWindowsAlone(t *testing.T) {
	items := scoredRun("m", CacheCandidateLimit)
	for _, tc := range []struct {
		items []ScoredItem
		limit int
	}{
		{items, 10},
		{items, 5},
		{items[:20], 20},
		{items[:15], 20},
		{items, CacheCandidateLimit},
	} {
		if got := rotateTail(tc.items, tc.limit, rotationSeed, rotationDay); !slices.Equal(got, tc.items) {
			t.Fatalf("%d items, limit %d: rotated to %v", len(tc.items), tc.limit, scoredIDs(got))
		}
	}
}

// Each day draws its own tail, and so do other profiles and rows: on a
// 60-item row at least 2 of the 20 served change from day to day, and a
// title two rows share is drawn independently in each.
func TestRotateTailChangesDailyAndPerRow(t *testing.T) {
	items := scoredRun("m", CacheCandidateLimit)
	served := func(seed string, day time.Time) []string {
		return scoredIDs(rotateTail(items, ServedRowSize, seed, day)[:ServedRowSize])
	}
	changed := func(a, b []string) int {
		n := 0
		for _, id := range a {
			if !slices.Contains(b, id) {
				n++
			}
		}
		return n
	}
	today := served(rotationSeed, rotationDay)
	for d := 1; d <= 14; d++ {
		next := served(rotationSeed, rotationDay.AddDate(0, 0, d))
		if n := changed(today, next); n < 2 {
			t.Fatalf("day +%d changes %d of %d served items, want at least 2", d, n, ServedRowSize)
		}
		if !slices.Equal(today[:rotationMaxPinned], next[:rotationMaxPinned]) {
			t.Fatalf("day +%d moved the pinned head", d)
		}
	}
	for _, seed := range []string{
		rotationSeedKey(8, "p1", RecTypeForYouMain),
		rotationSeedKey(7, "p2", RecTypeForYouMain),
		rotationSeedKey(7, "p1", RecTypeForYouClusterPrefix+"0"),
	} {
		if slices.Equal(served(seed, rotationDay), today) {
			t.Fatalf("seed %q draws the same tail as %q", seed, rotationSeed)
		}
	}
}

// Reads rotate the main row and the cluster rows, the same way all day and
// on every node, and leave Because You Watched and the global rows in rank
// order. The section read rotates like a default page and keeps the whole
// pool, and a "see all" page is never rotated.
func TestReadsRotateOnlyThePersonalRows(t *testing.T) {
	pool := scoredRun("m", CacheCandidateLimit)
	cluster := scoredRun("c", CacheCandidateLimit)
	for i := range cluster {
		cluster[i].Reason = clusterTitle("Drama")
	}
	popular := scoredRun("pop", CacheCandidateLimit)
	repo := &fakeReaderRepo{
		meta:     &TasteProfileMeta{SignalCounts: map[string]int{signalCountPositiveTitles: ColdStartFullPersonalized}},
		clusters: []TasteCluster{{ClusterIdx: 0, Label: "Drama", TotalWeight: 1}},
		personal: map[string][]ScoredItem{
			RecTypeForYouMain + "|":                pool,
			RecTypeForYouClusterPrefix + "0" + "|": cluster,
			RecTypeBecauseWatched + "|anchor":      scoredRun("b", CacheCandidateLimit),
		},
		global: map[string][]ScoredItem{RecTypePopular: popular},
	}
	reader := func(at time.Time) *Reader {
		return &Reader{
			repo:    repo,
			signals: NewSignalReader(&fakeSignalRepo{fallbackRecentCompleted: []string{"anchor"}}, nil),
			now:     func() time.Time { return at },
		}
	}
	ctx := t.Context()
	page := func(r *Reader) map[string][]string {
		rows, err := r.GetForYouPage(ctx, 7, "p1", ServedRowSize, catalog.AccessFilter{})
		if err != nil {
			t.Fatal(err)
		}
		out := map[string][]string{}
		for _, row := range rows {
			out[row.Label] = scoredIDs(row.Items)
		}
		return out
	}

	morning, evening, tomorrow := page(reader(rotationDay)), page(reader(rotationDay.Add(12*time.Hour))), page(reader(rotationDay.AddDate(0, 0, 1)))
	for label, ranked := range map[string][]ScoredItem{ForYouLabel: pool, clusterTitle("Drama"): cluster} {
		if slices.Equal(morning[label], scoredIDs(ranked[:ServedRowSize])) {
			t.Fatalf("%s served in rank order, want it rotated", label)
		}
		if !slices.Equal(morning[label], evening[label]) {
			t.Fatalf("%s rotated differently within the day", label)
		}
		if slices.Equal(morning[label], tomorrow[label]) {
			t.Fatalf("%s served the same rotation the next day", label)
		}
	}
	if !slices.Equal(morning[popularLabel], scoredIDs(popular[:ServedRowSize])) {
		t.Fatalf("Popular = %v, want rank order", morning[popularLabel])
	}

	r := reader(rotationDay)
	byw, err := r.GetBecauseYouWatchedRows(ctx, 7, "p1", 1, ServedRowSize, catalog.AccessFilter{})
	if err != nil || len(byw) != 1 || !slices.Equal(scoredIDs(byw[0].Items), scoredIDs(scoredRun("b", ServedRowSize))) {
		t.Fatalf("Because You Watched = %+v, %v; want rank order", byw, err)
	}
	section, err := r.SectionForYouMain(ctx, 7, "p1", catalog.AccessFilter{})
	if err != nil || section == nil || !slices.Equal(scoredIDs(section.Items[:ServedRowSize]), morning[ForYouLabel]) || len(section.Items) != CacheCandidateLimit {
		t.Fatalf("section row = %v, %v; want the page's rotation and the whole pool", section, err)
	}
	seeAll, err := r.GetSection(ctx, 7, "p1", SectionKindForYouMain, "", CacheCandidateLimit, catalog.AccessFilter{})
	if err != nil || seeAll == nil || !slices.Equal(scoredIDs(seeAll.Items), scoredIDs(pool)) {
		t.Fatalf("see all = %v, %v; want rank order", seeAll, err)
	}
}
