package streamtelemetry

import (
	"context"
	"fmt"
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
	egress    EgressView // view's SessionEgress projection, built with it
	status    ViewCacheStatus
	refreshes int64
	failures  int64
	failedAt  time.Time
}

// viewBuildTimeout bounds one rebuild. The rebuild belongs to the cache, not
// to the reader that started it: a reader that stops waiting, or disconnects,
// leaves it running for the readers after it.
const viewBuildTimeout = 10 * time.Second

// egressReadTimeout bounds how long one SessionEgress read waits for a
// rebuild. The admin sessions list waits on it, and a list without egress
// beats a list that hangs on a stuck Redis.
const egressReadTimeout = 2 * time.Second

// SessionEgress is one playback session's viewer egress as the merged view
// counted it: viewer-egress bytes summed across every publisher, internal
// relay hops excluded.
type SessionEgress struct {
	Bytes      int64
	MeasuredAt time.Time
	Series     string
	// Degraded is set when the count can't be used: a route cap dropped some
	// of the session's routes (BytesDegraded), so Bytes may be low, or a
	// counting publisher's clock runs ahead (ViewerBytesClockAhead), so
	// MeasuredAt can't be compared with later observations.
	Degraded bool
}

// EgressView is the per-session egress projection of one merged view. The
// map is shared by every reader of the same build and must not be modified.
type EgressView struct {
	// Distributed is false when the view holds only this process's publisher,
	// so bytes a proxy node sent a viewer are not in it.
	Distributed bool
	// Sessions is keyed by playback session ID and holds only sessions with
	// at least one viewer-edge publisher.
	Sessions map[string]SessionEgress
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

// View returns the merged global view and its freshness. It never panics on a
// nil cache, a nil registry, or a registry with telemetry disabled: those report
// Available false rather than an empty-but-complete view, which a consumer could
// mistake for "nothing is streaming".
func (c *ViewCache) View(ctx context.Context) (GlobalMonitoringView, ViewCacheStatus) {
	var view GlobalMonitoringView
	status := c.read(ctx, false, func() { view = c.view.clone() })
	return view, status
}

// SessionEgress returns the per-session viewer egress of the cached view and
// the view's status, under the same TTL and single flight as View. It differs
// from View in three ways, all for a request path that polls: it copies no
// sessions (the projection is built once per refresh and shared), it waits at
// most egressReadTimeout for a rebuild, and after a failed refresh it serves
// the last view for one TTL instead of retrying Redis on every request.
// Status.LastError is non-empty while the last refresh attempt failed.
func (c *ViewCache) SessionEgress(ctx context.Context) (EgressView, ViewCacheStatus) {
	ctx, cancel := context.WithTimeout(ctx, egressReadTimeout)
	defer cancel()
	var out EgressView
	status := c.read(ctx, true, func() { out = c.egress })
	return out, status
}

// Egress projects the view onto per-session viewer egress. SessionEgress
// serves it precomputed; call it directly only on a view you built.
// Distributed is left false: the view does not know how it was gathered.
func (v GlobalMonitoringView) Egress() EgressView {
	out := EgressView{Sessions: make(map[string]SessionEgress, len(v.Sessions))}
	for _, session := range v.Sessions {
		if len(session.ViewerEdgePublishers) == 0 {
			continue
		}
		out.Sessions[session.SessionID] = SessionEgress{Bytes: session.ViewerBytesAccepted, MeasuredAt: session.ViewerBytesMeasuredAt,
			Series: session.ViewerBytesSeries, Degraded: session.BytesDegraded || session.ViewerBytesClockAhead}
	}
	return out
}

// read serves the cached view, starting a rebuild once it is older than the
// TTL, and returns its status. project runs with c.mu held, only when a view
// is available, so each caller copies just what it needs. With
// failureCooldown, a read inside one TTL of a failed refresh serves the last
// view, stale, rather than starting another.
//
// The reader that starts a rebuild, and any reader with no view at all, waits
// for it up to its own ctx; a reader that gives up gets the older view, stale,
// or an unavailable status when there is none, and the rebuild carries on.
// Other readers take the cached view, stale, rather than queueing behind a
// rebuild that may take hundreds of milliseconds.
func (c *ViewCache) read(ctx context.Context, failureCooldown bool, project func()) ViewCacheStatus {
	if c == nil || c.registry == nil || !c.registry.Enabled() {
		return ViewCacheStatus{}
	}

	c.mu.Lock()
	age := now().Sub(c.status.RefreshedAt)
	if c.status.Available && age <= c.ttl {
		defer c.mu.Unlock()
		return c.snapshotLocked(age, false, project)
	}
	if failureCooldown && c.status.LastError != "" && now().Sub(c.failedAt) < c.ttl {
		defer c.mu.Unlock()
		return c.snapshotLocked(age, true, project)
	}
	if !c.building {
		c.startBuildLocked(ctx)
	} else if c.status.Available {
		defer c.mu.Unlock()
		return c.snapshotLocked(age, true, project)
	}
	wait := c.buildDone
	c.mu.Unlock()

	// Like a reader that found the rebuild already running, one that gives up
	// on it takes the older view, stale, when there is one.
	gaveUp := false
	select {
	case <-wait:
	case <-ctx.Done():
		gaveUp = true
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if gaveUp && !c.status.Available {
		return ViewCacheStatus{}
	}
	return c.snapshotLocked(now().Sub(c.status.RefreshedAt), gaveUp || c.status.LastError != "", project)
}

// startBuildLocked must be called with c.mu held and c.building false. The
// rebuild runs on a context detached from ctx's cancellation, so one
// reader's deadline or disconnect is never recorded as a cache failure.
func (c *ViewCache) startBuildLocked(ctx context.Context) {
	c.building = true
	done := make(chan struct{})
	c.buildDone = done
	buildCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), viewBuildTimeout)
	go func() {
		defer cancel()
		view, egress, took, err := c.build(buildCtx)

		c.mu.Lock()
		defer c.mu.Unlock()
		if err != nil {
			// Keep the last good view. Going blind is worse than being visibly
			// stale, and Stale plus LastError says exactly which one this is.
			c.failures++
			c.failedAt = now()
			c.status.Failures = c.failures
			c.status.LastError = err.Error()
		} else {
			c.refreshes++
			c.view = view
			c.egress = egress
			c.status.BuildTook = took
			c.status.Available = true
			c.status.RefreshedAt = now()
			c.status.Refreshes = c.refreshes
			c.status.LastError = ""
		}
		c.building = false
		close(done)
	}()
}

// build gathers a new view and its egress projection. took is the gather's
// duration, which the caller installs with the view so a reader never sees one
// build's timing beside another build's view.
func (c *ViewCache) build(ctx context.Context) (view GlobalMonitoringView, egress EgressView, took time.Duration, err error) {
	// The rebuild runs on its own goroutine, where a panic would take the
	// process down and leave building set; record it as a failed refresh.
	defer func() {
		if recovered := recover(); recovered != nil {
			c.logger.ErrorContext(ctx, "stream telemetry view rebuild panicked", "panic", recovered)
			view, egress, took = GlobalMonitoringView{}, EgressView{}, 0
			err = fmt.Errorf("stream telemetry view rebuild panicked: %v", recovered)
		}
	}()
	start := now()
	view, err = c.registry.GlobalView(ctx)
	if err != nil {
		return GlobalMonitoringView{}, EgressView{}, 0, err
	}
	took = now().Sub(start)
	view = view.clone()
	egress = view.Egress()
	egress.Distributed = c.registry.Distributed()
	return view, egress, took, nil
}

// snapshotLocked must be called with c.mu held. It runs project only when a
// view is available.
func (c *ViewCache) snapshotLocked(age time.Duration, forceStale bool, project func()) ViewCacheStatus {
	status := c.status
	status.Age = age
	status.Stale = forceStale || age > c.ttl
	if !status.Available {
		status.Age = 0
		return status
	}
	project()
	return status
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
