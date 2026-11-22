package audiobooks

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/Silo-Server/silo-server/internal/metadata"
)

const (
	// defaultProviderPause is how long the sweep stops calling a provider that
	// answered rate-limited without saying when to come back. It outlasts one
	// five-minute sweep interval, so the rest of the sweep and the next one
	// leave the provider alone.
	defaultProviderPause = 15 * time.Minute

	// A provider route that fails providerOutageStreak calls in a row with
	// transient errors is treated as down: it is paused for
	// providerOutagePause, doubling each time it is still failing when the
	// pause ends, up to providerOutagePauseMax. Items that meet the pause are
	// deferred without counting an attempt, so an outage of days costs a
	// handful of probe attempts instead of driving the whole backlog to the
	// failure cap.
	providerOutageStreak   = 3
	providerOutagePause    = 15 * time.Minute
	providerOutagePauseMax = 6 * time.Hour
)

// providerRoute is one of the two calls the enricher makes to a provider. A
// provider's source can lose one and keep the other: a title search that
// fails upstream while lookups by ID still answer. Outages are therefore
// counted and paused per route, so books a lookup can still match are not
// held back by a failing search. The reverse does not hold: a search is not
// made while the metadata route is paused, since its candidates could not be
// fetched.
type providerRoute int

const (
	routeSearch providerRoute = iota
	routeMetadata
	providerRouteCount
)

func (r providerRoute) String() string {
	if r == routeSearch {
		return "search"
	}
	return "metadata"
}

// providerGate pauses a provider for the whole enricher once it answers
// rate-limited, and a provider's route once it looks down, so the rest of the
// sweep does not keep calling into a closed window. The zero value is ready
// to use.
//
// The gate is per process. That is enough because only one node sweeps at a
// time (see Enricher.Run). When the next sweep lands on another node, that
// node relearns the pause from the provider's next answer, and each item's own
// backoff still holds.
type providerGate struct {
	mu        sync.Mutex
	providers map[string]*providerHealth
	now       func() time.Time
}

type providerHealth struct {
	// limitedUntil ends a rate-limit pause, which covers every route: the
	// provider said to stop calling it.
	limitedUntil time.Time
	routes       [providerRouteCount]routeHealth
}

type routeHealth struct {
	// downUntil ends an outage pause of this route.
	downUntil time.Time
	// failStreak counts consecutive transient failures; outages counts the
	// outage pauses since the route last answered.
	failStreak int
	outages    int
}

// pauseEnd returns when the route may be called again: the later of the
// provider's rate-limit pause and the route's own outage pause.
func (h *providerHealth) pauseEnd(route providerRoute) time.Time {
	if down := h.routes[route].downUntil; down.After(h.limitedUntil) {
		return down
	}
	return h.limitedUntil
}

func (g *providerGate) clock() time.Time {
	if g.now != nil {
		return g.now()
	}
	return time.Now()
}

// health returns the provider's record, creating it. Callers hold g.mu.
func (g *providerGate) health(slug string) *providerHealth {
	if g.providers == nil {
		g.providers = make(map[string]*providerHealth)
	}
	h, ok := g.providers[slug]
	if !ok {
		h = &providerHealth{}
		g.providers[slug] = h
	}
	return h
}

// pausedUntil reports when the provider's route may be called again, if it
// is paused.
func (g *providerGate) pausedUntil(slug string, route providerRoute) (time.Time, bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	h, ok := g.providers[slug]
	if !ok {
		return time.Time{}, false
	}
	until := h.pauseEnd(route)
	if !g.clock().Before(until) {
		return time.Time{}, false
	}
	return until, true
}

// latestRateLimit returns the end of the longest active rate-limit pause
// among slugs, or zero when none is rate limited. An outage pause of one
// route is left out: it holds back only the items that met it.
func (g *providerGate) latestRateLimit(slugs []string) time.Time {
	g.mu.Lock()
	defer g.mu.Unlock()
	now := g.clock()
	var latest time.Time
	for _, slug := range slugs {
		if h, ok := g.providers[slug]; ok && now.Before(h.limitedUntil) && h.limitedUntil.After(latest) {
			latest = h.limitedUntil
		}
	}
	return latest
}

// observe records the outcome of a call to a provider's route.
//
// A plain rate-limit answer -- a gRPC ResourceExhausted, an HTTP 429, or
// rate-limit wording -- pauses the whole provider for its retry-after, or
// defaultProviderPause when it gave none. An untyped HTTP 403 does not: the
// item still backs off as rate limited (see audiobookProviderErrors), but a
// source that refuses every request with 403 would otherwise stall the whole
// sweep for every other book.
//
// Consecutive transient failures pause the route as down. Any answer on that
// route, including an empty one or a permanent per-item error, clears its
// record; an answer on the other route does not, since it says nothing about
// this one.
func (g *providerGate) observe(ctx context.Context, slug string, route providerRoute, err error) {
	g.mu.Lock()
	health := g.health(slug)
	h := &health.routes[route]
	if err == nil {
		h.failStreak, h.outages = 0, 0
		g.mu.Unlock()
		return
	}
	now := g.clock()
	wasPaused := now.Before(health.pauseEnd(route))
	var until time.Time
	var reason string
	class, retryAfter := metadata.ClassifyProviderError(err)
	switch class {
	case metadata.ProviderErrorRateLimited:
		pause := defaultProviderPause
		if retryAfter > 0 {
			pause = min(retryAfter, audiobookMaxRetryAfter)
		}
		until, reason = now.Add(pause), "rate limited"
		if until.After(health.limitedUntil) {
			health.limitedUntil = until
		}
	case metadata.ProviderErrorPermanent:
		// A per-item answer such as NotFound shows the route is up.
		h.failStreak, h.outages = 0, 0
	case metadata.ProviderErrorTransient:
		h.failStreak++
		if h.failStreak >= providerOutageStreak {
			h.failStreak = 0
			h.outages++
			pause := providerOutagePause
			for range h.outages - 1 {
				pause = min(2*pause, providerOutagePauseMax)
			}
			until, reason = now.Add(pause), "failing"
			if until.After(h.downUntil) {
				h.downUntil = until
			}
		}
	}
	g.mu.Unlock()

	if reason != "" && !wasPaused {
		slog.WarnContext(ctx, "audiobook enrichment: pausing provider for this enricher", "component", "audiobooks",
			"provider", slug,
			"route", route.String(),
			"reason", reason,
			"until", until,
			"retry_after", retryAfter,
		)
	}
}

// allPaused reports whether every provider in the chain is paused, and the
// earliest pause end among them: the item is worth trying again as soon as
// one of its providers can answer. A provider that fetches records is paused
// when its metadata route is: nothing it finds can be fetched. A failing
// search alone does not pause it, because a book with a provider ID can still
// be looked up.
func (g *providerGate) allPaused(providers []metadata.Provider) (time.Time, bool) {
	if len(providers) == 0 {
		return time.Time{}, false
	}
	var earliest time.Time
	for _, p := range providers {
		route := routeSearch
		if _, ok := p.(metadata.MetadataProvider); ok {
			route = routeMetadata
		}
		until, ok := g.pausedUntil(p.Slug(), route)
		if !ok {
			return time.Time{}, false
		}
		if earliest.IsZero() || until.Before(earliest) {
			earliest = until
		}
	}
	return earliest, true
}

// providerPausedError is what a paused provider returns instead of a call.
// Its text reads as a rate limit to the classifier, so an item that met it is
// never recorded as a clean no-match. An item whose only errors are these is
// deferred rather than charged a failure (see Enricher.failOrDefer).
type providerPausedError struct {
	slug  string
	until time.Time
}

func (e *providerPausedError) Error() string {
	return fmt.Sprintf("%s is paused until %s after rate limiting or repeated failures", e.slug, e.until.UTC().Format(time.RFC3339))
}

// wrap returns the chain with each search and metadata call routed through
// the gate. It keeps each provider's capability set, since the enricher picks
// calls by type assertion.
func (g *providerGate) wrap(providers []metadata.Provider) []metadata.Provider {
	out := make([]metadata.Provider, len(providers))
	for i, p := range providers {
		search, isSearch := p.(metadata.SearchProvider)
		meta, isMeta := p.(metadata.MetadataProvider)
		base := gatedProvider{Provider: p, gate: g, search: search, meta: meta}
		switch {
		case isSearch && isMeta:
			out[i] = gatedSearchMetadataProvider{base}
		case isSearch:
			out[i] = gatedSearchProvider{base}
		case isMeta:
			out[i] = gatedMetadataProvider{base}
		default:
			out[i] = p
		}
	}
	return out
}

type gatedProvider struct {
	metadata.Provider
	gate   *providerGate
	search metadata.SearchProvider
	meta   metadata.MetadataProvider
}

func (p gatedProvider) doSearch(ctx context.Context, q metadata.SearchQuery) ([]metadata.SearchResult, error) {
	if until, ok := p.gate.pausedUntil(p.Slug(), routeSearch); ok {
		return nil, &providerPausedError{slug: p.Slug(), until: until}
	}
	// A search whose candidates cannot be fetched is a wasted call.
	if p.meta != nil {
		if until, ok := p.gate.pausedUntil(p.Slug(), routeMetadata); ok {
			return nil, &providerPausedError{slug: p.Slug(), until: until}
		}
	}
	results, err := p.search.Search(ctx, q)
	p.gate.observe(ctx, p.Slug(), routeSearch, err)
	return results, err
}

func (p gatedProvider) doGetMetadata(ctx context.Context, req metadata.MetadataRequest) (*metadata.MetadataResult, error) {
	if until, ok := p.gate.pausedUntil(p.Slug(), routeMetadata); ok {
		return nil, &providerPausedError{slug: p.Slug(), until: until}
	}
	result, err := p.meta.GetMetadata(ctx, req)
	// (nil, nil) is not evidence either way: the plugin adapter returns it
	// without a request when the item has no provider IDs yet. An error or a
	// returned result is a real answer.
	if err != nil || result != nil {
		p.gate.observe(ctx, p.Slug(), routeMetadata, err)
	}
	return result, err
}

type gatedSearchProvider struct{ gatedProvider }

func (p gatedSearchProvider) Search(ctx context.Context, q metadata.SearchQuery) ([]metadata.SearchResult, error) {
	return p.doSearch(ctx, q)
}

type gatedMetadataProvider struct{ gatedProvider }

func (p gatedMetadataProvider) GetMetadata(ctx context.Context, req metadata.MetadataRequest) (*metadata.MetadataResult, error) {
	return p.doGetMetadata(ctx, req)
}

type gatedSearchMetadataProvider struct{ gatedProvider }

func (p gatedSearchMetadataProvider) Search(ctx context.Context, q metadata.SearchQuery) ([]metadata.SearchResult, error) {
	return p.doSearch(ctx, q)
}

func (p gatedSearchMetadataProvider) GetMetadata(ctx context.Context, req metadata.MetadataRequest) (*metadata.MetadataResult, error) {
	return p.doGetMetadata(ctx, req)
}
