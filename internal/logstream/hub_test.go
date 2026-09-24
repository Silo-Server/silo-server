package logstream

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/cache"
)

// hangingBus models an unreachable Redis: Publish waits until its context ends.
type hangingBus struct{ calls atomic.Int32 }

func (b *hangingBus) Publish(ctx context.Context, _ string, _ cache.Event) error {
	b.calls.Add(1)
	<-ctx.Done()
	return ctx.Err()
}

func (b *hangingBus) Subscribe(context.Context, string, cache.EventHandler) error { return nil }
func (b *hangingBus) Close() error                                                { return nil }

func TestPublishAppendsBoundsAnUnreachableEventBus(t *testing.T) {
	prev := publishBudget
	publishBudget = 50 * time.Millisecond
	t.Cleanup(func() { publishBudget = prev })

	bus := &hangingBus{}
	hub := NewHub("node-a", bus)
	local, unsubscribe := hub.Subscribe(nil)
	defer unsubscribe()

	entries := []map[string]int{{"id": 1}, {"id": 2}, {"id": 3}}
	start := time.Now()
	failed, err := PublishAppends(context.Background(), hub, StreamApp, entries)
	elapsed := time.Since(start)

	if failed != len(entries) || err == nil {
		t.Fatalf("failed = %d, err = %v; want %d failures", failed, err, len(entries))
	}
	if elapsed > time.Second {
		t.Fatalf("PublishAppends took %v with the bus down, want about the %v budget", elapsed, publishBudget)
	}
	if got := len(local); got != len(entries) {
		t.Fatalf("local subscriber got %d entries, want %d", got, len(entries))
	}
}
