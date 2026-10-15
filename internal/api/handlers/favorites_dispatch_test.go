package handlers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Silo-Server/silo-server/internal/cache"
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

// A favorites or watchlist write that changes no row answers 204 and
// notifies nobody, so a client may retry it: the provider list event, the
// recommendation refresh, and the user-state event fire once, for the call
// that changed membership. Each write here runs on a request the client has
// already abandoned; the change it committed is still announced on a live
// context, since its retry would announce nothing.
func TestPersonalListWritesNotifyOnlyWhenMembershipChanges(t *testing.T) {
	cases := []struct {
		name   string
		method string
		path   string
		seed   func(h *PersonalDataHandler, w http.ResponseWriter, r *http.Request)
		write  func(h *PersonalDataHandler, w http.ResponseWriter, r *http.Request)
	}{
		{"add favorite", http.MethodPut, "/favorites/movie-1", nil, (*PersonalDataHandler).HandleAddFavorite},
		{"remove favorite", http.MethodDelete, "/favorites/movie-1", (*PersonalDataHandler).HandleAddFavorite, (*PersonalDataHandler).HandleRemoveFavorite},
		{"add to watchlist", http.MethodPut, "/watchlist/movie-1", nil, (*PersonalDataHandler).HandleAddToWatchlist},
		{"remove from watchlist", http.MethodDelete, "/watchlist/movie-1", (*PersonalDataHandler).HandleAddToWatchlist, (*PersonalDataHandler).HandleRemoveFromWatchlist},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			handler := NewPersonalDataHandler(testUserStoreProvider{store: newProfileTestStore(t)}, &fakeHistoryItemRepo{
				items: map[string]*models.MediaItem{"movie-1": {ContentID: "movie-1", Type: "movie", Title: "Fight Club"}},
			})
			call := func(fn func(h *PersonalDataHandler, w http.ResponseWriter, r *http.Request)) {
				t.Helper()
				req := withChiParam(newAuthorizedProfileRequestWithRole(tc.method, tc.path, "", "user", "profile-1"), "item_id", "movie-1")
				gone, cancel := context.WithCancel(req.Context())
				cancel()
				rr := httptest.NewRecorder()
				fn(handler, rr, req.WithContext(gone))
				if rr.Code != http.StatusNoContent {
					t.Fatalf("status = %d, want 204: %s", rr.Code, rr.Body.String())
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
