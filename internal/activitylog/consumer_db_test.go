package activitylog

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/logstream"
)

// TestConsumerRunPersistsBufferedEntriesOnStopDB stops the consumer with
// entries still buffered, the way shutdown does, and checks that they reach
// activity_log and the local live tail.
func TestConsumerRunPersistsBufferedEntriesOnStopDB(t *testing.T) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect test database: %v", err)
	}
	t.Cleanup(pool.Close)

	requestID := fmt.Sprintf("activitylog-consumer-%d", time.Now().UnixNano())
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM activity_log WHERE request_id = $1`, requestID)
	})

	hub := logstream.NewHub("node-a", nil)
	tail, unsubscribe := hub.Subscribe(nil)
	defer unsubscribe()

	// Full batches plus a partial one, within the live tail's 64-entry buffer.
	const n = 60
	ch := make(chan LogEntry, n)
	for i := range n {
		ch <- LogEntry{
			Timestamp: time.Now().UTC(), ClientIP: "192.0.2.1", RequestID: requestID, NodeID: "node-a",
			Method: "GET", Path: fmt.Sprintf("/api/v2/probe/%d", i), PathPattern: "/api/v2/probe/{id}", StatusCode: 204,
		}
	}
	stopped, cancel := context.WithCancel(ctx)
	cancel()
	consumer := NewConsumer(pool, hub)
	consumer.batchSize = 25
	consumer.Run(stopped, ch)

	var rows int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM activity_log WHERE request_id = $1`, requestID).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != n {
		t.Fatalf("persisted %d rows, want %d", rows, n)
	}
	if got := len(tail); got != n {
		t.Fatalf("live tail received %d appends, want %d", got, n)
	}
	var row AuditEntry
	if err := json.Unmarshal((<-tail).Entry, &row); err != nil || row.RequestID != requestID || row.ID == 0 {
		t.Fatalf("live tail entry = %+v, err %v", row, err)
	}
}
