package catalog

import (
	"context"
	"strings"
	"testing"
)

func TestPersonalCollectionSourcePlanDB(t *testing.T) {
	f := newSeasonCollectionFixture(t)
	// A realistic seasons table: with only a handful of rows the planner
	// rightly prefers a sequential scan, which says nothing about real sizes.
	batchEquivExec(t, f.pool, `INSERT INTO seasons(content_id,series_id,season_number)
		SELECT $1 || '-decoy-' || n, $1, 1000 + n FROM generate_series(1, 3000) n`, f.series)
	batchEquivExec(t, f.pool, `ANALYZE seasons`)
	batchEquivExec(t, f.pool, `ANALYZE user_personal_collection_items`)
	executor := f.resolver.queryExecutorForScope("", nil)
	usePersonalCollectionSource(executor, f.userID, f.collectionID)
	sql, args, err := executor.buildPreviewPageSQL(QueryDefinition{}, f.access(), 50, 0, false)
	if err != nil {
		t.Fatal(err)
	}
	rows, err := f.pool.Query(context.Background(), "EXPLAIN "+sql, args...)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var line string
		if err := rows.Scan(&line); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(line, "Seq Scan on seasons") {
			t.Fatalf("season branch scans all seasons:\n%s", line)
		}
		t.Log(line)
	}
}
