package storetest

import (
	"context"
	"maps"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/userstore"
	"github.com/Silo-Server/silo-server/internal/watchstate"
)

const (
	unmarkProfile = "p1"
	unmarkItem    = "movie-1"
	unmarkViewer  = "Viewer"
)

// SingleStoreProvider answers every user with one store.
type SingleStoreProvider struct{ Store userstore.UserStore }

func (p SingleStoreProvider) ForUser(context.Context, int) (userstore.UserStore, error) {
	return p.Store, nil
}

func (SingleStoreProvider) Close() error { return nil }

// RunUnmarkKeepsHistory checks every mark-unwatched path: the item stops
// counting as watched and loses its resume point, its plays stay in history, an
// import of an older play cannot mark it watched again, and only a history
// removal takes the plays out of history.
func RunUnmarkKeepsHistory(t *testing.T, newStore func(t *testing.T) userstore.UserStore) {
	const (
		userID  = 1
		profile = unmarkProfile
		item    = unmarkItem
		sibling = "movie-2"
	)
	unplayed := false
	for _, tc := range []struct {
		name   string
		unmark func(context.Context, *watchstate.Service) error
	}{
		{"native", func(ctx context.Context, svc *watchstate.Service) error {
			return svc.RecordManualMarkUnwatched(ctx, userID, profile, []string{item})
		}},
		{"jellycompat", func(ctx context.Context, svc *watchstate.Service) error {
			return svc.RecordJellycompatMarkUnplayed(ctx, userID, profile, item)
		}},
		{"jellycompat batch", func(ctx context.Context, svc *watchstate.Service) error {
			return svc.RecordJellycompatMarkUnplayedBatch(ctx, userID, profile, []string{item})
		}},
		{"jellycompat user data", func(ctx context.Context, svc *watchstate.Service) error {
			edit := userstore.JellycompatProgressEdit{MediaItemID: item, DurationSeconds: 1200}
			return svc.RecordJellycompatProgress(ctx, userID, profile, edit, &unplayed)
		}},
		{"jellycompat parent", func(ctx context.Context, svc *watchstate.Service) error {
			return svc.RecordJellycompatParent(ctx, userID, profile, "series-1", []string{item}, false, true)
		}},
		{"webhook", func(ctx context.Context, svc *watchstate.Service) error {
			return svc.RecordImportedMarkUnplayed(ctx, userID, profile, item, time.Time{})
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			store := newStore(t)
			if err := store.CreateProfile(ctx, userstore.Profile{ID: profile, Name: unmarkViewer}); err != nil {
				t.Fatalf("CreateProfile: %v", err)
			}
			svc := watchstate.NewService(SingleStoreProvider{Store: store})
			watchedAt := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)

			// A watched sibling must come through every step untouched.
			if err := svc.RecordManualMarkWatched(ctx, userID, profile, []watchstate.LeafWatchTarget{{MediaItemID: item, DurationSeconds: 1200}, {MediaItemID: sibling, DurationSeconds: 900}}, watchedAt); err != nil {
				t.Fatalf("mark watched: %v", err)
			}
			if err := store.SetProgressAt(ctx, profile, item, 300, 1200, true, watchedAt.Add(time.Minute)); err != nil {
				t.Fatalf("rewatch progress: %v", err)
			}
			if err := tc.unmark(ctx, svc); err != nil {
				t.Fatalf("unmark: %v", err)
			}
			assertHistory(t, store, profile, map[string]int{item: 1, sibling: 1}, "after unmark")
			assertUnwatched(t, store, profile, item, "after unmark")
			assertWatched(t, store, profile, sibling, "after unmark")

			// An older play arriving from a watch provider or a history
			// import must not mark the item watched again.
			if _, err := svc.RecordImportedWatchWithSource(ctx, userID, profile, item, 1200, 0, true, watchedAt, &watchedAt, userstore.WatchHistorySourceTrakt); err != nil {
				t.Fatalf("import older play: %v", err)
			}
			assertHistory(t, store, profile, map[string]int{item: 1, sibling: 1}, "after importing the older play")
			assertUnwatched(t, store, profile, item, "after importing the older play")

			// Marking watched again records a new play.
			if err := svc.RecordManualMarkWatched(ctx, userID, profile, []watchstate.LeafWatchTarget{{MediaItemID: item, DurationSeconds: 1200}}, time.Now().UTC()); err != nil {
				t.Fatalf("mark watched again: %v", err)
			}
			assertHistory(t, store, profile, map[string]int{item: 2, sibling: 1}, "after marking watched again")
			watched, err := userstore.ListProgressWithCompletedHistory(ctx, store, profile, []string{item})
			if err != nil || !watched[item].Completed {
				t.Fatalf("after marking watched again: progress %+v, err %v; want completed", watched[item], err)
			}

			// Removing the item from history takes every play out.
			if err := store.RemoveHistoryItems(ctx, profile, []string{item}, time.Now().UTC()); err != nil {
				t.Fatalf("RemoveHistoryItems: %v", err)
			}
			assertHistory(t, store, profile, map[string]int{sibling: 1}, "after removing from history")
			assertUnwatched(t, store, profile, item, "after removing from history")
			assertWatched(t, store, profile, sibling, "after removing from history")
		})
	}
}

// RunPlayFinishedAfterUnmark checks that a play recorded unfinished before a
// mark unwatched, then finished under the same play ID, counts as watched.
func RunPlayFinishedAfterUnmark(t *testing.T, newStore func(t *testing.T) userstore.UserStore) {
	ctx := context.Background()
	store := newStore(t)
	if err := store.CreateProfile(ctx, userstore.Profile{ID: unmarkProfile, Name: unmarkViewer}); err != nil {
		t.Fatalf("CreateProfile: %v", err)
	}
	play := userstore.WatchHistoryEntry{
		ID: "00000000-0000-4000-8000-000000000001", ProfileID: unmarkProfile, MediaItemID: unmarkItem,
		WatchedAt: time.Now().UTC().Add(-time.Hour).Format(time.RFC3339), Source: userstore.WatchHistorySourcePlayback,
	}
	if _, err := userstore.AddVisibleHistory(ctx, store, play); err != nil {
		t.Fatalf("unfinished play: %v", err)
	}
	if err := watchstate.NewService(SingleStoreProvider{Store: store}).RecordManualMarkUnwatched(ctx, 1, unmarkProfile, []string{unmarkItem}); err != nil {
		t.Fatalf("unmark: %v", err)
	}
	play.Completed = true
	play.WatchedAt = time.Now().UTC().Format(time.RFC3339)
	if _, err := userstore.AddVisibleHistory(ctx, store, play); err != nil {
		t.Fatalf("finished play: %v", err)
	}
	completed, err := store.ListCompletedHistoryItems(ctx, userstore.CompletedHistoryItemQuery{ProfileID: unmarkProfile, MediaItemIDs: []string{unmarkItem}})
	if err != nil || len(completed) != 1 {
		t.Fatalf("completed history after the play finished: %+v, err %v; want the play to count", completed, err)
	}
}

// assertHistory checks the plays each item has listed, through both history
// listings.
func assertHistory(t *testing.T, store userstore.UserStore, profile string, want map[string]int, when string) {
	t.Helper()
	history, err := store.ListHistory(context.Background(), profile, 50, 0)
	if err != nil {
		t.Fatalf("%s: ListHistory: %v", when, err)
	}
	page, err := store.ListHistoryPage(context.Background(), profile, nil, 50)
	if err != nil {
		t.Fatalf("%s: ListHistoryPage: %v", when, err)
	}
	for name, rows := range map[string][]userstore.WatchHistoryEntry{"ListHistory": history, "ListHistoryPage": page} {
		got := map[string]int{}
		for _, row := range rows {
			got[row.MediaItemID]++
		}
		if !maps.Equal(got, want) {
			t.Fatalf("%s: %s plays per item = %v, want %v", when, name, got, want)
		}
	}
}

func assertWatched(t *testing.T, store userstore.UserStore, profile, item, when string) {
	t.Helper()
	progress, err := userstore.ListProgressWithCompletedHistory(context.Background(), store, profile, []string{item})
	if err != nil || !progress[item].Completed {
		t.Fatalf("%s: %s progress %+v, err %v; want it still watched", when, item, progress[item], err)
	}
}

func assertUnwatched(t *testing.T, store userstore.UserStore, profile, item, when string) {
	t.Helper()
	progress, err := userstore.ListProgressWithCompletedHistory(context.Background(), store, profile, []string{item})
	if err != nil {
		t.Fatalf("%s: ListProgressWithCompletedHistory: %v", when, err)
	}
	if p, ok := progress[item]; ok && (p.Completed || p.PositionSeconds != 0) {
		t.Fatalf("%s: progress %+v, want not watched and no resume point", when, p)
	}
}
