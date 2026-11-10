package historyimport

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"sync"
)

// Source servers are user-supplied, so every response an import reads is
// bounded: a hostile or broken server must not be able to make Silo buffer an
// unbounded body or page forever.
const (
	// maxUpstreamResponseBytes caps one decoded (decompressed) response. The
	// heaviest legitimate response is a Plex page of 500 section items or a
	// batch of full movie metadata, a few MiB at most; Jellyfin and Emby pages
	// request only a handful of fields.
	maxUpstreamResponseBytes int64 = 16 << 20
	// maxListingBytes, maxListingItems and maxListingPages stop a runaway
	// source: one that keeps reporting more rows than it ever delivers. They
	// do not protect memory, since a listing is applied page by page and the
	// response limits below bound what one page can cost.
	maxListingBytes int64 = 32 << 30
	maxListingItems       = 5_000_000
	maxListingPages       = 200_000
	// maxUpstreamResponseValues caps the JSON values (objects, arrays, and
	// members or elements after the first) one response may open. Bytes alone
	// do not bound decoding: 16 MiB of "{}," decodes into millions of structs.
	// A full Plex page or metadata batch, cast lists included, is a few tens
	// of thousands.
	maxUpstreamResponseValues = 250_000
	// maxRunBytes stops a runaway source across every listing and lookup of
	// one run, so it cannot multiply the listing limit by naming many
	// sections or series.
	maxRunBytes int64 = 64 << 30
	// maxRunRetainedBytes caps what one run holds across pages, which is only
	// the in-progress lists and the Plex watchlist: roughly 100,000 items with their ids. Watched
	// history is never held, however long it is.
	maxRunRetainedBytes int64 = 64 << 20
	// upstreamErrorBodyBytes caps how much of a non-2xx body is kept for
	// diagnostics.
	upstreamErrorBodyBytes = 2048
)

// ErrUpstreamResponseTooLarge reports a source server whose response, or
// paged listing, is larger than an import accepts. The run fails with
// RunErrorSourceTooLarge instead of importing a truncated listing.
var ErrUpstreamResponseTooLarge = errors.New("history import source response exceeds the import size limit")

// upstreamLimitError names the limit a source exceeded, for the operator log.
type upstreamLimitError struct {
	limit  string
	detail string
}

func (e *upstreamLimitError) Error() string {
	return ErrUpstreamResponseTooLarge.Error() + ": " + e.detail
}
func (e *upstreamLimitError) Unwrap() error { return ErrUpstreamResponseTooLarge }

func limitErrorf(limit, format string, args ...any) error {
	return &upstreamLimitError{limit: limit, detail: fmt.Sprintf(format, args...)}
}

// exceededLimit returns the name of the limit err reports, or "".
func exceededLimit(err error) string {
	var limit *upstreamLimitError
	if errors.As(err, &limit) {
		return limit.limit
	}
	return ""
}

// listingBudget tracks one paged listing against the listing limits. A nil
// budget applies only the per-response limit.
type listingBudget struct {
	bytes int64
	items int
	pages int
}

// startPage counts a page request, failing once the listing has used its
// page allowance.
func (b *listingBudget) startPage() error {
	if b.pages >= maxListingPages {
		return limitErrorf("listing_pages", "listing exceeds %d pages", maxListingPages)
	}
	b.pages++
	return nil
}

// addItems counts the rows a page delivered.
func (b *listingBudget) addItems(n int) error {
	b.items += n
	if b.items > maxListingItems {
		return limitErrorf("listing_items", "listing exceeds %d items", maxListingItems)
	}
	return nil
}

// runBudget tracks one import run across every request its provider makes:
// listings, ID lookups, metadata sweeps, and every Plex section.
type runBudget struct {
	mu       sync.Mutex
	bytes    int64
	retained int64
}

type runBudgetKey struct{}

// withRunBudget gives the requests made with ctx one shared run budget.
// Requests without one apply only the response and listing limits.
func withRunBudget(ctx context.Context) context.Context {
	return context.WithValue(ctx, runBudgetKey{}, &runBudget{})
}

func runBudgetFrom(ctx context.Context) *runBudget {
	budget, _ := ctx.Value(runBudgetKey{}).(*runBudget)
	return budget
}

func (b *runBudget) remainingBytes() int64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	return max(0, maxRunBytes-b.bytes)
}

func (b *runBudget) addBytes(n int64) {
	b.mu.Lock()
	b.bytes += n
	b.mu.Unlock()
}

// retainedSizer estimates the memory a decoded source item holds.
type retainedSizer interface {
	retainedSize() int64
}

// retainItems charges items the run keeps against its retained-memory budget.
// Callers charge what they keep, after dropping anything they did not ask for.
func retainItems[T retainedSizer](ctx context.Context, items []T) error {
	budget := runBudgetFrom(ctx)
	if budget == nil {
		return nil
	}
	var size int64
	for i := range items {
		size += items[i].retainedSize()
	}
	budget.mu.Lock()
	defer budget.mu.Unlock()
	budget.retained += size
	if budget.retained > maxRunRetainedBytes {
		return limitErrorf("run_retained_bytes", "import keeps more than %d bytes of source items", maxRunRetainedBytes)
	}
	return nil
}

// releaseItems returns the retained-memory charge for items the run charged
// with retainItems and has since dropped.
func releaseItems[T retainedSizer](ctx context.Context, items []T) {
	budget := runBudgetFrom(ctx)
	if budget == nil {
		return
	}
	var size int64
	for i := range items {
		size += items[i].retainedSize()
	}
	budget.mu.Lock()
	budget.retained -= size
	budget.mu.Unlock()
}

// mapEntryOverhead approximates a map entry's storage beyond its key and
// value bytes.
const mapEntryOverhead = 48

func sizeOf[T any]() int64 {
	return int64(reflect.TypeFor[T]().Size())
}

func stringBytes(values ...string) int64 {
	var n int64
	for _, value := range values {
		n += int64(len(value))
	}
	return n
}

func stringMapBytes(m map[string]string) int64 {
	var n int64
	for key, value := range m {
		n += mapEntryOverhead + stringBytes(key, value)
	}
	return n
}

// decodeUpstreamJSON decodes one successful source response into out, reading
// at most the response limit, what remains of the listing budget when given,
// and what remains of the run budget carried by ctx. A body past a limit fails
// with ErrUpstreamResponseTooLarge rather than decoding a truncated value.
func decodeUpstreamJSON(ctx context.Context, body io.Reader, out any, listing *listingBudget) error {
	limit, scope, scopeLimit := maxUpstreamResponseBytes, "response", maxUpstreamResponseBytes
	if listing != nil {
		if remaining := max(0, maxListingBytes-listing.bytes); remaining < limit {
			limit, scope, scopeLimit = remaining, "listing", maxListingBytes
		}
	}
	run := runBudgetFrom(ctx)
	if run != nil {
		if remaining := run.remainingBytes(); remaining < limit {
			limit, scope, scopeLimit = remaining, "import", maxRunBytes
		}
	}
	read, err := decodeLimitedJSON(body, out, limit)
	if listing != nil {
		listing.bytes += read
	}
	if run != nil {
		run.addBytes(read)
	}
	if errors.Is(err, errUpstreamBytesLimit) {
		return limitErrorf(scope+"_bytes", "%s exceeds %d bytes", scope, scopeLimit)
	}
	return err
}

var errUpstreamBytesLimit = errors.New("byte limit reached")

// decodeLimitedJSON decodes body into out, reading at most limit bytes and
// maxUpstreamResponseValues JSON values. It returns the bytes read. The
// decoder buffers a whole value before it unmarshals, so a body stopped by
// either limit allocates nothing for its contents.
func decodeLimitedJSON(body io.Reader, out any, limit int64) (int64, error) {
	limited := &io.LimitedReader{R: body, N: limit + 1}
	err := json.NewDecoder(&jsonValueCounter{r: limited, limit: maxUpstreamResponseValues}).Decode(out)
	read := limit + 1 - limited.N
	if limited.N <= 0 {
		return read, errUpstreamBytesLimit
	}
	return read, err
}

// jsonValueCounter fails a read once the JSON passing through it opens more
// than limit values. It counts '{', '[', and ',' outside strings, which is at
// least one per array element or object member after the first.
type jsonValueCounter struct {
	r        io.Reader
	limit    int
	values   int
	inString bool
	escaped  bool
}

func (c *jsonValueCounter) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	for _, b := range p[:n] {
		switch {
		case c.escaped:
			c.escaped = false
		case c.inString:
			switch b {
			case '\\':
				c.escaped = true
			case '"':
				c.inString = false
			}
		case b == '"':
			c.inString = true
		case b == '{' || b == '[' || b == ',':
			c.values++
		}
	}
	if c.values > c.limit {
		return 0, limitErrorf("response_values", "response exceeds %d JSON values", c.limit)
	}
	return n, err
}

// discardUpstreamBody drains a body whose content is not needed, so the
// connection can be reused, without reading past the response limit.
func discardUpstreamBody(body io.Reader) {
	_, _ = io.Copy(io.Discard, io.LimitReader(body, maxUpstreamResponseBytes))
}

// readUpstreamErrorBody returns the start of a non-2xx body for diagnostics.
func readUpstreamErrorBody(body io.Reader) []byte {
	data, _ := io.ReadAll(io.LimitReader(body, upstreamErrorBodyBytes))
	return data
}

// keepRequested returns the first item for each requested id, in response
// order. A lookup by id keeps nothing else the source sends.
func keepRequested[T any](items []T, ids []string, id func(T) string) []T {
	pending := make(map[string]struct{}, len(ids))
	for _, value := range ids {
		pending[value] = struct{}{}
	}
	kept := make([]T, 0, min(len(items), len(ids)))
	for _, item := range items {
		key := id(item)
		if _, ok := pending[key]; ok {
			delete(pending, key)
			kept = append(kept, item)
		}
	}
	return kept
}
