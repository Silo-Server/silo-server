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

	// A provider that fails providerOutageStreak calls in a row with transient
	// errors is treated as down: it is paused for providerOutagePause,
	// doubling each time it is still failing when the pause ends, up to
	// providerOutagePauseMax. Items that meet the pause are deferred without
	// counting an attempt, so an outage of days costs a handful of probe
	// attempts instead of driving the whole backlog to the failure cap.
	providerOutageStreak   = 3
	providerOutagePause    = 15 * time.Minute
	providerOutagePauseMax = 6 * time.Hour
)

// providerGate pauses a provider for the whole enricher once it answers
// rate-limited or looks down, so the rest of the sweep does not keep calling
// into a closed window. The zero value is ready to use.
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
	pausedUntil time.Time
	// failStreak counts consecutive transient failures; outages counts the
	// outage pauses since the provider last answered.
	failStreak int
	outages    int
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

// pausedUntil reports when the provider may be called again, if it is paused.
func (g *providerGate) pausedUntil(slug string) (time.Time, bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	h, ok := g.providers[slug]
	if !ok || !g.clock().Before(h.pausedUntil) {
		return time.Time{}, false
	}
	return h.pausedUntil, true
}

// latestPause returns the end of the longest active pause among slugs, or
// zero when none of them is paused.
func (g *providerGate) latestPause(slugs []string) time.Time {
	var latest time.Time
	for _, slug := range slugs {
		if until, ok := g.pausedUntil(slug); ok && until.After(latest) {
			latest = until
		}
	}
	return latest
}

// observe records the outcome of a provider call.
//
// A plain rate-limit answer -- a gRPC ResourceExhausted, an HTTP 429, or
// rate-limit wording -- pauses the provider for its retry-after, or
// defaultProviderPause when it gave none. An untyped HTTP 403 does not: the
// item still backs off as rate limited (see audiobookProviderErrors), but a
// source that refuses every request with 403 would otherwise stall the whole
// sweep for every other book.
//
// Consecutive transient failures pause the provider as down. Any answer,
// including an empty one or a permanent per-item error, clears that record.
func (g *providerGate) observe(ctx context.Context, slug string, err error) {
	g.mu.Lock()
	h := g.health(slug)
	if err == nil {
		h.failStreak, h.outages = 0, 0
		g.mu.Unlock()
		return
	}
	now := g.clock()
	wasPaused := now.Before(h.pausedUntil)
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
	case metadata.ProviderErrorPermanent:
		// A per-item answer such as NotFound shows the provider is up.
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
		}
	}
	if until.After(h.pausedUntil) {
		h.pausedUntil = until
	}
	g.mu.Unlock()

	if reason != "" && !wasPaused {
		slog.WarnContext(ctx, "audiobook enrichment: pausing provider for this enricher", "component", "audiobooks",
			"provider", slug,
			"reason", reason,
			"until", until,
			"retry_after", retryAfter,
		)
	}
}

// allPaused reports whether every provider in the chain is paused, and the
// earliest pause end among them: the item is worth trying again as soon as
// one of its providers can answer.
func (g *providerGate) allPaused(providers []metadata.Provider) (time.Time, bool) {
	if len(providers) == 0 {
		return time.Time{}, false
	}
	var earliest time.Time
	for _, p := range providers {
		until, ok := g.pausedUntil(p.Slug())
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
	if until, ok := p.gate.pausedUntil(p.Slug()); ok {
		return nil, &providerPausedError{slug: p.Slug(), until: until}
	}
	results, err := p.search.Search(ctx, q)
	p.gate.observe(ctx, p.Slug(), err)
	return results, err
}

func (p gatedProvider) doGetMetadata(ctx context.Context, req metadata.MetadataRequest) (*metadata.MetadataResult, error) {
	if until, ok := p.gate.pausedUntil(p.Slug()); ok {
		return nil, &providerPausedError{slug: p.Slug(), until: until}
	}
	result, err := p.meta.GetMetadata(ctx, req)
	// (nil, nil) is not evidence either way: the plugin adapter returns it
	// without a request when the item has no provider IDs yet. An error or a
	// returned result is a real answer.
	if err != nil || result != nil {
		p.gate.observe(ctx, p.Slug(), err)
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
