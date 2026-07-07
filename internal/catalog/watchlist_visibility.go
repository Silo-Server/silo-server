package catalog

import (
	"context"

	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

// watchlistVisibilityItemLookup resolves watchlist entry IDs to catalog items
// so series entries can be told apart from movies. Satisfied by *ItemRepository.
type watchlistVisibilityItemLookup interface {
	GetByIDs(ctx context.Context, contentIDs []string) ([]*models.MediaItem, error)
}

// watchlistVisibilityEpisodeLookup lists the available episodes of the given
// series. Satisfied by *EpisodeRepository (whose ListBySeriesIDs already
// excludes unaired/unavailable episodes, which is exactly the "is there
// anything left to watch" semantics needed here).
type watchlistVisibilityEpisodeLookup interface {
	ListBySeriesIDs(ctx context.Context, seriesIDs []string) (map[string][]*models.Episode, error)
}

// watchlistVisibilityStore is the slice of the user store the filter needs.
// Satisfied by userstore.UserStore.
type watchlistVisibilityStore interface {
	RemoveWatchedFromWatchlist(ctx context.Context, profileID string) (bool, error)
	ListProgressByMediaItems(ctx context.Context, profileID string, mediaItemIDs []string) (map[string]userstore.WatchProgress, error)
}

// WatchlistVisibility hides fully-watched series from watchlist reads without
// removing them: the entry stays in the local list (and on any synced external
// provider), and reappears as soon as a new unwatched episode becomes
// available. Movies are untouched — fully-watched movies are removed outright
// by watchlist.Maintainer instead.
//
// Apply it on display surfaces only (watchlist rails, catalogs, and the
// watchlist API). Sync, recommendations, and notification paths must keep
// seeing the full list.
type WatchlistVisibility struct {
	items    watchlistVisibilityItemLookup
	episodes watchlistVisibilityEpisodeLookup
}

func NewWatchlistVisibility(items watchlistVisibilityItemLookup, episodes watchlistVisibilityEpisodeLookup) *WatchlistVisibility {
	return &WatchlistVisibility{items: items, episodes: episodes}
}

// NewWatchlistVisibilityFromRepos builds the filter from concrete repositories,
// tolerating nil repos (the filter then hides nothing).
func NewWatchlistVisibilityFromRepos(items *ItemRepository, episodes *EpisodeRepository) *WatchlistVisibility {
	if items == nil || episodes == nil {
		return nil
	}
	return NewWatchlistVisibility(items, episodes)
}

// HiddenSeriesIDs returns the subset of the given watchlist entry IDs that
// should be hidden: series whose available episodes are all completed for the
// profile. It returns an empty map when the profile has the remove-watched
// preference disabled, when the filter is missing dependencies, or when no
// entry qualifies. Non-series IDs are never returned.
func (v *WatchlistVisibility) HiddenSeriesIDs(ctx context.Context, store watchlistVisibilityStore, profileID string, entryIDs []string) (map[string]struct{}, error) {
	hidden := make(map[string]struct{})
	if v == nil || v.items == nil || v.episodes == nil || store == nil || profileID == "" || len(entryIDs) == 0 {
		return hidden, nil
	}
	enabled, err := store.RemoveWatchedFromWatchlist(ctx, profileID)
	if err != nil {
		return nil, err
	}
	if !enabled {
		return hidden, nil
	}

	items, err := v.items.GetByIDs(ctx, entryIDs)
	if err != nil {
		return nil, err
	}
	seriesIDs := make([]string, 0, len(items))
	for _, item := range items {
		if item != nil && item.Type == "series" {
			seriesIDs = append(seriesIDs, item.ContentID)
		}
	}
	if len(seriesIDs) == 0 {
		return hidden, nil
	}

	episodesBySeries, err := v.episodes.ListBySeriesIDs(ctx, seriesIDs)
	if err != nil {
		return nil, err
	}
	episodeIDs := make([]string, 0, 64)
	for _, eps := range episodesBySeries {
		for _, ep := range eps {
			if ep != nil && ep.ContentID != "" {
				episodeIDs = append(episodeIDs, ep.ContentID)
			}
		}
	}
	if len(episodeIDs) == 0 {
		return hidden, nil
	}
	progress, err := store.ListProgressByMediaItems(ctx, profileID, episodeIDs)
	if err != nil {
		return nil, err
	}

	for _, seriesID := range seriesIDs {
		eps := episodesBySeries[seriesID]
		if len(eps) == 0 {
			// A series with no available episodes has nothing to watch, but
			// hiding it would make a just-added series vanish; keep it visible.
			continue
		}
		fully := true
		for _, ep := range eps {
			if ep == nil || ep.ContentID == "" {
				continue
			}
			if p, ok := progress[ep.ContentID]; !ok || !p.Completed {
				fully = false
				break
			}
		}
		if fully {
			hidden[seriesID] = struct{}{}
		}
	}
	return hidden, nil
}
