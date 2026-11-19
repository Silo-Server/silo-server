package recommendations

import (
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
	libraries := map[string]int{"latest-series": tvLibrary, "older-movie": movieLibrary}
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
		signals: NewSignalReader(&fakeSignalRepo{}, fakeSignalProvider{store: anchorStore("latest-series", "older-movie")}).WithUserStoreOutsidePostgres(true),
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
