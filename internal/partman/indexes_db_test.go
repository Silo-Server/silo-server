package partman

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestOnlinePartitionIndexesKeepWritersAvailableDB(t *testing.T) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	suffix := fmt.Sprint(time.Now().UnixNano())
	table := "qa_online_idx_" + suffix
	parent := "qa_action_idx_" + suffix
	leafName := table + "_default"
	qualifiedParent := indexPublicSchema + "." + parent
	qualifiedLeaf := indexPublicSchema + "." + leafName
	_, err = pool.Exec(ctx, fmt.Sprintf(`CREATE TABLE public.%s(timestamp timestamptz NOT NULL,id bigint NOT NULL,action text NOT NULL DEFAULT '') PARTITION BY RANGE(timestamp);
 CREATE TABLE public.%s PARTITION OF public.%s DEFAULT;
 CREATE INDEX %s ON ONLY public.%s(action,timestamp DESC,id DESC) WHERE action<>'';
 INSERT INTO public.%s SELECT now(),n,'' FROM generate_series(1,1000)n;`, quoteIdent(table), quoteIdent(leafName), quoteIdent(table), quoteIdent(parent), quoteIdent(table), quoteIdent(table)))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = pool.Exec(context.Background(), "DROP TABLE public."+quoteIdent(table)+" CASCADE") }()
	var leafOID uint32
	if err := pool.QueryRow(ctx, `SELECT to_regclass($1)::oid`, qualifiedLeaf).Scan(&leafOID); err != nil {
		t.Fatal(err)
	}
	manager := NewManager(pool, table, Weekly, 2)
	// A canceled build must not leave an invalid leaf index or a pooled advisory
	// lock that prevents the next API instance from completing startup.
	writer, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := writer.Exec(ctx, "INSERT INTO public."+quoteIdent(table)+" VALUES(now(),1001,'pending')"); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = writer.Rollback(context.Background()) }()
	buildCtx, stopBuild := context.WithCancel(ctx)
	defer stopBuild()
	done := make(chan error, 1)
	go func() { done <- manager.EnsureIndexes(buildCtx, parent) }()
	waitIndexWriterPhase(t, ctx, pool, leafOID, done)
	// This write must complete while the old transaction keeps the concurrent
	// index build waiting. A regular index build would block it.
	writeCtx, stopWrite := context.WithTimeout(ctx, time.Second)
	_, err = pool.Exec(writeCtx, "INSERT INTO public."+quoteIdent(table)+" VALUES(now(),1002,'committed')")
	stopWrite()
	if err != nil {
		t.Fatalf("ordinary writer blocked by index build: %v", err)
	}
	stopBuild()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("canceled build succeeded")
		}
	case <-ctx.Done():
		t.Fatal("canceled build did not stop")
	}
	_ = writer.Rollback(ctx)
	// Two API startups race to resume the same canceled build. Both must succeed,
	// with one valid attached leaf and no duplicate build/name collision.
	results := make(chan error, 2)
	for range 2 {
		go func() { results <- manager.EnsureIndexes(ctx, parent) }()
	}
	for range 2 {
		select {
		case err := <-results:
			if err != nil {
				t.Fatal(err)
			}
		case <-ctx.Done():
			t.Fatal("resumed build timed out")
		}
	}
	var valid bool
	var attached int
	if err := pool.QueryRow(ctx, `SELECT indisvalid FROM pg_index WHERE indexrelid=to_regclass($1)`, qualifiedParent).Scan(&valid); err != nil || !valid {
		t.Fatalf("parent validity: %v, %v", valid, err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM pg_inherits WHERE inhparent=to_regclass($1)`, qualifiedParent).Scan(&attached); err != nil || attached != 1 {
		t.Fatalf("attached leaves: %d, %v", attached, err)
	}
	var plan string
	if err := pool.QueryRow(ctx, "EXPLAIN (FORMAT JSON) SELECT id FROM public."+quoteIdent(table)+" WHERE action='missing' ORDER BY timestamp DESC,id DESC LIMIT 100").Scan(&plan); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(plan, "Index") || strings.Contains(plan, "Seq Scan") {
		t.Fatalf("domain filter does not use index: %s", plan)
	}
	var count int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM public."+quoteIdent(table)).Scan(&count); err != nil || count != 1001 {
		t.Fatalf("history/writer rows lost: %d, %v", count, err)
	}
	// Future partitions automatically inherit and attach the valid parent index.
	_, err = pool.Exec(ctx, fmt.Sprintf(`CREATE TABLE public.%s PARTITION OF public.%s FOR VALUES FROM ('2100-01-01') TO ('2100-02-01')`, quoteIdent(table+"_future"), quoteIdent(table)))
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.EnsureIndexes(ctx, parent); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM pg_inherits WHERE inhparent=to_regclass($1)`, qualifiedParent).Scan(&attached); err != nil || attached != 2 {
		t.Fatalf("future leaf index missing: %d, %v", attached, err)
	}
}

func waitIndexWriterPhase(t *testing.T, ctx context.Context, pool *pgxpool.Pool, leaf uint32, done <-chan error) {
	t.Helper()
	for {
		var waiting bool
		if err := pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_progress_create_index WHERE relid=$1 AND phase LIKE 'waiting for writers%')`, leaf).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting {
			return
		}
		select {
		case err := <-done:
			t.Fatalf("build exited before observable writer wait: %v", err)
		case <-ctx.Done():
			t.Fatal("writer-wait phase not reached")
		case <-time.After(5 * time.Millisecond):
		}
	}
}
