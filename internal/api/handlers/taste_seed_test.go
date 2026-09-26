package handlers

import (
	"context"
	"testing"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/models"
)

type countingProfileRefresher struct{ calls int }

func (c *countingProfileRefresher) RequestProfileRefresh(context.Context, int, string) { c.calls++ }

// A taste-seed submission counts only the favorites it newly recorded: a
// duplicate pick, an already-favorited item, and a retried submission all
// report 0 added and queue no refresh.
func TestSubmitTasteSeedCountsOnlyNewFavorites(t *testing.T) {
	store := newHouseholdTestStore(t)
	refresher := &countingProfileRefresher{}
	h := &RecommendationsHandler{storeProvider: testUserStoreProvider{store: store}, RecWorker: refresher, Fetcher: stubDiscoverFetcher{items: []*models.MediaItem{{ContentID: "movie:heat-1995"}, {ContentID: "movie:already"}, {ContentID: "movie:collateral-2004"}}}}
	ctx := context.Background()

	if err := store.AddFavorite(ctx, "p1", "movie:already"); err != nil {
		t.Fatal(err)
	}

	added, err := h.SubmitTasteSeed(ctx, 7, "p1", []string{"movie:heat-1995", "movie:heat-1995", "movie:already", "movie:collateral-2004"}, catalog.AccessFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if added != 2 || refresher.calls != 1 {
		t.Fatalf("first submission: added=%d refreshes=%d, want 2 and 1", added, refresher.calls)
	}

	added, err = h.SubmitTasteSeed(ctx, 7, "p1", []string{"movie:heat-1995", "movie:collateral-2004"}, catalog.AccessFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if added != 0 || refresher.calls != 1 {
		t.Fatalf("retry: added=%d refreshes=%d, want 0 and 1", added, refresher.calls)
	}

	favorites, err := store.ListFavorites(ctx, "p1", 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(favorites) != 3 {
		t.Fatalf("favorites = %d, want 3", len(favorites))
	}
}

type tasteSeedAccessFetcher struct {
	stubDiscoverFetcher
	filter catalog.AccessFilter
}

func (f *tasteSeedAccessFetcher) FetchItemsByContentIDs(_ context.Context, _ []string, filter catalog.AccessFilter) ([]*models.MediaItem, error) {
	f.filter = filter
	return []*models.MediaItem{{ContentID: "movie:visible"}}, nil
}

func TestSubmitTasteSeedRejectsInvisiblePicksBeforeWriting(t *testing.T) {
	for _, inaccessible := range []string{"movie:hidden-library", "movie:unknown", " "} {
		t.Run(inaccessible, func(t *testing.T) {
			store := newHouseholdTestStore(t)
			refresher := &countingProfileRefresher{}
			fetcher := &tasteSeedAccessFetcher{}
			h := &RecommendationsHandler{storeProvider: testUserStoreProvider{store: store}, RecWorker: refresher, Fetcher: fetcher}
			filter := catalog.AccessFilter{AllowedLibraryIDs: []int{7}}
			added, err := h.SubmitTasteSeed(t.Context(), 7, "p1", []string{"movie:visible", inaccessible}, filter)
			if err == nil || added != 0 {
				t.Fatalf("added=%d err=%v, want rejection", added, err)
			}
			if len(fetcher.filter.AllowedLibraryIDs) != 1 || fetcher.filter.AllowedLibraryIDs[0] != 7 {
				t.Fatalf("lost profile access: %+v", fetcher.filter)
			}
			favorites, err := store.ListFavorites(t.Context(), "p1", 10, 0)
			if err != nil {
				t.Fatal(err)
			}
			if len(favorites) != 0 || refresher.calls != 0 {
				t.Fatalf("partial side effects: favorites=%v refreshes=%d", favorites, refresher.calls)
			}
		})
	}
}
