package opslog

import (
	"context"
	"fmt"
	"testing"
	"time"
)

// A shipped node batch is filed under the node the core authenticated,
// whatever node id an entry carries, and a client_ip that is not an address
// is cleared before the insert, so the row is stored with the rest of its
// batch instead of being rejected.
func TestInsertNodeBatchStampsTheNodeAndClearsABadAddressDB(t *testing.T) {
	ctx := t.Context()
	pool := testPool(t)
	requestID := fmt.Sprintf("opslog-node-batch-%d", time.Now().UnixNano())
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM operational_logs WHERE request_id = $1`, requestID)
	})
	entries := []Entry{
		{Timestamp: time.Now().UTC(), Level: "INFO", Component: "transcodenode", Message: "a", RequestID: requestID, NodeID: "spoofed"},
		{Timestamp: time.Now().UTC(), Level: "INFO", Component: "transcodenode", Message: "b", RequestID: requestID, ClientIP: "not-an-address"},
		{Timestamp: time.Now().UTC(), Level: "INFO", Component: "ffmpeg", Message: "c", RequestID: requestID, ClientIP: "192.0.2.7:4410"},
	}
	if err := NewConsumer(pool, nil).InsertNodeBatch(ctx, "gpu-1#7", entries); err != nil {
		t.Fatalf("InsertNodeBatch: %v", err)
	}
	rows, err := pool.Query(ctx, `SELECT message, node_id, COALESCE(host(client_ip), '-') FROM operational_logs WHERE request_id = $1 ORDER BY message`, requestID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var got []string
	for rows.Next() {
		var message, nodeID, clientIP string
		if err := rows.Scan(&message, &nodeID, &clientIP); err != nil {
			t.Fatal(err)
		}
		got = append(got, message+"@"+nodeID+"/"+clientIP)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(got) != "[a@gpu-1#7/- b@gpu-1#7/- c@gpu-1#7/192.0.2.7]" {
		t.Fatalf("stored %v, want every row under gpu-1#7 with the bad address cleared", got)
	}
}

// A node's clock dates its entries; the core keeps them between a day before
// and 30 seconds after its own clock, so no row lands outside retention or in
// a partition that does not exist yet.
func TestInsertNodeBatchClampsTimestampsDB(t *testing.T) {
	ctx := t.Context()
	pool := testPool(t)
	requestID := fmt.Sprintf("opslog-node-clock-%d", time.Now().UnixNano())
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM operational_logs WHERE request_id = $1`, requestID)
	})
	before := time.Now().UTC()
	entries := []Entry{
		{Timestamp: before.Add(365 * 24 * time.Hour), Level: "INFO", Component: "transcodenode", Message: "ahead", RequestID: requestID},
		{Timestamp: before.Add(-30 * 24 * time.Hour), Level: "INFO", Component: "transcodenode", Message: "behind", RequestID: requestID},
		{Timestamp: before.Add(-time.Minute), Level: "INFO", Component: "transcodenode", Message: "on time", RequestID: requestID},
	}
	if err := NewConsumer(pool, nil).InsertNodeBatch(ctx, "gpu-1#7", entries); err != nil {
		t.Fatalf("InsertNodeBatch: %v", err)
	}
	after := time.Now().UTC()
	stored := map[string]time.Time{}
	rows, err := pool.Query(ctx, `SELECT message, timestamp FROM operational_logs WHERE request_id = $1`, requestID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var message string
		var ts time.Time
		if err := rows.Scan(&message, &ts); err != nil {
			t.Fatal(err)
		}
		stored[message] = ts
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	// Postgres keeps microseconds.
	const precision = time.Millisecond
	if ts := stored["ahead"]; ts.Before(before.Add(nodeEntryMaxAhead-precision)) || ts.After(after.Add(nodeEntryMaxAhead)) {
		t.Fatalf("an entry dated a year ahead was stored at %v, want about %v", ts, before.Add(nodeEntryMaxAhead))
	}
	if ts := stored["behind"]; ts.Before(before.Add(-nodeEntryMaxAge-precision)) || ts.After(after.Add(-nodeEntryMaxAge)) {
		t.Fatalf("an entry dated a month back was stored at %v, want about %v", ts, before.Add(-nodeEntryMaxAge))
	}
	if ts := stored["on time"]; ts.Sub(before.Add(-time.Minute)).Abs() > precision {
		t.Fatalf("an entry within the bounds was stored at %v, want %v", ts, before.Add(-time.Minute))
	}
}
