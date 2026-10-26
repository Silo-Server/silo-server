package queryattr

import (
	"context"
	"log/slog"
	"sync"
)

const noQuery = "none"

type requestKey struct{}
type quietKey struct{}

// Request accumulates completed operations until its first LogAttrs snapshot.
// Concurrent completion and logging are safe; later completions leave the snapshot unchanged.
type Request struct {
	mu                       sync.Mutex
	queries, ns, acquireWait uint64
	slowest                  *row
	slowestNS                uint64
	counts                   map[*row]uint32
	closed                   bool
	attrs                    []any
}

// StartRequest reuses a request already in ctx, or returns a derived context and
// a new request. Off returns ctx unchanged and a nil request.
func StartRequest(ctx context.Context) (context.Context, *Request) {
	if !Enabled() {
		return ctx, nil
	}
	if r := requestFromContext(ctx); r != nil {
		return ctx, r
	}
	r := new(Request)
	return context.WithValue(ctx, requestKey{}, r), r
}

func requestFromContext(ctx context.Context) *Request {
	r, _ := ctx.Value(requestKey{}).(*Request)
	return r
}

func rowLess(a, b *row) bool {
	return b == nil || a.site.name < b.site.name || a.site.name == b.site.name && a.shape < b.shape
}

func (r *Request) add(entry *row, ns uint64) bool {
	if r == nil {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return false
	}
	if entry == nil {
		r.acquireWait += ns
		return true
	}
	r.queries++
	r.ns += ns
	if r.slowest == nil || ns > r.slowestNS || ns == r.slowestNS && rowLess(entry, r.slowest) {
		r.slowest = entry
		r.slowestNS = ns
	}
	if r.counts == nil {
		r.counts = make(map[*row]uint32)
	}
	r.counts[entry]++
	return true
}

// LogAttrs closes the request and returns nine slog.Attr values in the log contract's
// order, or nil if no operation and no acquire wait were recorded. It caches the
// first snapshot, releases per-row counts, and is nil-safe.
func (r *Request) LogAttrs() []any {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return r.attrs
	}
	r.closed = true
	if r.queries == 0 && r.acquireWait == 0 {
		return nil
	}
	slowSite, slowShape, topSite, topShape := noQuery, noQuery, noQuery, noQuery
	if r.slowest != nil {
		slowSite = r.slowest.site.name
		slowShape = r.slowest.shape
	}
	var top *row
	var count uint32
	for entry, n := range r.counts {
		if top == nil || n > count || n == count && rowLess(entry, top) {
			top = entry
			count = n
		}
	}
	if top != nil {
		topSite = top.site.name
		topShape = top.shape
	}
	r.counts = nil
	r.attrs = []any{
		slog.Uint64("db_queries", r.queries), slog.Uint64("db_query_ns", r.ns), slog.Uint64("db_acquire_wait_ns", r.acquireWait),
		slog.String("db_slowest_site", slowSite), slog.String("db_slowest_shape", slowShape), slog.Uint64("db_slowest_ns", r.slowestNS),
		slog.String("db_top_site", topSite), slog.String("db_top_shape", topShape), slog.Uint64("db_top_count", uint64(count)),
	}
	return r.attrs
}

// Detach hides request attribution while preserving cancellation, deadlines and
// other values. StartRequest on the result creates a new request. Off returns ctx.
func Detach(ctx context.Context) context.Context {
	if !Enabled() {
		return ctx
	}
	return context.WithValue(ctx, requestKey{}, (*Request)(nil))
}

// Quiet suppresses event output and suppression counts for operations begun under
// ctx. Site counters, metrics and request totals remain active. Off returns ctx.
func Quiet(ctx context.Context) context.Context {
	if !Enabled() {
		return ctx
	}
	return context.WithValue(ctx, quietKey{}, true)
}
