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
	// NotInterested is served by v2 only; the frozen v1 shape leaves it out.
	NotInterested bool `json:"-"`
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
	// Played reads an item's own watch progress unless the item is a series or
	// season, decided by its episodes, or an ebook, decided by reading progress.
	progressIDs := make([]string, 0, len(items))
	ebookIDs := make([]string, 0)
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
		case itemTypeEbook:
			ebookIDs = append(ebookIDs, item.ContentID)
		default:
			progressIDs = append(progressIDs, item.ContentID)
		}
	}

	// Series and season completion runs before the flag reads: when it falls
	// back to listing episodes, their IDs join the played reads.
	var err error
	seriesEpisodes := map[string][]string{}
	seasonEpisodes := map[string][]string{}
	var seriesCompletion, seasonCompletion map[string]bool
	if episodeRepo != nil {
		if completionStore, ok := store.(userstore.EpisodeParentCompletionStore); ok {
			// Keep the existing path for stores without SQL completion support,
			// and preserve its degradation behavior if the optional query fails.
			if len(seriesIDs) > 0 {
				seriesCompletion, err = completionStore.SeriesCompletion(ctx, profileID, seriesIDs)
				if err != nil {
					seriesCompletion = nil
				}
			}
			if len(seasonIDs) > 0 {
				seasonCompletion, err = completionStore.SeasonCompletion(ctx, profileID, seasonIDs)
				if err != nil {
					seasonCompletion = nil
				}
			}
		}
		if len(seriesIDs) > 0 && seriesCompletion == nil {
			seriesEpisodes, err = episodeRepo.ListIDsBySeriesIDs(ctx, seriesIDs)
			if err != nil {
				return nil, err
			}
			for _, episodes := range seriesEpisodes {
				for _, episodeID := range episodes {
					if episodeID == "" {
						continue
					}
					if _, ok := seenContent[episodeID]; ok {
						continue
					}
					seenContent[episodeID] = struct{}{}
					progressIDs = append(progressIDs, episodeID)
				}
			}
		}
		if len(seasonIDs) > 0 && seasonCompletion == nil {
			seasonEpisodes, err = episodeRepo.ListIDsBySeasonIDs(ctx, seasonIDs)
			if err != nil {
				return nil, err
			}
			for _, episodes := range seasonEpisodes {
				for _, episodeID := range episodes {
					if episodeID == "" {
						continue
					}
					if _, ok := seenContent[episodeID]; ok {
						continue
					}
					seenContent[episodeID] = struct{}{}
					progressIDs = append(progressIDs, episodeID)
				}
			}
		}
	}

	flags, err := readItemUserFlags(ctx, store, profileID, contentIDs, progressIDs)
	if err != nil {
		return nil, err
	}
	ebookProgressMap, err := resolveEbookProgressForUserStates(ctx, options, profileID, ebookIDs)
	if err != nil {
		return nil, err
	}

	for _, item := range items {
		if item == nil || item.ContentID == "" {
			continue
		}
		state := &itemUserStateResponse{
			IsFavorite:    flags.favorite[item.ContentID],
			InWatchlist:   flags.watchlist[item.ContentID],
			NotInterested: flags.notInterested[item.ContentID],
		}
		switch item.Type {
		case "series":
			state.Played = allEpisodesCompleted(seriesEpisodes[item.ContentID], flags.played)
			if seriesCompletion != nil {
				state.Played = seriesCompletion[item.ContentID]
			}
		case "season":
			state.Played = allEpisodesCompleted(seasonEpisodes[item.ContentID], flags.played)
			if seasonCompletion != nil {
				state.Played = seasonCompletion[item.ContentID]
			}
		case "ebook":
			state.Played = ebookProgressMap[item.ContentID].Progress >= models.EbookFinishedProgressThreshold
		default:
			state.Played = flags.played[item.ContentID]
		}
		result[item.ContentID] = state
	}

	return result, nil
}

// itemUserFlagReads holds a profile's list memberships for a page of items and
// which of its played IDs are completed.
type itemUserFlagReads struct {
	favorite, watchlist, notInterested, played map[string]bool
}

// readItemUserFlags reads the list memberships of contentIDs and the played
// state of playedIDs in one round trip when the store supports it. When it
// does not, or that read fails, it makes the per-table reads, whose separate
// failure handling the combined read cannot tell apart.
func readItemUserFlags(ctx context.Context, store userstore.UserStore, profileID string, contentIDs, playedIDs []string) (itemUserFlagReads, error) {
	if reader, ok := store.(userstore.ItemUserFlagStore); ok {
		if flags, ok, err := reader.ItemUserFlags(ctx, profileID, contentIDs, playedIDs); ok && err == nil {
			reads := itemUserFlagReads{
				favorite:      map[string]bool{},
				watchlist:     map[string]bool{},
				notInterested: map[string]bool{},
				played:        map[string]bool{},
			}
			for id, flag := range flags {
				reads.favorite[id] = flag.Favorite
				reads.watchlist[id] = flag.InWatchlist
				reads.notInterested[id] = flag.NotInterested
				reads.played[id] = flag.Played
			}
			return reads, nil
		}
	}

	var reads itemUserFlagReads
	var err error
	if reads.favorite, err = store.ListFavoritesByMediaItems(ctx, profileID, contentIDs); err != nil {
		return reads, err
	}
	if reads.watchlist, err = store.ListWatchlistByMediaItems(ctx, profileID, contentIDs); err != nil {
		return reads, err
	}
	if reads.notInterested, err = store.ListNotInterestedByMediaItems(ctx, profileID, contentIDs); err != nil {
		return reads, err
	}
	progressMap, err := userstore.ListProgressWithCompletedHistory(ctx, store, profileID, playedIDs)
	if err != nil {
		return reads, err
	}
	reads.played = make(map[string]bool, len(progressMap))
	for id, progress := range progressMap {
		reads.played[id] = progress.Completed
	}
	return reads, nil
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

func allEpisodesCompleted(episodes []string, played map[string]bool) bool {
	if len(episodes) == 0 {
		return false
	}
	for _, episodeID := range episodes {
		if episodeID == "" || !played[episodeID] {
			return false
		}
	}
	return true
}
