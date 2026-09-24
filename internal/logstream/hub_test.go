package logstream

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/Silo-Server/silo-server/internal/cache"
)

// stuckBus models an unreachable Redis: Publish waits until its context ends
// or the test releases it, then fails.
type stuckBus struct {
	release chan struct{}
	calls   chan struct{}
}

func newStuckBus() *stuckBus {
	return &stuckBus{release: make(chan struct{}), calls: make(chan struct{}, 1)}
}

func (b *stuckBus) Publish(ctx context.Context, _ string, _ cache.Event) error {
	select {
	case b.calls <- struct{}{}:
	default:
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-b.release:
		return errors.New("redis unreachable")
	}
}

func (b *stuckBus) Subscribe(context.Context, string, cache.EventHandler) error { return nil }
func (b *stuckBus) Close() error                                                { return nil }

// recordingBus records published events; err, when set, fails every publish.
type recordingBus struct {
	mu     sync.Mutex
	events []cache.Event
	err    error
}

func (b *recordingBus) Publish(_ context.Context, _ string, event cache.Event) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.err != nil {
		return b.err
	}
	b.events = append(b.events, event)
	return nil
}

func (b *recordingBus) Subscribe(context.Context, string, cache.EventHandler) error { return nil }
func (b *recordingBus) Close() error                                                { return nil }

func tailDrops(stream Stream, reason string) float64 {
	return testutil.ToFloat64(tailDropped.WithLabelValues(string(stream), reason))
}

// TestPublishAppendsDoesNotWaitOnAStuckEventBus publishes far more rows than
// the cross-node queue holds while every publish hangs. PublishAppends returns
// without waiting, local subscribers still get rows, and the overflow is
// counted.
func TestPublishAppendsDoesNotWaitOnAStuckEventBus(t *testing.T) {
	bus := newStuckBus()
	hub := NewHub("node-a", bus)
	defer hub.Close()
	defer close(bus.release)
	local, unsubscribe := hub.Subscribe(nil)
	defer unsubscribe()
	queueFull := tailDrops(StreamApp, tailDropQueueFull)

	const n = 3 * remoteQueueSize
	entries := make([]map[string]int, n)
	for i := range entries {
		entries[i] = map[string]int{"id": i}
	}
	<-publishOne(t, hub) // the publisher is now stuck on the bus
	start := time.Now()
	failed, err := PublishAppends(hub, StreamApp, entries)
	elapsed := time.Since(start)

	if failed != 0 || err != nil {
		t.Fatalf("failed = %d, err = %v", failed, err)
	}
	if elapsed > time.Second {
		t.Fatalf("PublishAppends took %v with the event bus stuck", elapsed)
	}
	if got := len(local); got != cap(local) {
		t.Fatalf("local subscriber got %d entries, want its full buffer of %d", got, cap(local))
	}
	if got := tailDrops(StreamApp, tailDropQueueFull) - queueFull; got != n-remoteQueueSize {
		t.Fatalf("queue_full drops = %v, want %d", got, n-remoteQueueSize)
	}
}

// publishOne publishes one row and returns a channel that fires once the
// publisher has handed it to the bus.
func publishOne(t *testing.T, hub *Hub) <-chan struct{} {
	t.Helper()
	if _, err := PublishAppends(hub, StreamApp, []int{0}); err != nil {
		t.Fatal(err)
	}
	bus := hub.eventBus.(*stuckBus)
	return bus.calls
}

func TestHubPublishesQueuedRowsToOtherNodesBeforeClose(t *testing.T) {
	bus := &recordingBus{}
	hub := NewHub("node-a", bus)

	entries := []map[string]int{{"id": 1}, {"id": 2}, {"id": 3}}
	if _, err := PublishAppends(hub, StreamAudit, entries); err != nil {
		t.Fatal(err)
	}
	hub.Close()

	bus.mu.Lock()
	defer bus.mu.Unlock()
	if len(bus.events) != len(entries) {
		t.Fatalf("published %d events, want %d", len(bus.events), len(entries))
	}
	for i, event := range bus.events {
		var envelope appendEnvelope
		if err := json.Unmarshal([]byte(event.Payload), &envelope); err != nil {
			t.Fatal(err)
		}
		var entry map[string]int
		if err := json.Unmarshal(envelope.Entry, &entry); err != nil {
			t.Fatal(err)
		}
		if event.Type != cache.EventAuditLogAppended || envelope.Source != "node-a" || envelope.Stream != StreamAudit || entry["id"] != i+1 {
			t.Fatalf("event %d = %s %+v, entry %v", i, event.Type, envelope, entry)
		}
	}
}

// TestHubLogsPublishFailuresOnce checks that a failing event bus is reported
// once, not per row: every report is itself persisted and published.
func TestHubLogsPublishFailuresOnce(t *testing.T) {
	prev := slog.Default()
	t.Cleanup(func() { slog.SetDefault(prev) })
	records := &recordCounter{}
	slog.SetDefault(slog.New(records))

	bus := &recordingBus{err: errors.New("redis unreachable")}
	hub := NewHub("node-a", bus)
	failedBefore := tailDrops(StreamApp, tailDropPublishFailed)

	const n = 10
	if _, err := PublishAppends(hub, StreamApp, make([]int, n)); err != nil {
		t.Fatal(err)
	}
	hub.Close()

	if got := tailDrops(StreamApp, tailDropPublishFailed) - failedBefore; got != n {
		t.Fatalf("publish_failed drops = %v, want %d", got, n)
	}
	if got := records.count(); got != 1 {
		t.Fatalf("logged %d records for %d failed publishes, want 1", got, n)
	}
}

type recordCounter struct {
	mu sync.Mutex
	n  int
}

func (c *recordCounter) Enabled(context.Context, slog.Level) bool { return true }
func (c *recordCounter) Handle(context.Context, slog.Record) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.n++
	return nil
}
func (c *recordCounter) WithAttrs([]slog.Attr) slog.Handler { return c }
func (c *recordCounter) WithGroup(string) slog.Handler      { return c }

func (c *recordCounter) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.n
}
