package handlers

import (
	"context"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

type itemUserStateResponse struct {
	Played      bool `json:"played"`
	IsFavorite  bool `json:"is_favorite"`
	InWatchlist bool `json:"in_watchlist"`
}

type itemUserStateOptions struct {
	UserID             int
	EbookProgressStore EbookReaderProgressLister
}

func resolveItemUserStates(
	ctx context.Context,
	store userstore.UserStore,
	profileID string,
	episodeRepo *catalog.EpisodeRepository,
	items []*models.MediaItem,
) (map[string]*itemUserStateResponse, error) {
	return resolveItemUserStatesWithOptions(ctx, store, profileID, episodeRepo, items, itemUserStateOptions{})
}

func resolveItemUserStatesWithOptions(
	ctx context.Context,
	store userstore.UserStore,
	profileID string,
	episodeRepo *catalog.EpisodeRepository,
	items []*models.MediaItem,
	options itemUserStateOptions,
) (map[string]*itemUserStateResponse, error) {
	result := make(map[string]*itemUserStateResponse, len(items))
	if store == nil || profileID == "" || len(items) == 0 {
		return result, nil
	}

	contentIDs := make([]string, 0, len(items))
	seriesIDs := make([]string, 0)
	seasonIDs := make([]string, 0)
	seenContent := make(map[string]struct{}, len(items))
	seenSeries := make(map[string]struct{})
	seenSeason := make(map[string]struct{})

	for _, item := range items {
		if item == nil || item.ContentID == "" {
			continue
		}
		if _, ok := seenContent[item.ContentID]; ok {
			continue
		}
		seenContent[item.ContentID] = struct{}{}
		contentIDs = append(contentIDs, item.ContentID)
		switch item.Type {
		case "series":
			if _, ok := seenSeries[item.ContentID]; !ok {
				seenSeries[item.ContentID] = struct{}{}
				seriesIDs = append(seriesIDs, item.ContentID)
			}
		case "season":
			if _, ok := seenSeason[item.ContentID]; !ok {
				seenSeason[item.ContentID] = struct{}{}
				seasonIDs = append(seasonIDs, item.ContentID)
			}
		}
	}

	favoriteMap, err := store.ListFavoritesByMediaItems(ctx, profileID, contentIDs)
	if err != nil {
		return nil, err
	}
	watchlistMap, err := store.ListWatchlistByMediaItems(ctx, profileID, contentIDs)
	if err != nil {
		return nil, err
	}

	progressIDs := append([]string{}, contentIDs...)
	seriesEpisodes := map[string][]*models.Episode{}
	seasonEpisodes := map[string][]*models.Episode{}
	if episodeRepo != nil {
		if len(seriesIDs) > 0 {
			seriesEpisodes, err = episodeRepo.ListBySeriesIDs(ctx, seriesIDs)
			if err != nil {
				return nil, err
			}
			for _, episodes := range seriesEpisodes {
				for _, episode := range episodes {
					if episode == nil || episode.ContentID == "" {
						continue
					}
					if _, ok := seenContent[episode.ContentID]; ok {
						continue
					}
					seenContent[episode.ContentID] = struct{}{}
					progressIDs = append(progressIDs, episode.ContentID)
				}
			}
		}
		if len(seasonIDs) > 0 {
			seasonEpisodes, err = episodeRepo.ListBySeasonIDs(ctx, seasonIDs)
			if err != nil {
				return nil, err
			}
			for _, episodes := range seasonEpisodes {
				for _, episode := range episodes {
					if episode == nil || episode.ContentID == "" {
						continue
					}
					if _, ok := seenContent[episode.ContentID]; ok {
						continue
					}
					seenContent[episode.ContentID] = struct{}{}
					progressIDs = append(progressIDs, episode.ContentID)
				}
			}
		}
	}

	progressMap, err := store.ListProgressByMediaItems(ctx, profileID, progressIDs)
	if err != nil {
		return nil, err
	}
	ebookProgressMap, err := resolveEbookProgressForUserStates(ctx, options, profileID, contentIDs)
	if err != nil {
		return nil, err
	}

	for _, item := range items {
		if item == nil || item.ContentID == "" {
			continue
		}
		state := &itemUserStateResponse{
			IsFavorite:  favoriteMap[item.ContentID],
			InWatchlist: watchlistMap[item.ContentID],
		}
		switch item.Type {
		case "series":
			state.Played = allEpisodesCompleted(seriesEpisodes[item.ContentID], progressMap)
		case "season":
			state.Played = allEpisodesCompleted(seasonEpisodes[item.ContentID], progressMap)
		case "ebook":
			state.Played = ebookProgressMap[item.ContentID].Progress >= 0.9
		default:
			state.Played = progressMap[item.ContentID].Completed
		}
		result[item.ContentID] = state
	}

	return result, nil
}

func resolveEbookProgressForUserStates(
	ctx context.Context,
	options itemUserStateOptions,
	profileID string,
	contentIDs []string,
) (map[string]EbookReaderProgress, error) {
	if options.EbookProgressStore == nil || options.UserID <= 0 || profileID == "" || len(contentIDs) == 0 {
		return nil, nil
	}
	return options.EbookProgressStore.ListByContentIDs(ctx, options.UserID, profileID, contentIDs)
}

func allEpisodesCompleted(episodes []*models.Episode, progressMap map[string]userstore.WatchProgress) bool {
	if len(episodes) == 0 {
		return false
	}
	for _, episode := range episodes {
		if episode == nil || episode.ContentID == "" {
			return false
		}
		progress, ok := progressMap[episode.ContentID]
		if !ok || !progress.Completed {
			return false
		}
	}
	return true
}
