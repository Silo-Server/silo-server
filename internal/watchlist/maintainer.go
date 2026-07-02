// Package watchlist holds cross-cutting watchlist behavior that doesn't belong
// to a single handler — currently the auto-removal of fully-watched items.
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
	GetByID(ctx context.Context, contentID string) (*models.MediaItem, error)
}

type episodeLookup interface {
	GetByID(ctx context.Context, contentID string) (*models.Episode, error)
	ListBySeries(ctx context.Context, seriesID string) ([]*models.Episode, error)
}

type listEventDispatcher interface {
	HandleLocalListEvent(ctx context.Context, event watchsync.LocalListEvent) error
}

// maintainerStore is the narrow slice of the user store the maintainer needs.
type maintainerStore interface {
	RemoveWatchedFromWatchlist(ctx context.Context, profileID string) (bool, error)
	InWatchlist(ctx context.Context, profileID, mediaItemID string) (bool, error)
	RemoveFromWatchlist(ctx context.Context, profileID, mediaItemID string) error
	ListProgressByMediaItems(ctx context.Context, profileID string, mediaItemIDs []string) (map[string]userstore.WatchProgress, error)
}

// Maintainer removes fully-watched items from a profile's watchlist when a watch
// completes: a movie when it is watched, a series once every episode is watched.
// It implements watchstate.CompletionObserver. Removals route through the same
// local-list-event path as a manual removal, so connected watchlist providers
// mirror the change.
type Maintainer struct {
	storeFor   func(ctx context.Context, userID int) (maintainerStore, error)
	items      itemLookup
	episodes   episodeLookup
	dispatcher listEventDispatcher
}

func NewMaintainer(stores userstore.UserStoreProvider, items itemLookup, episodes episodeLookup) *Maintainer {
	return &Maintainer{
		storeFor: func(ctx context.Context, userID int) (maintainerStore, error) {
			return stores.ForUser(ctx, userID)
		},
		items:    items,
		episodes: episodes,
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
func (m *Maintainer) HandleWatchedCompleted(_ context.Context, userID int, profileID string, mediaItemIDs []string) {
	if m == nil || m.storeFor == nil || userID == 0 || profileID == "" || len(mediaItemIDs) == 0 {
		return
	}
	ids := append([]string(nil), mediaItemIDs...)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		if err := m.process(ctx, userID, profileID, ids); err != nil {
			slog.Warn("watchlist auto-remove failed", "user_id", userID, "profile_id", profileID, "error", err)
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
	// Resolve each completed item to the watchlist entry it clears (a movie
	// clears itself; an episode clears its series only once the series is fully
	// watched), deduping so a batch completing many episodes of one series
	// checks that series once.
	seen := make(map[string]struct{})
	for _, id := range mediaItemIDs {
		candidate, ok, err := m.resolveCandidate(ctx, store, profileID, id)
		if err != nil {
			return err
		}
		if !ok {
			continue
		}
		if _, dup := seen[candidate]; dup {
			continue
		}
		seen[candidate] = struct{}{}
		if err := m.removeFromWatchlist(ctx, store, userID, profileID, candidate); err != nil {
			return err
		}
	}
	return nil
}

// resolveCandidate maps a completed media item id to the watchlist entry id to
// clear, or ok=false when nothing should be removed.
func (m *Maintainer) resolveCandidate(ctx context.Context, store maintainerStore, profileID, mediaItemID string) (string, bool, error) {
	// A media-items miss is the normal path for episodes (they live in their own
	// table), but a real repo error must not be silently swallowed — remember it
	// and surface it if the episode path also can't resolve the id.
	var itemLookupErr error
	if m.items != nil {
		item, err := m.items.GetByID(ctx, mediaItemID)
		switch {
		case err != nil:
			itemLookupErr = err
		case item != nil:
			switch item.Type {
			case "movie":
				return item.ContentID, true, nil
			case "series":
				// A series id can only reach here if the series itself was marked
				// watched; still require every episode to be watched before
				// clearing it, so a partially-watched series is never dropped.
				return m.seriesCandidate(ctx, store, profileID, item.ContentID)
			default:
				return "", false, nil
			}
		}
	}
	// Not a catalog item — treat as an episode and clear the parent series once
	// every episode has been watched.
	if m.episodes == nil {
		return "", false, itemLookupErr
	}
	episode, err := m.episodes.GetByID(ctx, mediaItemID)
	if err != nil {
		return "", false, err
	}
	if episode == nil {
		return "", false, itemLookupErr
	}
	return m.seriesCandidate(ctx, store, profileID, episode.SeriesID)
}

// seriesCandidate returns the series id as a removal candidate only if every
// episode is watched.
func (m *Maintainer) seriesCandidate(ctx context.Context, store maintainerStore, profileID, seriesID string) (string, bool, error) {
	fully, err := m.seriesFullyWatched(ctx, store, profileID, seriesID)
	if err != nil {
		return "", false, err
	}
	if !fully {
		return "", false, nil
	}
	return seriesID, true, nil
}

func (m *Maintainer) seriesFullyWatched(ctx context.Context, store maintainerStore, profileID, seriesID string) (bool, error) {
	if seriesID == "" {
		return false, nil
	}
	episodes, err := m.episodes.ListBySeries(ctx, seriesID)
	if err != nil {
		return false, err
	}
	ids := make([]string, 0, len(episodes))
	for _, ep := range episodes {
		if ep != nil && ep.ContentID != "" {
			ids = append(ids, ep.ContentID)
		}
	}
	if len(ids) == 0 {
		return false, nil
	}
	progress, err := store.ListProgressByMediaItems(ctx, profileID, ids)
	if err != nil {
		return false, err
	}
	for _, id := range ids {
		if p, ok := progress[id]; !ok || !p.Completed {
			return false, nil
		}
	}
	return true, nil
}

func (m *Maintainer) removeFromWatchlist(ctx context.Context, store maintainerStore, userID int, profileID, mediaItemID string) error {
	in, err := store.InWatchlist(ctx, profileID, mediaItemID)
	if err != nil {
		return err
	}
	if !in {
		return nil
	}
	if err := store.RemoveFromWatchlist(ctx, profileID, mediaItemID); err != nil {
		return err
	}
	m.dispatchRemoval(ctx, userID, profileID, mediaItemID)
	return nil
}

func (m *Maintainer) dispatchRemoval(ctx context.Context, userID int, profileID, mediaItemID string) {
	if m.dispatcher == nil || m.items == nil {
		return
	}
	item, err := m.items.GetByID(ctx, mediaItemID)
	if err != nil || item == nil {
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
