package catalog

import (
	"context"
	"testing"

	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

type visFakeItems map[string]*models.MediaItem

func (f visFakeItems) GetByIDs(_ context.Context, ids []string) ([]*models.MediaItem, error) {
	var out []*models.MediaItem
	for _, id := range ids {
		if item, ok := f[id]; ok {
			out = append(out, item)
		}
	}
	return out, nil
}

type visFakeEpisodes map[string][]*models.Episode

func (f visFakeEpisodes) ListBySeriesIDs(_ context.Context, seriesIDs []string) (map[string][]*models.Episode, error) {
	out := make(map[string][]*models.Episode, len(seriesIDs))
	for _, id := range seriesIDs {
		if eps, ok := f[id]; ok {
			out[id] = eps
		}
	}
	return out, nil
}

type visFakeStore struct {
	removeWatched bool
	progress      map[string]userstore.WatchProgress
}

func (f *visFakeStore) RemoveWatchedFromWatchlist(context.Context, string) (bool, error) {
	return f.removeWatched, nil
}

func (f *visFakeStore) ListProgressByMediaItems(_ context.Context, _ string, ids []string) (map[string]userstore.WatchProgress, error) {
	out := make(map[string]userstore.WatchProgress, len(ids))
	for _, id := range ids {
		if p, ok := f.progress[id]; ok {
			out[id] = p
		}
	}
	return out, nil
}

func visCompleted() userstore.WatchProgress { return userstore.WatchProgress{Completed: true} }

func TestHiddenSeriesIDsHidesFullyWatchedSeries(t *testing.T) {
	v := NewWatchlistVisibility(
		visFakeItems{
			"series-full":    {ContentID: "series-full", Type: "series"},
			"series-partial": {ContentID: "series-partial", Type: "series"},
			"movie-1":        {ContentID: "movie-1", Type: "movie"},
		},
		visFakeEpisodes{
			"series-full":    {{ContentID: "f1"}, {ContentID: "f2"}},
			"series-partial": {{ContentID: "p1"}, {ContentID: "p2"}},
		},
	)
	store := &visFakeStore{
		removeWatched: true,
		progress: map[string]userstore.WatchProgress{
			"f1": visCompleted(), "f2": visCompleted(),
			"p1": visCompleted(), // p2 unwatched
		},
	}

	hidden, err := v.HiddenSeriesIDs(context.Background(), store, "profile-1",
		[]string{"series-full", "series-partial", "movie-1"})
	if err != nil {
		t.Fatalf("HiddenSeriesIDs: %v", err)
	}
	if _, ok := hidden["series-full"]; !ok {
		t.Fatal("fully-watched series must be hidden")
	}
	if _, ok := hidden["series-partial"]; ok {
		t.Fatal("partially-watched series must stay visible")
	}
	if _, ok := hidden["movie-1"]; ok {
		t.Fatal("movies must never be hidden")
	}
}

func TestHiddenSeriesIDsResurfacesOnNewEpisode(t *testing.T) {
	// The same series with one extra (unwatched) episode is no longer hidden —
	// the "new episode brings the series back" behavior.
	items := visFakeItems{"series-1": {ContentID: "series-1", Type: "series"}}
	store := &visFakeStore{
		removeWatched: true,
		progress:      map[string]userstore.WatchProgress{"e1": visCompleted(), "e2": visCompleted()},
	}

	before := NewWatchlistVisibility(items, visFakeEpisodes{
		"series-1": {{ContentID: "e1"}, {ContentID: "e2"}},
	})
	hidden, err := before.HiddenSeriesIDs(context.Background(), store, "p", []string{"series-1"})
	if err != nil || len(hidden) != 1 {
		t.Fatalf("expected series hidden before new episode: hidden=%v err=%v", hidden, err)
	}

	after := NewWatchlistVisibility(items, visFakeEpisodes{
		"series-1": {{ContentID: "e1"}, {ContentID: "e2"}, {ContentID: "e3-new"}},
	})
	hidden, err = after.HiddenSeriesIDs(context.Background(), store, "p", []string{"series-1"})
	if err != nil || len(hidden) != 0 {
		t.Fatalf("series with a new unwatched episode must be visible: hidden=%v err=%v", hidden, err)
	}
}

func TestHiddenSeriesIDsRespectsPreferenceOff(t *testing.T) {
	v := NewWatchlistVisibility(
		visFakeItems{"series-1": {ContentID: "series-1", Type: "series"}},
		visFakeEpisodes{"series-1": {{ContentID: "e1"}}},
	)
	store := &visFakeStore{
		removeWatched: false,
		progress:      map[string]userstore.WatchProgress{"e1": visCompleted()},
	}
	hidden, err := v.HiddenSeriesIDs(context.Background(), store, "p", []string{"series-1"})
	if err != nil || len(hidden) != 0 {
		t.Fatalf("preference off must hide nothing: hidden=%v err=%v", hidden, err)
	}
}

func TestHiddenSeriesIDsKeepsSeriesWithoutEpisodes(t *testing.T) {
	v := NewWatchlistVisibility(
		visFakeItems{"series-1": {ContentID: "series-1", Type: "series"}},
		visFakeEpisodes{},
	)
	store := &visFakeStore{removeWatched: true}
	hidden, err := v.HiddenSeriesIDs(context.Background(), store, "p", []string{"series-1"})
	if err != nil || len(hidden) != 0 {
		t.Fatalf("a series with no available episodes must stay visible: hidden=%v err=%v", hidden, err)
	}
}

func TestHiddenSeriesIDsNilFilterHidesNothing(t *testing.T) {
	var v *WatchlistVisibility
	hidden, err := v.HiddenSeriesIDs(context.Background(), &visFakeStore{removeWatched: true}, "p", []string{"x"})
	if err != nil || len(hidden) != 0 {
		t.Fatalf("nil filter must be a no-op: hidden=%v err=%v", hidden, err)
	}
}
