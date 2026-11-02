package metadata

import (
	"context"
	"strings"
	"testing"
)

// TestArtworkReferenceChecksUsePathIndexesDB: the revision GC and the storage
// sweep check references one path batch at a time. For every sweep surface
// whose path column has an index, that check must be able to use it under a
// generic plan, where PostgreSQL cannot read the batch to prove a partial
// index predicate; otherwise each check scans the whole table.
func TestArtworkReferenceChecksUsePathIndexesDB(t *testing.T) {
	pool := imageCacheQueueTestPool(t)
	ctx := context.Background()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if _, err := tx.Exec(ctx, `SET LOCAL enable_seqscan = off; SET LOCAL plan_cache_mode = force_generic_plan`); err != nil {
		t.Fatal(err)
	}

	checked := 0
	for _, surface := range artworkSweepSurfaces() {
		rows, err := tx.Query(ctx, `
			SELECT i.relname
			FROM pg_index x
			JOIN pg_class i ON i.oid = x.indexrelid
			JOIN pg_class t ON t.oid = x.indrelid
			JOIN pg_namespace n ON n.oid = t.relnamespace
			JOIN pg_attribute a ON a.attrelid = t.oid AND a.attnum = x.indkey[0]
			WHERE n.nspname = current_schema() AND t.relname = $1 AND a.attname = $2`, surface.table, surface.pathCol)
		if err != nil {
			t.Fatalf("%s: list indexes: %v", surface.name, err)
		}
		var indexes []string
		for rows.Next() {
			var name string
			if err := rows.Scan(&name); err != nil {
				t.Fatalf("%s: scan index: %v", surface.name, err)
			}
			indexes = append(indexes, name)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			t.Fatalf("%s: list indexes: %v", surface.name, err)
		}
		if len(indexes) == 0 {
			continue
		}
		checked++

		if _, err := tx.Exec(ctx, `PREPARE artwork_reference_check(text[]) AS `+artworkReferenceSQL(surface, "$1")); err != nil {
			t.Fatalf("%s: prepare: %v", surface.name, err)
		}
		var plan strings.Builder
		planRows, err := tx.Query(ctx, `EXPLAIN EXECUTE artwork_reference_check(ARRAY['local/a/b/poster/original.r.webp'])`)
		if err != nil {
			t.Fatalf("%s: explain: %v", surface.name, err)
		}
		for planRows.Next() {
			var line string
			if err := planRows.Scan(&line); err != nil {
				t.Fatalf("%s: scan plan: %v", surface.name, err)
			}
			plan.WriteString(line + "\n")
		}
		planRows.Close()
		if err := planRows.Err(); err != nil {
			t.Fatalf("%s: explain: %v", surface.name, err)
		}
		if _, err := tx.Exec(ctx, `DEALLOCATE artwork_reference_check`); err != nil {
			t.Fatal(err)
		}
		used := false
		for _, index := range indexes {
			used = used || strings.Contains(plan.String(), index)
		}
		if !used {
			t.Errorf("%s: reference check does not use any of %v:\n%s", surface.name, indexes, plan.String())
		}
	}
	if checked == 0 {
		t.Fatal("no sweep surface has a path index; the schema is not migrated")
	}
}
