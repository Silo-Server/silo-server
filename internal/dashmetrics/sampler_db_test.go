package dashmetrics

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// samplerTestPool opens a pool on a fresh schema holding empty copies of the
// tables the sampler reads and writes, so the test sees only its own rows on a
// shared migrated database.
func samplerTestPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	admin, err := pgxpool.New(t.Context(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(admin.Close)
	schema := pgx.Identifier{fmt.Sprintf("dashmetrics_%d", time.Now().UnixNano())}.Sanitize()
	if _, err := admin.Exec(t.Context(), "CREATE SCHEMA "+schema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, err := admin.Exec(ctx, "DROP SCHEMA "+schema+" CASCADE"); err != nil {
			t.Error(err)
		}
	})
	for _, table := range []string{"dashboard_metric_samples", "dashboard_node_egress_samples", "stream_nodes", "playback_sessions_sync"} {
		if _, err := admin.Exec(t.Context(), fmt.Sprintf("CREATE TABLE %s.%s (LIKE public.%s INCLUDING ALL)", schema, table, table)); err != nil {
			t.Fatal(err)
		}
	}
	config, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	config.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(t.Context(), config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	return pool
}

// TestSampleSharedRecordsEgressPerNodeDB: the shared row keeps the cluster's
// node egress total, and every enabled, healthy node that is serving traffic
// gets a dashboard_node_egress_samples row from the same snapshot. Idle,
// disabled, and unhealthy nodes write no row. A replica that loses the race
// for the minute's shared row writes no node rows either, so a minute's split
// always comes from the replica whose total won.
func TestSampleSharedRecordsEgressPerNodeDB(t *testing.T) {
	pool := samplerTestPool(t)
	ctx := t.Context()

	nodeID := func(name string, enabled, healthy bool, kbps int) int {
		t.Helper()
		var id int
		if err := pool.QueryRow(ctx, `
			INSERT INTO stream_nodes (name, type, url, enabled, healthy, egress_kbps)
			VALUES ($1, 'proxy', 'http://' || $1, $2, $3, $4) RETURNING id
		`, name, enabled, healthy, kbps).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	busy := nodeID("busy", true, true, 4000)
	other := nodeID("other", true, true, 1500)
	nodeID("idle", true, true, 0)
	nodeID("disabled", false, true, 9000)
	nodeID("unhealthy", true, false, 7000)

	samples := func() []string {
		t.Helper()
		rows, err := pool.Query(ctx, `
			SELECT source, egress_kbps FROM dashboard_metric_samples
			UNION ALL
			SELECT 'node:' || node_id, egress_kbps FROM dashboard_node_egress_samples
			ORDER BY 2 DESC
		`)
		if err != nil {
			t.Fatal(err)
		}
		got, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (string, error) {
			var source string
			var kbps int64
			err := row.Scan(&source, &kbps)
			return fmt.Sprintf("%s=%d", source, kbps), err
		})
		if err != nil {
			t.Fatal(err)
		}
		return got
	}

	NewSampler(pool, nil, "replica-a").sampleShared(ctx)
	want := []string{
		"shared=5500",
		fmt.Sprintf("node:%d=4000", busy),
		fmt.Sprintf("node:%d=1500", other),
	}
	if got := samples(); fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("samples = %v, want %v", got, want)
	}

	// A replica that loses the race for a minute's shared row must not land
	// node rows either. Stand in for the winner with shared rows for this
	// minute and the next, so the check holds even if the call below crosses a
	// minute boundary.
	if _, err := pool.Exec(ctx, `TRUNCATE dashboard_metric_samples, dashboard_node_egress_samples`); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO dashboard_metric_samples (bucket, source, egress_kbps)
		SELECT date_trunc('minute', now()) + make_interval(mins => m), 'shared', 1 FROM generate_series(0, 1) m
	`); err != nil {
		t.Fatal(err)
	}
	NewSampler(pool, nil, "replica-b").sampleShared(ctx)
	if got := samples(); fmt.Sprint(got) != fmt.Sprint([]string{"shared=1", "shared=1"}) {
		t.Fatalf("after a losing replica: samples = %v, want only the winner's shared rows", got)
	}
}
