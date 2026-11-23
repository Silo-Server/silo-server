package recommendations

import (
	"context"
	"slices"
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
// and similar-users sources, falling back for cold-start users to the cached
// Popular row and the live Recently Added row, which needs no cache and so
// still fills on a fresh server or with recommendations disabled, when no
// worker rebuilds the cached rows. Results are filtered before trimming so
// deeper candidates can backfill watched, rated, or access-disallowed
// recommendations, and the profile's exclusion set is read once for both
// passes. With personal rows off (see WithPersonalRows) only the cold-start
// rows are used.
func (r *Reader) GetWatchTonight(ctx context.Context, userID int, profileID string, limit int, filter catalog.AccessFilter) (WatchTonightResult, error) {
	if limit <= 0 {
		limit = 8
	}
	if limit > 20 {
		limit = 20
	}

	pool := newWatchTonightPool(limit * 3)
	read := r.newRowRead(userID, profileID, filter)

	// With recommendations disabled no personal row is served, even one
	// still cached from before, and the cold-start rows stand in.
	if !r.personalOff {
		// 1. For-you main row — highest relevance band (0.80–1.00).
		forYouItems, err := r.repo.GetRecommendationCache(ctx, userID, profileID, RecTypeForYouMain, "")
		if err != nil {
			return WatchTonightResult{}, err
		}
		pool.merge(forYouItems, 0.80, 1.00)

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
			pool.merge(items, 0.55, 0.75)
		}

		// 3. Similar-users-liked — lower band (0.35–0.55).
		similarItems, err := r.repo.GetRecommendationCache(ctx, userID, profileID, RecTypeSimilarUsersLiked, "")
		if err != nil {
			return WatchTonightResult{}, err
		}
		pool.merge(similarItems, 0.35, 0.55)
	}

	// The personal candidates are filtered before the fallback is chosen:
	// a cache holding only titles the viewer cannot be served, such as books
	// cached before they left personal rows, is as cold as an empty one.
	items, err := read.rankWatchTonight(ctx, pool)
	if err != nil {
		return WatchTonightResult{}, err
	}
	isCold := len(items) == 0

	// 4. Cold-start fallback: popular + recently-added.
	if isCold {
		pool = newWatchTonightPool(limit * 2)
		popular, err := r.repo.GetRecommendationCache(ctx, GlobalCacheUserID, GlobalCacheProfileID, RecTypePopular, "")
		if err != nil {
			return WatchTonightResult{}, err
		}
		pool.merge(popular, 0.20, 0.40)

		recentlyAdded, err := read.defaultRow(ctx, RecTypeRecentlyAdded, limit)
		if err != nil {
			return WatchTonightResult{}, err
		}
		// Its score is the time added in Unix seconds, all within a sliver
		// of the largest, so it is scored by rank to spread across its band.
		pool.merge(rankScored(recentlyAdded.Items), 0.15, 0.35)

		if items, err = read.rankWatchTonight(ctx, pool); err != nil {
			return WatchTonightResult{}, err
		}
	}

	if len(items) > limit {
		items = items[:limit]
	}
	return WatchTonightResult{Items: items, IsCold: isCold}, nil
}

// watchTonightPool holds Watch Tonight candidates in the order they were
// merged: higher bands first, each source in its own rank order. That order
// breaks score ties, so a read answers the same list every time.
type watchTonightPool struct {
	items []ScoredItem
	seen  map[string]struct{}
}

func newWatchTonightPool(capacity int) *watchTonightPool {
	return &watchTonightPool{items: make([]ScoredItem, 0, capacity), seen: make(map[string]struct{}, capacity)}
}

// rankWatchTonight sorts pool's candidates by descending score, ties in merge
// order, and filters them for the viewer, keeping every survivor; the caller
// trims.
func (rr *rowRead) rankWatchTonight(ctx context.Context, pool *watchTonightPool) ([]ScoredItem, error) {
	if len(pool.items) == 0 {
		return []ScoredItem{}, nil
	}
	items := slices.Clone(pool.items)
	sort.SliceStable(items, func(i, j int) bool {
		return items[i].Score > items[j].Score
	})
	rows, err := rr.filter(ctx, []ForYouRow{{
		Type:  "watch_tonight",
		Label: "Watch Tonight",
		Items: items,
	}})
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return []ScoredItem{}, nil
	}
	return rows[0].Items, nil
}

// rankScored copies items, best first, scored by rank: the first n of n
// items scores n, the last 1.
func rankScored(items []ScoredItem) []ScoredItem {
	out := slices.Clone(items)
	for i := range out {
		out[i].Score = float64(len(out) - i)
	}
	return out
}

// merge normalizes raw ScoredItem scores into the given band and appends
// them to the pool. First-seen (higher-band) items win on collision.
func (p *watchTonightPool) merge(items []ScoredItem, bandMin, bandMax float64) {
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
		if _, exists := p.seen[item.MediaItemID]; exists {
			continue
		}
		p.seen[item.MediaItemID] = struct{}{}
		item.Score = bandMin + (item.Score/maxRaw)*(bandMax-bandMin)
		p.items = append(p.items, item)
	}
}
