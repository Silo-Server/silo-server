// Package watchlist holds cross-cutting watchlist behavior that doesn't belong
// to a single handler — currently the auto-removal of fully-watched movies.
// Series are intentionally never removed: they stay on the watchlist and the
// read paths hide fully-watched ones via catalog.WatchlistVisibility, so a
// newly added episode makes the series reappear without any re-add machinery.
package watchlist

import (
	"context"
	"log/slog"
	"time"

	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/userstore"
	"github.com/Silo-Server/silo-server/internal/watchsync"
)

type itemLookup interface {
	// GetByIDs returns the catalog items for the given content IDs, silently
	// omitting IDs that don't resolve (episode IDs live in their own table and
	// are expected misses here).
	GetByIDs(ctx context.Context, contentIDs []string) ([]*models.MediaItem, error)
}

type listEventDispatcher interface {
	HandleLocalListEvent(ctx context.Context, event watchsync.LocalListEvent) error
}

// maintainerStore is the narrow slice of the user store the maintainer needs.
type maintainerStore interface {
	RemoveWatchedFromWatchlist(ctx context.Context, profileID string) (bool, error)
	InWatchlist(ctx context.Context, profileID, mediaItemID string) (bool, error)
	RemoveFromWatchlist(ctx context.Context, profileID, mediaItemID string) error
}

// Maintainer removes a fully-watched movie from a profile's watchlist when its
// watch completes. It implements watchstate.CompletionObserver. Removals route
// through the same local-list-event path as a manual removal, so connected
// watchlist providers mirror the change.
//
// Episode and series completions are deliberately ignored: removing a series on
// full watch would strand it off the watchlist when new episodes air later, so
// fully-watched series are hidden at read time instead (see
// catalog.WatchlistVisibility) and resurface as soon as an unwatched episode
// appears.
type Maintainer struct {
	storeFor   func(ctx context.Context, userID int) (maintainerStore, error)
	items      itemLookup
	dispatcher listEventDispatcher
}

func NewMaintainer(stores userstore.UserStoreProvider, items itemLookup) *Maintainer {
	return &Maintainer{
		storeFor: func(ctx context.Context, userID int) (maintainerStore, error) {
			return stores.ForUser(ctx, userID)
		},
		items: items,
	}
}

func (m *Maintainer) WithListEventDispatcher(d listEventDispatcher) *Maintainer {
	if m == nil {
		return nil
	}
	m.dispatcher = d
	return m
}

// HandleWatchedCompleted reacts to completed watches asynchronously so it never
// blocks the caller's watch-recording path.
func (m *Maintainer) HandleWatchedCompleted(ctx context.Context, userID int, profileID string, mediaItemIDs []string) {
	if m == nil || m.storeFor == nil || userID == 0 || profileID == "" || len(mediaItemIDs) == 0 {
		return
	}
	ids := append([]string(nil), mediaItemIDs...)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		if err := m.process(ctx, userID, profileID, ids); err != nil {
			slog.WarnContext(ctx, "watchlist auto-remove failed", "component", "watchlist", "user_id", userID, "profile_id", profileID, "error", err)
		}
	}()
}

func (m *Maintainer) process(ctx context.Context, userID int, profileID string, mediaItemIDs []string) error {
	store, err := m.storeFor(ctx, userID)
	if err != nil {
		return err
	}
	enabled, err := store.RemoveWatchedFromWatchlist(ctx, profileID)
	if err != nil {
		return err
	}
	if !enabled {
		return nil
	}
	if m.items == nil {
		return nil
	}
	items, err := m.items.GetByIDs(ctx, mediaItemIDs)
	if err != nil {
		return err
	}
	for _, item := range items {
		if item == nil || item.Type != "movie" {
			continue
		}
		if err := m.removeFromWatchlist(ctx, store, userID, profileID, item); err != nil {
			return err
		}
	}
	return nil
}

func (m *Maintainer) removeFromWatchlist(ctx context.Context, store maintainerStore, userID int, profileID string, item *models.MediaItem) error {
	in, err := store.InWatchlist(ctx, profileID, item.ContentID)
	if err != nil {
		return err
	}
	if !in {
		return nil
	}
	if err := store.RemoveFromWatchlist(ctx, profileID, item.ContentID); err != nil {
		return err
	}
	m.dispatchRemoval(ctx, userID, profileID, item)
	return nil
}

func (m *Maintainer) dispatchRemoval(ctx context.Context, userID int, profileID string, item *models.MediaItem) {
	if m.dispatcher == nil {
		return
	}
	_ = m.dispatcher.HandleLocalListEvent(ctx, watchsync.LocalListEvent{
		List:      watchsync.ListKindWatchlist,
		Change:    watchsync.ListChangeRemoved,
		UserID:    userID,
		ProfileID: profileID,
		Items: []watchsync.LocalFavorite{{
			MediaItemID: item.ContentID,
			Kind:        item.Type,
			Title:       item.Title,
			Year:        item.Year,
			IMDbID:      item.ImdbID,
			TMDBID:      item.TmdbID,
			TVDBID:      item.TvdbID,
			FavoritedAt: time.Now().UTC(),
		}},
	})
}
