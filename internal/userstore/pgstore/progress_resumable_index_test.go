package pgstore

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// lastQueryTracer records the SQL and arguments of the most recent query so a
// test can EXPLAIN exactly what a store method sent.
type lastQueryTracer struct {
	mu   sync.Mutex
	sql  string
	args []any
}

func (r *lastQueryTracer) TraceQueryStart(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.sql, r.args = data.SQL, data.Args
	return ctx
}

func (*lastQueryTracer) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}

func (r *lastQueryTracer) last() (string, []any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.sql, r.args
}

type explainNode struct {
	NodeType         string        `json:"Node Type"`
	RelationName     string        `json:"Relation Name"`
	IndexName        string        `json:"Index Name"`
	ActualRows       float64       `json:"Actual Rows"`
	ActualLoops      float64       `json:"Actual Loops"`
	RemovedByFilter  float64       `json:"Rows Removed by Filter"`
	RemovedByRecheck float64       `json:"Rows Removed by Index Recheck"`
	Plans            []explainNode `json:"Plans"`
}

// progressRowsRead sums the user_watch_progress tuples every scan node in the
// plan fetched, kept or filtered out, and names the indexes those scans used.
func progressRowsRead(node explainNode, indexes map[string]bool) float64 {
	var read float64
	if node.RelationName == "user_watch_progress" {
		read = (node.ActualRows + node.RemovedByFilter + node.RemovedByRecheck) * node.ActualLoops
		scanIndexNames(node, indexes)
	}
	for _, child := range node.Plans {
		read += progressRowsRead(child, indexes)
	}
	return read
}

// scanIndexNames records the index a scan node read. An index or index-only
// scan names it on the node itself; a Bitmap Heap Scan carries only the
// relation, and its Bitmap Index Scan children (under BitmapAnd/BitmapOr when
// several combine) carry only the index.
func scanIndexNames(node explainNode, indexes map[string]bool) {
	if node.IndexName != "" {
		indexes[node.IndexName] = true
	}
	for _, child := range node.Plans {
		switch child.NodeType {
		case "Bitmap Index Scan", "BitmapAnd", "BitmapOr":
			scanIndexNames(child, indexes)
		}
	}
}

// A bitmap plan must count as reading the index its Bitmap Index Scan names,
// or the guard below would fail whenever the planner prefers a bitmap scan of
// idx_uwp_profile_resumable (as ListProgressPage does at production scale).
func TestProgressRowsReadNamesBitmapIndexes(t *testing.T) {
	const plan = `{
		"Node Type": "Hash Join",
		"Plans": [
			{"Node Type": "Bitmap Heap Scan", "Relation Name": "user_watch_progress",
			 "Actual Rows": 409, "Actual Loops": 1, "Rows Removed by Filter": 100,
			 "Plans": [{"Node Type": "Bitmap Index Scan", "Index Name": "idx_uwp_profile_resumable",
			            "Actual Rows": 509, "Actual Loops": 1}]},
			{"Node Type": "Hash", "Plans": [
				{"Node Type": "Bitmap Heap Scan", "Relation Name": "user_history_hidden_items",
				 "Actual Rows": 150, "Actual Loops": 1,
				 "Plans": [{"Node Type": "Bitmap Index Scan", "Index Name": "user_history_hidden_items_pkey",
				            "Actual Rows": 150, "Actual Loops": 1}]}
			]}
		]
	}`
	var node explainNode
	if err := json.Unmarshal([]byte(plan), &node); err != nil {
		t.Fatal(err)
	}
	indexes := map[string]bool{}
	if read := progressRowsRead(node, indexes); read != 509 {
		t.Errorf("rows read = %.0f, want 509", read)
	}
	if len(indexes) != 1 || !indexes["idx_uwp_profile_resumable"] {
		t.Errorf("indexes = %v, want only idx_uwp_profile_resumable", indexes)
	}
}

// The in-progress listings (Continue Watching, Next Up's resumable branch,
// Jellyfin Resume, and v2 GET /progress?status=in_progress) filter on
// position_seconds > 0. idx_uwp_profile_resumable carries that exact
// predicate, so a page reads only the profile's resumable rows. An index whose
// predicate the query does not imply (the old completed = FALSE one) leaves the
// planner reading every progress row of the profile, completed or not.
func TestInProgressListingsReadOnlyResumableRows(t *testing.T) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	config, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	tracer := &lastQueryTracer{}
	config.ConnConfig.Tracer = tracer
	config.MaxConns = 1
	ctx := t.Context()
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)

	var userID int
	if err := pool.QueryRow(ctx, `INSERT INTO users (username, role) VALUES ($1, 'user') RETURNING id`,
		fmt.Sprintf("progress-resumable-%d", time.Now().UnixNano())).Scan(&userID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := pool.Exec(context.WithoutCancel(ctx), `DELETE FROM users WHERE id = $1`, userID); err != nil {
			t.Errorf("clean up user: %v", err)
		}
	})

	// A long watch history with few resume points: 2000 completed rows, 200
	// rows reset by mark-unplayed, 15 partial watches, and 5 rewatches of
	// completed items. Only the last 20 have position_seconds > 0.
	const resumable = 20
	if _, err := pool.Exec(ctx, `
		INSERT INTO user_watch_progress
			(user_id, profile_id, media_item_id, position_seconds, duration_seconds, completed, updated_at)
		SELECT $1, 'p', 'item-' || g,
		       CASE WHEN g > 2200 THEN 60 ELSE 0 END,
		       1800,
		       g <= 2000 OR g > 2215,
		       timestamptz '2026-01-01' + g * interval '1 minute'
		FROM generate_series(1, 2220) g`, userID); err != nil {
		t.Fatal(err)
	}

	store := newStore(pool, userID)
	listings := []struct {
		name string
		call func() (int, error)
	}{
		{"ListProgress", func() (int, error) {
			rows, err := store.ListProgress(ctx, "p", "in_progress", 100, 0)
			return len(rows), err
		}},
		{"ListProgressPage", func() (int, error) {
			rows, err := store.ListProgressPage(ctx, "p", "in_progress", nil, 100)
			return len(rows), err
		}},
		{"ListProgressFiltered", func() (int, error) {
			rows, err := store.ListProgressFiltered(ctx, "p", "in_progress", nil, nil, 100, 0)
			return len(rows), err
		}},
	}
	for _, listing := range listings {
		t.Run(listing.name, func(t *testing.T) {
			got, err := listing.call()
			if err != nil {
				t.Fatal(err)
			}
			if got != resumable {
				t.Fatalf("listed %d rows, want %d", got, resumable)
			}
			query, args := tracer.last()

			tx, err := pool.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = tx.Rollback(ctx) }()
			// The fixture is small next to a production table, so rule out the
			// sequential scan a planner may prefer for a few thousand rows.
			if _, err := tx.Exec(ctx, "SET LOCAL enable_seqscan = off"); err != nil {
				t.Fatal(err)
			}
			var plan []struct {
				Plan explainNode `json:"Plan"`
			}
			var raw []byte
			if err := tx.QueryRow(ctx, "EXPLAIN (ANALYZE, FORMAT JSON) "+query, args...).Scan(&raw); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(raw, &plan); err != nil || len(plan) != 1 {
				t.Fatalf("decode plan (%v): %s", err, raw)
			}
			indexes := map[string]bool{}
			read := progressRowsRead(plan[0].Plan, indexes)
			t.Logf("user_watch_progress rows read: %.0f via %v", read, indexes)
			if read != resumable || !indexes["idx_uwp_profile_resumable"] {
				t.Fatalf("read %.0f progress rows via %v, want %d via idx_uwp_profile_resumable:\n%s",
					read, indexes, resumable, raw)
			}
		})
	}
}
