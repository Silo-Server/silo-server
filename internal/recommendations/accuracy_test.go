package recommendations

import (
	"slices"
	"testing"

	"github.com/Silo-Server/silo-server/internal/config"
)

func TestApplyGenreCapCountsAllGenres(t *testing.T) {
	items := []ScoredItem{
		{MediaItemID: "a", Score: 1.0},
		{MediaItemID: "b", Score: 0.9},
		{MediaItemID: "c", Score: 0.8},
		{MediaItemID: "d", Score: 0.7},
	}
	genres := map[string][]string{
		"a": {"Action", "Drama"},
		"b": {"Action", "Comedy"},
		"c": {"Action", "Thriller"},
		"d": {"Comedy"},
	}

	capped := applyGenreCap(items, genres, 0.67)

	if len(capped) != 3 {
		t.Fatalf("expected 3 capped items, got %d", len(capped))
	}
	for _, item := range capped {
		if item.MediaItemID == "c" {
			t.Fatalf("expected lowest-scored Action item to be removed, got %#v", capped)
		}
	}
}

func TestHNSWEfSearchUsesCandidateLimitFloor(t *testing.T) {
	tests := []struct {
		name           string
		candidateLimit int
		want           int
	}{
		{name: "raises small scans", candidateLimit: 40, want: minHNSWEfSearch},
		{name: "keeps exact floor", candidateLimit: minHNSWEfSearch, want: minHNSWEfSearch},
		{name: "keeps larger scans", candidateLimit: 900, want: 900},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := hnswEfSearch(tt.candidateLimit); got != tt.want {
				t.Fatalf("hnswEfSearch(%d) = %d, want %d", tt.candidateLimit, got, tt.want)
			}
		})
	}
}

func TestApplyMediaTypeFloorAddsAvailableSupplementalType(t *testing.T) {
	items := []ScoredItem{
		{MediaItemID: "s1", Score: 1.00},
		{MediaItemID: "s2", Score: 0.99},
		{MediaItemID: "s3", Score: 0.98},
		{MediaItemID: "s4", Score: 0.97},
		{MediaItemID: "s5", Score: 0.96},
		{MediaItemID: "s6", Score: 0.95},
		{MediaItemID: "s7", Score: 0.94},
		{MediaItemID: "s8", Score: 0.93},
		{MediaItemID: "s9", Score: 0.92},
		{MediaItemID: "s10", Score: 0.91},
	}
	candidates := append([]ScoredItem(nil), items...)
	candidates = append(candidates,
		ScoredItem{MediaItemID: "m1", Score: 0.90},
		ScoredItem{MediaItemID: "m2", Score: 0.89},
	)
	mediaTypes := map[string]string{
		"s1": "series", "s2": "series", "s3": "series", "s4": "series", "s5": "series",
		"s6": "series", "s7": "series", "s8": "series", "s9": "series", "s10": "series",
		"m1": "movie", "m2": "movie",
	}

	mixed := applyMediaTypeFloor(items, candidates, mediaTypes)

	if got := len(mixed); got != len(items) {
		t.Fatalf("expected result length to stay %d, got %d", len(items), got)
	}
	if got := countMediaType(mixed, mediaTypes, "movie"); got != 2 {
		t.Fatalf("expected 2 movies from supplemental candidates, got %d in %#v", got, mixed)
	}
	if slices.ContainsFunc(mixed, func(item ScoredItem) bool { return item.MediaItemID == "s10" }) {
		t.Fatalf("expected lowest-ranked series tail item to be replaced, got %#v", mixed)
	}
}

func TestApplyMediaTypeFloorAddsAvailableAudiobooks(t *testing.T) {
	items := []ScoredItem{
		{MediaItemID: "m1", Score: 1.00},
		{MediaItemID: "m2", Score: 0.99},
		{MediaItemID: "m3", Score: 0.98},
		{MediaItemID: "m4", Score: 0.97},
		{MediaItemID: "m5", Score: 0.96},
		{MediaItemID: "m6", Score: 0.95},
		{MediaItemID: "m7", Score: 0.94},
		{MediaItemID: "m8", Score: 0.93},
		{MediaItemID: "m9", Score: 0.92},
		{MediaItemID: "m10", Score: 0.91},
	}
	candidates := append([]ScoredItem(nil), items...)
	candidates = append(candidates,
		ScoredItem{MediaItemID: "a1", Score: 0.90},
		ScoredItem{MediaItemID: "a2", Score: 0.89},
	)
	mediaTypes := map[string]string{
		"m1": "movie", "m2": "movie", "m3": "movie", "m4": "movie", "m5": "movie",
		"m6": "movie", "m7": "movie", "m8": "movie", "m9": "movie", "m10": "movie",
		"a1": "audiobook", "a2": "audiobook",
	}

	mixed := applyMediaTypeFloor(items, candidates, mediaTypes)

	if got := countMediaType(mixed, mediaTypes, "audiobook"); got != 2 {
		t.Fatalf("expected 2 audiobooks from supplemental candidates, got %d in %#v", got, mixed)
	}
}

func TestApplyMediaTypeFloorNoopsWithoutSupplementalType(t *testing.T) {
	items := []ScoredItem{
		{MediaItemID: "s1", Score: 1.00},
		{MediaItemID: "s2", Score: 0.99},
		{MediaItemID: "s3", Score: 0.98},
		{MediaItemID: "s4", Score: 0.97},
		{MediaItemID: "s5", Score: 0.96},
	}
	mediaTypes := map[string]string{
		"s1": "series", "s2": "series", "s3": "series", "s4": "series", "s5": "series",
	}

	mixed := applyMediaTypeFloor(items, items, mediaTypes)

	if !slices.EqualFunc(mixed, items, func(a, b ScoredItem) bool {
		return a.MediaItemID == b.MediaItemID && a.Score == b.Score
	}) {
		t.Fatalf("expected unchanged result without supplemental type, got %#v", mixed)
	}
}

func TestApplyMediaTypeFloorIncludesEbookSupplement(t *testing.T) {
	items := []ScoredItem{
		{MediaItemID: "s1", Score: 1.00},
		{MediaItemID: "s2", Score: 0.99},
		{MediaItemID: "s3", Score: 0.98},
		{MediaItemID: "s4", Score: 0.97},
		{MediaItemID: "s5", Score: 0.96},
	}
	candidates := append([]ScoredItem(nil), items...)
	candidates = append(candidates, ScoredItem{MediaItemID: "e1", Score: 0.95})
	mediaTypes := map[string]string{
		"s1": "series",
		"s2": "series",
		"s3": "series",
		"s4": "series",
		"s5": "series",
		"e1": "ebook",
	}

	mixed := applyMediaTypeFloor(items, candidates, mediaTypes)

	if got := countMediaType(mixed, mediaTypes, "ebook"); got != 1 {
		t.Fatalf("expected 1 ebook from supplemental candidates, got %d in %#v", got, mixed)
	}
}

func TestApplyGenreCapDoesNotCollapseConcentratedRows(t *testing.T) {
	items := []ScoredItem{
		{MediaItemID: "a", Score: 1.0},
		{MediaItemID: "b", Score: 0.9},
		{MediaItemID: "c", Score: 0.8},
		{MediaItemID: "d", Score: 0.7},
		{MediaItemID: "e", Score: 0.6},
		{MediaItemID: "f", Score: 0.5},
	}
	genres := map[string][]string{
		"a": {"Science Fiction", "Drama"},
		"b": {"Science Fiction", "Drama"},
		"c": {"Science Fiction", "Drama"},
		"d": {"Science Fiction", "Drama"},
		"e": {"Science Fiction", "Drama"},
		"f": {"Science Fiction", "Drama"},
	}

	capped := applyGenreCap(items, genres, 0.4)

	if len(capped) != 3 {
		t.Fatalf("got %d capped items, want retained half of concentrated row", len(capped))
	}
	if capped[0].MediaItemID != "a" || capped[1].MediaItemID != "b" || capped[2].MediaItemID != "c" {
		t.Fatalf("unexpected capped items: %#v", capped)
	}
}

func TestCollaborativeSupportAggregatesAcrossPeers(t *testing.T) {
	candidates := map[string]collaborativeCandidate{}

	addCollaborativeSupport(candidates, "shared", 0.4)
	addCollaborativeSupport(candidates, "shared", 0.3)
	addCollaborativeSupport(candidates, "single", 0.6)

	if candidates["shared"].score <= candidates["single"].score {
		t.Fatalf("shared score = %f, single score = %f; expected aggregated shared support to win", candidates["shared"].score, candidates["single"].score)
	}
	if candidates["shared"].support != 2 {
		t.Fatalf("shared support = %d, want 2", candidates["shared"].support)
	}
}

func TestCowatchMatrixTreatsProfilesAsDistinctWatchers(t *testing.T) {
	watchers := map[string][]string{
		"a": {"1:p1", "1:p2"},
		"b": {"1:p1", "1:p2"},
	}

	pairs := computeCowatchMatrix(watchers, 2, 2, 10)
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

func TestMMRLambdaUsesConfiguredGlobalOverride(t *testing.T) {
	engine := &Engine{cfg: config.RecommendationsConfig{DiversityLambda: 0.25}}
	if got := engine.mmrLambda(0.8); got != 0.25 {
		t.Fatalf("mmrLambda = %f, want configured override", got)
	}

	engine.cfg.DiversityLambda = 1.2
	if got := engine.mmrLambda(0.8); got != 0.8 {
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

func TestDeduplicateThenTrimKeepsBackfillCandidates(t *testing.T) {
	seen := map[string]struct{}{"already-seen": {}}
	items := []ScoredItem{
		{MediaItemID: "already-seen", Score: 1.0},
		{MediaItemID: "next-best", Score: 0.9},
		{MediaItemID: "backfill", Score: 0.8},
	}

	row := ForYouRow{Items: deduplicateItems(items, seen)}
	rows := trimRows([]ForYouRow{row}, 2)

	if len(rows[0].Items) != 2 {
		t.Fatalf("got %d items, want 2", len(rows[0].Items))
	}
	if rows[0].Items[0].MediaItemID != "next-best" || rows[0].Items[1].MediaItemID != "backfill" {
		t.Fatalf("unexpected retained items: %#v", rows[0].Items)
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
