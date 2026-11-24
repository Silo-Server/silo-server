package handlers

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/sync/singleflight"

	"github.com/Silo-Server/silo-server/internal/cache"
)

// AdminInsightsProvider caches standard preset Insights reads in memory for
// 60 seconds and clears local entries on admin/playback bus events (the same
// channels the sibling admin_stats_* providers subscribe to). Title detail is
// read on demand and not cached. Misses for one key fill once per generation:
// concurrent requests share the read, and a later fill starts only after the
// earlier one published, so fills publish in order. The generation counter,
// compared and published under publishMu, keeps a fill whose DB snapshot
// predates an invalidation from repopulating the cache with stale results.
type AdminInsightsProvider struct {
	pool      *pgxpool.Pool
	activity  *cache.TTLCache[*AdminInsightsActivity]
	people    *cache.TTLCache[*AdminInsightsPeople]
	devices   *cache.TTLCache[*AdminInsightsDevices]
	bandwidth *cache.TTLCache[*AdminInsightsBandwidth]
	titles    *cache.TTLCache[*AdminInsightsTitles]
	ttl       time.Duration
	fills     singleflight.Group
	publishMu sync.Mutex
	gen       uint64 // guarded by publishMu
}

var _ AdminInsightsSource = (*AdminInsightsProvider)(nil)

// NewAdminInsightsProvider creates an event-bus-invalidated Insights cache provider.
func NewAdminInsightsProvider(ctx context.Context, pool *pgxpool.Pool, bus cache.EventBus) (*AdminInsightsProvider, error) {
	p := &AdminInsightsProvider{
		pool:      pool,
		activity:  cache.NewTTLCache[*AdminInsightsActivity](),
		people:    cache.NewTTLCache[*AdminInsightsPeople](),
		devices:   cache.NewTTLCache[*AdminInsightsDevices](),
		bandwidth: cache.NewTTLCache[*AdminInsightsBandwidth](),
		titles:    cache.NewTTLCache[*AdminInsightsTitles](),
		ttl:       adminInsightsCacheTTL,
	}
	if bus == nil || ctx == nil {
		return p, nil
	}
	handler := func(cache.Event) {
		p.Invalidate()
	}
	for _, channel := range []string{cache.ChannelAdmin, cache.ChannelPlayback} {
		if err := bus.Subscribe(ctx, channel, handler); err != nil {
			p.Close()
			return nil, fmt.Errorf("subscribing admin insights provider to %s: %w", channel, err)
		}
	}
	return p, nil
}

// Invalidate drops all cached Insights entries across every tab and advances
// the generation counter so concurrent in-flight reads do not repopulate the
// cache with pre-invalidation snapshots.
func (p *AdminInsightsProvider) Invalidate() {
	if p == nil {
		return
	}
	p.publishMu.Lock()
	defer p.publishMu.Unlock()
	p.gen++
	if p.activity != nil {
		p.activity.InvalidatePrefix(adminInsightsCacheKeyPrefix)
	}
	if p.people != nil {
		p.people.InvalidatePrefix(adminInsightsCacheKeyPrefix)
	}
	if p.devices != nil {
		p.devices.InvalidatePrefix(adminInsightsCacheKeyPrefix)
	}
	if p.bandwidth != nil {
		p.bandwidth.InvalidatePrefix(adminInsightsCacheKeyPrefix)
	}
	if p.titles != nil {
		p.titles.InvalidatePrefix(adminInsightsCacheKeyPrefix)
	}
}

func (p *AdminInsightsProvider) generation() uint64 {
	p.publishMu.Lock()
	defer p.publishMu.Unlock()
	return p.gen
}

// Close stops all background TTL sweepers.
func (p *AdminInsightsProvider) Close() {
	if p == nil {
		return
	}
	if p.activity != nil {
		p.activity.Close()
	}
	if p.people != nil {
		p.people.Close()
	}
	if p.devices != nil {
		p.devices.Close()
	}
	if p.bandwidth != nil {
		p.bandwidth.Close()
	}
	if p.titles != nil {
		p.titles.Close()
	}
}

func insightsWindowCacheable(rangeVal string) bool {
	return normalizeInsightsRange(rangeVal) != InsightsRangeCustom
}

// cachedInsightsRead serves key from c or fills it. One fill runs per key and
// generation, on a context detached from the caller so one request leaving
// does not fail the others sharing it; adminHistoryReadTimeout still bounds it. The fill is published only when no
// invalidation ran while it was in flight; the comparison and the Set share
// publishMu with Invalidate, so an invalidation cannot slip in between.
func cachedInsightsRead[T comparable](ctx context.Context, p *AdminInsightsProvider, c *cache.TTLCache[T], key string, load func(context.Context) (T, error)) (T, error) {
	if v, ok := c.Get(key); ok {
		return v, nil
	}
	gen := p.generation()
	shared, err, _ := p.fills.Do(fmt.Sprintf("%s#%d", key, gen), func() (any, error) {
		fillCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), adminHistoryReadTimeout)
		defer cancel()
		v, err := load(fillCtx)
		if err != nil {
			return nil, err
		}
		p.publishMu.Lock()
		if p.gen == gen {
			c.Set(key, v, p.ttl)
		}
		p.publishMu.Unlock()
		return insightsFill[T]{v}, nil
	})
	var zero T
	if err != nil {
		return zero, err
	}
	fill, ok := shared.(insightsFill[T])
	if !ok {
		return zero, fmt.Errorf("insights cache fill for %s returned %T", key, shared)
	}
	return fill.v, nil
}

// insightsFill carries a fill's value through singleflight's untyped result.
type insightsFill[T any] struct{ v T }

// insightsScopeKey is the cache-key fragment for the shared row filters.
func insightsScopeKey(q AdminInsightsWindowQuery) string {
	return fmt.Sprintf("r=%s:tz=%s:lib=%d:u=%d:p=%q", normalizeInsightsRange(q.Range), normalizeInsightsTZ(q.TZ), q.LibraryID, q.UserID, q.ProfileID)
}

func (p *AdminInsightsProvider) Activity(ctx context.Context, q AdminInsightsWindowQuery) (*AdminInsightsActivity, error) {
	if p == nil || p.pool == nil {
		return nil, ErrAdminDashboardUnavailable
	}
	if !insightsWindowCacheable(q.Range) {
		return queryAdminInsightsActivity(ctx, p.pool, q)
	}
	key := adminInsightsCacheKeyPrefix + "activity:" + insightsScopeKey(q)
	return cachedInsightsRead(ctx, p, p.activity, key, func(ctx context.Context) (*AdminInsightsActivity, error) {
		return queryAdminInsightsActivity(ctx, p.pool, q)
	})
}

func (p *AdminInsightsProvider) People(ctx context.Context, q AdminInsightsPeopleQuery) (*AdminInsightsPeople, error) {
	if p == nil || p.pool == nil {
		return nil, ErrAdminDashboardUnavailable
	}
	if !insightsWindowCacheable(q.Range) || strings.TrimSpace(q.Search) != "" {
		return queryAdminInsightsPeople(ctx, p.pool, q)
	}
	// Cache the full sorted snapshot (independent of limit/offset) so paging
	// past the first page reads from the same snapshot without skipping or
	// duplicating accounts across concurrent plays.
	key := fmt.Sprintf("%speople:r=%s:tz=%s:lib=%d:s=%s:d=%s", adminInsightsCacheKeyPrefix, normalizeInsightsRange(q.Range), normalizeInsightsTZ(q.TZ), q.LibraryID, strings.TrimSpace(q.Sort), strings.TrimSpace(q.Dir))
	full, err := cachedInsightsRead(ctx, p, p.people, key, func(ctx context.Context) (*AdminInsightsPeople, error) {
		return queryAdminInsightsPeopleSnapshot(ctx, p.pool, q)
	})
	if err != nil {
		return nil, err
	}
	return sliceAdminInsightsPeople(full, q.Limit, q.Offset), nil
}

func (p *AdminInsightsProvider) Devices(ctx context.Context, q AdminInsightsDevicesQuery) (*AdminInsightsDevices, error) {
	if p == nil || p.pool == nil {
		return nil, ErrAdminDashboardUnavailable
	}
	if !insightsWindowCacheable(q.Range) {
		return queryAdminInsightsDevices(ctx, p.pool, q)
	}
	key := fmt.Sprintf("%sdevices:%s:m=%d", adminInsightsCacheKeyPrefix, insightsScopeKey(q.AdminInsightsWindowQuery), clampInsightsMinSessions(q.MinSessions))
	return cachedInsightsRead(ctx, p, p.devices, key, func(ctx context.Context) (*AdminInsightsDevices, error) {
		return queryAdminInsightsDevices(ctx, p.pool, q)
	})
}

func (p *AdminInsightsProvider) Bandwidth(ctx context.Context, q AdminInsightsBandwidthQuery) (*AdminInsightsBandwidth, error) {
	if p == nil || p.pool == nil {
		return nil, ErrAdminDashboardUnavailable
	}
	if !insightsWindowCacheable(q.Range) {
		return queryAdminInsightsBandwidth(ctx, p.pool, q)
	}
	key := fmt.Sprintf("%sbandwidth:r=%s:tz=%s", adminInsightsCacheKeyPrefix, normalizeInsightsRange(q.Range), normalizeInsightsTZ(q.TZ))
	return cachedInsightsRead(ctx, p, p.bandwidth, key, func(ctx context.Context) (*AdminInsightsBandwidth, error) {
		return queryAdminInsightsBandwidth(ctx, p.pool, q)
	})
}

func (p *AdminInsightsProvider) Titles(ctx context.Context, q AdminInsightsTitlesQuery) (*AdminInsightsTitles, error) {
	if p == nil || p.pool == nil {
		return nil, ErrAdminDashboardUnavailable
	}
	if !insightsWindowCacheable(q.Range) {
		return queryAdminInsightsTitles(ctx, p.pool, q)
	}
	key := fmt.Sprintf("%stitles:%s:s=%s:n=%d", adminInsightsCacheKeyPrefix, insightsScopeKey(q.AdminInsightsWindowQuery), normalizeInsightsTitlesSort(q.Sort), clampInsightsTitlesLimit(q.Limit))
	return cachedInsightsRead(ctx, p, p.titles, key, func(ctx context.Context) (*AdminInsightsTitles, error) {
		return queryAdminInsightsTitles(ctx, p.pool, q)
	})
}
