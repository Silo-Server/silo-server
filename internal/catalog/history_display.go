package catalog

import (
	"context"
	"strings"

	"github.com/Silo-Server/silo-server/internal/userstore"
)

func ResolveHistoryDisplayIDs(ctx context.Context, entries []userstore.WatchHistoryEntry, episodeRepo *EpisodeRepository) ([]string, error) {
	episodeSeriesByID := make(map[string]string)
	if episodeRepo != nil {
		episodeIDs := make([]string, 0, len(entries))
		seenEpisodeIDs := make(map[string]struct{}, len(entries))
		for _, entry := range entries {
			mediaItemID := strings.TrimSpace(entry.MediaItemID)
			if mediaItemID == "" {
				continue
			}
			if _, ok := seenEpisodeIDs[mediaItemID]; ok {
				continue
			}
			seenEpisodeIDs[mediaItemID] = struct{}{}
			episodeIDs = append(episodeIDs, mediaItemID)
		}

		episodes, err := episodeRepo.GetByIDs(ctx, episodeIDs)
		if err != nil {
			return nil, err
		}
		for _, episode := range episodes {
			if episode == nil {
				continue
			}
			if seriesID := strings.TrimSpace(episode.SeriesID); seriesID != "" {
				episodeSeriesByID[episode.ContentID] = seriesID
			}
		}
	}

	ids := make([]string, 0, len(entries))
	seen := make(map[string]struct{}, len(entries))
	for _, entry := range entries {
		displayID := strings.TrimSpace(entry.MediaItemID)
		if seriesID, ok := episodeSeriesByID[displayID]; ok {
			displayID = seriesID
		}
		if displayID == "" {
			continue
		}
		if _, ok := seen[displayID]; ok {
			continue
		}
		seen[displayID] = struct{}{}
		ids = append(ids, displayID)
	}
	return ids, nil
}
