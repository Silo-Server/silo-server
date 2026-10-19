package catalog

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// This opt-in diagnostic uses an empty, disposable PostgreSQL database. It
// measures admitted construction and query submissions, not catalog query cost.
func TestQueryBudgetReproduction(t *testing.T) {
	dsn := os.Getenv("SILO_C24_REPRO_DATABASE_URL")
	if dsn == "" {
		t.Skip("requires a task-owned empty PostgreSQL database")
	}
	t.Run("structure", func(t *testing.T) {
		for _, n := range []int{1, 64, 128, 256} {
			rules := make([]QueryRule, n)
			for i := range rules {
				rules[i] = QueryRule{Field: "year", Op: "gte", Value: 2000 + i}
			}
			def := QueryDefinition{MediaScope: "movie", Groups: []QueryGroup{{Rules: rules}}}.Normalize()
			if err := def.ValidateWithOptions(true, true); err != nil {
				t.Fatal(err)
			}
			raw, err := json.Marshal(def)
			if err != nil {
				t.Fatal(err)
			}
			sql, args, err := personalCollectionCountQuery(1, PersonalCollectionDefinition{ID: "synthetic", CollectionType: "smart", QueryDefinition: string(raw)}, AccessFilter{})
			if err != nil {
				t.Fatal(err)
			}
			t.Logf("rules=%d definition_bytes=%d sql_bytes=%d args=%d accepted=true", n, len(raw), len(sql), len(args))
			if n == 1 {
				t.Logf("synthetic count SQL: %s", sql)
			}
			if n == 256 && os.Getenv("SILO_C24_EXPECT_BUDGETS") == "1" {
				t.Error("proposed 128-rule structural budget absent: 256 rules accepted")
			}
		}
	})
	tracer := &queryBudgetTrace{codes: map[string]int{}}
	config, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	config.MaxConns = 4
	config.ConnConfig.Tracer = tracer
	pool, err := pgxpool.NewWithConfig(t.Context(), config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if err := pool.Ping(t.Context()); err != nil {
		t.Fatal(err)
	}
	t.Run("distinct_and_duplicate_definitions", func(t *testing.T) {
		for _, tc := range []struct {
			n          int
			duplicates bool
			want       int
		}{{8, false, 12}, {20, false, 24}, {20, true, 2}} {
			collections := make([]PersonalCollectionDefinition, tc.n)
			for i := range collections {
				year := 2000 + i
				if tc.duplicates {
					year = 2000
				}
				collections[i] = PersonalCollectionDefinition{ID: fmt.Sprintf("synthetic-%d", i), CollectionType: "smart", QueryDefinition: fmt.Sprintf(`{"media_scope":"movie","groups":[{"rules":[{"field":"year","op":"gte","value":%d}]}]}`, year)}
			}
			tracer.reset()
			counts, err := CountPersonalCollections(t.Context(), pool, 1, collections, AccessFilter{})
			calls, peak, codes := tracer.result()
			t.Logf("definitions=%d duplicate=%t submissions=%d peak=%d sqlstates=%v returned_counts=%d error=%t", tc.n, tc.duplicates, calls, peak, codes, len(counts), err != nil)
			if calls != tc.want || err == nil || peak > 4 {
				t.Fatalf("unexpected diagnostic result: submissions=%d peak=%d err=%v", calls, peak, err)
			}
		}
	})
	t.Run("successful_distinct_counts", func(t *testing.T) {
		if _, err := pool.Exec(t.Context(), `CREATE TABLE media_items(content_id text, type text, year int); CREATE TABLE manga_chapters(chapter_content_id text); INSERT INTO media_items VALUES ('synthetic-a','movie',2000),('synthetic-b','movie',2001),('synthetic-c','movie',2002)`); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DROP TABLE manga_chapters, media_items`) })
		for _, n := range []int{8, 20, 132} {
			collections := make([]PersonalCollectionDefinition, n)
			for i := range collections {
				collections[i] = PersonalCollectionDefinition{ID: fmt.Sprintf("synthetic-%d", i), CollectionType: "smart", QueryDefinition: fmt.Sprintf(`{"media_scope":"movie","groups":[{"rules":[{"field":"year","op":"gte","value":%d}]}]}`, 2000+i)}
			}
			tracer.reset()
			counts, err := CountPersonalCollections(t.Context(), pool, 1, collections, AccessFilter{})
			calls, peak, codes := tracer.result()
			t.Logf("definitions=%d submissions=%d peak=%d sqlstates=%v returned_counts=%d", n, calls, peak, codes, len(counts))
			if err != nil || len(counts) != n || counts["synthetic-0"] != 3 || counts["synthetic-1"] != 2 || counts["synthetic-2"] != 1 || peak > 4 {
				t.Fatalf("successful-count control failed: counts=%v err=%v", counts, err)
			}
			if n == 132 && calls != 5 {
				t.Errorf("want five batches for 132 definitions, got %d", calls)
			}
		}
	})
	// Raise a resource SQLSTATE immediately: no costly query or timeout is
	// needed to show that the fallback retries a budget failure.
	if _, err := pool.Exec(t.Context(), `CREATE FUNCTION c24_resource_error() RETURNS int LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'synthetic resource budget exhausted' USING ERRCODE='54000'; END $$`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DROP FUNCTION c24_resource_error()`) })
	t.Run("resource_failure_fallback", func(t *testing.T) {
		batch := []collectionCountStatement{{id: "a", sql: "SELECT c24_resource_error()"}, {id: "b", sql: "SELECT c24_resource_error()"}, {id: "c", sql: "SELECT c24_resource_error()"}}
		tracer.reset()
		_, failures := countPersonalCollectionBatch(t.Context(), pool, batch)
		calls, _, codes := tracer.result()
		t.Logf("definitions=3 submissions=%d sqlstates=%v failures=%d", calls, codes, len(failures))
		if calls != 4 || len(failures) != 3 {
			t.Fatal("resource failure diagnostic changed")
		}
		if os.Getenv("SILO_C24_EXPECT_BUDGETS") == "1" {
			t.Error("resource budget failure retried each definition; expected one attempt")
		}
	})
	t.Run("cancellation_negative_control", func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		tracer.reset()
		_, failures := countPersonalCollectionBatch(ctx, pool, []collectionCountStatement{{id: "a", sql: "SELECT 1"}, {id: "b", sql: "SELECT 2"}})
		calls, _, _ := tracer.result()
		t.Logf("cancelled_context=true submissions=%d fallback_attempts=0", calls)
		if calls > 1 || len(failures) != 1 || !errors.Is(failures[0], context.Canceled) {
			t.Fatalf("cancellation control failed: calls=%d failures=%v", calls, failures)
		}
	})
}

type queryBudgetTrace struct {
	mu                  sync.Mutex
	calls, active, peak int
	codes               map[string]int
}

func (q *queryBudgetTrace) TraceQueryStart(ctx context.Context, _ *pgx.Conn, _ pgx.TraceQueryStartData) context.Context {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.calls++
	q.active++
	q.peak = max(q.peak, q.active)
	return ctx
}

func (q *queryBudgetTrace) TraceQueryEnd(_ context.Context, _ *pgx.Conn, data pgx.TraceQueryEndData) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.active--
	if pgErr, ok := errors.AsType[*pgconn.PgError](data.Err); ok {
		q.codes[pgErr.Code]++
	}
}

func (q *queryBudgetTrace) reset() {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.calls, q.active, q.peak = 0, 0, 0
	q.codes = map[string]int{}
}

func (q *queryBudgetTrace) result() (int, int, map[string]int) {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.calls, q.peak, q.codes
}
