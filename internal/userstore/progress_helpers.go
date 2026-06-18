package userstore

import (
	"context"
	"strings"
)

// CompletedHistoryItemIDSet returns the completed-history item IDs for a scoped
// item query as a set. Lookup failures degrade to an empty set so user-data
// enrichment can keep returning progress rows.
func CompletedHistoryItemIDSet(ctx context.Context, store UserStore, query CompletedHistoryItemIDQuery) map[string]bool {
	result := map[string]bool{}
	if store == nil || query.ProfileID == "" {
		return result
	}
	query.MediaItemIDs = compactHistoryMediaItemIDs(query.MediaItemIDs)
	if len(query.MediaItemIDs) == 0 {
		return result
	}
	ids, err := store.ListCompletedHistoryItemIDs(ctx, query)
	if err != nil {
		return result
	}
	for _, id := range ids {
		if id != "" {
			result[id] = true
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
	completed := CompletedHistoryItemIDSet(ctx, store, CompletedHistoryItemIDQuery{
		ProfileID:    profileID,
		MediaItemIDs: []string{mediaItemID},
	})[mediaItemID]
	if !completed {
		return progress, nil
	}
	if progress == nil {
		return &WatchProgress{
			ProfileID:   profileID,
			MediaItemID: mediaItemID,
			Completed:   true,
		}, nil
	}
	progress.Completed = true
	return progress, nil
}

// ListProgressWithCompletedHistory returns progress for mediaItemIDs with
// completed history folded into the map. History is only queried for IDs that
// are not already completed by a progress row.
func ListProgressWithCompletedHistory(ctx context.Context, store UserStore, profileID string, mediaItemIDs []string) (map[string]WatchProgress, error) {
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

	completed := CompletedHistoryItemIDSet(ctx, store, CompletedHistoryItemIDQuery{
		ProfileID:    profileID,
		MediaItemIDs: candidates,
	})
	for mediaItemID := range completed {
		if progress, ok := progressMap[mediaItemID]; ok {
			progress.Completed = true
			progressMap[mediaItemID] = progress
			continue
		}
		progressMap[mediaItemID] = WatchProgress{
			ProfileID:   profileID,
			MediaItemID: mediaItemID,
			Completed:   true,
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
