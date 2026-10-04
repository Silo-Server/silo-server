package recommendations

import (
	"fmt"
	"math"
	"slices"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/config"
)

// The genre pass keeps MMR's order: an over-represented genre's later items
// move out of the served window, the window fills from the other genres in
// order, and the rest of the row keeps its order with nothing removed.
func TestApplyGenreCapDemotesWithinTheServedWindow(t *testing.T) {
	row := make([]ScoredItem, 60)
	genres := map[string][]string{}
	for i := range row {
		id := fmt.Sprintf("i%02d", i)
		row[i] = ScoredItem{MediaItemID: id, Score: 1 - float64(i)/100}
		switch {
		case i < 30 && i%2 == 0:
			genres[id] = []string{"Thriller", "Crime"}
		case i < 30:
			genres[id] = []string{"Thriller"}
		case i%2 == 0:
			genres[id] = []string{"Drama"}
		default:
			genres[id] = []string{"Comedy", "Comedy"}
		}
	}

	got := mmrItemIDs(applyGenreCap(row, genres))

	var want []string
	for _, span := range [][2]int{{0, 10}, {30, 40}, {10, 30}, {40, 60}} {
		for i := span[0]; i < span[1]; i++ {
			want = append(want, fmt.Sprintf("i%02d", i))
		}
	}
	if !slices.Equal(got, want) {
		t.Fatalf("applyGenreCap = %v, want %v", got, want)
	}
}

// A row with nothing else to offer keeps its order and length: the window
// fills with the over-represented genre instead of coming up short.
func TestApplyGenreCapFillsFromTheSkippedItems(t *testing.T) {
	row := make([]ScoredItem, 30)
	genres := map[string][]string{}
	for i := range row {
		id := fmt.Sprintf("i%02d", i)
		row[i] = ScoredItem{MediaItemID: id}
		genres[id] = []string{"Science Fiction", "Drama"}
	}

	if got := applyGenreCap(row, genres); !slices.Equal(mmrItemIDs(got), mmrItemIDs(row)) {
		t.Fatalf("applyGenreCap = %v, want the row unchanged", mmrItemIDs(got))
	}
}

// Items without genre data are never counted against a genre's places.
func TestApplyGenreCapIgnoresItemsWithoutGenres(t *testing.T) {
	row := make([]ScoredItem, 30)
	genres := map[string][]string{}
	for i := range row {
		id := fmt.Sprintf("i%02d", i)
		row[i] = ScoredItem{MediaItemID: id}
		if i < 15 {
			genres[id] = []string{"Thriller"}
		}
	}

	got := mmrItemIDs(applyGenreCap(row, genres))

	var want []string
	for _, span := range [][2]int{{0, 10}, {15, 25}, {10, 15}, {25, 30}} {
		for i := span[0]; i < span[1]; i++ {
			want = append(want, fmt.Sprintf("i%02d", i))
		}
	}
	if !slices.Equal(got, want) {
		t.Fatalf("applyGenreCap = %v, want %v", got, want)
	}
}

func TestHNSWEfSearchUsesCandidateLimitWithinBounds(t *testing.T) {
	tests := []struct {
		name           string
		candidateLimit int
		want           int
	}{
		{name: "raises small scans", candidateLimit: 40, want: minHNSWEfSearch},
		{name: "keeps exact floor", candidateLimit: minHNSWEfSearch, want: minHNSWEfSearch},
		{name: "keeps larger scans", candidateLimit: 900, want: 900},
		{name: "keeps exact cap", candidateLimit: maxHNSWEfSearch, want: maxHNSWEfSearch},
		{name: "caps at the pgvector maximum", candidateLimit: 1200, want: 1000},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := hnswEfSearch(tt.candidateLimit); got != tt.want {
				t.Fatalf("hnswEfSearch(%d) = %d, want %d", tt.candidateLimit, got, tt.want)
			}
		})
	}
}

// typedRow returns n items of mediaType named prefix00, prefix01, …, scored
// from top down, and records their type.
func typedRow(prefix, mediaType string, n int, top float64, types map[string]string) []ScoredItem {
	items := make([]ScoredItem, n)
	for i := range items {
		id := fmt.Sprintf("%s%02d", prefix, i)
		items[i] = ScoredItem{MediaItemID: id, Score: top - float64(i)/1000}
		types[id] = mediaType
	}
	return items
}

// A movie-only row gets the pool's best series after its served window, in
// place of its last movies, so a TV library section scoping the row still
// fills; the served window does not change.
func TestPlaceTypeSupplementsFillsOnlyTheTail(t *testing.T) {
	types := map[string]string{}
	row := typedRow("m", "movie", CacheCandidateLimit, 0.9, types)
	series := typedRow("s", "series", 20, 0.5, types)
	pool := append(append([]ScoredItem(nil), row...), series...)

	got := placeTypeSupplements(row, pool, types)

	if len(got) != len(row) {
		t.Fatalf("row length = %d, want %d", len(got), len(row))
	}
	floor := mediaTypeFloor(len(row))
	want := append(mmrItemIDs(row[:len(row)-floor]), mmrItemIDs(series[:floor])...)
	if !slices.Equal(mmrItemIDs(got), want) {
		t.Fatalf("placeTypeSupplements = %v, want %v", mmrItemIDs(got), want)
	}
}

// Room is made only from types above the floor, so a type's supplements
// never push its own titles at the row's end back out. Only
// recommendableMediaTypes are supplemented: the pool's audiobooks stay out.
func TestPlaceTypeSupplementsKeepsOtherTypesAtTheirFloor(t *testing.T) {
	types := map[string]string{}
	row := append(typedRow("s", "series", 50, 0.9, types), typedRow("m", "movie", 10, 0.8, types)...)
	movies := typedRow("n", "movie", 5, 0.5, types)
	books := typedRow("a", "audiobook", 15, 0.4, types)
	pool := append(append(append([]ScoredItem(nil), row...), movies...), books...)

	got := placeTypeSupplements(row, pool, types)

	floor := mediaTypeFloor(len(row))
	if !slices.Equal(mmrItemIDs(got[:ServedRowSize]), mmrItemIDs(row[:ServedRowSize])) {
		t.Fatalf("served window changed: %v", mmrItemIDs(got[:ServedRowSize]))
	}
	for mediaType, want := range map[string]int{"movie": floor, "series": len(row) - floor, "audiobook": 0} {
		if n := countMediaType(got, types, mediaType); n != want {
			t.Fatalf("%s count = %d, want %d in %v", mediaType, n, want, mmrItemIDs(got))
		}
	}
	for _, id := range mmrItemIDs(row[50:]) {
		if !slices.Contains(mmrItemIDs(got), id) {
			t.Fatalf("row %v lost movie %s", mmrItemIDs(got), id)
		}
	}
}

// A row no longer than the served window, or a pool with no other type,
// stays as it is.
func TestPlaceTypeSupplementsNoops(t *testing.T) {
	types := map[string]string{}
	short := typedRow("m", "movie", ServedRowSize, 0.9, types)
	series := typedRow("s", "series", 20, 0.5, types)
	if got := placeTypeSupplements(short, append(short, series...), types); !slices.Equal(mmrItemIDs(got), mmrItemIDs(short)) {
		t.Fatalf("short row = %v, want unchanged", mmrItemIDs(got))
	}
	row := typedRow("x", "movie", CacheCandidateLimit, 0.9, types)
	if got := placeTypeSupplements(row, row, types); !slices.Equal(mmrItemIDs(got), mmrItemIDs(row)) {
		t.Fatalf("single-type row = %v, want unchanged", mmrItemIDs(got))
	}
}

func TestCollaborativeSupportAggregatesAcrossPeers(t *testing.T) {
	candidates := collaborativeCandidates([]peerLikes{
		{userID: 1, similarity: 1, weights: map[string]float64{"shared": 0.4, "single": 0.6}},
		{userID: 2, similarity: 1, weights: map[string]float64{"shared": 0.3, "single-other": 0.6}},
	})

	shared, ok := candidates["shared"]
	if !ok {
		t.Fatalf("candidates = %#v, want the title two accounts liked", candidates)
	}
	if math.Abs(shared.score-0.7) > 1e-9 {
		t.Fatalf("shared score = %f, want the peers' scores summed", shared.score)
	}
	if len(shared.accounts) != 2 {
		t.Fatalf("shared accounts = %d, want 2", len(shared.accounts))
	}
}

func TestCowatchMatrixTreatsProfilesAsDistinctWatchers(t *testing.T) {
	watchers := map[string][]string{
		"a": {"1:p1", "1:p2"},
		"b": {"1:p1", "1:p2"},
	}

	pairs, err := computeCowatchMatrix(t.Context(), watchers, 2, 2, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(pairs) != 2 {
		t.Fatalf("got %d co-watch pairs, want 2: %#v", len(pairs), pairs)
	}
	for _, pair := range pairs {
		if pair.CowatchCount != 2 {
			t.Fatalf("cowatch count = %d, want two profile identities: %#v", pair.CowatchCount, pair)
		}
	}
}

func TestCompatiblePeerContentRatingsIncludesLowerAndExcludesHigher(t *testing.T) {
	ratings := compatiblePeerContentRatings("PG-13")

	for _, want := range []string{"G", "PG", "PG-13", "TV-14"} {
		if !slices.Contains(ratings, want) {
			t.Fatalf("expected %q in compatible ratings: %#v", want, ratings)
		}
	}
	for _, blocked := range []string{"R", "NC-17", "TV-MA"} {
		if slices.Contains(ratings, blocked) {
			t.Fatalf("did not expect %q in compatible ratings: %#v", blocked, ratings)
		}
	}
}

// The profile editor writes bare ages for ceilings outside the US ladder, so a
// peer carrying "12" must be a neighbor of a PG-13 viewer, and a peer carrying
// "16" must not be.
func TestCompatiblePeerContentRatingsComparesAgeCeilings(t *testing.T) {
	ratings := compatiblePeerContentRatings("PG-13")
	if !slices.Contains(ratings, "12") {
		t.Fatalf("expected age ceiling 12 in compatible ratings: %#v", ratings)
	}
	if slices.Contains(ratings, "16") {
		t.Fatalf("did not expect age ceiling 16 in compatible ratings: %#v", ratings)
	}

	ratings = compatiblePeerContentRatings("15")
	if !slices.Contains(ratings, "PG-13") || slices.Contains(ratings, "R") {
		t.Fatalf("a 15 ceiling must admit PG-13 peers and exclude R peers: %#v", ratings)
	}
}

func TestMMRLambdaUsesConfiguredGlobalOverride(t *testing.T) {
	engine := &Engine{cfg: config.RecommendationsConfig{DiversityLambda: 0.25}}
	if got := engine.mmrLambda(); got != 0.25 {
		t.Fatalf("mmrLambda = %f, want configured override", got)
	}

	engine.cfg.DiversityLambda = 1.2
	if got := engine.mmrLambda(); got != defaultMMRLambda {
		t.Fatalf("mmrLambda = %f, want fallback default for invalid override", got)
	}
}

func TestEmbeddingTextNeedsRefreshIncludesEmptyCanonicalText(t *testing.T) {
	if !embeddingTextNeedsRefresh("model-a", "", "generated text", "model-a") {
		t.Fatal("expected same-model row with empty canonical text to be stale")
	}
	if !embeddingTextNeedsRefresh("model-a", "old text", "generated text", "model-a") {
		t.Fatal("expected changed canonical text to be stale")
	}
	if embeddingTextNeedsRefresh("model-a", "generated text", "generated text", "model-a") {
		t.Fatal("did not expect matching model and canonical text to be stale")
	}
}

func TestBuildTasteClustersDeterministic(t *testing.T) {
	items := []clusterItem{
		clusterTestItem("a1", []float32{1, 0}, 1, "Action"),
		clusterTestItem("a2", []float32{0.98, 0.02}, 0.9, "Action"),
		clusterTestItem("a3", []float32{0.95, 0.05}, 0.8, "Action"),
		clusterTestItem("a4", []float32{0.9, 0.1}, 0.7, "Action"),
		clusterTestItem("a5", []float32{0.88, 0.12}, 0.6, "Action"),
		clusterTestItem("a6", []float32{0.86, 0.14}, 0.5, "Action"),
		clusterTestItem("d1", []float32{0, 1}, 1, "Drama"),
		clusterTestItem("d2", []float32{0.02, 0.98}, 0.9, "Drama"),
		clusterTestItem("d3", []float32{0.05, 0.95}, 0.8, "Drama"),
		clusterTestItem("d4", []float32{0.1, 0.9}, 0.7, "Drama"),
		clusterTestItem("d5", []float32{0.12, 0.88}, 0.6, "Drama"),
		clusterTestItem("d6", []float32{0.14, 0.86}, 0.5, "Drama"),
	}

	first := buildTasteClusters(items)
	second := buildTasteClusters(items)

	if len(first) != len(second) {
		t.Fatalf("cluster count changed: %d vs %d", len(first), len(second))
	}
	for i := range first {
		if first[i].Label != second[i].Label ||
			first[i].MemberCount != second[i].MemberCount ||
			first[i].TotalWeight != second[i].TotalWeight {
			t.Fatalf("cluster %d changed: %#v vs %#v", i, first[i], second[i])
		}
	}
}

func TestDeduplicateKeepsBackfillCandidates(t *testing.T) {
	seen := map[string]struct{}{"already-seen": {}}
	items := []ScoredItem{
		{MediaItemID: "already-seen", Score: 1.0},
		{MediaItemID: "next-best", Score: 0.9},
		{MediaItemID: "backfill", Score: 0.8},
		{MediaItemID: "not-shown", Score: 0.7},
	}

	kept := deduplicateItems(items, seen, 2)

	if len(kept) != 2 || kept[0].MediaItemID != "next-best" || kept[1].MediaItemID != "backfill" {
		t.Fatalf("unexpected retained items: %#v", kept)
	}
	// Only shown items are spent: the row's third survivor stays free for a
	// later row.
	if _, spent := seen["not-shown"]; spent {
		t.Fatal("an item past the row's limit was marked seen")
	}
	if later := deduplicateItems([]ScoredItem{{MediaItemID: "backfill"}, {MediaItemID: "not-shown"}}, seen, 2); len(later) != 1 || later[0].MediaItemID != "not-shown" {
		t.Fatalf("later row kept %#v, want only not-shown", later)
	}
}

func clusterTestItem(id string, embedding []float32, weight float64, genre string) clusterItem {
	return clusterItem{
		itemID:    id,
		embedding: embedding,
		weight:    weight,
		genres:    []string{genre},
	}
}

// The recency boost multiplies a new title's score by up to
// RecencyBoostMultiplier, less the older it is, and keeps the pool's order;
// old, undated and negatively scored titles keep their scores.
func TestApplyRecencyBoostKeepsOrder(t *testing.T) {
	now := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	items := []ScoredItem{
		{MediaItemID: "old", Score: 0.9}, {MediaItemID: "new", Score: 0.8}, {MediaItemID: "undated", Score: 0.7},
		{MediaItemID: "week-old", Score: 0.6}, {MediaItemID: "old-2", Score: 0.5}, {MediaItemID: "old-3", Score: 0.4},
		{MediaItemID: "old-4", Score: 0.3}, {MediaItemID: "new-negative", Score: -0.1},
	}
	added := map[string]time.Time{
		"old": now.AddDate(-1, 0, 0), "new": now, "week-old": now.AddDate(0, 0, -RecencyBoostDays/2),
		"old-2": now.AddDate(0, 0, -RecencyBoostDays), "old-3": now.AddDate(-1, 0, 0), "old-4": now.AddDate(-1, 0, 0),
		"new-negative": now,
	}
	// Four more old titles keep the new ones at a quarter of the pool.
	for i := range 4 {
		id := fmt.Sprintf("old-%d", 5+i)
		items = append(items, ScoredItem{MediaItemID: id, Score: 0.2})
		added[id] = now.AddDate(-1, 0, 0)
	}

	got := applyRecencyBoost(items, added, now)

	if !slices.Equal(mmrItemIDs(got), mmrItemIDs(items)) {
		t.Fatalf("order = %v, want %v", mmrItemIDs(got), mmrItemIDs(items))
	}
	if want := 0.8 * RecencyBoostMultiplier; math.Abs(got[1].Score-want) > 1e-9 {
		t.Fatalf("new title score = %f, want %f", got[1].Score, want)
	}
	if want := 0.6 * (1 + (RecencyBoostMultiplier-1)/2); math.Abs(got[3].Score-want) > 1e-9 {
		t.Fatalf("half-window title score = %f, want %f", got[3].Score, want)
	}
	for _, i := range []int{0, 2, 4, 5, 6, 7, 8, 9, 10, 11} {
		if got[i].Score != items[i].Score {
			t.Fatalf("%s score = %f, want it unchanged at %f", items[i].MediaItemID, got[i].Score, items[i].Score)
		}
	}
	if items[1].Score != 0.8 {
		t.Fatalf("input changed: %v", items)
	}
}

// A pool with more than a quarter of its titles inside the window, such as
// a freshly imported library, keeps every score: boosting it would rank by
// scan order.
func TestApplyRecencyBoostSkipsBulkImports(t *testing.T) {
	now := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	items := scoredRun("item", 8)
	added := map[string]time.Time{}
	for i, item := range items {
		added[item.MediaItemID] = now.AddDate(-1, 0, 0)
		if i < 3 {
			added[item.MediaItemID] = now.AddDate(0, 0, -i)
		}
	}
	if got := applyRecencyBoost(items, added, now); !slices.Equal(got, items) {
		t.Fatalf("3 of 8 new: scores %v, want them unchanged", got)
	}
	added[items[2].MediaItemID] = now.AddDate(-1, 0, 0)
	if got := applyRecencyBoost(items, added, now); got[0].Score == items[0].Score || got[2].Score != items[2].Score {
		t.Fatalf("2 of 8 new: scores %v, want only the new titles boosted", got)
	}
}

// Applied before selection, the boost lets a new title just below the
// selection margin into the row, while a new title far below it stays out.
func TestRecencyBoostLiftsNearMissesIntoTheRow(t *testing.T) {
	now := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	var pool []ScoredItem
	added := map[string]time.Time{}
	for i := range 40 {
		id := fmt.Sprintf("old-%02d", i)
		pool = append(pool, ScoredItem{MediaItemID: id, Score: 0.80 - 0.005*float64(i)})
		added[id] = now.AddDate(-1, 0, 0)
	}
	// The row takes 20: the margin is old-19 at 0.705.
	pool = append(pool, ScoredItem{MediaItemID: "new-near", Score: 0.69}, ScoredItem{MediaItemID: "new-far", Score: 0.55})
	added["new-near"], added["new-far"] = now, now

	pick := func(items []ScoredItem) []string {
		return mmrItemIDs(applyMMR(items, nil, defaultMMRLambda, ServedRowSize))
	}
	if got := pick(pool); slices.Contains(got, "new-near") {
		t.Fatalf("without the boost the row %v holds new-near", got)
	}
	got := pick(applyRecencyBoost(pool, added, now))
	if !slices.Contains(got, "new-near") || slices.Contains(got, "new-far") {
		t.Fatalf("boosted row %v, want new-near in and new-far out", got)
	}
}
