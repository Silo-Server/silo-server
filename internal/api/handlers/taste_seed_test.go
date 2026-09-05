package handlers

import (
	"context"
	"testing"
)

type countingProfileRefresher struct{ calls int }

func (c *countingProfileRefresher) RequestProfileRefresh(context.Context, int, string) { c.calls++ }

// A taste-seed submission counts only the favorites it newly recorded: a
// duplicate pick, an already-favorited item, and a retried submission all
// report 0 added and queue no refresh.
func TestSubmitTasteSeedCountsOnlyNewFavorites(t *testing.T) {
	store := newHouseholdTestStore(t)
	refresher := &countingProfileRefresher{}
	h := &RecommendationsHandler{storeProvider: testUserStoreProvider{store: store}, RecWorker: refresher}
	ctx := context.Background()

	if err := store.AddFavorite(ctx, "p1", "movie:already"); err != nil {
		t.Fatal(err)
	}

	added, err := h.SubmitTasteSeed(ctx, 7, "p1", []string{"movie:heat-1995", "movie:heat-1995", "movie:already", "", "movie:collateral-2004"})
	if err != nil {
		t.Fatal(err)
	}
	if added != 2 || refresher.calls != 1 {
		t.Fatalf("first submission: added=%d refreshes=%d, want 2 and 1", added, refresher.calls)
	}

	added, err = h.SubmitTasteSeed(ctx, 7, "p1", []string{"movie:heat-1995", "movie:collateral-2004"})
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
