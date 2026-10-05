package recommendations

import (
	"context"
	"sort"

	"github.com/Silo-Server/silo-server/internal/catalog"
)

// WatchTonightResult holds the recommendation-engine candidates for the
// Watch Tonight feature. Items are scored and sorted by descending relevance.
// The handler is responsible for merging in live Continue Watching / Next Up
// items and applying source-priority boosts.
type WatchTonightResult struct {
	Items  []ScoredItem `json:"items"`
	IsCold bool         `json:"is_cold"`
}

// GetWatchTonight returns a scored list of recommendation-engine candidates
// for the Watch Tonight feature. It pulls from cached for-you, because-you-watched,
// and similar-users sources, falling back to popular/recently-added for cold-start users.
// Results are filtered before trimming so deeper cached candidates can backfill
// watched, low-rated, or access-disallowed recommendations. With personal
// rows off (see WithPersonalRows) only the cold-start rows are used.
func (r *Reader) GetWatchTonight(ctx context.Context, userID int, profileID string, limit int, filter catalog.AccessFilter) (WatchTonightResult, error) {
	if limit <= 0 {
		limit = 8
	}
	if limit > 20 {
		limit = 20
	}

	byID := make(map[string]ScoredItem, limit*3)

	// With recommendations disabled no personal row is served, even one
	// still cached from before, and the cold-start rows stand in.
	if !r.personalOff {
		// 1. For-you main row — highest relevance band (0.80–1.00).
		forYouItems, err := r.repo.GetRecommendationCache(ctx, userID, profileID, RecTypeForYouMain, "")
		if err != nil {
			return WatchTonightResult{}, err
		}
		mergeScored(byID, forYouItems, 0.80, 1.00)

		// 2. Because-you-watched — mid band (0.55–0.75).
		anchors, err := anchorItemIDs(ctx, r.signalReader(), r.ratingsRepo, userID, profileID, BecauseYouWatchedAnchors)
		if err != nil {
			return WatchTonightResult{}, err
		}
		for _, sourceID := range anchors {
			items, err := r.repo.GetRecommendationCache(ctx, userID, profileID, RecTypeBecauseWatched, sourceID)
			if err != nil {
				return WatchTonightResult{}, err
			}
			mergeScored(byID, items, 0.55, 0.75)
		}

		// 3. Similar-users-liked — lower band (0.35–0.55).
		similarItems, err := r.repo.GetRecommendationCache(ctx, userID, profileID, RecTypeSimilarUsersLiked, "")
		if err != nil {
			return WatchTonightResult{}, err
		}
		mergeScored(byID, similarItems, 0.35, 0.55)
	}

	isCold := len(byID) == 0

	// 4. Cold-start fallback: popular + recently-added.
	if isCold {
		popular, err := r.repo.GetRecommendationCache(ctx, GlobalCacheUserID, GlobalCacheProfileID, RecTypePopular, "")
		if err != nil {
			return WatchTonightResult{}, err
		}
		mergeScored(byID, popular, 0.20, 0.40)

		recentlyAdded, err := r.repo.GetRecommendationCache(ctx, GlobalCacheUserID, GlobalCacheProfileID, RecTypeRecentlyAdded, "")
		if err != nil {
			return WatchTonightResult{}, err
		}
		mergeScored(byID, recentlyAdded, 0.15, 0.35)
	}

	// Collect, sort descending by score, trim.
	items := make([]ScoredItem, 0, len(byID))
	for _, item := range byID {
		items = append(items, item)
	}
	sort.Slice(items, func(i, j int) bool {
		return items[i].Score > items[j].Score
	})
	rows, err := r.filterRows(ctx, userID, profileID, []ForYouRow{{
		Type:  "watch_tonight",
		Label: "Watch Tonight",
		Items: items,
	}}, filter)
	if err != nil {
		return WatchTonightResult{}, err
	}
	if len(rows) == 0 {
		return WatchTonightResult{Items: []ScoredItem{}, IsCold: isCold}, nil
	}
	items = rows[0].Items
	if len(items) > limit {
		items = items[:limit]
	}

	return WatchTonightResult{Items: items, IsCold: isCold}, nil
}

// mergeScored normalizes raw ScoredItem scores into the given band and merges
// them into byID. First-seen (higher-band) items win on collision.
func mergeScored(byID map[string]ScoredItem, items []ScoredItem, bandMin, bandMax float64) {
	if len(items) == 0 {
		return
	}

	maxRaw := items[0].Score
	for _, item := range items[1:] {
		if item.Score > maxRaw {
			maxRaw = item.Score
		}
	}
	if maxRaw <= 0 {
		maxRaw = 1.0
	}

	for _, item := range items {
		if _, exists := byID[item.MediaItemID]; exists {
			continue
		}
		normalized := bandMin + (item.Score/maxRaw)*(bandMax-bandMin)
		item.Score = normalized
		byID[item.MediaItemID] = item
	}
}
