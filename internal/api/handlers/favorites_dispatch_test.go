package handlers

import (
	"context"
	"testing"

	"github.com/Silo-Server/silo-server/internal/cache"
	"github.com/Silo-Server/silo-server/internal/catalog"
	evt "github.com/Silo-Server/silo-server/internal/events"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/watchsync"
)

// recordingListDispatcher records each list event and whether the context it
// arrived on was already canceled.
type recordingListDispatcher struct {
	events   []watchsync.LocalListEvent
	canceled int
}

func (d *recordingListDispatcher) HandleLocalListEvent(ctx context.Context, event watchsync.LocalListEvent) error {
	d.events = append(d.events, event)
	if ctx.Err() != nil {
		d.canceled++
	}
	return nil
}

// A favorites or watchlist write that changes no row notifies nobody, so a
// client may retry it: the provider list event, the recommendation refresh,
// and the user-state event fire once, for the call that changed membership.
// Each write runs on a context the client has already abandoned; the change it
// committed is still announced on a live context, since its retry would
// announce nothing.
func TestPersonalListWritesNotifyOnlyWhenMembershipChanges(t *testing.T) {
	type write func(*PersonalDataHandler, context.Context, PersonalListViewer, string) error
	cases := []struct {
		name        string
		seed, write write
	}{
		{"add favorite", nil, (*PersonalDataHandler).AddFavorite},
		{"remove favorite", (*PersonalDataHandler).AddFavorite, (*PersonalDataHandler).RemoveFavorite},
		{"add to watchlist", nil, (*PersonalDataHandler).AddToWatchlist},
		{"remove from watchlist", (*PersonalDataHandler).AddToWatchlist, (*PersonalDataHandler).RemoveFromWatchlist},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			handler := NewPersonalDataHandler(testUserStoreProvider{store: newProfileTestStore(t)}, &fakeHistoryItemRepo{
				items: map[string]*models.MediaItem{"movie-1": {ContentID: "movie-1", Type: "movie", Title: "Fight Club"}},
			})
			viewer := PersonalListViewer{UserID: 1, ProfileID: "profile-1", Access: catalog.AccessFilter{UserID: 1, ProfileID: "profile-1"}}
			gone, cancel := context.WithCancel(t.Context())
			cancel()
			call := func(fn write) {
				t.Helper()
				if err := fn(handler, gone, viewer, "movie-1"); err != nil {
					t.Fatalf("write: %v", err)
				}
			}
			if tc.seed != nil {
				call(tc.seed)
			}

			dispatcher := &recordingListDispatcher{}
			notifier := &countingSignalsNotifier{}
			handler.SetLocalListEventDispatcher(dispatcher)
			handler.SetSignalsChangedNotifier(notifier)
			handler.EventsHub = evt.NewHub("test", &cache.NoopEventBus{})
			events, unsubscribe := handler.EventsHub.Subscribe()
			defer unsubscribe()

			call(tc.write)
			call(tc.write)

			if len(dispatcher.events) != 1 || notifier.calls != 1 || len(events) != 1 {
				t.Fatalf("two identical writes: %d list events, %d signal notices, %d user-state events; want 1 each",
					len(dispatcher.events), notifier.calls, len(events))
			}
			if dispatcher.canceled != 0 {
				t.Fatal("the list event arrived on the abandoned request's canceled context")
			}
		})
	}
}
