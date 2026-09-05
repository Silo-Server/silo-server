package handlers

import (
	"context"
	"testing"

	"github.com/Silo-Server/silo-server/internal/watchstate"
	"github.com/Silo-Server/silo-server/internal/watchsync"
)

type recordingWatchDispatcher struct {
	events []watchsync.LocalWatchEvent
}

func (d *recordingWatchDispatcher) HandleLocalWatchEvent(_ context.Context, event watchsync.LocalWatchEvent) error {
	d.events = append(d.events, event)
	return nil
}

// A retried mark-watched must not record a second play: the seam behind v1
// POST /watched/{id} and v2 markWatched skips the history insert and the
// provider event when the profile already completed the target. An unmark
// clears that state, so the next mark records again.
func TestMarkLeafTargetsWatchedIsIdempotentUntilUnmarked(t *testing.T) {
	ctx := context.Background()
	store := newProfileTestStore(t)
	dispatcher := &recordingWatchDispatcher{}
	handler := &ItemsHandler{
		watchState:           watchstate.NewService(testUserStoreProvider{store: store}),
		localWatchDispatcher: dispatcher,
	}
	targets := []watchedLeafTarget{{ContentID: "movie-1", DurationSeconds: 7200}}

	for i := 0; i < 2; i++ {
		if err := handler.markLeafTargetsWatched(ctx, 1, "profile-1", targets); err != nil {
			t.Fatalf("mark %d: %v", i+1, err)
		}
	}
	history, err := store.ListHistory(ctx, "profile-1", 10, 0)
	if err != nil {
		t.Fatalf("ListHistory: %v", err)
	}
	if len(history) != 1 {
		t.Fatalf("history after two marks = %d rows, want 1", len(history))
	}
	if len(dispatcher.events) != 1 || dispatcher.events[0].Kind != watchsync.LocalWatchEventMarkedWatched || len(dispatcher.events[0].Plays) != 1 {
		t.Fatalf("events after two marks = %+v, want one marked-watched event with one play", dispatcher.events)
	}

	if err := handler.markLeafTargetsUnwatched(ctx, 1, "profile-1", targets); err != nil {
		t.Fatalf("unmark: %v", err)
	}
	if err := handler.markLeafTargetsWatched(ctx, 1, "profile-1", targets); err != nil {
		t.Fatalf("mark after unmark: %v", err)
	}
	history, err = store.ListHistory(ctx, "profile-1", 10, 0)
	if err != nil {
		t.Fatalf("ListHistory: %v", err)
	}
	if len(history) != 1 {
		t.Fatalf("visible history after unmark+mark = %d rows, want the one new play", len(history))
	}
	if len(dispatcher.events) != 3 ||
		dispatcher.events[1].Kind != watchsync.LocalWatchEventMarkedUnwatched ||
		dispatcher.events[2].Kind != watchsync.LocalWatchEventMarkedWatched || len(dispatcher.events[2].Plays) != 1 {
		t.Fatalf("events = %+v, want watched, unwatched, watched", dispatcher.events)
	}
	if dispatcher.events[2].Plays[0].HistoryID == dispatcher.events[0].Plays[0].HistoryID {
		t.Fatal("the second play reused the first history id")
	}
}
