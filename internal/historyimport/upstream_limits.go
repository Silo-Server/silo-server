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
	// maxListingBytes caps the bytes one paged listing reads in total.
	maxListingBytes int64 = 512 << 20
	// maxListingItems and maxListingPages cap one paged listing. A very large
	// library or a decade of Plex plays is well under these; a server that
	// keeps reporting more rows than it ever delivers is not.
	maxListingItems = 500_000
	maxListingPages = 5_000
	// maxUpstreamResponseValues caps the JSON values (objects, arrays, and
	// members or elements after the first) one response may open. Bytes alone
	// do not bound decoding: 16 MiB of "{}," decodes into millions of structs.
	// A full Plex page or metadata batch, cast lists included, is a few tens
	// of thousands.
	maxUpstreamResponseValues = 250_000
	// maxRunBytes caps what one import run reads across every listing and
	// lookup, so a source cannot multiply the listing limit by naming many
	// sections or series.
	maxRunBytes int64 = 2 << 30
	// maxRunRetainedBytes caps the estimated memory of the items one import
	// run keeps until it finishes: roughly 500,000 items with their ids.
	maxRunRetainedBytes int64 = 256 << 20
	// upstreamErrorBodyBytes caps how much of a non-2xx body is kept for
	// diagnostics.
	upstreamErrorBodyBytes = 2048
)

// ErrUpstreamResponseTooLarge reports a source server whose response, or
// paged listing, is larger than an import accepts. The run fails with
// RunErrorSourceTooLarge instead of importing a truncated listing.
var ErrUpstreamResponseTooLarge = errors.New("history import source response exceeds the import size limit")

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
		return fmt.Errorf("%w: listing exceeds %d pages", ErrUpstreamResponseTooLarge, maxListingPages)
	}
	b.pages++
	return nil
}

// addItems counts the rows a page delivered.
func (b *listingBudget) addItems(n int) error {
	b.items += n
	if b.items > maxListingItems {
		return fmt.Errorf("%w: listing exceeds %d items", ErrUpstreamResponseTooLarge, maxListingItems)
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
		return fmt.Errorf("%w: import keeps more than %d bytes of source items", ErrUpstreamResponseTooLarge, maxRunRetainedBytes)
	}
	return nil
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
		return fmt.Errorf("%w: %s exceeds %d bytes", ErrUpstreamResponseTooLarge, scope, scopeLimit)
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
		return 0, fmt.Errorf("%w: response exceeds %d JSON values", ErrUpstreamResponseTooLarge, c.limit)
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
