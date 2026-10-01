package streamtelemetry

import (
	"context"
	"log/slog"
	"sync"
	"time"
)

// DefaultViewTTL bounds how stale a served global view may be before a read
// rebuilds it.
const DefaultViewTTL = 5 * time.Second

// ViewCacheStatus describes the served view's freshness. It travels with every
// read so a consumer can tell a fresh answer from a stale one instead of
// guessing.
type ViewCacheStatus struct {
	// Available is false only before the first successful build. A zero view
	// with Available false is explicitly "we do not know yet", which a consumer
	// must not read as "no sessions are live".
	Available   bool
	RefreshedAt time.Time
	Age         time.Duration
	// Stale is true when the served value is older than the TTL, which happens
	// when a refresh failed or one is in flight and a cached value was served
	// rather than making the reader wait.
	Stale bool
	// BuildTook is the cost of the last successful rebuild. This is the number
	// to watch: BuildGlobalView measured 347 ms at 50 000 sessions.
	BuildTook time.Duration
	Refreshes int64
	Failures  int64
	LastError string
}

// ViewCache serves the merged global view from a bounded-staleness cache rather
// than rebuilding it per read.
//
// BuildGlobalView is ~347 ms at the 50 000-session cap, so an admin endpoint
// that rebuilt per request would be a self-inflicted denial of service. The
// refresh is driven by reads rather than a ticker: a ticker would pay that cost
// on every server forever whether or not anyone is looking, while a read-driven
// TTL pays only when someone asks and never more than once per interval however
// many readers arrive at once.
//
// P1 needs a periodically refreshed view for its evaluator and can add a
// background ticker to this type; the read path will not have to change.
type ViewCache struct {
	registry *Registry
	ttl      time.Duration
	logger   *slog.Logger

	mu        sync.Mutex
	building  bool
	buildDone chan struct{}

	view      GlobalMonitoringView
	status    ViewCacheStatus
	refreshes int64
	failures  int64
}

// NewViewCache returns a cache over registry's global view. A non-positive ttl
// falls back to DefaultViewTTL.
func NewViewCache(registry *Registry, ttl time.Duration, logger *slog.Logger) *ViewCache {
	if ttl <= 0 {
		ttl = DefaultViewTTL
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &ViewCache{registry: registry, ttl: ttl, logger: logger}
}

// TTL reports the configured staleness bound.
func (c *ViewCache) TTL() time.Duration {
	if c == nil {
		return 0
	}
	return c.ttl
}

// View returns the merged global view and its freshness. It never panics on a
// nil cache, a nil registry, or a registry with telemetry disabled: those report
// Available false rather than an empty-but-complete view, which a consumer could
// mistake for "nothing is streaming".
func (c *ViewCache) View(ctx context.Context) (GlobalMonitoringView, ViewCacheStatus) {
	if c == nil || c.registry == nil || !c.registry.Enabled() {
		return GlobalMonitoringView{}, ViewCacheStatus{}
	}

	for {
		c.mu.Lock()
		age := now().Sub(c.status.RefreshedAt)
		if c.status.Available && age <= c.ttl {
			view, status := c.snapshotLocked(age, false)
			c.mu.Unlock()
			return view, status
		}
		if c.building {
			// A refresh is already in flight. A reader that already has a value
			// takes it rather than queueing behind a rebuild that may take
			// hundreds of milliseconds; only a reader with nothing at all waits.
			if c.status.Available {
				view, status := c.snapshotLocked(age, true)
				c.mu.Unlock()
				return view, status
			}
			wait := c.buildDone
			c.mu.Unlock()
			select {
			case <-wait:
				continue
			case <-ctx.Done():
				return GlobalMonitoringView{}, ViewCacheStatus{}
			}
		}
		c.building = true
		c.buildDone = make(chan struct{})
		done := c.buildDone
		c.mu.Unlock()

		view, err := c.build(ctx)

		c.mu.Lock()
		if err != nil {
			// Keep the last good view. Going blind is worse than being visibly
			// stale, and Stale plus LastError says exactly which one this is.
			c.failures++
			c.status.Failures = c.failures
			c.status.LastError = err.Error()
		} else {
			c.refreshes++
			c.view = view.clone()
			c.status.Available = true
			c.status.RefreshedAt = now()
			c.status.Refreshes = c.refreshes
			c.status.LastError = ""
		}
		c.building = false
		close(done)
		served, status := c.snapshotLocked(now().Sub(c.status.RefreshedAt), err != nil)
		c.mu.Unlock()
		return served, status
	}
}

func (c *ViewCache) build(ctx context.Context) (GlobalMonitoringView, error) {
	start := now()
	view, err := c.registry.GlobalView(ctx)
	if err != nil {
		return GlobalMonitoringView{}, err
	}
	c.mu.Lock()
	c.status.BuildTook = now().Sub(start)
	c.mu.Unlock()
	return view, nil
}

// snapshotLocked must be called with c.mu held.
func (c *ViewCache) snapshotLocked(age time.Duration, forceStale bool) (GlobalMonitoringView, ViewCacheStatus) {
	status := c.status
	status.Age = age
	status.Stale = forceStale || age > c.ttl
	if !status.Available {
		status.Age = 0
		return GlobalMonitoringView{}, status
	}
	return c.view.clone(), status
}

// clone deep-copies the slices a caller could otherwise mutate through the
// cached value. The maps and slices inside each session are shared with the
// cached copy, so consumers must treat the result as read-only — which every
// consumer of a monitoring view already does.
func (v GlobalMonitoringView) clone() GlobalMonitoringView {
	out := v
	out.IncompleteReasons = append([]string(nil), v.IncompleteReasons...)
	out.Publishers = append([]PublisherStatus(nil), v.Publishers...)
	out.MissingPublishers = append([]PublisherRef(nil), v.MissingPublishers...)
	out.Sessions = append([]GlobalSessionView(nil), v.Sessions...)
	out.Transfers = append([]GlobalTransferView(nil), v.Transfers...)
	return out
}
