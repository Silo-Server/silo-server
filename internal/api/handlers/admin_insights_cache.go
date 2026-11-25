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

// AdminInsightsProvider serves preset Insights reads from memory and keeps them
// current in the background, so a tab opens at once however busy the server is.
//
// Each read is cached under its key (tab, window, scope). A playback event
// marks every entry stale by advancing the generation and drops nothing:
// dropping meant that on a server with steady playback nearly every tab load
// ran its full scan. A read then:
//
//   - returns a fresh entry: filled in the current generation and younger than
//     adminInsightsCacheTTL;
//   - returns a stale entry younger than adminInsightsServeStaleFor at once and
//     starts a background refresh of its key, at most every
//     insightsRefreshInterval of the entry's read cost;
//   - fills a missing or older entry while the request waits. Concurrent
//     requests in one generation share that fill, which is canceled once every
//     request waiting on it has left.
//
// Invalidate, which admin events (account, item and node changes) call, drops
// every entry instead, and a read that started before it publishes nothing,
// so the change shows on the next read. A refresh=true read (the Refresh
// button) marks every entry stale and waits for fresh data for its own key.
//
// A sweep refreshes keys an event marked stale and read within
// adminInsightsKeepWarmFor, every insightsKeepWarmInterval of their cost, and
// drops keys nobody read within adminInsightsServeStaleFor. Background
// refreshes run one at a time, so they hold at most one read-gate slot. Title
// detail and custom ranges are read on demand and not cached.
type AdminInsightsProvider struct {
	pool *pgxpool.Pool

	mu         sync.Mutex
	gen        uint64                       // guarded by mu
	dropGen    uint64                       // guarded by mu; advanced by Invalidate
	entries    map[string]*insightsEntry    // guarded by mu
	fills      map[string]*insightsFillCall // guarded by mu; key and generation
	refreshing map[string]bool              // guarded by mu; keys with a background refresh

	refreshSlot   chan struct{}      // one background refresh at a time
	refreshCancel context.CancelFunc // guarded by mu; cancels the refresh reading now
	// refreshContext gives a background refresh its context, canceled by
	// Close; nil until NewAdminInsightsProvider starts the provider.
	refreshContext func() (context.Context, context.CancelFunc)
	done           <-chan struct{} // closed by Close
	stop           context.CancelFunc
	stopOnce       sync.Once
}

const (
	// adminInsightsServeStaleFor bounds how old a cached read can be and
	// still be served at once, and how long an unread key is kept: a tab
	// someone is browsing stays warm, and one opened again after a longer
	// break, or polled less often, waits for fresh data as before.
	adminInsightsServeStaleFor = 10 * time.Minute
	// adminInsightsKeepWarmFor is how long after its last read the sweep
	// keeps refreshing a key, so background reads stop soon after the
	// Insights page is closed.
	adminInsightsKeepWarmFor = 5 * time.Minute
	// adminInsightsMaxEntries caps the cached reads one server holds. Keys
	// multiply across ranges, timezones, filters and parameters, and an
	// admin:monitor API key can ask for any of them; the least recently read
	// entry makes room for a new one.
	adminInsightsMaxEntries = 128
	// adminInsightsMaxQueuedRefreshes caps the background refreshes started
	// or waiting for the single slot. A waiting one keeps its entry alive
	// even after eviction, so the cap also bounds what the queue holds.
	adminInsightsMaxQueuedRefreshes = 8
	// adminInsightsSweepInterval is how often the warm keys are checked.
	adminInsightsSweepInterval = 15 * time.Second
)

// insightsRefreshInterval is how old a stale entry must be before a read
// refreshes it in the background: a cheap key refreshes often, and no key
// spends more than a quarter of its time reading however often events arrive.
func insightsRefreshInterval(cost time.Duration) time.Duration {
	return max(15*time.Second, 4*cost)
}

// insightsKeepWarmInterval is the same for the sweep, which refreshes keys
// nobody is reading right now and so can lag further behind.
func insightsKeepWarmInterval(cost time.Duration) time.Duration {
	return max(2*time.Minute, 10*cost)
}

// insightsEntry is one cached read.
type insightsEntry struct {
	val      any
	gen      uint64        // generation its read started in
	filledAt time.Time     // when its read started
	cost     time.Duration // how long its read took, or a failed refresh if longer
	lastRead time.Time     // last request served from it
	triedAt  time.Time     // when its last background refresh started
	load     func(context.Context) (any, error)
}

// freshLocked reports whether e needs no refresh: read in the current
// generation and younger than adminInsightsCacheTTL.
func (p *AdminInsightsProvider) freshLocked(e *insightsEntry, now time.Time) bool {
	return e.gen == p.gen && now.Sub(e.filledAt) < adminInsightsCacheTTL
}

// refreshDue reports whether a background refresh of e may start now: the
// interval runs from its last read or refresh attempt, whichever is later, so
// a refresh that keeps failing is retried no faster than one that succeeds.
func (e *insightsEntry) refreshDue(now time.Time, interval func(time.Duration) time.Duration) bool {
	from := e.filledAt
	if e.triedAt.After(from) {
		from = e.triedAt
	}
	return now.Sub(from) >= interval(e.cost)
}

var _ AdminInsightsSource = (*AdminInsightsProvider)(nil)

// NewAdminInsightsProvider creates an event-bus-invalidated Insights cache
// provider. Its sweep runs until ctx ends or Close.
func NewAdminInsightsProvider(ctx context.Context, pool *pgxpool.Pool, bus cache.EventBus) (*AdminInsightsProvider, error) {
	p := &AdminInsightsProvider{pool: pool}
	if ctx == nil {
		return p, nil
	}
	life, stop := context.WithCancel(context.WithoutCancel(ctx))
	p.stop, p.done = stop, life.Done()
	p.refreshSlot = make(chan struct{}, 1)
	p.refreshContext = func() (context.Context, context.CancelFunc) {
		return context.WithTimeout(life, adminHistoryReadTimeout)
	}
	go func() {
		select {
		case <-ctx.Done():
			p.Close()
		case <-p.done:
		}
	}()
	go p.sweepLoop()
	if bus == nil {
		return p, nil
	}
	for _, sub := range []struct {
		channel string
		handler cache.EventHandler
	}{
		{channel: cache.ChannelAdmin, handler: p.handleAdminEvent},
		{channel: cache.ChannelPlayback, handler: p.handlePlaybackEvent},
	} {
		if err := bus.Subscribe(ctx, sub.channel, sub.handler); err != nil {
			p.Close()
			return nil, fmt.Errorf("subscribing admin insights provider to %s: %w", sub.channel, err)
		}
	}
	return p, nil
}

func (p *AdminInsightsProvider) handleAdminEvent(event cache.Event) {
	if shouldInvalidateInsightsOnAdminEvent(event) {
		p.Invalidate()
	}
}

func (p *AdminInsightsProvider) handlePlaybackEvent(event cache.Event) {
	if shouldInvalidateInsightsOnPlaybackEvent(event) {
		p.markStale()
	}
}

// shouldInvalidateInsightsOnAdminEvent reports whether a cache.ChannelAdmin
// event can change data shown by the Insights tabs. Unrelated admin chatter
// (node reachability/drain, settings, policies, plugins, auth/marker config)
// is ignored so it does not wipe cached tabs or discard in-flight fills.
func shouldInvalidateInsightsOnAdminEvent(event cache.Event) bool {
	switch event.Type {
	case cache.EventAdminStatsInvalidated,
		cache.EventUserDisabled,
		cache.EventUserDeleted,
		cache.EventNodePoolChanged,
		cache.EventItemUpdated,
		// Sign-in revocation follows account changes the People tab shows,
		// such as a provider role change or a username edit with a new
		// password; those paths publish no stats event of their own.
		cache.EventUserSessionsRevoked:
		return true
	default:
		return false
	}
}

// shouldInvalidateInsightsOnPlaybackEvent reports whether a
// cache.ChannelPlayback event can change data shown by the Insights tabs.
// ChannelPlayback also carries realtime coordination traffic (Watch Together
// room state and suggestions, subtitle ready/timing/sync, marker updates and
// thumbnails) that does not alter playback history or active session stats.
func shouldInvalidateInsightsOnPlaybackEvent(event cache.Event) bool {
	return event.Type == cache.EventPlaybackSessionsChanged
}

// Invalidate drops every cached Insights entry and keeps reads that started
// before it from publishing, for an admin event: a change a stale entry must
// not show.
func (p *AdminInsightsProvider) Invalidate() {
	if p == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.gen++
	p.dropGen++
	clear(p.entries)
	// A refresh reading now would publish nothing; stop it so it frees its
	// read slot for the reads that follow.
	if p.refreshCancel != nil {
		p.refreshCancel()
	}
}

// Refresh handles a user's refresh=true read: it marks every cached entry
// stale, so each refreshes when next read, and returns a context whose reads
// wait for fresh data. Nothing is dropped, so a Refresh on one page doesn't
// cost every other tab its cached read.
func (p *AdminInsightsProvider) Refresh(ctx context.Context) context.Context {
	if p != nil {
		p.markStale()
	}
	return withInsightsForceRefresh(ctx)
}

// markStale marks every cached entry stale. Reads still serve them while a
// background refresh replaces them, and a fill already in flight still
// publishes, since its result is newer than the entry it replaces.
func (p *AdminInsightsProvider) markStale() {
	p.mu.Lock()
	p.gen++
	p.mu.Unlock()
}

func (p *AdminInsightsProvider) closed() bool {
	select {
	case <-p.done:
		return true
	default:
		return false
	}
}

// Close stops the sweep and cancels background refreshes.
func (p *AdminInsightsProvider) Close() {
	if p == nil {
		return
	}
	p.stopOnce.Do(func() {
		if p.stop != nil {
			p.stop()
		}
	})
}

func insightsWindowCacheable(rangeVal string) bool {
	return normalizeInsightsRange(rangeVal) != InsightsRangeCustom
}

type insightsForceRefreshKey struct{}

// withInsightsForceRefresh marks ctx's reads as user-requested refreshes: they
// skip the cached entry and wait for a fresh read.
func withInsightsForceRefresh(ctx context.Context) context.Context {
	return context.WithValue(ctx, insightsForceRefreshKey{}, true)
}

func insightsForceRefresh(ctx context.Context) bool {
	force, _ := ctx.Value(insightsForceRefreshKey{}).(bool)
	return force
}

// insightsFillCall is one shared fill and the requests waiting on it.
type insightsFillCall struct {
	done    chan struct{} // closed once val and err are set
	val     any
	err     error
	waiters int // guarded by AdminInsightsProvider.mu
	cancel  context.CancelFunc
}

// insightsFill carries a fill's value through the cache's untyped entries.
// Asserting to this struct, not to T itself, keeps the route inventory check
// from reading the assertion as a possible router recovery.
type insightsFill[T any] struct{ v T }

// cachedInsightsRead serves key from the cache, or fills it while the request
// waits (see AdminInsightsProvider).
func cachedInsightsRead[T any](ctx context.Context, p *AdminInsightsProvider, key string, load func(context.Context) (T, error)) (T, error) {
	var zero T
	v, err := p.read(ctx, key, func(ctx context.Context) (any, error) {
		v, err := load(ctx)
		if err != nil {
			return nil, err
		}
		return insightsFill[T]{v}, nil
	})
	if err != nil {
		return zero, err
	}
	filled, ok := v.(insightsFill[T])
	if !ok {
		return zero, fmt.Errorf("insights cache entry for %s holds %T", key, v)
	}
	return filled.v, nil
}

func (p *AdminInsightsProvider) read(ctx context.Context, key string, load func(context.Context) (any, error)) (any, error) {
	p.mu.Lock()
	fillKey := fmt.Sprintf("%s#%d", key, p.dropGen)
	if insightsForceRefresh(ctx) {
		// A forced read joins only fills that started after the Refresh
		// behind it, which advanced the generation; it still counts as a read.
		fillKey = fmt.Sprintf("%s#%d#%d", key, p.dropGen, p.gen)
		if e := p.entries[key]; e != nil {
			e.lastRead = time.Now()
		}
	} else if val, ok := p.cachedLocked(key); ok {
		p.mu.Unlock()
		return val, nil
	}
	// Otherwise one fill per key until Invalidate: a read after it never
	// joins a fill that started before it, while playback events, which only
	// mark entries stale, leave concurrent misses sharing one read.
	call := p.fills[fillKey]
	if call == nil {
		fillCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), adminHistoryReadTimeout)
		call = &insightsFillCall{done: make(chan struct{}), cancel: cancel}
		if p.fills == nil {
			p.fills = make(map[string]*insightsFillCall)
		}
		p.fills[fillKey] = call
		go p.runFill(fillCtx, call, fillKey, key, p.gen, p.dropGen, load)
	}
	call.waiters++
	p.mu.Unlock()

	select {
	case <-call.done:
		return call.val, call.err
	case <-ctx.Done():
		p.leaveFill(call, fillKey)
		return nil, ctx.Err()
	}
}

// cachedLocked serves key from its entry when it is fresh, or stale but
// younger than adminInsightsServeStaleFor, starting a background refresh of a
// stale one at most every insightsRefreshInterval of its cost.
func (p *AdminInsightsProvider) cachedLocked(key string) (any, bool) {
	e := p.entries[key]
	if e == nil {
		return nil, false
	}
	now := time.Now()
	e.lastRead = now
	if p.freshLocked(e, now) {
		return e.val, true
	}
	if now.Sub(e.filledAt) >= adminInsightsServeStaleFor {
		return nil, false
	}
	if e.refreshDue(now, insightsRefreshInterval) {
		p.startRefreshLocked(key, e, false)
	}
	return e.val, true
}

// runFill runs one shared foreground fill, publishes it unless every waiter
// left first, and retires it.
func (p *AdminInsightsProvider) runFill(ctx context.Context, call *insightsFillCall, fillKey, key string, gen, dropGen uint64, load func(context.Context) (any, error)) {
	defer call.cancel()
	start := time.Now()
	call.val, call.err = load(ctx)
	p.mu.Lock()
	// A fill canceled after its last waiter left publishes nothing, even when
	// its read finished in that moment.
	if call.err == nil && ctx.Err() == nil {
		p.publishLocked(key, gen, dropGen, start, load, call.val)
	}
	if p.fills[fillKey] == call {
		delete(p.fills, fillKey)
	}
	p.mu.Unlock()
	close(call.done)
}

// leaveFill drops one waiter from call and cancels the fill when it was the
// last. The fill also leaves the map then, so a later request starts a fresh
// fill instead of joining one that is shutting down.
func (p *AdminInsightsProvider) leaveFill(call *insightsFillCall, fillKey string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	call.waiters--
	if call.waiters > 0 {
		return
	}
	call.cancel()
	if p.fills[fillKey] == call {
		delete(p.fills, fillKey)
	}
}

// publishLocked stores a read that started at start in generation gen, unless
// an admin change dropped the cache since (dropGen moved) or the entry already
// holds a read that started later.
func (p *AdminInsightsProvider) publishLocked(key string, gen, dropGen uint64, start time.Time, load func(context.Context) (any, error), val any) {
	if dropGen != p.dropGen {
		return
	}
	if p.entries == nil {
		p.entries = make(map[string]*insightsEntry)
	}
	e := p.entries[key]
	if e != nil && e.filledAt.After(start) {
		return
	}
	if e == nil && len(p.entries) >= adminInsightsMaxEntries {
		p.evictLeastReadLocked()
	}
	// A refresh is no read: keeping lastRead lets an unread key expire.
	lastRead := time.Now()
	if e != nil {
		lastRead = e.lastRead
	}
	p.entries[key] = &insightsEntry{val: val, gen: gen, filledAt: start, cost: time.Since(start), lastRead: lastRead, load: load}
}

// evictLeastReadLocked drops the entry read longest ago.
func (p *AdminInsightsProvider) evictLeastReadLocked() {
	var oldestKey string
	var oldest time.Time
	for key, e := range p.entries {
		if oldestKey == "" || e.lastRead.Before(oldest) {
			oldestKey, oldest = key, e.lastRead
		}
	}
	delete(p.entries, oldestKey)
}

// startRefreshLocked starts a background refresh of key's entry e unless one is
// running; warm marks one the sweep started for a key nobody is reading. It
// runs only while the provider is open (NewAdminInsightsProvider).
func (p *AdminInsightsProvider) startRefreshLocked(key string, e *insightsEntry, warm bool) {
	if p.refreshContext == nil || p.closed() || p.refreshing[key] ||
		len(p.refreshing) >= adminInsightsMaxQueuedRefreshes {
		return
	}
	if p.refreshing == nil {
		p.refreshing = make(map[string]bool)
	}
	p.refreshing[key] = true
	go p.refresh(key, e, warm)
}

// refreshStillNeededLocked reports whether a refresh of e, which waited for
// its slot, should still read: e is still key's entry (not dropped, evicted or
// replaced), still not fresh, and, for the sweep, still in its warm window.
func (p *AdminInsightsProvider) refreshStillNeededLocked(key string, e *insightsEntry, warm bool, now time.Time) bool {
	if p.entries[key] != e || p.freshLocked(e, now) {
		return false
	}
	return !warm || now.Sub(e.lastRead) < adminInsightsKeepWarmFor
}

func (p *AdminInsightsProvider) refresh(key string, e *insightsEntry, warm bool) {
	defer func() {
		p.mu.Lock()
		delete(p.refreshing, key)
		p.mu.Unlock()
	}()
	select {
	case p.refreshSlot <- struct{}{}:
		defer func() { <-p.refreshSlot }()
	case <-p.done:
		return
	}
	ctx, cancel := p.refreshContext()
	defer cancel()
	p.mu.Lock()
	start := time.Now()
	if !p.refreshStillNeededLocked(key, e, warm, start) {
		p.mu.Unlock()
		return
	}
	// The attempt counts from when it reads, not from when it queued, so a
	// long wait for the slot doesn't shorten the next retry's spacing.
	e.triedAt = start
	gen, dropGen := p.gen, p.dropGen
	p.refreshCancel = cancel
	p.mu.Unlock()
	val, err := e.load(ctx)
	p.mu.Lock()
	defer p.mu.Unlock()
	p.refreshCancel = nil
	if err != nil {
		// The stale entry stays. Counting the failed attempt's time in its
		// cost spaces the retries out further when the read is slow to fail.
		if p.entries[key] == e {
			e.cost = max(e.cost, time.Since(start))
		}
		return
	}
	// A refresh never brings back a key the sweep or an admin change
	// dropped while it ran.
	if p.entries[key] != nil {
		p.publishLocked(key, gen, dropGen, start, e.load, val)
	}
}

// sweepLoop keeps recently read keys warm and drops keys nobody reads.
func (p *AdminInsightsProvider) sweepLoop() {
	ticker := time.NewTicker(adminInsightsSweepInterval)
	defer ticker.Stop()
	for {
		select {
		case <-p.done:
			return
		case <-ticker.C:
			p.sweep()
		}
	}
}

func (p *AdminInsightsProvider) sweep() {
	now := time.Now()
	p.mu.Lock()
	defer p.mu.Unlock()
	for key, e := range p.entries {
		if now.Sub(e.lastRead) >= adminInsightsServeStaleFor {
			delete(p.entries, key)
			continue
		}
		// Only an event can have changed what a key shows; one that is
		// merely older than adminInsightsCacheTTL refreshes when read.
		changed := e.gen != p.gen
		recent := now.Sub(e.lastRead) < adminInsightsKeepWarmFor
		if changed && recent && e.refreshDue(now, insightsKeepWarmInterval) {
			p.startRefreshLocked(key, e, true)
		}
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
	return cachedInsightsRead(ctx, p, key, func(ctx context.Context) (*AdminInsightsActivity, error) {
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
	full, err := cachedInsightsRead(ctx, p, key, func(ctx context.Context) (*AdminInsightsPeople, error) {
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
	return cachedInsightsRead(ctx, p, key, func(ctx context.Context) (*AdminInsightsDevices, error) {
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
	return cachedInsightsRead(ctx, p, key, func(ctx context.Context) (*AdminInsightsBandwidth, error) {
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
	return cachedInsightsRead(ctx, p, key, func(ctx context.Context) (*AdminInsightsTitles, error) {
		return queryAdminInsightsTitles(ctx, p.pool, q)
	})
}

func (p *AdminInsightsProvider) Library(ctx context.Context, q AdminInsightsWindowQuery) (*AdminInsightsLibrary, error) {
	if p == nil || p.pool == nil {
		return nil, ErrAdminDashboardUnavailable
	}
	if !insightsWindowCacheable(q.Range) {
		return queryAdminInsightsLibrary(ctx, p.pool, q)
	}
	key := adminInsightsCacheKeyPrefix + "library:" + insightsScopeKey(q)
	return cachedInsightsRead(ctx, p, key, func(ctx context.Context) (*AdminInsightsLibrary, error) {
		return queryAdminInsightsLibrary(ctx, p.pool, q)
	})
}
