package catalog

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/sync/singleflight"

	"github.com/Silo-Server/silo-server/internal/cache"
)

type groupsCacheEntry struct {
	groups []AudiobookGroup
	total  int
}

// audiobookGroupsFetcher fetches the complete grouped list for a query.
type audiobookGroupsFetcher func(ctx context.Context, q AudiobookGroupsQuery, filter AccessFilter) ([]AudiobookGroup, int, error)

// AudiobookGroupsCache serves paged audiobook-group browse results from a
// short-lived in-process cache of the full grouped list.
//
// The author/narrator grouping is an expensive aggregation over the whole
// library, and the client pages through the entire result on every load
// (sequential 500-row requests until the total is reached). Without a cache
// each page re-runs the full aggregation, so a cold load is N pages times the
// per-page cost; caching the full sorted list per (library, group_by, sort,
// viewer) lets one computation serve every page and survive a quick refresh.
type AudiobookGroupsCache struct {
	cache *cache.TTLCache[*groupsCacheEntry]
	ttl   time.Duration
	fetch audiobookGroupsFetcher
	group singleflight.Group
}

// NewAudiobookGroupsCache builds a cache that warms itself from the given pool.
func NewAudiobookGroupsCache(pool *pgxpool.Pool, ttl time.Duration) *AudiobookGroupsCache {
	return &AudiobookGroupsCache{
		cache: cache.NewTTLCache[*groupsCacheEntry](),
		ttl:   ttl,
		fetch: func(ctx context.Context, q AudiobookGroupsQuery, filter AccessFilter) ([]AudiobookGroup, int, error) {
			return listAllAudiobookGroups(ctx, pool, q, filter)
		},
	}
}

// Close stops the cache's background expiry sweeper.
func (c *AudiobookGroupsCache) Close() {
	if c != nil && c.cache != nil {
		c.cache.Close()
	}
}

// Page returns the offset/limit slice of the grouped list plus the full group
// count.
func (c *AudiobookGroupsCache) Page(ctx context.Context, q AudiobookGroupsQuery, filter AccessFilter) ([]AudiobookGroup, int, error) {
	key := audiobookGroupsCacheKey(q, filter)
	if entry, ok := c.cache.Get(key); ok {
		return sliceGroups(entry.groups, q.Offset, q.Limit), entry.total, nil
	}

	value, err, _ := c.group.Do(key, func() (any, error) {
		if entry, ok := c.cache.Get(key); ok {
			return entry, nil
		}
		groups, total, err := c.fetch(ctx, q, filter)
		if err != nil {
			return nil, err
		}
		entry := &groupsCacheEntry{groups: groups, total: total}
		c.cache.Set(key, entry, c.ttl)
		return entry, nil
	})
	if err != nil {
		return nil, 0, err
	}
	entry := value.(*groupsCacheEntry)
	return sliceGroups(entry.groups, q.Offset, q.Limit), entry.total, nil
}

func sliceGroups(groups []AudiobookGroup, offset, limit int) []AudiobookGroup {
	if offset < 0 {
		offset = 0
	}
	if offset >= len(groups) {
		return []AudiobookGroup{}
	}
	end := len(groups)
	if limit > 0 && offset+limit < end {
		end = offset + limit
	}
	return groups[offset:end]
}

// audiobookGroupsCacheKey identifies a cached full list. The cached value
// embeds per-profile progress counts, so the key adds UserID/ProfileID on top
// of the shared access-scope component (WriteAccessScopeCacheKey), which
// serializes every row-visibility boundary and keeps this cache in sync with
// the other access-scoped caches when AccessFilter grows.
func audiobookGroupsCacheKey(q AudiobookGroupsQuery, filter AccessFilter) string {
	sortKey := strings.ToLower(strings.TrimSpace(q.Sort))
	if sortKey == "" {
		sortKey = "name"
	}
	searchKey := strings.ToLower(strings.TrimSpace(q.SearchPrefix))
	var b strings.Builder
	fmt.Fprintf(&b, "%d|%s|%s|q=%s|u=%d|p=%s", q.LibraryID, q.GroupBy, sortKey, searchKey, filter.UserID, filter.ProfileID)
	filter.WriteAccessScopeCacheKey(&b)
	return b.String()
}
