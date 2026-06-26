package watchlist

import (
	"context"
	"errors"
	"testing"

	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/userstore"
	"github.com/Silo-Server/silo-server/internal/watchsync"
)

var errNotFound = errors.New("not found")

type fakeStore struct {
	removeWatched bool
	watchlist     map[string]bool
	progress      map[string]userstore.WatchProgress
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

func (f *fakeStore) ListProgressByMediaItems(_ context.Context, _ string, ids []string) (map[string]userstore.WatchProgress, error) {
	out := make(map[string]userstore.WatchProgress, len(ids))
	for _, id := range ids {
		if p, ok := f.progress[id]; ok {
			out[id] = p
		}
	}
	return out, nil
}

type fakeItems map[string]*models.MediaItem

func (f fakeItems) GetByID(_ context.Context, id string) (*models.MediaItem, error) {
	if item, ok := f[id]; ok {
		return item, nil
	}
	return nil, errNotFound
}

type fakeEpisodes struct {
	byID     map[string]*models.Episode
	bySeries map[string][]*models.Episode
}

func (f fakeEpisodes) GetByID(_ context.Context, id string) (*models.Episode, error) {
	if ep, ok := f.byID[id]; ok {
		return ep, nil
	}
	return nil, errNotFound
}

func (f fakeEpisodes) ListBySeries(_ context.Context, seriesID string) ([]*models.Episode, error) {
	return f.bySeries[seriesID], nil
}

type fakeDispatcher struct {
	events []watchsync.LocalListEvent
}

func (f *fakeDispatcher) HandleLocalListEvent(_ context.Context, event watchsync.LocalListEvent) error {
	f.events = append(f.events, event)
	return nil
}

func newMaintainer(store *fakeStore, items fakeItems, episodes fakeEpisodes, dispatcher *fakeDispatcher) *Maintainer {
	return &Maintainer{
		storeFor:   func(context.Context, int) (maintainerStore, error) { return store, nil },
		items:      items,
		episodes:   episodes,
		dispatcher: dispatcher,
	}
}

func completed() userstore.WatchProgress { return userstore.WatchProgress{Completed: true} }

func TestMaintainerRemovesWatchedMovie(t *testing.T) {
	store := &fakeStore{removeWatched: true, watchlist: map[string]bool{"movie-1": true}}
	items := fakeItems{"movie-1": {ContentID: "movie-1", Type: "movie", Title: "M", ImdbID: "tt1"}}
	dispatcher := &fakeDispatcher{}
	m := newMaintainer(store, items, fakeEpisodes{}, dispatcher)

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
	m := newMaintainer(store, items, fakeEpisodes{}, dispatcher)

	if err := m.process(context.Background(), 7, "profile-1", []string{"movie-1"}); err != nil {
		t.Fatalf("process: %v", err)
	}
	if len(store.removed) != 0 || len(dispatcher.events) != 0 {
		t.Fatalf("nothing should happen for a movie not on the watchlist: removed=%v events=%v", store.removed, dispatcher.events)
	}
}

func TestMaintainerRemovesSeriesOnlyWhenFullyWatched(t *testing.T) {
	episodes := fakeEpisodes{
		byID: map[string]*models.Episode{
			"ep-1": {ContentID: "ep-1", SeriesID: "series-1"},
			"ep-2": {ContentID: "ep-2", SeriesID: "series-1"},
		},
		bySeries: map[string][]*models.Episode{
			"series-1": {{ContentID: "ep-1"}, {ContentID: "ep-2"}},
		},
	}
	items := fakeItems{"series-1": {ContentID: "series-1", Type: "series", TvdbID: "99"}}

	// Only ep-1 watched: series stays on the watchlist.
	partial := &fakeStore{
		removeWatched: true,
		watchlist:     map[string]bool{"series-1": true},
		progress:      map[string]userstore.WatchProgress{"ep-1": completed()},
	}
	m := newMaintainer(partial, items, episodes, &fakeDispatcher{})
	if err := m.process(context.Background(), 7, "profile-1", []string{"ep-1"}); err != nil {
		t.Fatalf("process: %v", err)
	}
	if !partial.watchlist["series-1"] {
		t.Fatal("a partially-watched series must stay on the watchlist")
	}

	// Both episodes watched: series is removed.
	full := &fakeStore{
		removeWatched: true,
		watchlist:     map[string]bool{"series-1": true},
		progress:      map[string]userstore.WatchProgress{"ep-1": completed(), "ep-2": completed()},
	}
	dispatcher := &fakeDispatcher{}
	m = newMaintainer(full, items, episodes, dispatcher)
	if err := m.process(context.Background(), 7, "profile-1", []string{"ep-2"}); err != nil {
		t.Fatalf("process: %v", err)
	}
	if full.watchlist["series-1"] {
		t.Fatal("a fully-watched series must leave the watchlist")
	}
	if len(dispatcher.events) != 1 {
		t.Fatalf("expected one removal event for the series, got %+v", dispatcher.events)
	}
}

func TestMaintainerRespectsPreferenceOff(t *testing.T) {
	store := &fakeStore{removeWatched: false, watchlist: map[string]bool{"movie-1": true}}
	items := fakeItems{"movie-1": {ContentID: "movie-1", Type: "movie"}}
	dispatcher := &fakeDispatcher{}
	m := newMaintainer(store, items, fakeEpisodes{}, dispatcher)

	if err := m.process(context.Background(), 7, "profile-1", []string{"movie-1"}); err != nil {
		t.Fatalf("process: %v", err)
	}
	if !store.watchlist["movie-1"] || len(store.removed) != 0 {
		t.Fatal("preference off must leave the watchlist untouched")
	}
}

func TestMaintainerDedupesEpisodesOfSameSeries(t *testing.T) {
	episodes := fakeEpisodes{
		byID: map[string]*models.Episode{
			"ep-1": {ContentID: "ep-1", SeriesID: "series-1"},
			"ep-2": {ContentID: "ep-2", SeriesID: "series-1"},
		},
		bySeries: map[string][]*models.Episode{
			"series-1": {{ContentID: "ep-1"}, {ContentID: "ep-2"}},
		},
	}
	items := fakeItems{"series-1": {ContentID: "series-1", Type: "series"}}
	store := &fakeStore{
		removeWatched: true,
		watchlist:     map[string]bool{"series-1": true},
		progress:      map[string]userstore.WatchProgress{"ep-1": completed(), "ep-2": completed()},
	}
	m := newMaintainer(store, items, episodes, &fakeDispatcher{})

	// Both episodes complete in one batch; the series should be removed once.
	if err := m.process(context.Background(), 7, "profile-1", []string{"ep-1", "ep-2"}); err != nil {
		t.Fatalf("process: %v", err)
	}
	if got := len(store.removed); got != 1 {
		t.Fatalf("series should be removed exactly once, got %d removals (%v)", got, store.removed)
	}
}

type erroringItems struct{ err error }

func (e erroringItems) GetByID(context.Context, string) (*models.MediaItem, error) {
	return nil, e.err
}

type emptyEpisodes struct{}

func (emptyEpisodes) GetByID(context.Context, string) (*models.Episode, error) { return nil, nil }
func (emptyEpisodes) ListBySeries(context.Context, string) ([]*models.Episode, error) {
	return nil, nil
}

func TestMaintainerPropagatesItemLookupError(t *testing.T) {
	boom := errors.New("db down")
	store := &fakeStore{removeWatched: true, watchlist: map[string]bool{}}
	m := &Maintainer{
		storeFor: func(context.Context, int) (maintainerStore, error) { return store, nil },
		items:    erroringItems{err: boom},
		episodes: emptyEpisodes{},
	}
	if err := m.process(context.Background(), 7, "profile-1", []string{"x"}); err == nil {
		t.Fatal("a transient catalog lookup error must propagate, not be swallowed")
	}
}
