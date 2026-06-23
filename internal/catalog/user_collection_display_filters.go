package catalog

import (
	"context"
	"strings"

	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

type UserCollectionEpisodeLister interface {
	ListBySeriesIDs(ctx context.Context, seriesIDs []string) (map[string][]*models.Episode, error)
	ListBySeasonIDs(ctx context.Context, seasonIDs []string) (map[string][]*models.Episode, error)
}

type UserCollectionDisplayFilterOptions struct {
	ProfileID     string
	WatchFilter   string
	MediaFilter   string
	EpisodeLister UserCollectionEpisodeLister
}

// FilterUserCollectionDisplayItems applies the per-collection display filters
// used by exact user-collection read surfaces. It preserves source order.
func FilterUserCollectionDisplayItems(
	ctx context.Context,
	store userstore.ProgressCompletionStore,
	items []*models.MediaItem,
	options UserCollectionDisplayFilterOptions,
) ([]*models.MediaItem, error) {
	if len(items) == 0 {
		return []*models.MediaItem{}, nil
	}

	mediaFilter, ok := userstore.NormalizeCollectionMediaFilter(options.MediaFilter)
	if !ok {
		mediaFilter = userstore.CollectionMediaFilterAll
	}
	watchFilter, ok := userstore.NormalizeCollectionWatchFilter(options.WatchFilter)
	if !ok {
		watchFilter = userstore.CollectionWatchFilterAll
	}

	mediaFiltered := make([]*models.MediaItem, 0, len(items))
	for _, item := range items {
		if item == nil {
			continue
		}
		if !userCollectionMediaFilterMatches(item, mediaFilter) {
			continue
		}
		mediaFiltered = append(mediaFiltered, item)
	}
	if watchFilter == userstore.CollectionWatchFilterAll || store == nil || strings.TrimSpace(options.ProfileID) == "" {
		return mediaFiltered, nil
	}

	played, err := userCollectionPlayedMap(ctx, store, options.ProfileID, mediaFiltered, options.EpisodeLister)
	if err != nil {
		return nil, err
	}

	filtered := make([]*models.MediaItem, 0, len(mediaFiltered))
	for _, item := range mediaFiltered {
		isPlayed := played[item.ContentID]
		if watchFilter == userstore.CollectionWatchFilterWatched && !isPlayed {
			continue
		}
		if watchFilter == userstore.CollectionWatchFilterUnwatched && isPlayed {
			continue
		}
		filtered = append(filtered, item)
	}
	return filtered, nil
}

func userCollectionMediaFilterMatches(item *models.MediaItem, mediaFilter string) bool {
	switch mediaFilter {
	case userstore.CollectionMediaFilterMovie:
		return item.Type == "movie"
	case userstore.CollectionMediaFilterSeries:
		return item.Type == "series"
	default:
		return true
	}
}

func userCollectionPlayedMap(
	ctx context.Context,
	store userstore.ProgressCompletionStore,
	profileID string,
	items []*models.MediaItem,
	episodes UserCollectionEpisodeLister,
) (map[string]bool, error) {
	result := make(map[string]bool, len(items))
	if len(items) == 0 {
		return result, nil
	}

	contentIDs := make([]string, 0, len(items))
	seriesIDs := make([]string, 0)
	seasonIDs := make([]string, 0)
	seen := make(map[string]struct{}, len(items))
	for _, item := range items {
		if item == nil || strings.TrimSpace(item.ContentID) == "" {
			continue
		}
		if _, ok := seen[item.ContentID]; ok {
			continue
		}
		seen[item.ContentID] = struct{}{}
		contentIDs = append(contentIDs, item.ContentID)
		switch item.Type {
		case "series":
			seriesIDs = append(seriesIDs, item.ContentID)
		case "season":
			seasonIDs = append(seasonIDs, item.ContentID)
		}
	}

	seriesEpisodes := map[string][]*models.Episode{}
	seasonEpisodes := map[string][]*models.Episode{}
	if episodes != nil {
		if len(seriesIDs) > 0 {
			var err error
			seriesEpisodes, err = episodes.ListBySeriesIDs(ctx, seriesIDs)
			if err != nil {
				return nil, err
			}
			for _, eps := range seriesEpisodes {
				for _, ep := range eps {
					if ep == nil || ep.ContentID == "" {
						continue
					}
					if _, ok := seen[ep.ContentID]; ok {
						continue
					}
					seen[ep.ContentID] = struct{}{}
					contentIDs = append(contentIDs, ep.ContentID)
				}
			}
		}
		if len(seasonIDs) > 0 {
			var err error
			seasonEpisodes, err = episodes.ListBySeasonIDs(ctx, seasonIDs)
			if err != nil {
				return nil, err
			}
			for _, eps := range seasonEpisodes {
				for _, ep := range eps {
					if ep == nil || ep.ContentID == "" {
						continue
					}
					if _, ok := seen[ep.ContentID]; ok {
						continue
					}
					seen[ep.ContentID] = struct{}{}
					contentIDs = append(contentIDs, ep.ContentID)
				}
			}
		}
	}

	progress, err := userstore.ListProgressWithCompletedHistory(ctx, store, profileID, contentIDs)
	if err != nil {
		return nil, err
	}
	for _, item := range items {
		if item == nil || item.ContentID == "" {
			continue
		}
		switch item.Type {
		case "series":
			result[item.ContentID] = userCollectionAllEpisodesCompleted(seriesEpisodes[item.ContentID], progress)
		case "season":
			result[item.ContentID] = userCollectionAllEpisodesCompleted(seasonEpisodes[item.ContentID], progress)
		default:
			result[item.ContentID] = progress[item.ContentID].Completed
		}
	}
	return result, nil
}

func userCollectionAllEpisodesCompleted(
	episodes []*models.Episode,
	progress map[string]userstore.WatchProgress,
) bool {
	if len(episodes) == 0 {
		return false
	}
	for _, episode := range episodes {
		if episode == nil || episode.ContentID == "" {
			return false
		}
		if !progress[episode.ContentID].Completed {
			return false
		}
	}
	return true
}
