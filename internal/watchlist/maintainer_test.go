package watchlist

import (
	"context"

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

func (f *fakeStore) RemoveFromWatchlist(_ context.Context, _ string, mediaItemID string) (bool, error) {
	if !f.watchlist[mediaItemID] {
		return false, nil
	}
	delete(f.watchlist, mediaItemID)
	f.removed = append(f.removed, mediaItemID)
	return true, nil
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
