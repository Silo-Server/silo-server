package opslog

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/cache"
	"github.com/Silo-Server/silo-server/internal/logstream"
)

func testPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatalf("connect test database: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

// TestConsumerRunPersistsBufferedEntriesOnStopDB stops the consumer with
// entries still buffered, the way shutdown does, and checks that they reach
// operational_logs and the local live tail.
func TestConsumerRunPersistsBufferedEntriesOnStopDB(t *testing.T) {
	ctx := context.Background()
	pool := testPool(t)

	requestID := fmt.Sprintf("opslog-consumer-%d", time.Now().UnixNano())
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM operational_logs WHERE request_id = $1`, requestID)
	})

	hub := logstream.NewHub("node-a", nil)
	tail, unsubscribe := hub.Subscribe(nil)
	defer unsubscribe()

	// Full batches plus a partial one, within the live tail's 64-entry buffer.
	const n = 60
	ch := make(chan Entry, n)
	for i := range n {
		ch <- Entry{Timestamp: time.Now().UTC(), Level: "info", Component: "probe", Message: fmt.Sprintf("probe %d", i), RequestID: requestID, NodeID: "node-a"}
	}
	stopped, cancel := context.WithCancel(ctx)
	cancel()
	consumer := NewConsumer(pool, hub)
	consumer.batchSize = 25
	consumer.Run(stopped, ch)

	var rows int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM operational_logs WHERE request_id = $1`, requestID).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != n {
		t.Fatalf("persisted %d rows, want %d", rows, n)
	}
	if got := len(tail); got != n {
		t.Fatalf("live tail received %d appends, want %d", got, n)
	}
	var row EntryRow
	if err := json.Unmarshal((<-tail).Entry, &row); err != nil || row.RequestID != requestID || row.ID == 0 {
		t.Fatalf("live tail entry = %+v, err %v", row, err)
	}
}

// stuckBus models an unreachable Redis: every publish hangs until its context
// ends or the test releases it.
type stuckBus struct{ release chan struct{} }

func (b *stuckBus) Publish(ctx context.Context, _ string, _ cache.Event) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-b.release:
		return errors.New("redis unreachable")
	}
}

func (b *stuckBus) Subscribe(context.Context, string, cache.EventHandler) error { return nil }
func (b *stuckBus) Close() error                                                { return nil }

// TestConsumerPersistsAtFullSpeedWhileRedisIsStuckDB drains many batches
// while every cross-node publish hangs. Persistence must not wait on the event
// bus: a consumer that published inline would spend at least the publish
// timeout on each of the ten batches.
func TestConsumerPersistsAtFullSpeedWhileRedisIsStuckDB(t *testing.T) {
	ctx := context.Background()
	pool := testPool(t)

	requestID := fmt.Sprintf("opslog-stuck-bus-%d", time.Now().UnixNano())
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM operational_logs WHERE request_id = $1`, requestID)
	})

	bus := &stuckBus{release: make(chan struct{})}
	hub := logstream.NewHub("node-a", bus)
	defer hub.Close()
	defer close(bus.release)

	const n = 1000
	ch := make(chan Entry, n)
	for i := range n {
		ch <- Entry{Timestamp: time.Now().UTC(), Level: "info", Component: "probe", Message: fmt.Sprintf("probe %d", i), RequestID: requestID, NodeID: "node-a"}
	}
	close(ch)
	start := time.Now()
	NewConsumer(pool, hub).Run(ctx, ch)
	elapsed := time.Since(start)

	var rows int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM operational_logs WHERE request_id = $1`, requestID).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	t.Logf("persisted %d rows in %v with every publish stuck", rows, elapsed)
	if rows != n {
		t.Fatalf("persisted %d rows, want %d", rows, n)
	}
	if elapsed > 5*time.Second {
		t.Fatalf("persisting %d rows took %v with the event bus stuck", n, elapsed)
	}
}

// TestConsumerKeepsEntriesBesideInvalidTextDB persists a batch whose message,
// component and attrs hold bytes Postgres text and jsonb reject, plus one row
// whose client address is not an inet. The bytes are replaced and only the bad
// address is dropped; before, any one of them failed the whole batch.
func TestConsumerKeepsEntriesBesideInvalidTextDB(t *testing.T) {
	ctx := context.Background()
	pool := testPool(t)

	requestID := fmt.Sprintf("opslog-invalid-text-%d", time.Now().UnixNano())
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM operational_logs WHERE request_id = $1`, requestID)
	})
	hub := logstream.NewHub("node-a", nil)
	tail, unsubscribe := hub.Subscribe(nil)
	defer unsubscribe()

	entry := func(component, message, clientIP string, attrs map[string]any) Entry {
		return Entry{Timestamp: time.Now().UTC(), Level: "info", Component: component, Message: message,
			RequestID: requestID, ClientIP: clientIP, NodeID: "node-a", Attrs: attrs}
	}
	ch := make(chan Entry, 5)
	ch <- entry("probe", "open caf\xe9.mkv", "", nil)
	ch <- entry("probe", "attrs", "", map[string]any{"nul": "a\x00b", "k\x00": "v", "literal": `\u0000`})
	ch <- entry("probe\x00", "component", "192.0.2.1", nil)
	ch <- entry("probe", "bad address", "bogus", nil)
	ch <- entry("probe", "last", "", nil)
	close(ch)
	NewConsumer(pool, hub).Run(ctx, ch)

	rows, err := pool.Query(ctx, `SELECT component, message, attrs::text FROM operational_logs WHERE request_id = $1 ORDER BY id`, requestID)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for rows.Next() {
		var component, message, attrsJSON string
		if err := rows.Scan(&component, &message, &attrsJSON); err != nil {
			t.Fatal(err)
		}
		// Re-encode through a Go map, which sorts keys; jsonb orders them
		// its own way.
		var attrs map[string]string
		if err := json.Unmarshal([]byte(attrsJSON), &attrs); err != nil {
			t.Fatalf("decode attrs %s: %v", attrsJSON, err)
		}
		encoded, _ := json.Marshal(attrs)
		got = append(got, component+"|"+message+"|"+string(encoded))
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"probe|open caf\uFFFD.mkv|null",
		`probe|attrs|{"k` + "\uFFFD" + `":"v","literal":"\\u0000","nul":"a` + "\uFFFD" + `b"}`,
		"probe\uFFFD|component|null",
		"probe|last|null",
	}
	if len(got) != len(want) {
		t.Fatalf("persisted %d rows %q, want %q", len(got), got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("row %d = %q, want %q", i, got[i], want[i])
		}
	}
	if n := len(tail); n != len(want) {
		t.Fatalf("live tail received %d appends, want %d", n, len(want))
	}
}
