package handlers

import (
	"context"
	"log/slog"

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
	// WatchedCount and UnplayedCount are served by v2 only, on a series or
	// season the profile has not finished, so a card can say how many
	// episodes are left. Nil everywhere else.
	WatchedCount  *int `json:"-"`
	UnplayedCount *int `json:"-"`
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
	notInterestedMap, err := store.ListNotInterestedByMediaItems(ctx, profileID, contentIDs)
	if err != nil {
		return nil, err
	}

	progressIDs := append([]string{}, contentIDs...)
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

	progressMap, err := userstore.ListProgressWithCompletedHistory(ctx, store, profileID, progressIDs)
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
			IsFavorite:    favoriteMap[item.ContentID],
			InWatchlist:   watchlistMap[item.ContentID],
			NotInterested: notInterestedMap[item.ContentID],
		}
		switch item.Type {
		case "series":
			state.Played = allEpisodesCompleted(seriesEpisodes[item.ContentID], progressMap)
			if seriesCompletion != nil {
				state.Played = seriesCompletion[item.ContentID]
			}
		case "season":
			state.Played = allEpisodesCompleted(seasonEpisodes[item.ContentID], progressMap)
			if seasonCompletion != nil {
				state.Played = seasonCompletion[item.ContentID]
			}
		case "ebook":
			state.Played = ebookProgressMap[item.ContentID].Progress >= models.EbookFinishedProgressThreshold
		default:
			state.Played = progressMap[item.ContentID].Completed
		}
		result[item.ContentID] = state
	}

	attachEpisodeCounts(ctx, store, profileID, result, seriesIDs, seasonIDs)
	return result, nil
}

// attachEpisodeCounts fills WatchedCount and UnplayedCount on the series and
// seasons in result the profile has not finished, with one rollup query per
// kind. Played keeps coming from the completion path above; the counts only
// decorate a card, so a store without the rollup or a failed query leaves
// them absent.
func attachEpisodeCounts(
	ctx context.Context,
	store userstore.UserStore,
	profileID string,
	result map[string]*itemUserStateResponse,
	seriesIDs, seasonIDs []string,
) {
	rollup, ok := store.(userstore.SeriesEpisodeRollupStore)
	if !ok {
		return
	}
	unfinished := func(ids []string) []string {
		out := make([]string, 0, len(ids))
		for _, id := range ids {
			if state := result[id]; state != nil && !state.Played {
				out = append(out, id)
			}
		}
		return out
	}
	attach := func(counts map[string]userstore.SeriesWatchCounts) {
		for id, c := range counts {
			state := result[id]
			if state == nil || state.Played || c.TotalEpisodes == 0 || c.WatchedCount >= c.TotalEpisodes {
				continue
			}
			watched, unplayed := c.WatchedCount, c.TotalEpisodes-c.WatchedCount
			state.WatchedCount, state.UnplayedCount = &watched, &unplayed
		}
	}
	if ids := unfinished(seriesIDs); len(ids) > 0 {
		counts, err := rollup.SeriesEpisodeWatchCounts(ctx, profileID, ids)
		if err != nil {
			slog.WarnContext(ctx, "series episode counts failed", "component", "api", "error", err)
		} else {
			attach(counts)
		}
	}
	if ids := unfinished(seasonIDs); len(ids) > 0 {
		counts, err := rollup.SeasonEpisodeWatchCounts(ctx, profileID, ids)
		if err != nil {
			slog.WarnContext(ctx, "season episode counts failed", "component", "api", "error", err)
		} else {
			attach(counts)
		}
	}
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

func allEpisodesCompleted(episodes []string, progressMap map[string]userstore.WatchProgress) bool {
	if len(episodes) == 0 {
		return false
	}
	for _, episodeID := range episodes {
		if episodeID == "" {
			return false
		}
		progress, ok := progressMap[episodeID]
		if !ok || !progress.Completed {
			return false
		}
	}
	return true
}
