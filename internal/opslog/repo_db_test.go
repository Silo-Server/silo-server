package opslog

import (
	"context"
	"strings"
	"testing"
	"time"
)

// A cursor page must skip the daily partitions newer than the cursor instead
// of opening an index scan on every one of them.
func TestListCursorPrunesNewerPartitionsDB(t *testing.T) {
	pool := testPool(t)
	query, args, _, err := buildListQuery(ListOptions{Cursor: encodeCursor(time.Now().AddDate(0, 0, -30), 1)})
	if err != nil {
		t.Fatal(err)
	}
	rows, err := pool.Query(context.Background(), "EXPLAIN (COSTS OFF) "+query, args...)
	if err != nil {
		t.Fatal(err)
	}
	var plan strings.Builder
	for rows.Next() {
		var line string
		if err := rows.Scan(&line); err != nil {
			t.Fatal(err)
		}
		plan.WriteString(line + "\n")
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(plan.String(), "operational_logs_p_") {
		t.Fatalf("cursor page scans partitions newer than the cursor:\n%s", plan.String())
	}
}
