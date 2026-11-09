package historyimport

import (
	"context"
	"errors"
)

// StreamPosition says where a provider's listing stands. It is stored with
// the run so a restart continues near where it stopped.
type StreamPosition struct {
	Stage   string `json:"stage"`
	Section string `json:"section,omitempty"`
	Offset  int    `json:"offset"`
}

// RecordBatch is one page of normalized records. Resume restarts the stream
// at this same page, one page behind what was applied, because offsets shift
// when the source changes during the import. Re-applying a page is safe: each
// write refuses anything already recorded. A batch with an empty Resume.Stage
// carries warnings only and moves no checkpoint.
type RecordBatch struct {
	Records  []Record
	Warnings []string
	Resume   StreamPosition
}

// RecordSink applies a batch before the provider requests the next page, so
// a slow database slows the source instead of growing memory.
type RecordSink func(RecordBatch) error

// Provider streams a source's watch state page by page. Memory must not grow
// with history size: anything held across pages has to be bounded, by the run
// budget (in-progress lists) or a fixed entry cap (lookup caches). from is nil
// for a fresh run.
type Provider interface {
	Stream(ctx context.Context, from *StreamPosition, sink RecordSink) error
}

// sinkError marks a failure of the sink, so a provider that downgrades listing
// failures to warnings does not swallow one.
type sinkError struct{ err error }

func (e *sinkError) Error() string { return e.err.Error() }
func (e *sinkError) Unwrap() error { return e.err }

func isSinkError(err error) bool {
	var target *sinkError
	return errors.As(err, &target)
}

func markSinkErrors(sink RecordSink) RecordSink {
	return func(batch RecordBatch) error {
		if err := sink(batch); err != nil {
			return &sinkError{err: err}
		}
		return nil
	}
}

// addRunWarning keeps the first warnings a run can store. A systematic failure
// produces one per record, which would otherwise grow with history size.
func addRunWarning(summary *ExecutionSummary, warnings ...string) {
	for _, warning := range warnings {
		if len(summary.Warnings) >= maxStoredWarnings {
			return
		}
		summary.Warnings = append(summary.Warnings, warning)
	}
}

// Lookup and dedupe sizes. All are fixed entry counts, so the same history
// imports the same way on any hardware.
const (
	// seriesLookupCacheEntries covers the shows a few consecutive pages touch.
	seriesLookupCacheEntries = 4096
	// maxTrackedSeries bounds per-show state kept for a whole run. It is far
	// above the number of shows that exist in any metadata provider's catalog.
	maxTrackedSeries = 100_000
	// maxSeenHistoryItems bounds the admin history's per-item dedupe, about
	// 20 MiB when full. Past it an older play of an item may import as its own
	// history row, which is true, just more than a merged import would keep.
	maxSeenHistoryItems = 500_000
)

// lookupCache is a fixed-size first-in-first-out cache.
type lookupCache[V any] struct {
	items map[string]V
	ring  []string
	next  int
}

func newLookupCache[V any](capacity int) *lookupCache[V] {
	return &lookupCache[V]{items: make(map[string]V, capacity), ring: make([]string, capacity)}
}

func (c *lookupCache[V]) get(key string) (V, bool) {
	value, ok := c.items[key]
	return value, ok
}

func (c *lookupCache[V]) put(key string, value V) {
	if _, ok := c.items[key]; !ok {
		delete(c.items, c.ring[c.next])
		c.ring[c.next] = key
		c.next = (c.next + 1) % len(c.ring)
	}
	c.items[key] = value
}

// heldItems is a short listing kept while a longer one streams past it, so
// the two merge per item: the in-progress list held against the watched pages.
// It is charged to the run's retained budget.
type heldItems[T retainedSizer] struct {
	items    []T
	index    map[string]int
	consumed []bool
}

func holdItems[T retainedSizer](ctx context.Context, items []T, id func(T) string) (*heldItems[T], error) {
	if err := retainItems(ctx, items); err != nil {
		return nil, err
	}
	held := &heldItems[T]{items: items, index: make(map[string]int, len(items)), consumed: make([]bool, len(items))}
	for i, item := range items {
		held.index[id(item)] = i
	}
	return held, nil
}

// take returns the held item for id and marks it as merged, so it is left out
// of remaining. An item can be taken twice when a listing is read again.
func (h *heldItems[T]) take(id string) (T, bool) {
	i, ok := h.index[id]
	if !ok {
		var zero T
		return zero, false
	}
	h.consumed[i] = true
	return h.items[i], true
}

func (h *heldItems[T]) remaining() []T {
	var rest []T
	for i, item := range h.items {
		if !h.consumed[i] {
			rest = append(rest, item)
		}
	}
	return rest
}

func (h *heldItems[T]) release(ctx context.Context) { releaseItems(ctx, h.items) }

// stageReached reports whether a stream resuming at from should still run
// stage, given the provider's stage order.
func stageReached(from *StreamPosition, order []string, stage string) bool {
	if from == nil {
		return true
	}
	current, target := -1, -1
	for i, name := range order {
		if name == from.Stage {
			current = i
		}
		if name == stage {
			target = i
		}
	}
	return current < 0 || target >= current
}

// stageOffset is the offset stage starts from when resuming at from.
func stageOffset(from *StreamPosition, stage string) int {
	if from != nil && from.Stage == stage {
		return from.Offset
	}
	return 0
}
