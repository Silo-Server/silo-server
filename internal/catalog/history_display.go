package catalog

import (
	"context"
	"strings"

	"github.com/Silo-Server/silo-server/internal/userstore"
)

// HistoryEpisodeScopeIDs returns history entry ids for the episode-scoped
// history view: each watched item keeps its own id (episodes are NOT collapsed
// into their series), deduplicated to the most recent watch — entries arrive
// most-recent-first from ListHistory. Non-episode ids (movies, audiobooks)
// pass through unchanged; the episode catalog relation drops them at
// hydration, so no episodes lookup is needed here.
func HistoryEpisodeScopeIDs(entries []userstore.WatchHistoryEntry) []string {
	ids := make([]string, 0, len(entries))
	seen := make(map[string]struct{}, len(entries))
	for _, entry := range entries {
		mediaItemID := strings.TrimSpace(entry.MediaItemID)
		if mediaItemID == "" {
			continue
		}
		if _, ok := seen[mediaItemID]; ok {
			continue
		}
		seen[mediaItemID] = struct{}{}
		ids = append(ids, mediaItemID)
	}
	return ids
}

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
