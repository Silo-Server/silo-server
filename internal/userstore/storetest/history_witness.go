package storetest

import (
	"reflect"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/userstore"
)

// RunHistoryWitness verifies grouping before pagination for both stores.
func RunHistoryWitness(t *testing.T, newStore func(*testing.T) userstore.UserStore) {
	const (
		movieID      = "movie"
		episodeTwoID = "ep2"
		missingID    = "missing"
		seriesID     = "series"
	)
	ctx := t.Context()
	store := newStore(t)
	for _, id := range []string{"p1", "p2"} {
		if err := store.CreateProfile(ctx, userstore.Profile{ID: id, Name: id}); err != nil {
			t.Fatal(err)
		}
	}
	entries := []userstore.WatchHistoryEntry{
		{ID: "movie-new", ProfileID: "p1", MediaItemID: movieID, WatchedAt: "2026-01-05T00:00:00Z"},
		{ID: "series-z", ProfileID: "p1", MediaItemID: episodeTwoID, WatchedAt: "2026-01-04T00:00:00Z"},
		{ID: "series-a", ProfileID: "p1", MediaItemID: "ep1", WatchedAt: "2026-01-04T00:00:00Z"},
		{ID: "movie-old", ProfileID: "p1", MediaItemID: movieID, WatchedAt: "2026-01-03T00:00:00Z"},
		{ID: "series-old", ProfileID: "p1", MediaItemID: episodeTwoID, WatchedAt: "2026-01-02T00:00:00Z"},
		{ID: "other-profile", ProfileID: "p2", MediaItemID: movieID, WatchedAt: "2026-01-06T00:00:00Z"},
	}
	for _, e := range entries {
		if err := store.AddHistory(ctx, e); err != nil {
			t.Fatal(err)
		}
	}
	groups := map[string][]string{movieID: {movieID}, seriesID: {seriesID, "ep1", episodeTwoID}, missingID: {missingID}}
	got, err := store.LatestHistoryIDs(ctx, "p1", groups)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{movieID: "movie-new", seriesID: "series-z"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("witnesses = %v, want %v", got, want)
	}
	// Older repeats straddle the cursor; their global witnesses stay before it.
	page, err := store.ListHistoryPage(ctx, "p1", &userstore.HistoryKey{WatchedAt: entries[1].WatchedAt, ID: entries[1].ID}, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(page) != 3 {
		t.Fatalf("later window = %v", page)
	}
	for _, e := range page {
		if e.ID == got[movieID] || e.ID == got[seriesID] {
			t.Fatalf("older repeat became witness: %v", e)
		}
	}
	// Hidden watches must not suppress a visible sibling episode.
	if err := store.RemoveHistoryItems(ctx, "p1", []string{episodeTwoID}, time.Date(2026, 1, 5, 0, 0, 0, 0, time.UTC)); err != nil {
		t.Fatal(err)
	}
	got, err = store.LatestHistoryIDs(ctx, "p1", groups)
	if err != nil {
		t.Fatal(err)
	}
	if got[seriesID] != "series-a" {
		t.Fatalf("hidden witness survived: %v", got)
	}
}
