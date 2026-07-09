package userstore

import (
	"context"
	"strings"
	"time"
)

type HistoryVisibilityStore interface {
	VisibleHistoryTimestamps(ctx context.Context, profileID string, mediaItemIDs []string, at time.Time) (map[string]string, error)
}

type VisibleHistoryAdder interface {
	AddVisibleHistory(ctx context.Context, entry WatchHistoryEntry) (WatchHistoryEntry, error)
}

func AddVisibleHistory(ctx context.Context, store UserStore, entry WatchHistoryEntry) (WatchHistoryEntry, error) {
	if adder, ok := store.(VisibleHistoryAdder); ok {
		return adder.AddVisibleHistory(ctx, entry)
	}
	entryTimes, err := VisibleHistoryTimestamps(ctx, store, entry.ProfileID, []string{entry.MediaItemID}, parseHistoryTimestamp(entry.WatchedAt))
	if err != nil {
		return entry, err
	}
	if entryTime := entryTimes[entry.MediaItemID]; entryTime != "" {
		entry.WatchedAt = entryTime
	}
	if err := store.AddHistory(ctx, entry); err != nil {
		return entry, err
	}
	return entry, nil
}

func VisibleHistoryTimestamps(ctx context.Context, store UserStore, profileID string, mediaItemIDs []string, at time.Time) (map[string]string, error) {
	mediaItemIDs = compactHistoryMediaItemIDs(mediaItemIDs)
	result := make(map[string]string, len(mediaItemIDs))
	if len(mediaItemIDs) == 0 {
		return result, nil
	}
	if visibilityStore, ok := store.(HistoryVisibilityStore); ok {
		return visibilityStore.VisibleHistoryTimestamps(ctx, profileID, mediaItemIDs, at)
	}
	timestamp := at.UTC().Format(time.RFC3339)
	if at.IsZero() {
		timestamp = time.Now().UTC().Format(time.RFC3339)
	}
	for _, mediaItemID := range mediaItemIDs {
		result[mediaItemID] = timestamp
	}
	return result, nil
}

func parseHistoryTimestamp(value string) time.Time {
	if value == "" {
		return time.Time{}
	}
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return time.Time{}
	}
	return parsed
}

// SeriesWatchCounts is the aggregate episode watch state for one series, as
// computed by SeriesEpisodeRollupStore.
type SeriesWatchCounts struct {
	TotalEpisodes   int
	WatchedCount    int
	InProgressCount int
}

// SeriesEpisodeRollupStore is an optional store capability: compute the
// per-series episode watch-state rollup (total / watched / in-progress
// episode counts) in SQL instead of materializing every episode of every
// series and batching per-episode progress lookups through
// ListProgressWithCompletedHistory. Implemented by the Postgres store, where
// episodes and progress live in the same database; SQLite-backed stores fall
// back to the chunked in-memory path. Semantics must match
// ListProgressWithCompletedHistory + catalog.EpisodeRollupUserData: an episode
// is watched when its visible progress row is completed or a visible completed
// history row exists, and in-progress when it is not watched and its visible
// progress row has position_seconds > 0.
type SeriesEpisodeRollupStore interface {
	SeriesEpisodeWatchCounts(ctx context.Context, profileID string, seriesIDs []string) (map[string]SeriesWatchCounts, error)
}

// CompletedHistoryItemMap returns the latest completed-history item row for a
// scoped item query. Lookup failures degrade to an empty map so user-data
// enrichment can keep returning progress rows.
func CompletedHistoryItemMap(ctx context.Context, store ProgressCompletionStore, query CompletedHistoryItemQuery) map[string]CompletedHistoryItem {
	result := map[string]CompletedHistoryItem{}
	if store == nil || query.ProfileID == "" {
		return result
	}
	query.MediaItemIDs = compactHistoryMediaItemIDs(query.MediaItemIDs)
	if len(query.MediaItemIDs) == 0 {
		return result
	}
	items, err := store.ListCompletedHistoryItems(ctx, query)
	if err != nil {
		return result
	}
	for _, item := range items {
		if item.MediaItemID != "" {
			result[item.MediaItemID] = item
		}
	}
	return result
}

// GetProgressWithCompletedHistory returns normal progress overlaid with
// completed history for callers that present a single item's played state.
func GetProgressWithCompletedHistory(ctx context.Context, store UserStore, profileID, mediaItemID string) (*WatchProgress, error) {
	mediaItemID = strings.TrimSpace(mediaItemID)
	if store == nil || profileID == "" || mediaItemID == "" {
		return nil, nil
	}
	progress, err := store.GetProgress(ctx, profileID, mediaItemID)
	if err != nil {
		return nil, err
	}
	if progress != nil && progress.Completed {
		return progress, nil
	}
	completed := CompletedHistoryItemMap(ctx, store, CompletedHistoryItemQuery{
		ProfileID:    profileID,
		MediaItemIDs: []string{mediaItemID},
	})[mediaItemID]
	if completed.MediaItemID == "" {
		return progress, nil
	}
	if progress == nil {
		return &WatchProgress{
			ProfileID:   profileID,
			MediaItemID: mediaItemID,
			Completed:   true,
			UpdatedAt:   completed.WatchedAt,
		}, nil
	}
	progress.Completed = true
	if timestampAfter(completed.WatchedAt, progress.UpdatedAt) {
		progress.UpdatedAt = completed.WatchedAt
	}
	return progress, nil
}

// ListProgressWithCompletedHistory returns progress for mediaItemIDs with
// completed history folded into the map. History is only queried for IDs that
// are not already completed by a progress row.
func ListProgressWithCompletedHistory(ctx context.Context, store ProgressCompletionStore, profileID string, mediaItemIDs []string) (map[string]WatchProgress, error) {
	mediaItemIDs = compactHistoryMediaItemIDs(mediaItemIDs)
	if store == nil || profileID == "" || len(mediaItemIDs) == 0 {
		return map[string]WatchProgress{}, nil
	}
	progressMap, err := store.ListProgressByMediaItems(ctx, profileID, mediaItemIDs)
	if err != nil {
		return nil, err
	}
	if progressMap == nil {
		progressMap = map[string]WatchProgress{}
	}

	candidates := make([]string, 0, len(mediaItemIDs))
	for _, mediaItemID := range mediaItemIDs {
		if progress, ok := progressMap[mediaItemID]; ok && progress.Completed {
			continue
		}
		candidates = append(candidates, mediaItemID)
	}
	if len(candidates) == 0 {
		return progressMap, nil
	}

	completed := CompletedHistoryItemMap(ctx, store, CompletedHistoryItemQuery{
		ProfileID:    profileID,
		MediaItemIDs: candidates,
	})
	for mediaItemID, completedItem := range completed {
		if progress, ok := progressMap[mediaItemID]; ok {
			progress.Completed = true
			if timestampAfter(completedItem.WatchedAt, progress.UpdatedAt) {
				progress.UpdatedAt = completedItem.WatchedAt
			}
			progressMap[mediaItemID] = progress
			continue
		}
		progressMap[mediaItemID] = WatchProgress{
			ProfileID:   profileID,
			MediaItemID: mediaItemID,
			Completed:   true,
			UpdatedAt:   completedItem.WatchedAt,
		}
	}
	return progressMap, nil
}

func compactHistoryMediaItemIDs(mediaItemIDs []string) []string {
	result := make([]string, 0, len(mediaItemIDs))
	seen := make(map[string]struct{}, len(mediaItemIDs))
	for _, mediaItemID := range mediaItemIDs {
		mediaItemID = strings.TrimSpace(mediaItemID)
		if mediaItemID == "" {
			continue
		}
		if _, ok := seen[mediaItemID]; ok {
			continue
		}
		seen[mediaItemID] = struct{}{}
		result = append(result, mediaItemID)
	}
	return result
}

func timestampAfter(left, right string) bool {
	if left == "" {
		return false
	}
	if right == "" {
		return true
	}
	leftTime, leftErr := time.Parse(time.RFC3339, left)
	rightTime, rightErr := time.Parse(time.RFC3339, right)
	if leftErr == nil && rightErr == nil {
		return leftTime.After(rightTime)
	}
	return left > right
}
