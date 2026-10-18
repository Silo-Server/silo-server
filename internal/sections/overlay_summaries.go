package sections

import (
	"context"
	"sync"
	"time"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/models"
)

// The overlay summary cache keeps each card's badges for overlaySummaryTTL.
// Ranking a series' files costs a pass over every episode file, so a page of
// long-running shows took about half a second on every Home load; the badges
// only change when a scan adds or replaces a file.
//
// Entries are shared by every viewer: a summary is computed over all available
// files, not the viewer's library access or playback-quality ceiling. A viewer
// limited to 1080p can see a 4K badge, which is acceptable for a badge; what
// they can play is still decided by the playback path. A scan's new best file
// shows its badge once the entry expires.
//
// The cache is process-global, like the resolved-list cache, so the Fetchers
// the process builds share it. Each node warms its own copy.
const (
	overlaySummaryTTL           = 10 * time.Minute
	overlaySummaryPruneInterval = time.Minute
)

type overlaySummaryEntry struct {
	summary   *models.OverlaySummary // nil: the card has no badge-bearing file
	expiresAt time.Time
}

var (
	overlaySummaryCacheMu   sync.Mutex
	overlaySummaryCache     = make(map[string]overlaySummaryEntry)
	overlaySummaryLastPrune time.Time
	overlaySummaryNow       = time.Now
)

// ListOverlaySummaries returns each card's badges, reading only the cards the
// cache does not hold. The access filter is ignored; see the cache comment.
func (f *Fetcher) ListOverlaySummaries(ctx context.Context, contentIDs []string, _ catalog.AccessFilter) (map[string]*models.OverlaySummary, error) {
	result := make(map[string]*models.OverlaySummary, len(contentIDs))
	if f.pool == nil || len(contentIDs) == 0 {
		return result, nil
	}
	now := overlaySummaryNow()
	var missing []string
	overlaySummaryCacheMu.Lock()
	for _, id := range contentIDs {
		if _, done := result[id]; done {
			continue
		}
		entry, ok := overlaySummaryCache[id]
		if !ok || !now.Before(entry.expiresAt) {
			missing = append(missing, id)
			// Holds the place so a repeated ID is not requested twice.
			result[id] = nil
			continue
		}
		result[id] = copyOverlaySummary(entry.summary)
	}
	overlaySummaryCacheMu.Unlock()

	if len(missing) > 0 {
		fetched, err := catalog.ListOverlaySummaries(ctx, f.pool, missing, catalog.AccessFilter{})
		if err != nil {
			return nil, err
		}
		expiresAt := now.Add(overlaySummaryTTL)
		overlaySummaryCacheMu.Lock()
		for _, id := range missing {
			overlaySummaryCache[id] = overlaySummaryEntry{summary: fetched[id], expiresAt: expiresAt}
			result[id] = copyOverlaySummary(fetched[id])
		}
		pruneOverlaySummariesLocked(now)
		overlaySummaryCacheMu.Unlock()
	}

	// Callers expect only cards that have badges.
	for id, summary := range result {
		if summary == nil {
			delete(result, id)
		}
	}
	return result, nil
}

// copyOverlaySummary keeps callers from changing a shared cache entry.
func copyOverlaySummary(summary *models.OverlaySummary) *models.OverlaySummary {
	if summary == nil {
		return nil
	}
	copied := *summary
	return &copied
}

// pruneOverlaySummariesLocked drops expired entries at most once per
// overlaySummaryPruneInterval, so cards that are never shown again do not
// stay in memory. The caller holds overlaySummaryCacheMu.
func pruneOverlaySummariesLocked(now time.Time) {
	if now.Sub(overlaySummaryLastPrune) < overlaySummaryPruneInterval {
		return
	}
	overlaySummaryLastPrune = now
	for id, entry := range overlaySummaryCache {
		if !now.Before(entry.expiresAt) {
			delete(overlaySummaryCache, id)
		}
	}
}

func resetOverlaySummaryCacheForTest() {
	overlaySummaryCacheMu.Lock()
	defer overlaySummaryCacheMu.Unlock()
	overlaySummaryCache = make(map[string]overlaySummaryEntry)
	overlaySummaryLastPrune = time.Time{}
	overlaySummaryNow = time.Now
}
