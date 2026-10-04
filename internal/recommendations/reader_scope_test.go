package recommendations

import (
	"context"
	"fmt"
	"slices"
	"testing"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

// scoredRun returns n items named prefix-00, prefix-01, ... in descending
// score order.
func scoredRun(prefix string, n int) []ScoredItem {
	items := make([]ScoredItem, n)
	for i := range items {
		items[i] = ScoredItem{MediaItemID: fmt.Sprintf("%s-%02d", prefix, i), Score: float64(n - i)}
	}
	return items
}

func scoredIDs(items []ScoredItem) []string {
	out := make([]string, len(items))
	for i, item := range items {
		out[i] = item.MediaItemID
	}
	return out
}

func TestNormalizeRecommendationLimit(t *testing.T) {
	for _, tc := range []struct{ in, want int }{
		{-1, 20}, {0, 20}, {1, 1}, {20, 20}, {21, 21}, {50, 50}, {51, 50}, {CacheCandidateLimit, 50},
	} {
		if got := normalizeRecommendationLimit(tc.in); got != tc.want {
			t.Errorf("normalizeRecommendationLimit(%d) = %d, want %d", tc.in, got, tc.want)
		}
	}
}

// Public reads answer up to the v2 maximum of 50 items per row; section
// reads answer every cached candidate, so a section can scope the row to
// its libraries before trimming it.
func TestPublicReadsServeUpToFiftyAndSectionReadsTheWholePool(t *testing.T) {
	pool := scoredRun("m", CacheCandidateLimit)
	repo := &fakeReaderRepo{
		meta:     &TasteProfileMeta{SignalCounts: map[string]int{"watch_high": ColdStartFullPersonalized}},
		clusters: []TasteCluster{{ClusterIdx: 0, Label: "Drama", TotalWeight: 1}},
		personal: map[string][]ScoredItem{
			RecTypeForYouMain + "|":                pool,
			RecTypeSimilarUsersLiked + "|":         pool,
			RecTypeForYouClusterPrefix + "0" + "|": pool,
		},
	}
	r := &Reader{repo: repo, signals: NewSignalReader(&fakeSignalRepo{}, nil)}
	ctx := t.Context()

	for _, tc := range []struct {
		limit, want int
	}{{0, 20}, {50, 50}, {60, 50}} {
		main, err := r.GetForYouMain(ctx, 7, "p1", tc.limit, catalog.AccessFilter{})
		if err != nil || main == nil || len(main.Items) != tc.want {
			t.Fatalf("GetForYouMain(limit %d) = %v, %v; want %d items", tc.limit, main, err, tc.want)
		}
		similar, err := r.GetSimilarUsersLiked(ctx, 7, "p1", tc.limit, catalog.AccessFilter{})
		if err != nil || len(similar) != tc.want {
			t.Fatalf("GetSimilarUsersLiked(limit %d) = %d items, %v; want %d", tc.limit, len(similar), err, tc.want)
		}
	}

	main, err := r.SectionForYouMain(ctx, 7, "p1", catalog.AccessFilter{})
	if err != nil || main == nil || !slices.Equal(scoredIDs(main.Items), scoredIDs(pool)) {
		t.Fatalf("SectionForYouMain = %v, %v; want the whole cached pool", main, err)
	}
	similar, err := r.SectionSimilarUsersLiked(ctx, 7, "p1", catalog.AccessFilter{})
	if err != nil || len(similar) != CacheCandidateLimit {
		t.Fatalf("SectionSimilarUsersLiked = %d items, %v; want %d", len(similar), err, CacheCandidateLimit)
	}
	taste, err := r.SectionTasteMatchRow(ctx, 7, "p1", "", catalog.AccessFilter{})
	if err != nil || taste == nil || len(taste.Items) != CacheCandidateLimit {
		t.Fatalf("SectionTasteMatchRow = %v, %v; want %d items", taste, err, CacheCandidateLimit)
	}
}

// anchorStore is a store whose profile p1 completed the given items, the
// first most recently.
func anchorStore(itemIDs ...string) *fakeSignalStore {
	store := &fakeSignalStore{}
	for i, id := range itemIDs {
		store.progress = append(store.progress, userstore.WatchProgress{
			ProfileID:   "p1",
			MediaItemID: id,
			Completed:   true,
			UpdatedAt:   fmt.Sprintf("2026-08-10T12:%02d:00Z", 59-i),
		})
	}
	return store
}

// A section's Because You Watched row takes the most recent anchor whose row
// keeps items in the section's libraries, not the most recent anchor.
func TestSectionBecauseYouWatchedPicksAnAnchorWithItemsInScope(t *testing.T) {
	const tvLibrary, movieLibrary = 1, 2
	seriesRow, movieRow := scoredRun("series", 5), scoredRun("movie", 5)
	libraries := map[string]int{}
	for _, item := range seriesRow {
		libraries[item.MediaItemID] = tvLibrary
	}
	for _, item := range movieRow {
		libraries[item.MediaItemID] = movieLibrary
	}
	refresher := &countingReadRefresher{}
	r := &Reader{
		repo: &fakeReaderRepo{
			personal: map[string][]ScoredItem{
				RecTypeBecauseWatched + "|latest-series": seriesRow,
				RecTypeBecauseWatched + "|older-movie":   movieRow,
			},
			libraries: libraries,
		},
		refresh: refresher,
		signals: NewSignalReader(&fakeSignalRepo{}, fakeSignalProvider{store: anchorStore("latest-series", "older-movie")}),
	}
	ctx := t.Context()

	for _, tc := range []struct {
		name       string
		libraryIDs []int
		filter     catalog.AccessFilter
		wantSource string
		wantItems  []ScoredItem
	}{
		{"no scope", nil, catalog.AccessFilter{}, "latest-series", seriesRow},
		{"TV library", []int{tvLibrary}, catalog.AccessFilter{}, "latest-series", seriesRow},
		{"movie library", []int{movieLibrary}, catalog.AccessFilter{}, "older-movie", movieRow},
		{"movie library the viewer cannot see", []int{movieLibrary}, catalog.AccessFilter{AllowedLibraryIDs: []int{tvLibrary}}, "", nil},
		{"viewer limited to movies", nil, catalog.AccessFilter{AllowedLibraryIDs: []int{movieLibrary}}, "older-movie", movieRow},
	} {
		t.Run(tc.name, func(t *testing.T) {
			items, source, err := r.SectionBecauseYouWatched(ctx, 7, "p1", "", tc.libraryIDs, tc.filter)
			if err != nil {
				t.Fatal(err)
			}
			if source != tc.wantSource || !slices.Equal(scoredIDs(items), scoredIDs(tc.wantItems)) {
				t.Fatalf("source=%q items=%v; want source %q items %v", source, scoredIDs(items), tc.wantSource, scoredIDs(tc.wantItems))
			}
		})
	}
	// Every anchor had a cached row; filtering emptied them, which a refresh
	// would not change.
	if refresher.calls != 0 {
		t.Fatalf("refreshes = %d, want none", refresher.calls)
	}
}

// An anchor whose cached row the profile has watched through moves on to
// the next anchor instead of answering an empty row.
func TestSectionBecauseYouWatchedMovesPastAnEmptiedAnchor(t *testing.T) {
	store := anchorStore("first", "second")
	store.progress = append(store.progress, userstore.WatchProgress{ProfileID: "p1", MediaItemID: "seen", Completed: true, UpdatedAt: "2026-08-01T00:00:00Z"})
	r := &Reader{
		repo: &fakeReaderRepo{personal: map[string][]ScoredItem{
			RecTypeBecauseWatched + "|first":  {{MediaItemID: "seen", Score: 1}},
			RecTypeBecauseWatched + "|second": {{MediaItemID: "fresh", Score: 1}},
		}},
		signals: NewSignalReader(&fakeSignalRepo{}, fakeSignalProvider{store: store}),
	}
	items, source, err := r.SectionBecauseYouWatched(t.Context(), 7, "p1", "", nil, catalog.AccessFilter{})
	if err != nil || source != "second" || !slices.Equal(scoredIDs(items), []string{"fresh"}) {
		t.Fatalf("source=%q items=%v err=%v; want the second anchor's row", source, scoredIDs(items), err)
	}
}

// A profile whose recent completions were all deleted from the catalog has
// no anchor, so reading the row asks for no refresh that could never fill it.
func TestBecauseYouWatchedWithOnlyDeletedCompletionsRequestsNoRefresh(t *testing.T) {
	refresher := &countingReadRefresher{}
	r := &Reader{
		repo:    &fakeReaderRepo{},
		refresh: refresher,
		signals: NewSignalReader(
			&fakeSignalRepo{missing: map[string]struct{}{"deleted-movie": {}, "orphan-episode": {}}},
			fakeSignalProvider{store: anchorStore("deleted-movie", "orphan-episode")},
		),
	}
	for range 3 {
		items, source, err := r.SectionBecauseYouWatched(t.Context(), 7, "p1", "", nil, catalog.AccessFilter{})
		if err != nil || len(items) != 0 || source != "" {
			t.Fatalf("items=%v source=%q err=%v; want an empty row", items, source, err)
		}
	}
	if refresher.calls != 0 {
		t.Fatalf("refreshes = %d, want none", refresher.calls)
	}
}

// progressPageCounter counts the full progress walks the watched set makes.
type progressPageCounter struct {
	*fakeSignalStore
	allPages int
}

func (s *progressPageCounter) ListProgressPage(ctx context.Context, profileID, status string, after *userstore.ProgressKey, limit int) ([]userstore.WatchProgress, error) {
	if status == "all" {
		s.allPages++
	}
	return s.fakeSignalStore.ListProgressPage(ctx, profileID, status, after, limit)
}

// One request loads the profile's exclusion set once, however many rows,
// clusters or anchors it filters.
func TestReadLoadsTheExclusionSetOncePerRequest(t *testing.T) {
	store := &progressPageCounter{fakeSignalStore: anchorStore("first", "second", "third")}
	r := &Reader{
		repo: &fakeReaderRepo{
			meta: &TasteProfileMeta{SignalCounts: map[string]int{"watch_high": ColdStartFullPersonalized}},
			clusters: []TasteCluster{
				{ClusterIdx: 0, Label: "Drama", TotalWeight: 3},
				{ClusterIdx: 1, Label: "Comedy", TotalWeight: 2},
			},
			personal: map[string][]ScoredItem{
				RecTypeForYouMain + "|":                {{MediaItemID: "main"}},
				RecTypeSimilarUsersLiked + "|":         {{MediaItemID: "similar"}},
				RecTypeForYouClusterPrefix + "0" + "|": {{MediaItemID: "first"}},
				RecTypeForYouClusterPrefix + "1" + "|": {{MediaItemID: "comedy"}},
				RecTypeBecauseWatched + "|first":       {{MediaItemID: "second"}},
				RecTypeBecauseWatched + "|second":      {{MediaItemID: "third"}},
				RecTypeBecauseWatched + "|third":       {{MediaItemID: "fresh"}},
			},
		},
		signals: NewSignalReader(&fakeSignalRepo{}, fakeSignalProvider{store: store}),
	}
	ctx := t.Context()
	for name, read := range map[string]func() error{
		"discover": func() error {
			_, err := r.GetDiscoverRows(ctx, 7, "p1", 20, catalog.AccessFilter{})
			return err
		},
		"taste match past a watched cluster": func() error {
			row, err := r.SectionTasteMatchRow(ctx, 7, "p1", "", catalog.AccessFilter{})
			if err == nil && (row == nil || row.ClusterIndex != 1) {
				return fmt.Errorf("row = %+v, want cluster 1", row)
			}
			return err
		},
		"because you watched past two watched rows": func() error {
			items, source, err := r.SectionBecauseYouWatched(ctx, 7, "p1", "", nil, catalog.AccessFilter{})
			if err == nil && (source != "third" || len(items) != 1) {
				return fmt.Errorf("source=%q items=%v, want the third anchor", source, items)
			}
			return err
		},
	} {
		store.allPages = 0
		if err := read(); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if store.allPages != 1 {
			t.Fatalf("%s walked the watch history %d times, want once", name, store.allPages)
		}
	}
}
