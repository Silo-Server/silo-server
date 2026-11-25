package handlers

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/cache"
)

// AdminInsightsProvider caches standard preset Insights reads in memory for
// 60 seconds and clears local entries on admin/playback bus events (the same
// channels the sibling admin_stats_* providers subscribe to). Title detail is
// read on demand and not cached. Misses for one key fill once per generation:
// concurrent requests share the read, and the read is canceled once every
// request waiting on it has left. The generation counter,
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
	fillMu    sync.Mutex
	fills     map[string]*insightsFillCall // guarded by fillMu
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

// insightsFillCall is one shared cache fill and the requests waiting on it.
type insightsFillCall struct {
	done    chan struct{} // closed once val and err are set
	val     any
	err     error
	waiters int // guarded by AdminInsightsProvider.fillMu
	cancel  context.CancelFunc
}

// cachedInsightsRead serves key from c or fills it. One fill runs per key and
// generation, shared by every request that misses while it runs. It runs on a
// context detached from the caller, so one request leaving does not fail the
// others, but it is canceled when the last request waiting on it leaves: a
// fill nobody waits for gives up its read-gate slot or its queued place
// instead of running out adminHistoryReadTimeout. The fill is published only
// when no invalidation ran while it was in flight; the comparison and the Set
// share publishMu with Invalidate, so an invalidation cannot slip in between.
func cachedInsightsRead[T comparable](ctx context.Context, p *AdminInsightsProvider, c *cache.TTLCache[T], key string, load func(context.Context) (T, error)) (T, error) {
	var zero T
	if v, ok := c.Get(key); ok {
		return v, nil
	}
	gen := p.generation()
	fillKey := fmt.Sprintf("%s#%d", key, gen)

	p.fillMu.Lock()
	call := p.fills[fillKey]
	if call == nil {
		fillCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), adminHistoryReadTimeout)
		call = &insightsFillCall{done: make(chan struct{}), cancel: cancel}
		if p.fills == nil {
			p.fills = make(map[string]*insightsFillCall)
		}
		p.fills[fillKey] = call
		go p.runInsightsFill(fillCtx, call, fillKey, func(ctx context.Context) (any, error) {
			v, err := load(ctx)
			if err != nil {
				return nil, err
			}
			p.publishMu.Lock()
			// A fill canceled after its last waiter left publishes nothing,
			// even when its read finished in that moment.
			if p.gen == gen && ctx.Err() == nil {
				c.Set(key, v, p.ttl)
			}
			p.publishMu.Unlock()
			return insightsFill[T]{v}, nil
		})
	}
	call.waiters++
	p.fillMu.Unlock()

	select {
	case <-call.done:
	case <-ctx.Done():
		p.leaveInsightsFill(call, fillKey)
		return zero, ctx.Err()
	}
	if call.err != nil {
		return zero, call.err
	}
	filled, ok := call.val.(insightsFill[T])
	if !ok {
		return zero, fmt.Errorf("insights cache fill for %s returned %T", key, call.val)
	}
	return filled.v, nil
}

// insightsFill carries a fill's value through insightsFillCall's untyped
// result. Asserting to this struct, not to T itself, keeps the route
// inventory check from reading the assertion as a possible router recovery.
type insightsFill[T any] struct{ v T }

// runInsightsFill runs one shared fill, records its result, and retires it.
func (p *AdminInsightsProvider) runInsightsFill(ctx context.Context, call *insightsFillCall, fillKey string, fill func(context.Context) (any, error)) {
	defer call.cancel()
	call.val, call.err = fill(ctx)
	p.fillMu.Lock()
	if p.fills[fillKey] == call {
		delete(p.fills, fillKey)
	}
	p.fillMu.Unlock()
	close(call.done)
}

// leaveInsightsFill drops one waiter from call and cancels the fill when it
// was the last. The fill also leaves the map then, so a later request starts a
// fresh fill instead of joining one that is shutting down.
func (p *AdminInsightsProvider) leaveInsightsFill(call *insightsFillCall, fillKey string) {
	p.fillMu.Lock()
	defer p.fillMu.Unlock()
	call.waiters--
	if call.waiters > 0 {
		return
	}
	call.cancel()
	if p.fills[fillKey] == call {
		delete(p.fills, fillKey)
	}
}

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
