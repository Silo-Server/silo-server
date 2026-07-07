package watchlist

import (
	"context"
	"errors"
	"testing"

	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/watchsync"
)

type fakeStore struct {
	removeWatched bool
	watchlist     map[string]bool
	removed       []string
}

func (f *fakeStore) RemoveWatchedFromWatchlist(context.Context, string) (bool, error) {
	return f.removeWatched, nil
}

func (f *fakeStore) InWatchlist(_ context.Context, _ string, mediaItemID string) (bool, error) {
	return f.watchlist[mediaItemID], nil
}

func (f *fakeStore) RemoveFromWatchlist(_ context.Context, _ string, mediaItemID string) error {
	delete(f.watchlist, mediaItemID)
	f.removed = append(f.removed, mediaItemID)
	return nil
}

type fakeItems map[string]*models.MediaItem

func (f fakeItems) GetByIDs(_ context.Context, ids []string) ([]*models.MediaItem, error) {
	var out []*models.MediaItem
	seen := make(map[string]bool, len(ids))
	for _, id := range ids {
		if item, ok := f[id]; ok && !seen[id] {
			seen[id] = true
			out = append(out, item)
		}
	}
	return out, nil
}

type fakeDispatcher struct {
	events []watchsync.LocalListEvent
}

func (f *fakeDispatcher) HandleLocalListEvent(_ context.Context, event watchsync.LocalListEvent) error {
	f.events = append(f.events, event)
	return nil
}

func newMaintainer(store *fakeStore, items fakeItems, dispatcher *fakeDispatcher) *Maintainer {
	return &Maintainer{
		storeFor:   func(context.Context, int) (maintainerStore, error) { return store, nil },
		items:      items,
		dispatcher: dispatcher,
	}
}

func TestMaintainerRemovesWatchedMovie(t *testing.T) {
	store := &fakeStore{removeWatched: true, watchlist: map[string]bool{"movie-1": true}}
	items := fakeItems{"movie-1": {ContentID: "movie-1", Type: "movie", Title: "M", ImdbID: "tt1"}}
	dispatcher := &fakeDispatcher{}
	m := newMaintainer(store, items, dispatcher)

	if err := m.process(context.Background(), 7, "profile-1", []string{"movie-1"}); err != nil {
		t.Fatalf("process: %v", err)
	}
	if store.watchlist["movie-1"] {
		t.Fatal("watched movie should be removed from the watchlist")
	}
	if len(dispatcher.events) != 1 || dispatcher.events[0].List != watchsync.ListKindWatchlist ||
		dispatcher.events[0].Change != watchsync.ListChangeRemoved {
		t.Fatalf("expected one watchlist-removed event, got %+v", dispatcher.events)
	}
}

func TestMaintainerSkipsMovieNotOnWatchlist(t *testing.T) {
	store := &fakeStore{removeWatched: true, watchlist: map[string]bool{}}
	items := fakeItems{"movie-1": {ContentID: "movie-1", Type: "movie"}}
	dispatcher := &fakeDispatcher{}
	m := newMaintainer(store, items, dispatcher)

	if err := m.process(context.Background(), 7, "profile-1", []string{"movie-1"}); err != nil {
		t.Fatalf("process: %v", err)
	}
	if len(store.removed) != 0 || len(dispatcher.events) != 0 {
		t.Fatalf("nothing should happen for a movie not on the watchlist: removed=%v events=%v", store.removed, dispatcher.events)
	}
}

func TestMaintainerNeverRemovesSeries(t *testing.T) {
	// Even a series explicitly marked watched stays on the watchlist: the read
	// paths hide it instead, so newly added episodes make it reappear.
	items := fakeItems{"series-1": {ContentID: "series-1", Type: "series", TvdbID: "99"}}
	store := &fakeStore{removeWatched: true, watchlist: map[string]bool{"series-1": true}}
	dispatcher := &fakeDispatcher{}
	m := newMaintainer(store, items, dispatcher)

	if err := m.process(context.Background(), 7, "profile-1", []string{"series-1"}); err != nil {
		t.Fatalf("process: %v", err)
	}
	if !store.watchlist["series-1"] {
		t.Fatal("a fully-watched series must stay on the watchlist (hidden at read time)")
	}
	if len(dispatcher.events) != 0 {
		t.Fatalf("no removal events expected for series, got %+v", dispatcher.events)
	}
}

func TestMaintainerIgnoresEpisodeCompletions(t *testing.T) {
	// Episode IDs don't resolve in the media-items table; completions must be
	// ignored without error and without touching the series watchlist entry.
	items := fakeItems{"series-1": {ContentID: "series-1", Type: "series"}}
	store := &fakeStore{removeWatched: true, watchlist: map[string]bool{"series-1": true}}
	m := newMaintainer(store, items, &fakeDispatcher{})

	if err := m.process(context.Background(), 7, "profile-1", []string{"ep-1", "ep-2"}); err != nil {
		t.Fatalf("process: %v", err)
	}
	if !store.watchlist["series-1"] || len(store.removed) != 0 {
		t.Fatalf("episode completions must not remove anything: removed=%v", store.removed)
	}
}

func TestMaintainerRespectsPreferenceOff(t *testing.T) {
	store := &fakeStore{removeWatched: false, watchlist: map[string]bool{"movie-1": true}}
	items := fakeItems{"movie-1": {ContentID: "movie-1", Type: "movie"}}
	dispatcher := &fakeDispatcher{}
	m := newMaintainer(store, items, dispatcher)

	if err := m.process(context.Background(), 7, "profile-1", []string{"movie-1"}); err != nil {
		t.Fatalf("process: %v", err)
	}
	if !store.watchlist["movie-1"] || len(store.removed) != 0 {
		t.Fatal("preference off must leave the watchlist untouched")
	}
}

type erroringItems struct{ err error }

func (e erroringItems) GetByIDs(context.Context, []string) ([]*models.MediaItem, error) {
	return nil, e.err
}

func TestMaintainerPropagatesItemLookupError(t *testing.T) {
	boom := errors.New("db down")
	store := &fakeStore{removeWatched: true, watchlist: map[string]bool{}}
	m := &Maintainer{
		storeFor: func(context.Context, int) (maintainerStore, error) { return store, nil },
		items:    erroringItems{err: boom},
	}
	if err := m.process(context.Background(), 7, "profile-1", []string{"x"}); err == nil {
		t.Fatal("a transient catalog lookup error must propagate, not be swallowed")
	}
}
