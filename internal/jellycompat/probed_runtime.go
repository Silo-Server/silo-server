package jellycompat

import (
	"context"
	"strings"
)

// probedDurationSource is the DetailService subset used to resolve probed
// file durations. Declared as an interface so tests can substitute a
// counting fake without standing up a Postgres pool.
type probedDurationSource interface {
	ProbedDurationsByContentIDs(ctx context.Context, ids []string) map[string]int
	ProbedDurationsByEpisodeIDs(ctx context.Context, ids []string) map[string]int
}

// fillListItemDurations resolves movie and episode durations in separate
// batches and mutates items in place.
func fillListItemDurations(ctx context.Context, src probedDurationSource, items []upstreamListItem) {
	if src == nil || len(items) == 0 {
		return
	}

	movieIDs := make([]string, 0, len(items))
	episodeIDs := make([]string, 0, len(items))
	seenMovies := make(map[string]struct{}, len(items))
	seenEpisodes := make(map[string]struct{}, len(items))
	for _, item := range items {
		switch {
		case strings.EqualFold(item.Type, "movie"):
			if item.ContentID != "" {
				if _, ok := seenMovies[item.ContentID]; !ok {
					seenMovies[item.ContentID] = struct{}{}
					movieIDs = append(movieIDs, item.ContentID)
				}
			}
		case strings.EqualFold(item.Type, "episode"):
			if item.ContentID != "" {
				if _, ok := seenEpisodes[item.ContentID]; !ok {
					seenEpisodes[item.ContentID] = struct{}{}
					episodeIDs = append(episodeIDs, item.ContentID)
				}
			}
		}
	}

	var movieDurations, episodeDurations map[string]int
	if len(movieIDs) > 0 {
		movieDurations = src.ProbedDurationsByContentIDs(ctx, movieIDs)
	}
	if len(episodeIDs) > 0 {
		episodeDurations = src.ProbedDurationsByEpisodeIDs(ctx, episodeIDs)
	}
	// Only a positive lookup overwrites the field: an absent id, or a nil map
	// from a failed query, must leave an already-resolved duration intact
	// rather than silently zeroing it back to "unknown".
	for i := range items {
		switch {
		case strings.EqualFold(items[i].Type, "movie"):
			if duration := movieDurations[items[i].ContentID]; duration > 0 {
				items[i].DurationSeconds = duration
			}
		case strings.EqualFold(items[i].Type, "episode"):
			if duration := episodeDurations[items[i].ContentID]; duration > 0 {
				items[i].DurationSeconds = duration
			}
		}
	}
}

// fillEpisodeTargetDurations resolves durations when a caller builds its DTOs
// directly from compat episode targets. Overlay-only callers already have an
// enriched list item and must not pay for the same lookup again.
func fillEpisodeTargetDurations(ctx context.Context, src probedDurationSource, targets map[string]compatEpisodeTarget) {
	if len(targets) == 0 {
		return
	}

	items := make([]upstreamListItem, 0, len(targets))
	for _, target := range targets {
		items = append(items, target.Item)
	}
	fillListItemDurations(ctx, src, items)
	for _, item := range items {
		target := targets[item.ContentID]
		target.Item = item
		targets[item.ContentID] = target
	}
}
